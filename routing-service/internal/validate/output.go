package validate

import (
	"fmt"
	"io"
	"log/slog"
)

// row is one line of the dry-run routing table.
type row struct {
	id       string
	summary  string // short "source=… user=…" description
	newGroup string // group from the config under test
	oldGroup string // group from the baseline (diff mode only)
	changed  bool   // true when oldGroup != newGroup
}

// printTable writes the routing table. In diff mode it includes an OLD column
// and a CHANGED marker on rows whose routing differs from the baseline.
func printTable(w io.Writer, rows []row, diff bool) {
	const (
		colSample = 24
		colInput  = 32
		colGroup  = 16
	)
	if diff {
		_, _ = fmt.Fprintf(w, "%-*s %-*s %-*s %-*s %s\n",
			colSample, "SAMPLE",
			colInput, "INPUT",
			colGroup, "OLD",
			colGroup, "NEW",
			"")
		for _, r := range rows {
			marker := ""
			if r.changed {
				marker = "CHANGED"
			}
			_, _ = fmt.Fprintf(w, "%-*s %-*s %-*s %-*s %s\n",
				colSample, r.id,
				colInput, r.summary,
				colGroup, groupDisplay(r.oldGroup),
				colGroup, groupDisplay(r.newGroup),
				marker)
		}
		return
	}

	_, _ = fmt.Fprintf(w, "%-*s %-*s %s\n",
		colSample, "SAMPLE",
		colInput, "INPUT",
		"GROUP")
	for _, r := range rows {
		_, _ = fmt.Fprintf(w, "%-*s %-*s %s\n",
			colSample, r.id,
			colInput, r.summary,
			groupDisplay(r.newGroup))
	}
}

// groupDisplay renders a routing group for the table; an empty group (the
// pipeline deferred to default, which Evaluate never returns empty, but guard
// anyway) shows an em-dash.
func groupDisplay(g string) string {
	if g == "" {
		return "—"
	}
	return g
}

// inputSummary returns a compact one-line description of a sample's salient
// inputs for the table.
func inputSummary(source, user string) string {
	switch {
	case source != "" && user != "":
		return fmt.Sprintf("source=%s user=%s", source, user)
	case source != "":
		return "source=" + source
	case user != "":
		return "user=" + user
	default:
		return "(empty)"
	}
}

// discardLogger returns a logger that discards everything; the CLI reports
// through stdout, not logs.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
