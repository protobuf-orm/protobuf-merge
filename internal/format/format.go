// Package format pretty-prints protobuf source text using protocompile's
// experimental printer, which is the exact engine that `buf format` wraps.
//
// This is the only place in the tool that depends on the experimental
// protocompile API; keeping it isolated means an upstream API break is
// localized to this file.
package format

import (
	"fmt"
	"strings"

	"github.com/bufbuild/protocompile/experimental/ast/printer"
	"github.com/bufbuild/protocompile/experimental/parser"
	"github.com/bufbuild/protocompile/experimental/report"
	"github.com/bufbuild/protocompile/experimental/source"
)

// Style selects the formatting preset.
type Style int

const (
	// Legacy reproduces `buf format` byte-for-byte.
	Legacy Style = iota
	// Compact uses the printer's dynamic/Default layout (short bodies inline).
	Compact
)

func (s Style) formatting() printer.Formatting {
	if s == Compact {
		return printer.Default()
	}
	return printer.Legacy()
}

// Format parses src (which may be deliberately rough, e.g. the assembled merge
// output with inconsistent indentation) and returns canonically formatted
// protobuf source. name is used only for diagnostics.
func Format(name, src string, style Style) (string, error) {
	r := &report.Report{Options: report.Options{SuppressWarnings: true}}
	file, ok := parser.Parse(name, source.NewFile(name, src), r)
	if !ok || file == nil {
		return "", fmt.Errorf("format: parse %s failed: %s", name, diagnostics(r))
	}

	out, err := printer.PrintFile(printer.Options{Format: true, Formatting: style.formatting()}, file)
	if err != nil {
		return "", fmt.Errorf("format: print %s: %w", name, err)
	}
	return out, nil
}

// diagnostics renders a report's diagnostics into a single line for errors.
func diagnostics(r *report.Report) string {
	if len(r.Diagnostics) == 0 {
		return "unknown error"
	}
	msgs := make([]string, 0, len(r.Diagnostics))
	for _, d := range r.Diagnostics {
		msgs = append(msgs, d.Message())
	}
	return strings.Join(msgs, "; ")
}
