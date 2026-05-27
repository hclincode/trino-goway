package diffharness

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Runner replays scenarios against a Java and Go target and produces diffs.
type Runner struct {
	Java   Target
	Go     Target
	Client *http.Client
}

// NewRunner builds a Runner with a default HTTP client that does not follow
// redirects (Hard Invariant #2 — we want to see the gateway's redirect verbatim,
// not the one it forwards to).
func NewRunner(java, goT Target) *Runner {
	return &Runner{
		Java: java,
		Go:   goT,
		Client: &http.Client{
			Timeout: 60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// RunScenario replays one scenario at both gateways concurrently, normalizes
// responses, and returns a Result.
func (r *Runner) RunScenario(ctx context.Context, s *Scenario) Result {
	type sideResult struct {
		resp Response
		err  error
	}

	var (
		jSide, gSide sideResult
		wg           sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		jSide.resp, jSide.err = Run(ctx, r.Client, r.Java, s, map[string]string{})
	}()
	go func() {
		defer wg.Done()
		gSide.resp, gSide.err = Run(ctx, r.Client, r.Go, s, map[string]string{})
	}()
	wg.Wait()

	if jSide.err != nil || gSide.err != nil {
		out := Result{Scenario: s.Name, Verdict: VerdictError}
		switch {
		case jSide.err != nil && gSide.err != nil:
			out.Reason = "both sides errored: java=" + jSide.err.Error() + "; go=" + gSide.err.Error()
		case jSide.err != nil:
			out.Reason = "java errored: " + jSide.err.Error()
		default:
			out.Reason = "go errored: " + gSide.err.Error()
		}
		return out
	}

	jNorm := Normalize(jSide.resp, s.Diff, r.Java.Host())
	gNorm := Normalize(gSide.resp, s.Diff, r.Go.Host())

	res := Diff(jNorm, gNorm, s.Diff)
	res.Scenario = s.Name
	return res
}

// RunAll runs every scenario sequentially and returns the result set.
// Sequential by design: gateways under test may have global state (cookies,
// query-id cache) that we don't want concurrent scenarios polluting.
func (r *Runner) RunAll(ctx context.Context, scenarios []*Scenario) []Result {
	out := make([]Result, 0, len(scenarios))
	for _, s := range scenarios {
		out = append(out, r.RunScenario(ctx, s))
	}
	return out
}

// RecordScenario replays a scenario against the Java target only, normalizes
// the response per the scenario's policy, and returns the Golden. The caller
// writes it to disk via WriteGolden.
func (r *Runner) RecordScenario(ctx context.Context, s *Scenario) (Golden, error) {
	resp, err := Run(ctx, r.Client, r.Java, s, map[string]string{})
	if err != nil {
		return Golden{}, err
	}
	norm := Normalize(resp, s.Diff, r.Java.Host())
	return newGolden(s.Name, norm), nil
}

// ReplayScenario runs a scenario against the Go target only and diffs the
// normalized Go response against the given Golden. Cheap mode: no Java
// gateway required.
func (r *Runner) ReplayScenario(ctx context.Context, s *Scenario, golden Golden) Result {
	if golden.Scenario != s.Name {
		return Result{
			Scenario: s.Name,
			Verdict:  VerdictError,
			Reason: fmt.Sprintf("golden scenario %q does not match requested scenario %q",
				golden.Scenario, s.Name),
		}
	}
	resp, err := Run(ctx, r.Client, r.Go, s, map[string]string{})
	if err != nil {
		return Result{Scenario: s.Name, Verdict: VerdictError, Reason: "go errored: " + err.Error()}
	}
	gNorm := Normalize(resp, s.Diff, r.Go.Host())
	res := Diff(golden.toResponse(), gNorm, s.Diff)
	res.Scenario = s.Name
	return res
}
