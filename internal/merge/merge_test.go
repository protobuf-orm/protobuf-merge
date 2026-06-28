package merge

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/protobuf-orm/protobuf-merge/internal/protoast"
)

// Run `go test ./internal/merge -update` to (re)generate the want.proto golden
// files after an intentional behavior change, then review the diff.
var update = flag.Bool("update", false, "regenerate testdata want.proto golden files")

const testdataDir = "../../testdata"

func casePath(name, file string) string { return filepath.Join(testdataDir, name, file) }

func readCase(t *testing.T, name string) (a, b []byte) {
	t.Helper()
	a, err := os.ReadFile(casePath(name, "a.proto"))
	if err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(casePath(name, "b.proto"))
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

// caseNames returns every testdata/<name> directory that has both inputs.
func caseNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(testdataDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(casePath(e.Name(), "a.proto")); err != nil {
			continue
		}
		if _, err := os.Stat(casePath(e.Name(), "b.proto")); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

// TestGolden merges testdata/<case>/{a,b}.proto and compares the result against
// the checked-in want.proto. Each case is self-describing: read a.proto, b.proto
// and want.proto to see exactly what is merged and what comes out.
func TestGolden(t *testing.T) {
	for _, name := range caseNames(t) {
		t.Run(name, func(t *testing.T) {
			a, b := readCase(t, name)

			got, err := Merge("a.proto", a, "b.proto", b, Options{})
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}

			wantPath := casePath(name, "want.proto")
			if *update {
				if err := os.WriteFile(wantPath, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}

			want, err := os.ReadFile(wantPath)
			if err != nil {
				t.Fatalf("read want.proto (run `go test -update` to generate): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("merged output != want.proto\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}

			// The merged output must always be valid, parseable proto.
			if _, err := protoast.Parse("want.proto", string(got)); err != nil {
				t.Errorf("merged output does not parse: %v", err)
			}

			// Re-applying the same overlay must change nothing (idempotent).
			again, err := Merge("a.proto", got, "b.proto", b, Options{})
			if err != nil {
				t.Errorf("re-merge errored: %v", err)
			} else if string(again) != string(got) {
				t.Errorf("merge is not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got, again)
			}
		})
	}
}

// TestStrict checks which testdata cases -strict accepts and which it rejects.
func TestStrict(t *testing.T) {
	// Overlay incompatibly redefines a base element: -strict must fail.
	mustError := []string{"override-field"}
	// Pure additions and explicit `_` overrides are intentional: -strict allows them.
	mustPass := []string{"add-field", "two-messages", "field-underscore", "rpc-override-response"}

	for _, name := range mustError {
		t.Run("reject/"+name, func(t *testing.T) {
			a, b := readCase(t, name)
			if _, err := Merge("a.proto", a, "b.proto", b, Options{Strict: true}); err == nil {
				t.Errorf("expected -strict to reject %q", name)
			}
		})
	}
	for _, name := range mustPass {
		t.Run("allow/"+name, func(t *testing.T) {
			a, b := readCase(t, name)
			if _, err := Merge("a.proto", a, "b.proto", b, Options{Strict: true}); err != nil {
				t.Errorf("expected -strict to allow %q, got: %v", name, err)
			}
		})
	}
}
