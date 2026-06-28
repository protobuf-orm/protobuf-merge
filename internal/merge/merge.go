// Package merge performs a semantic merge of two parsed .proto files.
//
// A is the base: it supplies the file header (edition/syntax, package) and the
// ordering skeleton. B is overlaid onto A: within same-named messages, services
// and enums, B's elements either replace the matching A element in place
// (B-wins) or are appended; top-level declarations unique to B are appended at
// the end.
//
// The engine reconstructs a (deliberately rough) merged source string by
// stitching together the exact source text of retained/added nodes — comments
// included. Final whitespace/indentation normalization is delegated to the
// formatter, so the assembled text only needs to be syntactically valid.
package merge

import (
	"fmt"
	"strings"

	"github.com/bufbuild/protocompile/ast"

	"github.com/protobuf-orm/protobuf-merge/internal/protoast"
)

// Options configures a merge.
type Options struct {
	// Strict makes Merge return an error when B redefines an A element
	// incompatibly (field number reused with a different name/type, rpc
	// signature change, package/edition mismatch) instead of silently
	// letting B win.
	Strict bool
}

// Conflict describes an incompatible redefinition detected during the merge.
type Conflict struct {
	Kind   string // e.g. "field-renamed", "field-retyped", "rpc-signature", "edition"
	Name   string // qualified element name
	Detail string // human-readable description of A-vs-B difference
	Pos    string // file:line:col of the A element
}

func (c Conflict) String() string {
	return fmt.Sprintf("%s: %s (%s) at %s", c.Kind, c.Name, c.Detail, c.Pos)
}

// Merge merges b onto a and returns assembled (unformatted) merged source plus
// any conflicts detected. The caller is expected to run the result through the
// formatter.
func Merge(a, b *protoast.File, opts Options) (string, []Conflict) {
	m := &merger{a: a, b: b, opts: opts}
	return m.file(), m.conflicts
}

type merger struct {
	a, b      *protoast.File
	opts      Options
	conflicts []Conflict
}

func (m *merger) conflict(kind, name, detail string, f *protoast.File, n ast.Node) {
	p := f.Node.NodeInfo(n).Start()
	m.conflicts = append(m.conflicts, Conflict{
		Kind:   kind,
		Name:   name,
		Detail: detail,
		Pos:    fmt.Sprintf("%s:%d:%d", p.Filename, p.Line, p.Col),
	})
}

// file assembles the whole merged file.
func (m *merger) file() string {
	var head strings.Builder
	if s := firstNonEmpty(syntaxText(m.a), syntaxText(m.b)); s != "" {
		head.WriteString(s)
		head.WriteString("\n")
	}
	if m.b.Node.Edition != nil || m.b.Node.Syntax != nil {
		m.checkEdition()
	}
	if p := firstNonEmpty(packageText(m.a), packageText(m.b)); p != "" {
		head.WriteString(p)
		head.WriteString("\n")
	}
	for _, imp := range unionTopLevel[*ast.ImportNode](m.a, m.b) {
		head.WriteString(imp)
		head.WriteString("\n")
	}
	for _, opt := range unionOptions(m.a, m.b) {
		head.WriteString(opt)
		head.WriteString("\n")
	}

	blocks := m.topLevel()

	var sb strings.Builder
	sb.WriteString(head.String())
	if head.Len() > 0 && len(blocks) > 0 {
		sb.WriteString("\n")
	}
	sb.WriteString(strings.Join(blocks, "\n\n"))
	sb.WriteString("\n")
	return sb.String()
}

// topLevel merges the message/enum/service/extend declarations.
func (m *merger) topLevel() []string {
	b_msg := map[string]*ast.MessageNode{}
	b_enum := map[string]*ast.EnumNode{}
	b_svc := map[string]*ast.ServiceNode{}
	for _, d := range m.b.Node.Decls {
		switch n := d.(type) {
		case *ast.MessageNode:
			b_msg[name(n.Name)] = n
		case *ast.EnumNode:
			b_enum[name(n.Name)] = n
		case *ast.ServiceNode:
			b_svc[name(n.Name)] = n
		}
	}

	consumed := map[ast.Node]bool{}
	var blocks []string
	for _, d := range m.a.Node.Decls {
		switch n := d.(type) {
		case *ast.MessageNode:
			if bn, ok := b_msg[name(n.Name)]; ok {
				consumed[bn] = true
				blocks = append(blocks, m.message(n, bn))
			} else {
				blocks = append(blocks, m.a.LeadingAndText(n))
			}
		case *ast.EnumNode:
			if bn, ok := b_enum[name(n.Name)]; ok {
				consumed[bn] = true
				blocks = append(blocks, m.enum(n, bn))
			} else {
				blocks = append(blocks, m.a.LeadingAndText(n))
			}
		case *ast.ServiceNode:
			if bn, ok := b_svc[name(n.Name)]; ok {
				consumed[bn] = true
				blocks = append(blocks, m.service(n, bn))
			} else {
				blocks = append(blocks, m.a.LeadingAndText(n))
			}
		case *ast.ExtendNode:
			blocks = append(blocks, m.a.LeadingAndText(n))
		}
	}
	for _, d := range m.b.Node.Decls {
		if consumed[d] {
			continue
		}
		switch d.(type) {
		case *ast.MessageNode, *ast.EnumNode, *ast.ServiceNode, *ast.ExtendNode:
			blocks = append(blocks, m.b.LeadingAndText(d))
		}
	}
	return blocks
}

// message merges B's message body onto A's.
func (m *merger) message(a, b *ast.MessageNode) string {
	b_field_num, b_field_name := map[uint64]ast.Node{}, map[string]ast.Node{}
	b_oneof := map[string]*ast.OneofNode{}
	b_msg := map[string]*ast.MessageNode{}
	b_enum := map[string]*ast.EnumNode{}
	for _, el := range b.Decls {
		switch n := el.(type) {
		case *ast.FieldNode:
			indexField(b_field_num, b_field_name, n.Tag, n.Name, n)
		case *ast.MapFieldNode:
			indexField(b_field_num, b_field_name, n.Tag, n.Name, n)
		case *ast.OneofNode:
			b_oneof[name(n.Name)] = n
		case *ast.MessageNode:
			b_msg[name(n.Name)] = n
		case *ast.EnumNode:
			b_enum[name(n.Name)] = n
		}
	}

	consumed := map[ast.Node]bool{}
	var body strings.Builder
	emit := func(s string) { body.WriteString("\n"); body.WriteString(s) }

	for _, el := range a.Decls {
		switch n := el.(type) {
		case *ast.FieldNode:
			if bel, ok := matchField(b_field_num, b_field_name, consumed, n.Tag, n.Name); ok {
				consumed[bel] = true
				m.checkField(n, bel)
				emit(m.b.LeadingAndText(bel))
			} else {
				emit(m.a.LeadingAndText(n))
			}
		case *ast.MapFieldNode:
			if bel, ok := matchField(b_field_num, b_field_name, consumed, n.Tag, n.Name); ok {
				consumed[bel] = true
				emit(m.b.LeadingAndText(bel))
			} else {
				emit(m.a.LeadingAndText(n))
			}
		case *ast.OneofNode:
			if bn, ok := b_oneof[name(n.Name)]; ok {
				consumed[bn] = true
				emit(m.oneof(n, bn))
			} else {
				emit(m.a.LeadingAndText(n))
			}
		case *ast.MessageNode:
			if bn, ok := b_msg[name(n.Name)]; ok {
				consumed[bn] = true
				emit(m.message(n, bn))
			} else {
				emit(m.a.LeadingAndText(n))
			}
		case *ast.EnumNode:
			if bn, ok := b_enum[name(n.Name)]; ok {
				consumed[bn] = true
				emit(m.enum(n, bn))
			} else {
				emit(m.a.LeadingAndText(n))
			}
		default:
			// reserved, options, extension ranges, groups, empty: keep A's.
			emit(m.a.LeadingAndText(el))
		}
	}
	for _, el := range b.Decls {
		if consumed[el] {
			continue
		}
		switch el.(type) {
		case *ast.FieldNode, *ast.MapFieldNode, *ast.OneofNode, *ast.MessageNode, *ast.EnumNode, *ast.OptionNode:
			emit(m.b.LeadingAndText(el))
		}
	}

	return m.a.Leading(a) + m.a.Between(a, a.OpenBrace) + body.String() + "\n}"
}

// oneof merges B's oneof entries onto A's matching oneof.
func (m *merger) oneof(a, b *ast.OneofNode) string {
	b_field_num, b_field_name := map[uint64]ast.Node{}, map[string]ast.Node{}
	for _, el := range b.Decls {
		if f, ok := el.(*ast.FieldNode); ok {
			indexField(b_field_num, b_field_name, f.Tag, f.Name, f)
		}
	}

	consumed := map[ast.Node]bool{}
	var body strings.Builder
	emit := func(s string) { body.WriteString("\n"); body.WriteString(s) }

	for _, el := range a.Decls {
		if f, ok := el.(*ast.FieldNode); ok {
			if bel, ok := matchField(b_field_num, b_field_name, consumed, f.Tag, f.Name); ok {
				consumed[bel] = true
				m.checkField(f, bel)
				emit(m.b.LeadingAndText(bel))
				continue
			}
		}
		emit(m.a.LeadingAndText(el))
	}
	for _, el := range b.Decls {
		if consumed[el] {
			continue
		}
		switch el.(type) {
		case *ast.FieldNode, *ast.OptionNode:
			emit(m.b.LeadingAndText(el))
		}
	}

	return m.a.Leading(a) + m.a.Between(a, a.OpenBrace) + body.String() + "\n}"
}

// enum merges B's enum values onto A's, matching by name.
func (m *merger) enum(a, b *ast.EnumNode) string {
	b_val := map[string]*ast.EnumValueNode{}
	for _, el := range b.Decls {
		if v, ok := el.(*ast.EnumValueNode); ok {
			b_val[name(v.Name)] = v
		}
	}

	consumed := map[ast.Node]bool{}
	var body strings.Builder
	emit := func(s string) { body.WriteString("\n"); body.WriteString(s) }

	for _, el := range a.Decls {
		if v, ok := el.(*ast.EnumValueNode); ok {
			if bv, ok := b_val[name(v.Name)]; ok {
				consumed[bv] = true
				emit(m.b.LeadingAndText(bv))
				continue
			}
		}
		emit(m.a.LeadingAndText(el))
	}
	for _, el := range b.Decls {
		if consumed[el] {
			continue
		}
		switch el.(type) {
		case *ast.EnumValueNode, *ast.OptionNode:
			emit(m.b.LeadingAndText(el))
		}
	}

	return m.a.Leading(a) + m.a.Between(a, a.OpenBrace) + body.String() + "\n}"
}

// service merges B's rpcs onto A's, matching by name.
func (m *merger) service(a, b *ast.ServiceNode) string {
	b_rpc := map[string]*ast.RPCNode{}
	for _, el := range b.Decls {
		if r, ok := el.(*ast.RPCNode); ok {
			b_rpc[name(r.Name)] = r
		}
	}

	consumed := map[ast.Node]bool{}
	var body strings.Builder
	emit := func(s string) { body.WriteString("\n"); body.WriteString(s) }

	for _, el := range a.Decls {
		if r, ok := el.(*ast.RPCNode); ok {
			if br, ok := b_rpc[name(r.Name)]; ok {
				consumed[br] = true
				m.checkRPC(r, br)
				emit(m.b.LeadingAndText(br))
				continue
			}
		}
		emit(m.a.LeadingAndText(el))
	}
	for _, el := range b.Decls {
		if consumed[el] {
			continue
		}
		switch el.(type) {
		case *ast.RPCNode, *ast.OptionNode:
			emit(m.b.LeadingAndText(el))
		}
	}

	return m.a.Leading(a) + m.a.Between(a, a.OpenBrace) + body.String() + "\n}"
}

// --- conflict checks (only recorded; enforced by the public API in strict mode) ---

func (m *merger) checkField(a *ast.FieldNode, bn ast.Node) {
	b, ok := bn.(*ast.FieldNode)
	if !ok {
		return
	}
	an, b_num := tagVal(a.Tag), tagVal(b.Tag)
	a_name, b_name := name(a.Name), name(b.Name)
	switch {
	case an == b_num && a_name != b_name:
		m.conflict("field-renamed", a_name, fmt.Sprintf("number %d renamed %q -> %q", an, a_name, b_name), m.a, a)
	case an != b_num && a_name == b_name:
		m.conflict("field-renumbered", a_name, fmt.Sprintf("%q renumbered %d -> %d", a_name, an, b_num), m.a, a)
	case an == b_num && a_name == b_name:
		if at, bt := m.a.Text(a.FldType), m.b.Text(b.FldType); at != bt {
			m.conflict("field-retyped", a_name, fmt.Sprintf("type %s -> %s", at, bt), m.a, a)
		}
	}
}

func (m *merger) checkRPC(a, b *ast.RPCNode) {
	ai, bi := m.a.Text(a.Input), m.b.Text(b.Input)
	ao, bo := m.a.Text(a.Output), m.b.Text(b.Output)
	if ai != bi || ao != bo {
		m.conflict("rpc-signature", name(a.Name),
			fmt.Sprintf("%s returns %s -> %s returns %s", ai, ao, bi, bo), m.a, a)
	}
}

func (m *merger) checkEdition() {
	as, bs := firstNonEmpty(syntaxText(m.a)), firstNonEmpty(syntaxText(m.b))
	if as != "" && bs != "" && as != bs {
		m.conflict("edition", "<file>", fmt.Sprintf("%q vs %q", as, bs), m.b, editionNode(m.b))
	}
}

// --- small helpers ---

func name(n *ast.IdentNode) string {
	if n == nil {
		return ""
	}
	return n.Val
}

func tagVal(t *ast.UintLiteralNode) uint64 {
	if t == nil {
		return 0
	}
	return t.Val
}

func indexField(by_num map[uint64]ast.Node, by_name map[string]ast.Node, tag *ast.UintLiteralNode, nm *ast.IdentNode, node ast.Node) {
	if tag != nil {
		by_num[tag.Val] = node
	}
	if nm != nil && nm.Val != "" {
		by_name[nm.Val] = node
	}
}

// matchField finds the B element that overrides the A field identified by tag
// and name, preferring a number match over a name match. Already-consumed B
// elements are skipped so that a single B field is never adopted by more than
// one A field (which would otherwise duplicate it in the output).
func matchField(by_num map[uint64]ast.Node, by_name map[string]ast.Node, consumed map[ast.Node]bool, tag *ast.UintLiteralNode, nm *ast.IdentNode) (ast.Node, bool) {
	if tag != nil {
		if n, ok := by_num[tag.Val]; ok && !consumed[n] {
			return n, true
		}
	}
	if nm != nil {
		if n, ok := by_name[nm.Val]; ok && !consumed[n] {
			return n, true
		}
	}
	return nil, false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func syntaxText(f *protoast.File) string {
	if f.Node.Edition != nil {
		return f.Text(f.Node.Edition)
	}
	if f.Node.Syntax != nil {
		return f.Text(f.Node.Syntax)
	}
	return ""
}

func editionNode(f *protoast.File) ast.Node {
	if f.Node.Edition != nil {
		return f.Node.Edition
	}
	return f.Node.Syntax
}

func packageText(f *protoast.File) string {
	for _, d := range f.Node.Decls {
		if p, ok := d.(*ast.PackageNode); ok {
			return f.Text(p)
		}
	}
	return ""
}

// unionOptions collects file-level options from a then b, de-duplicated by
// option name (not by exact text) so that a singular option set to different
// values — or merely spaced differently — in both files collapses to one. The
// overlay (b) wins on a name collision. Position is first-seen.
func unionOptions(a, b *protoast.File) []string {
	var order []string
	val := map[string]string{}
	for _, f := range []*protoast.File{a, b} {
		for _, d := range f.Node.Decls {
			o, ok := d.(*ast.OptionNode)
			if !ok {
				continue
			}
			key := f.Text(o)
			if o.Name != nil {
				key = f.Text(o.Name)
			}
			if _, seen := val[key]; !seen {
				order = append(order, key)
			}
			val[key] = f.Text(o)
		}
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, val[k])
	}
	return out
}

// unionTopLevel collects the text of all top-level decls of type T from a then
// b, de-duplicated by exact text and preserving first-seen order.
func unionTopLevel[T ast.Node](a, b *protoast.File) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range []*protoast.File{a, b} {
		for _, d := range f.Node.Decls {
			if n, ok := d.(T); ok {
				t := f.Text(n)
				if !seen[t] {
					seen[t] = true
					out = append(out, t)
				}
			}
		}
	}
	return out
}
