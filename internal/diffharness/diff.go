package diffharness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/google/go-cmp/cmp"
)

// Result is one scenario's outcome.
type Result struct {
	Scenario    string       `json:"scenario"`
	StatusMatch bool         `json:"statusMatch"`
	JavaStatus  int          `json:"javaStatus"`
	GoStatus    int          `json:"goStatus"`
	HeaderDiffs []HeaderDiff `json:"headerDiffs,omitempty"`
	BodyDiff    string       `json:"bodyDiff,omitempty"`
	Verdict     Verdict      `json:"verdict"`
	Reason      string       `json:"reason,omitempty"`
}

// HeaderDiff is one header that differs after normalization.
type HeaderDiff struct {
	Name   string   `json:"name"`
	Java   []string `json:"java,omitempty"`
	Go     []string `json:"go,omitempty"`
	Reason string   `json:"reason"`
}

// Verdict summarizes a scenario's pass/fail.
type Verdict string

const (
	VerdictPass  Verdict = "PASS"
	VerdictFail  Verdict = "FAIL"
	VerdictSkip  Verdict = "SKIP"
	VerdictError Verdict = "ERROR"
)

// Diff compares two normalized responses according to policy. The returned
// Result has the Scenario field unset; callers fill it in.
func Diff(java, goResp Response, policy DiffPolicy) Result {
	r := Result{
		JavaStatus:  java.StatusCode,
		GoStatus:    goResp.StatusCode,
		StatusMatch: java.StatusCode == goResp.StatusCode,
	}

	r.HeaderDiffs = diffHeaders(java.Headers, goResp.Headers)
	r.BodyDiff = diffBodies(java.Body, goResp.Body)

	if !r.StatusMatch || r.BodyDiff != "" || hasMeaningfulHeaderDiff(r.HeaderDiffs) {
		r.Verdict = VerdictFail
	} else {
		r.Verdict = VerdictPass
	}
	return r
}

// diffHeaders returns one entry per header name that differs between the two
// header sets. Both sides have already been passed through Normalize.
func diffHeaders(java, goH http.Header) []HeaderDiff {
	seen := map[string]bool{}
	var diffs []HeaderDiff

	for name, jv := range java {
		seen[name] = true
		gv, ok := goH[name]
		if !ok {
			diffs = append(diffs, HeaderDiff{Name: name, Java: jv, Reason: "java-only"})
			continue
		}
		if !slicesEqualUnordered(jv, gv) {
			diffs = append(diffs, HeaderDiff{Name: name, Java: jv, Go: gv, Reason: "value differs"})
		}
	}
	for name, gv := range goH {
		if seen[name] {
			continue
		}
		diffs = append(diffs, HeaderDiff{Name: name, Go: gv, Reason: "go-only"})
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Name < diffs[j].Name })
	return diffs
}

// hasMeaningfulHeaderDiff returns true if any header diff would cause a FAIL
// verdict. Today every diff is meaningful (caller's IgnoreHeaders is applied
// in Normalize, so nothing should reach here that the policy wanted ignored).
func hasMeaningfulHeaderDiff(diffs []HeaderDiff) bool {
	return len(diffs) > 0
}

// diffBodies returns "" when the two bodies are structurally equivalent JSON
// (or byte-equal when not JSON). When they differ, returns a human-readable
// diff produced by go-cmp.
func diffBodies(java, goBody []byte) string {
	if string(java) == string(goBody) {
		return ""
	}
	var jDoc, gDoc any
	jErr := json.Unmarshal(java, &jDoc)
	gErr := json.Unmarshal(goBody, &gDoc)
	if jErr != nil || gErr != nil {
		// Fall back to byte-level diff when either side isn't JSON.
		return cmp.Diff(string(java), string(goBody))
	}
	if d := cmp.Diff(jDoc, gDoc); d != "" {
		return d
	}
	return ""
}

func slicesEqualUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aSorted := append([]string(nil), a...)
	bSorted := append([]string(nil), b...)
	sort.Strings(aSorted)
	sort.Strings(bSorted)
	for i := range aSorted {
		if aSorted[i] != bSorted[i] {
			return false
		}
	}
	return true
}

// Summary is the aggregate over a run.
type Summary struct {
	Pass    int `json:"pass"`
	Fail    int `json:"fail"`
	Skip    int `json:"skip"`
	Errored int `json:"error"`
}

// WriteText prints a human-readable report of the results to w.
func WriteText(w io.Writer, results []Result) Summary {
	var s Summary
	for _, r := range results {
		switch r.Verdict {
		case VerdictPass:
			s.Pass++
		case VerdictFail:
			s.Fail++
		case VerdictSkip:
			s.Skip++
		case VerdictError:
			s.Errored++
		}
		_, _ = fmt.Fprintf(w, "=== %s ===\n", r.Scenario)
		statusMark := "OK"
		if !r.StatusMatch {
			statusMark = "MISMATCH"
		}
		_, _ = fmt.Fprintf(w, "status:   java=%d  go=%d   %s\n", r.JavaStatus, r.GoStatus, statusMark)
		if len(r.HeaderDiffs) > 0 {
			_, _ = fmt.Fprintln(w, "headers:")
			for _, hd := range r.HeaderDiffs {
				switch hd.Reason {
				case "java-only":
					_, _ = fmt.Fprintf(w, "  - %s: %s [java-only]\n", hd.Name, strings.Join(hd.Java, ", "))
				case "go-only":
					_, _ = fmt.Fprintf(w, "  + %s: %s [go-only]\n", hd.Name, strings.Join(hd.Go, ", "))
				default:
					_, _ = fmt.Fprintf(w, "  ~ %s: java=%v go=%v\n", hd.Name, hd.Java, hd.Go)
				}
			}
		}
		if r.BodyDiff != "" {
			_, _ = fmt.Fprintln(w, "body:")
			for _, line := range strings.Split(r.BodyDiff, "\n") {
				_, _ = fmt.Fprintf(w, "  %s\n", line)
			}
		}
		if r.Reason != "" {
			_, _ = fmt.Fprintf(w, "reason:   %s\n", r.Reason)
		}
		_, _ = fmt.Fprintf(w, "result:   %s\n\n", r.Verdict)
	}
	_, _ = fmt.Fprintf(w, "PASS %d / FAIL %d / SKIP %d / ERROR %d\n",
		s.Pass, s.Fail, s.Skip, s.Errored)
	return s
}

// WriteJSON emits the result set as a single JSON document for machine
// consumption (CI gates, dashboards).
func WriteJSON(w io.Writer, results []Result) (Summary, error) {
	var s Summary
	for _, r := range results {
		switch r.Verdict {
		case VerdictPass:
			s.Pass++
		case VerdictFail:
			s.Fail++
		case VerdictSkip:
			s.Skip++
		case VerdictError:
			s.Errored++
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(struct {
		Results []Result `json:"results"`
		Summary Summary  `json:"summary"`
	}{results, s}); err != nil {
		return s, err
	}
	return s, nil
}
