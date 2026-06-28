package format

import (
	"strings"
	"testing"
)

func TestFormatNormalizesMessyInput(t *testing.T) {
	// Deliberately rough: mixed indentation, stray tabs, trailing space, no
	// blank line between messages, comment with trailing space.
	const messy = "edition = \"2023\";\n" +
		"package sample;\n" +
		"message Foo {\n" +
		"\t\tuint64 id = 1;\n" +
		"  // a name \n" +
		"      string name = 2;   \n" +
		"}\n" +
		"message Bar {\n" +
		"string label = 2;\n" +
		"}\n"

	out, err := Format("messy.proto", messy, Legacy)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	if strings.Contains(out, "\t\t") {
		t.Errorf("expected normalized indentation, got:\n%s", out)
	}
	if strings.Contains(out, "name \n") || strings.Contains(out, "2;   \n") {
		t.Errorf("expected trailing whitespace stripped, got:\n%q", out)
	}
	for _, want := range []string{"message Foo", "uint64 id = 1;", "string name = 2;", "message Bar"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	t.Logf("formatted:\n%s", out)
}

func TestFormatIsIdempotent(t *testing.T) {
	const src = "edition = \"2023\";\npackage sample;\nmessage Foo {\n  uint64 id = 1;\n}\n"
	once, err := Format("a.proto", src, Legacy)
	if err != nil {
		t.Fatalf("Format once: %v", err)
	}
	twice, err := Format("a.proto", once, Legacy)
	if err != nil {
		t.Fatalf("Format twice: %v", err)
	}
	if once != twice {
		t.Errorf("Format not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}
