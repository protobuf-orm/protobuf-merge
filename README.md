# protobuf-merge

Semantically merge two `.proto` files.

Unlike a textual concatenation, `protobuf-merge` understands protobuf structure:
given a **base** and an **overlay**, it overlays the overlay's declarations onto
the base — appending new fields/RPCs/messages and overriding matching ones —
then re-emits clean, `buf`-formatted source.

```shell
protobuf-merge base.proto overlay.proto > merged.proto
```

## Semantics

The **base** (first argument) provides the file header (`edition`/`syntax`,
`package`) and the ordering skeleton. The **overlay** (second argument) is laid
on top:

| Construct | Match key | Behavior |
|---|---|---|
| message / enum / service | name | same-named are merged; overlay-only are appended |
| field (incl. `map`) | number, then name | base order kept; new fields appended; on a match the **overlay wins** in place |
| rpc | name | new RPCs appended; on a match the overlay wins |
| oneof | name | overlay's new entries appended into the matching `oneof` |
| enum value | name | new values appended; on a match the overlay wins |
| nested message / enum | name | merged recursively |
| reserved / extension ranges | — | base's are preserved |
| imports / file options | — | unioned (de-duplicated) |
| comments | — | leading and trailing comments are preserved |

The overlay may be an **incomplete fragment**: it need not declare a header and
may reference types it doesn't define. Declaration ordering in the output is not
guaranteed to match either input — the result is reformatted for cleanliness.

## Flags

| Flag | Meaning |
|---|---|
| `-o <path>` | write output to a file (default: stdout) |
| `-strict` | fail (with `file:line`) on an incompatible redefinition — a field number reused with a different name/type, an RPC signature change, or an edition mismatch — instead of letting the overlay win |
| `-compact` | use the formatter's dynamic layout (short bodies inline) instead of the default `buf`-style layout |

## How it works

1. Parse both files with [`protocompile`](https://github.com/bufbuild/protocompile)'s
   full-fidelity AST (comments and positions preserved).
2. Use the AST as a semantic index and reconstruct a merged source string (base
   skeleton + overlay), carrying each retained/added node's verbatim text and
   comments.
3. Re-parse and pretty-print the result with protocompile's printer — the same
   engine `buf format` uses — so the output is clean regardless of how rough the
   inputs or the intermediate assembly are.

## Limitations

- Output declaration order is not preserved (by design).
- `extend` blocks and extension ranges are passed through from the base but not
  deep-merged; a base-only `reserved` is kept but overlay `reserved` is not unioned.
- Field matching is scoped: an overlay top-level field is matched against base
  top-level fields, and overlay `oneof` entries against the matching base
  `oneof`. An overlay field that targets a base field living inside a `oneof`
  (or vice-versa) is not cross-matched and may yield a duplicate tag.
- Message/enum/oneof-level options are not de-duplicated (only **file**-level
  options are): if the base and overlay set the same option on the same element,
  both survive. The trailing comment on an element's own closing `}` line is not
  carried over.
- Inputs must be valid for the resulting edition. Overlaying constructs that are
  invalid under the base's edition (e.g. proto2 `required` onto an `edition`
  file, or string `reserved` names under editions) is reported as a format
  error on the merged text, not silently corrected.
