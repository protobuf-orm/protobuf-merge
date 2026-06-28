// Command protobuf-merge semantically merges two .proto files: it overlays
// <overlay> onto <base> and prints the formatted result.
//
//	protobuf-merge [flags] <base.proto> <overlay.proto>
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/protobuf-orm/protobuf-merge/internal/merge"
)

func main() {
	out := flag.String("o", "", "write merged output to this file (default: stdout)")
	strict := flag.Bool("strict", false, "fail on incompatible redefinitions instead of letting the overlay win")
	compact := flag.Bool("compact", false, "use compact (dynamic) formatting instead of the default buf-style layout")
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() != 2 {
		usage()
		os.Exit(2)
	}

	base_path, overlay_path := flag.Arg(0), flag.Arg(1)
	base, err := os.ReadFile(base_path)
	check(err)
	overlay, err := os.ReadFile(overlay_path)
	check(err)

	merged, err := merge.Merge(base_path, base, overlay_path, overlay, merge.Options{
		Strict:  *strict,
		Compact: *compact,
	})
	check(err)

	if *out == "" {
		if _, err := os.Stdout.Write(merged); err != nil {
			check(err)
		}
		return
	}
	check(os.WriteFile(*out, merged, 0o644))
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: protobuf-merge [flags] <base.proto> <overlay.proto>

Overlay the overlay file onto the base file and print the formatted result.
The base provides the file header (edition/syntax, package); the overlay's
declarations are merged in by name (fields matched by number, then name), with
the overlay winning on collisions. The overlay may be an incomplete fragment.

flags:
`)
	flag.PrintDefaults()
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "protobuf-merge:", err)
		os.Exit(1)
	}
}
