package protomerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/protobuf-orm/protobuf-merge/internal/protoast"
)

// mustMerge merges and fails the test on error.
func mustMerge(t *testing.T, a, b string, opts Options) string {
	t.Helper()
	out, err := Merge("a.proto", []byte(a), "b.proto", []byte(b), opts)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	// Every successful merge must produce parseable proto.
	if _, err := protoast.Parse("out.proto", string(out)); err != nil {
		t.Fatalf("merged output does not parse: %v\n%s", err, out)
	}
	return string(out)
}

// wantContains asserts every needle appears in haystack.
func wantContains(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("output missing %q in:\n%s", n, haystack)
		}
	}
}

// TestTestdataCases merges every input pair under testdata/ and checks the
// output parses and contains the union of A's and B's distinctive elements.
// The c.proto files in the source repo are NOT golden, so we assert semantic
// properties rather than byte equality.
func TestTestdataCases(t *testing.T) {
	cases := map[string][]string{
		"add-field":             {"uint64 id = 1;", "string name = 2;"},
		"add-multiple-fields":   {"uint64 id = 1;"},
		"add-repeated-field":    {"uint64 id = 1;"},
		"add-map-field":         {"map<string, string> labels = 3;"},
		"add-field-with-option": {"uint64 id = 1;"},
		"two-messages":          {"message Foo", "message Bar", "string name = 2;", "string label = 2;"},
		"add-field-to-oneof":    {"oneof key", "uint64 id = 1;", "string slug = 2;", "string uuid = 3;"},
		"add-method":            {"rpc FooGet", "rpc FooAdd"},
		"add-message":           {"message Foo", "message Bar"},
		"with-comment":          {"rpc FooAdd", "// FooAdd insert a new Foo.", "message FooAddRequest"},
	}
	for name, needles := range cases {
		t.Run(name, func(t *testing.T) {
			a, err := os.ReadFile(filepath.Join("testdata", name, "a.proto"))
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join("testdata", name, "b.proto"))
			if err != nil {
				t.Fatal(err)
			}
			out := mustMerge(t, string(a), string(b), Options{})
			wantContains(t, out, needles...)
		})
	}
}

const base = "edition = \"2023\";\npackage sample;\n"

func TestEnumMerge(t *testing.T) {
	a := base + "enum Color {\n  COLOR_UNSPECIFIED = 0;\n  COLOR_RED = 1;\n}\n"
	b := "enum Color {\n  COLOR_GREEN = 2;\n}\n"
	out := mustMerge(t, a, b, Options{})
	wantContains(t, out, "COLOR_UNSPECIFIED = 0;", "COLOR_RED = 1;", "COLOR_GREEN = 2;")
}

func TestNestedMessageMerge(t *testing.T) {
	a := base + "message Outer {\n  message Inner {\n    uint64 id = 1;\n  }\n  uint64 x = 1;\n}\n"
	b := "message Outer {\n  message Inner {\n    string name = 2;\n  }\n  string y = 2;\n}\n"
	out := mustMerge(t, a, b, Options{})
	// Inner gains name=2; Outer gains y=2; existing members survive.
	wantContains(t, out, "message Inner", "uint64 id = 1;", "string name = 2;", "uint64 x = 1;", "string y = 2;")
}

func TestNestedEnumMerge(t *testing.T) {
	a := base + "message Outer {\n  enum Kind {\n    KIND_UNSPECIFIED = 0;\n  }\n}\n"
	b := "message Outer {\n  enum Kind {\n    KIND_A = 1;\n  }\n}\n"
	out := mustMerge(t, a, b, Options{})
	wantContains(t, out, "KIND_UNSPECIFIED = 0;", "KIND_A = 1;")
}

func TestReservedPreserved(t *testing.T) {
	a := base + "message Foo {\n  reserved 2, 3;\n  uint64 id = 1;\n}\n"
	b := "message Foo {\n  string name = 4;\n}\n"
	out := mustMerge(t, a, b, Options{})
	wantContains(t, out, "reserved 2, 3;", "uint64 id = 1;", "string name = 4;")
}

func TestBWinsOverride(t *testing.T) {
	// B redefines field number 2 with a different type; B wins by default.
	a := base + "message Foo {\n  uint64 id = 1;\n  string name = 2;\n}\n"
	b := "message Foo {\n  int64 name = 2;\n}\n"
	out := mustMerge(t, a, b, Options{})
	wantContains(t, out, "int64 name = 2;")
	if strings.Contains(out, "string name = 2;") {
		t.Errorf("expected B to override A's field type, but A's field survived:\n%s", out)
	}
}

func TestStrictConflictErrors(t *testing.T) {
	a := base + "message Foo {\n  string name = 2;\n}\n"
	b := "message Foo {\n  int64 name = 2;\n}\n"
	if _, err := Merge("a.proto", []byte(a), "b.proto", []byte(b), Options{Strict: true}); err == nil {
		t.Fatal("expected strict mode to reject the incompatible redefinition")
	}
}

func TestStrictAllowsCompatibleAddition(t *testing.T) {
	// Pure addition is not a conflict; strict mode must allow it.
	a := base + "message Foo {\n  uint64 id = 1;\n}\n"
	b := "message Foo {\n  string name = 2;\n}\n"
	out := mustMerge(t, a, b, Options{Strict: true})
	wantContains(t, out, "uint64 id = 1;", "string name = 2;")
}

func TestIdempotent(t *testing.T) {
	a := base + "message Foo {\n  uint64 id = 1;\n}\n"
	b := "message Foo {\n  string name = 2;\n}\n"
	once := mustMerge(t, a, b, Options{})
	twice := mustMerge(t, once, b, Options{})
	if once != twice {
		t.Errorf("merge not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func countSub(s, sub string) int {
	n, i := 0, 0
	for {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return n
		}
		n++
		i += j + len(sub)
	}
}

// A B field that matches one A field by name and a different A field by number
// must not be emitted twice (regression for the double-match bug).
func TestNoDuplicateBFieldEmit(t *testing.T) {
	a := base + "message M {\n  string a = 1;\n  string b = 2;\n}\n"
	b := "message M {\n  string a = 2;\n}\n"
	out := mustMerge(t, a, b, Options{})
	if got := countSub(out, "a = 2"); got != 1 {
		t.Errorf("expected B's field emitted exactly once, got %d times:\n%s", got, out)
	}
	wantContains(t, out, "string b = 2;") // A's own field is not lost
}

// A singular file option present in both files (possibly spaced differently)
// must collapse to a single declaration, with the overlay winning.
func TestFileOptionDedup(t *testing.T) {
	a := "edition = \"2023\";\noption go_package = \"a/pkg\";\nmessage Foo {\n  uint64 id = 1;\n}\n"
	b := "option go_package = \"b/pkg\";\nmessage Foo {\n  string name = 2;\n}\n"
	out := mustMerge(t, a, b, Options{})
	if got := countSub(out, "go_package"); got != 1 {
		t.Errorf("expected one go_package option, got %d:\n%s", got, out)
	}
	wantContains(t, out, "b/pkg")
	if strings.Contains(out, "a/pkg") {
		t.Errorf("expected overlay option to win:\n%s", out)
	}
}

// A B-only option declared inside a merged message must be preserved.
func TestBOnlyMessageOptionPreserved(t *testing.T) {
	a := base + "message Foo {\n  uint64 id = 1;\n}\n"
	b := "message Foo {\n  option deprecated = true;\n  string name = 2;\n}\n"
	out := mustMerge(t, a, b, Options{})
	wantContains(t, out, "option deprecated = true", "string name = 2;", "uint64 id = 1;")
}

func TestTrailingCommentPreserved(t *testing.T) {
	a := base + "message Foo {\n  uint64 id = 1; // the id\n}\n"
	b := "message Foo {\n  string name = 2; // the name\n}\n"
	out := mustMerge(t, a, b, Options{})
	wantContains(t, out, "// the id", "// the name")
}

func TestHeaderlessOverlay(t *testing.T) {
	// B is a bare fragment with no edition/package and an unresolved type ref.
	a := base + "message Foo {\n  uint64 id = 1;\n}\n"
	b := "message Foo {\n  Bar ref = 2;\n}\n"
	out := mustMerge(t, a, b, Options{})
	wantContains(t, out, "edition = \"2023\";", "package sample;", "Bar ref = 2;")
}
