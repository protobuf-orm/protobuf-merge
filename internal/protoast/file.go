// Package protoast wraps protocompile's stable, full-fidelity AST and exposes
// the small set of helpers the merge engine needs: parse a (possibly
// incomplete) .proto fragment, and pull a node's exact source text and its
// leading comments back out.
package protoast

import (
	"fmt"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
)

// File is a parsed .proto file together with its original source. All text
// extraction is done relative to this file, because protocompile node
// positions are only meaningful within their owning FileNode.
type File struct {
	Name string
	Src  string
	Node *ast.FileNode
}

// Parse parses src into a File. The stable parser is purely syntactic (no type
// resolution or linking), so an incomplete overlay fragment — even one that
// references types it does not define — parses fine, as long as it is
// syntactically valid under the active edition/syntax.
func Parse(name, src string) (*File, error) {
	rep := reporter.NewHandler(reporter.NewReporter(
		func(err reporter.ErrorWithPos) error { return err },
		nil, // ignore warnings
	))
	node, err := parser.Parse(name, strings.NewReader(src), rep)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	return &File{Name: name, Src: src, Node: node}, nil
}

// Text returns a node's exact source text with trailing whitespace trimmed.
// Leading comments are NOT included (use Leading for those).
func (f *File) Text(n ast.Node) string {
	return strings.TrimRight(f.Node.NodeInfo(n).RawText(), " \t\r\n")
}

// Leading returns the block of // or /* */ comments attached immediately before
// n, one comment per line with a trailing newline, or "" when there are none.
func (f *File) Leading(n ast.Node) string {
	c := f.Node.NodeInfo(n).LeadingComments()
	if c.Len() == 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < c.Len(); i++ {
		sb.WriteString(strings.TrimRight(c.Index(i).RawText(), " \t\r\n"))
		sb.WriteString("\n")
	}
	return sb.String()
}

// Trailing returns any inline comments attached after n (e.g. a "// ..." on the
// same line as a field), joined by a space, or "" when there are none.
func (f *File) Trailing(n ast.Node) string {
	c := f.Node.NodeInfo(n).TrailingComments()
	if c.Len() == 0 {
		return ""
	}
	parts := make([]string, 0, c.Len())
	for i := 0; i < c.Len(); i++ {
		parts = append(parts, strings.TrimSpace(c.Index(i).RawText()))
	}
	return strings.Join(parts, " ")
}

// LeadingAndText returns a node's leading comments, its source text, and any
// trailing inline comment, suitable for emitting the node verbatim into
// assembled output. The formatter normalizes placement of the trailing comment.
func (f *File) LeadingAndText(n ast.Node) string {
	s := f.Leading(n) + f.Text(n)
	if t := f.Trailing(n); t != "" {
		s += " " + t
	}
	return s
}

// Between returns the source slice from the start of start through the end of
// end (both inclusive), e.g. start=MessageNode, end=its open brace yields the
// "message Foo {" opener including any export/local visibility keyword.
//
// The end is computed as the end node's start offset plus the byte length of
// its own text, because protocompile's NodeInfo.End reports the start of the
// node's final token rather than the offset just past it.
func (f *File) Between(start, end ast.Node) string {
	s := f.Node.NodeInfo(start).Start().Offset
	end_info := f.Node.NodeInfo(end)
	e := end_info.Start().Offset + len(end_info.RawText())
	return f.Src[s:e]
}
