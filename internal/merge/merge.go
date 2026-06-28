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

	"github.com/protobuf-orm/protobuf-merge/internal/format"
	"github.com/protobuf-orm/protobuf-merge/internal/protoast"
)

// Options configures a merge.
type Options struct {
	// Strict makes Merge return an error when the overlay redefines a base
	// element incompatibly (field number reused with a different name/type,
	// rpc signature change, edition mismatch) instead of silently letting the
	// overlay win.
	Strict bool
	// Compact uses the formatter's dynamic layout (short bodies inline)
	// instead of the default buf-compatible layout.
	Compact bool
}

// Merge parses base a and overlay b, merges b onto a, and returns formatted
// merged source. a_name/b_name are used for diagnostics only. The overlay may
// be an incomplete fragment (no header, unresolved type references).
func Merge(a_name string, a []byte, b_name string, b []byte, opts Options) ([]byte, error) {
	af, err := protoast.Parse(a_name, string(a))
	if err != nil {
		return nil, err
	}
	bf, err := protoast.Parse(b_name, string(b))
	if err != nil {
		return nil, err
	}

	assembled, conflicts := build(af, bf, opts)
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

// build merges b onto a and returns assembled (unformatted) merged source plus
// any conflicts detected. The result must be run through the formatter.
func build(a, b *protoast.File, opts Options) (string, []Conflict) {
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

// regEntry is a merged top-level message/enum with the names its fields
// reference (used to pull referenced messages in right after their referrer).
type regEntry struct {
	text string
	refs []string
}

// topLevel merges and orders the top-level declarations. Services are emitted
// first (base order, then overlay-only). Messages and enums are then ordered:
//   - for each rpc, its request and response are emitted consecutively (unless
//     one was already emitted), each followed by the messages its fields
//     reference, transitively; then
//   - any remaining base messages/enums in source order, each followed by what
//     it references; then
//   - any remaining overlay-only messages/enums, likewise.
//
// Extend blocks are appended last (base, then overlay-only).
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

	reg := map[string]*regEntry{}
	var a_order, b_order []string
	addEntry := func(nm, text string, refs []string, fromB bool) {
		if nm == "" || reg[nm] != nil {
			return
		}
		reg[nm] = &regEntry{text: text, refs: refs}
		if fromB {
			b_order = append(b_order, nm)
		} else {
			a_order = append(a_order, nm)
		}
	}

	consumed := map[ast.Node]bool{}
	var svc_blocks, extend_blocks []string
	var rpc_pairs [][2]string

	for _, d := range m.a.Node.Decls {
		switch n := d.(type) {
		case *ast.MessageNode:
			nm := name(n.Name)
			if bn, ok := b_msg[nm]; ok {
				consumed[bn] = true
				refs := append(referencedNames(m.a, n), referencedNames(m.b, bn)...)
				addEntry(nm, m.message(n, bn), refs, false)
			} else {
				addEntry(nm, m.a.LeadingAndText(n), referencedNames(m.a, n), false)
			}
		case *ast.EnumNode:
			nm := name(n.Name)
			if bn, ok := b_enum[nm]; ok {
				consumed[bn] = true
				addEntry(nm, m.enum(n, bn), nil, false)
			} else {
				addEntry(nm, m.a.LeadingAndText(n), nil, false)
			}
		case *ast.ServiceNode:
			if bn, ok := b_svc[name(n.Name)]; ok {
				consumed[bn] = true
				svc_blocks = append(svc_blocks, m.service(n, bn))
				rpc_pairs = append(rpc_pairs, m.rpcPairs(n, bn)...)
			} else {
				svc_blocks = append(svc_blocks, m.a.LeadingAndText(n))
				rpc_pairs = append(rpc_pairs, m.rpcPairs(n, nil)...)
			}
		case *ast.ExtendNode:
			extend_blocks = append(extend_blocks, m.a.LeadingAndText(n))
		}
	}
	for _, d := range m.b.Node.Decls {
		if consumed[d] {
			continue
		}
		switch n := d.(type) {
		case *ast.MessageNode:
			addEntry(name(n.Name), m.b.LeadingAndText(n), referencedNames(m.b, n), true)
		case *ast.EnumNode:
			addEntry(name(n.Name), m.b.LeadingAndText(n), nil, true)
		case *ast.ServiceNode:
			svc_blocks = append(svc_blocks, m.b.LeadingAndText(n))
			rpc_pairs = append(rpc_pairs, m.rpcPairs(nil, n)...)
		case *ast.ExtendNode:
			extend_blocks = append(extend_blocks, m.b.LeadingAndText(n))
		}
	}

	emitted := map[string]bool{}
	var order []string
	var emitMsg func(nm string)
	emitMsg = func(nm string) {
		e := reg[nm]
		if e == nil || emitted[nm] {
			return
		}
		emitted[nm] = true
		order = append(order, nm)
		for _, ref := range e.refs {
			emitMsg(ref)
		}
	}
	for _, p := range rpc_pairs {
		req, res := p[0], p[1]
		req_new := reg[req] != nil && !emitted[req]
		res_new := reg[res] != nil && !emitted[res]
		if req_new {
			emitted[req] = true
			order = append(order, req)
		}
		if res_new {
			emitted[res] = true
			order = append(order, res)
		}
		if req_new {
			for _, ref := range reg[req].refs {
				emitMsg(ref)
			}
		}
		if res_new {
			for _, ref := range reg[res].refs {
				emitMsg(ref)
			}
		}
	}
	for _, nm := range a_order {
		emitMsg(nm)
	}
	for _, nm := range b_order {
		emitMsg(nm)
	}

	blocks := svc_blocks
	for _, nm := range order {
		blocks = append(blocks, reg[nm].text)
	}
	return append(blocks, extend_blocks...)
}

// referencedNames returns the type names referenced by msg's fields (including
// fields inside oneofs, map value types, and nested messages), in first-seen
// order. Scalar type names are included but harmlessly ignored by the caller,
// which only follows names that resolve to a top-level message/enum.
func referencedNames(f *protoast.File, msg *ast.MessageNode) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	var walk func(decls []ast.MessageElement)
	walk = func(decls []ast.MessageElement) {
		for _, el := range decls {
			switch n := el.(type) {
			case *ast.FieldNode:
				if n.FldType != nil {
					add(f.Text(n.FldType))
				}
			case *ast.MapFieldNode:
				if n.MapType != nil && n.MapType.ValueType != nil {
					add(f.Text(n.MapType.ValueType))
				}
			case *ast.OneofNode:
				for _, oe := range n.Decls {
					if fld, ok := oe.(*ast.FieldNode); ok && fld.FldType != nil {
						add(f.Text(fld.FldType))
					}
				}
			case *ast.MessageNode:
				walk(n.Decls)
			}
		}
	}
	walk(msg.Decls)
	return out
}

// rpcPairs returns the (request, response) message-name pairs of the merged
// service in emission order: base rpcs (with overlay overrides resolved, incl.
// `_`), then overlay-only rpcs. Either service node may be nil.
func (m *merger) rpcPairs(a_svc, b_svc *ast.ServiceNode) [][2]string {
	b_rpc := map[string]*ast.RPCNode{}
	if b_svc != nil {
		for _, el := range b_svc.Decls {
			if r, ok := el.(*ast.RPCNode); ok {
				b_rpc[name(r.Name)] = r
			}
		}
	}
	done := map[string]bool{}
	var pairs [][2]string
	if a_svc != nil {
		for _, el := range a_svc.Decls {
			r, ok := el.(*ast.RPCNode)
			if !ok {
				continue
			}
			if br, ok := b_rpc[name(r.Name)]; ok {
				done[name(r.Name)] = true
				pairs = append(pairs, [2]string{m.rpcTypeName(r.Input, br.Input), m.rpcTypeName(r.Output, br.Output)})
			} else {
				pairs = append(pairs, [2]string{m.aTypeName(r.Input), m.aTypeName(r.Output)})
			}
		}
	}
	if b_svc != nil {
		for _, el := range b_svc.Decls {
			r, ok := el.(*ast.RPCNode)
			if !ok || done[name(r.Name)] {
				continue
			}
			pairs = append(pairs, [2]string{m.bTypeName(r.Input), m.bTypeName(r.Output)})
		}
	}
	return pairs
}

func (m *merger) aTypeName(t *ast.RPCTypeNode) string {
	if t != nil && t.MessageType != nil {
		return strings.TrimSpace(m.a.Text(t.MessageType))
	}
	return ""
}

func (m *merger) bTypeName(t *ast.RPCTypeNode) string {
	if t != nil && t.MessageType != nil {
		return strings.TrimSpace(m.b.Text(t.MessageType))
	}
	return ""
}

// rpcTypeName resolves the effective request/response name when base rpc type
// a_type is overridden by overlay b_type: `_` keeps the base, else the overlay.
func (m *merger) rpcTypeName(a_type, b_type *ast.RPCTypeNode) string {
	if b_type != nil && b_type.MessageType != nil {
		if t := strings.TrimSpace(m.b.Text(b_type.MessageType)); t != "_" {
			return t
		}
	}
	return m.aTypeName(a_type)
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
				emit(m.overrideField(name(n.Name), bel))
			} else {
				emit(m.a.LeadingAndText(n))
			}
		case *ast.MapFieldNode:
			if bel, ok := matchField(b_field_num, b_field_name, consumed, n.Tag, n.Name); ok {
				consumed[bel] = true
				emit(m.overrideField(name(n.Name), bel))
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
	a_opts := optionKeysOf(m.a, a.Decls)
	for _, el := range b.Decls {
		if consumed[el] {
			continue
		}
		switch e := el.(type) {
		case *ast.OptionNode:
			if !a_opts[optionKey(m.b, e)] {
				emit(m.b.LeadingAndText(e))
			}
		case *ast.FieldNode, *ast.MapFieldNode, *ast.OneofNode, *ast.MessageNode, *ast.EnumNode:
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
				emit(m.overrideField(name(f.Name), bel))
				continue
			}
		}
		emit(m.a.LeadingAndText(el))
	}
	a_opts := optionKeysOf(m.a, a.Decls)
	for _, el := range b.Decls {
		if consumed[el] {
			continue
		}
		switch e := el.(type) {
		case *ast.OptionNode:
			if !a_opts[optionKey(m.b, e)] {
				emit(m.b.LeadingAndText(e))
			}
		case *ast.FieldNode:
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
	a_opts := optionKeysOf(m.a, a.Decls)
	for _, el := range b.Decls {
		if consumed[el] {
			continue
		}
		switch e := el.(type) {
		case *ast.OptionNode:
			if !a_opts[optionKey(m.b, e)] {
				emit(m.b.LeadingAndText(e))
			}
		case *ast.EnumValueNode:
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
				emit(m.mergeRPC(r, br))
				continue
			}
		}
		emit(m.a.LeadingAndText(el))
	}
	a_opts := optionKeysOf(m.a, a.Decls)
	for _, el := range b.Decls {
		if consumed[el] {
			continue
		}
		switch e := el.(type) {
		case *ast.OptionNode:
			if !a_opts[optionKey(m.b, e)] {
				emit(m.b.LeadingAndText(e))
			}
		case *ast.RPCNode:
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
	if b_name == "_" {
		// Overlay explicitly keeps the base name; not a conflict.
		return
	}
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

// overrideField renders the overlay element bn that replaces a base field named
// a_name. If the overlay used `_` for the field name, the base name is kept and
// only the rest of the overlay definition (type, number, options) is applied.
func (m *merger) overrideField(a_name string, bn ast.Node) string {
	var nm *ast.IdentNode
	switch n := bn.(type) {
	case *ast.FieldNode:
		nm = n.Name
	case *ast.MapFieldNode:
		nm = n.Name
	}
	if nm == nil || nm.Val != "_" {
		return m.b.LeadingAndText(bn)
	}
	s := m.b.Leading(bn) + m.b.TextReplacing(bn, nm, a_name)
	if t := m.b.Trailing(bn); t != "" {
		s += " " + t
	}
	return s
}

// mergeRPC renders the overlay rpc b overriding the base rpc a (same name). A
// `_` request/response message name in the overlay keeps the base type; any
// other name uses the overlay's type. The overlay's method body (or bare `;`)
// is used.
func (m *merger) mergeRPC(a, b *ast.RPCNode) string {
	in := m.rpcSide(a.Input, b.Input)
	out := m.rpcSide(a.Output, b.Output)
	tail := ";"
	if b.OpenBrace != nil && b.CloseBrace != nil {
		tail = " " + m.b.Between(b.OpenBrace, b.CloseBrace)
	}
	return m.b.Leading(b) + "rpc " + name(b.Name) + " " + in + " returns " + out + tail
}

// rpcSide returns the source text of the chosen request/response type node: the
// base side when the overlay used `_`, otherwise the overlay side.
func (m *merger) rpcSide(a_type, b_type *ast.RPCTypeNode) string {
	if b_type != nil && b_type.MessageType != nil && m.b.Text(b_type.MessageType) == "_" {
		if a_type != nil {
			return m.a.Text(a_type)
		}
	}
	if b_type != nil {
		return m.b.Text(b_type)
	}
	if a_type != nil {
		return m.a.Text(a_type)
	}
	return "()"
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

// optionKey identifies an option by its name (e.g. "go_package"), falling back
// to its full text for anonymous/compact forms.
func optionKey(f *protoast.File, o *ast.OptionNode) string {
	if o.Name != nil {
		return f.Text(o.Name)
	}
	return f.Text(o)
}

// optionKeysOf returns the set of option names declared directly in decls.
func optionKeysOf[T ast.Node](f *protoast.File, decls []T) map[string]bool {
	keys := map[string]bool{}
	for _, d := range decls {
		if o, ok := any(d).(*ast.OptionNode); ok {
			keys[optionKey(f, o)] = true
		}
	}
	return keys
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
			key := optionKey(f, o)
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
