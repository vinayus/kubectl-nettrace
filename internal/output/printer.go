package output

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/vinayus/kubectl-nettrace/internal/trace"
)

const (
	symOK   = "✓"
	symWarn = "~"
	symFail = "✗"
)

func Print(w io.Writer, result *trace.Result) {
	fmt.Fprintln(w, result.Header)
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "HOP\tTYPE\tNAME\tSTATUS")

	for _, hop := range result.Hops {
		sym := statusSym(hop.Status)
		msg := ""
		if hop.Message != "" {
			msg = sym + " " + hop.Message
		} else {
			msg = sym
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", hop.Number, hop.Kind, hop.Name, msg)

		for _, sub := range hop.SubRows {
			subSym := statusSym(sub.Status)
			subMsg := subSym
			if sub.Message != "" {
				subMsg = subSym + " " + sub.Message
			}
			fmt.Fprintf(tw, "\t\t  %s\t%s\n", sub.Name, subMsg)
		}
	}

	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Result: %s\n", result.Summary)
}

func statusSym(s trace.HopStatus) string {
	switch s {
	case trace.StatusOK:
		return symOK
	case trace.StatusWarn:
		return symWarn
	case trace.StatusFail:
		return symFail
	}
	return "?"
}
