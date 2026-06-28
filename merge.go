// Package protomerge semantically merges two protobuf sources.
//
// A is the base (it provides the file header and ordering skeleton); B is
// overlaid onto A. Within same-named messages/services/enums, B's elements
// replace the matching A element in place (matched by field number, then name)
// or are appended; declarations unique to B are appended at the end. Comments
// are preserved. The merged result is reformatted with buf-compatible
// formatting so the output is clean regardless of how rough the inputs are.
//
// B may be an incomplete fragment: it need not declare a header and may
// reference types it does not define.
package protomerge

import (
	"fmt"
	"strings"

	"github.com/protobuf-orm/protobuf-merge/internal/format"
	"github.com/protobuf-orm/protobuf-merge/internal/merge"
	"github.com/protobuf-orm/protobuf-merge/internal/protoast"
)

// Options configures a merge.
type Options struct {
	// Strict fails the merge when B redefines an A element incompatibly
	// (field number reused with a different name/type, rpc signature change,
	// edition mismatch) instead of silently letting B win.
	Strict bool
	// Compact uses the formatter's dynamic layout (short bodies inline)
	// instead of the default buf-compatible layout.
	Compact bool
}

// Merge merges overlay b onto base a and returns formatted merged source.
// a_name/b_name are used for diagnostics only.
func Merge(a_name string, a []byte, b_name string, b []byte, opts Options) ([]byte, error) {
	af, err := protoast.Parse(a_name, string(a))
	if err != nil {
		return nil, err
	}
	bf, err := protoast.Parse(b_name, string(b))
	if err != nil {
		return nil, err
	}

	assembled, conflicts := merge.Merge(af, bf, merge.Options{Strict: opts.Strict})
	if opts.Strict && len(conflicts) > 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("merge: %d strict conflict(s):", len(conflicts)))
		for _, c := range conflicts {
			sb.WriteString("\n  - ")
			sb.WriteString(c.String())
		}
		return nil, fmt.Errorf("%s", sb.String())
	}

	style := format.Legacy
	if opts.Compact {
		style = format.Compact
	}
	// Use a neutral name: the assembled text spans both inputs, so a format
	// error must not be blamed on the base file specifically.
	out, err := format.Format("<merged>", assembled, style)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}
