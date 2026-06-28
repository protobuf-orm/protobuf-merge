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

| Construct                   | Match key         | Behavior                                                                       |
| --------------------------- | ----------------- | ------------------------------------------------------------------------------ |
| message / enum / service    | name              | same-named are merged; overlay-only are appended                               |
| field (incl. `map`)         | number, then name | base order kept; new fields appended; on a match the **overlay wins** in place |
| rpc                         | name              | new RPCs appended; a same-named rpc is **overwritten** (see below)             |
| oneof                       | name              | overlay's new entries appended into the matching `oneof`                       |
| enum value                  | name              | new values appended; on a match the overlay wins                               |
| nested message / enum       | name              | merged recursively                                                             |
| reserved / extension ranges | —                 | base's are preserved                                                           |
| imports / file options      | —                 | unioned (de-duplicated; file options by name, overlay wins)                    |
| comments                    | —                 | leading and trailing comments are preserved                                    |

The overlay may be an **incomplete fragment**: it need not declare a header and
may reference types it doesn't define.

### The `_` placeholder — keep part of the base

When overriding, use `_` where you want to **inherit the base** and only change
the rest:

- **rpc** — `rpc Get(_) returns (GetResponseV2);` keeps the base request type and
  changes only the response (and vice-versa). This lets you tweak one side of an
  existing rpc without restating the other.
- **field** — `int64 _ = 2;` keeps the base field's *name* for tag 2 and changes
  only its type/options. (The field is matched to the base by number.)

`_`-based overrides are intentional, so `-strict` does not flag them.

### Output ordering

Services are emitted first, then messages/enums in dependency order:

1. For each rpc, its **request and response are emitted consecutively** (unless
   one was already emitted), each immediately followed by the messages its
   fields reference, transitively.
2. Then any remaining **base** messages/enums in source order — and whenever a
   message's field references a not-yet-emitted message, that message is emitted
   right after it.
3. Then any remaining **overlay-only** messages/enums, likewise.

## Flags

| Flag        | Meaning                                                                                                                                                                                                      |
| ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `-o <path>` | write output to a file (default: stdout)                                                                                                                                                                     |
| `-strict`   | fail (with `file:line`) on an incompatible redefinition — a field number reused with a different name/type, or an edition mismatch — instead of letting the overlay win (explicit `_` overrides are allowed) |
| `-compact`  | use the formatter's dynamic layout (short bodies inline) instead of the default `buf`-style layout                                                                                                           |

## How it works

1. Parse both files with [`protocompile`](https://github.com/bufbuild/protocompile)'s
   full-fidelity AST (comments and positions preserved).
2. Use the AST as a semantic index and reconstruct a merged source string (base
   skeleton + overlay), carrying each retained/added node's verbatim text and
   comments.
3. Re-parse and pretty-print the result with protocompile's printer — the same
   engine `buf format` uses — so the output is clean regardless of how rough the
   inputs or the intermediate assembly are.

## Testing

Each scenario lives in `testdata/<case>/` as three files you can read:
`a.proto` (base), `b.proto` (overlay), and `want.proto` (expected merged
output). `go test ./...` merges every case and checks it against its
`want.proto` (and that the result parses and is idempotent). After an
intentional behavior change, regenerate the goldens and review the diff:

```shell
go test ./internal/merge -update
```

## Limitations

- Top-level declaration order follows the ordering rules above, not either
  input's literal order.
- `extend` blocks and extension ranges are passed through from the base but not
  deep-merged; a base-only `reserved` is kept but overlay `reserved` is not unioned.
- Field matching is scoped: an overlay top-level field is matched against base
  top-level fields, and overlay `oneof` entries against the matching base
  `oneof`. An overlay field that targets a base field living inside a `oneof`
  (or vice-versa) is not cross-matched and may yield a duplicate tag.
- Options are de-duplicated by name: a **file** option takes the overlay's value
  on a collision (overlay wins); an option on a message/enum/oneof keeps the
  **base's** value on a collision (base wins). The trailing comment on an
  element's own closing `}` line is not carried over.
- Inputs must be valid for the resulting edition. Overlaying constructs that are
  invalid under the base's edition (e.g. proto2 `required` onto an `edition`
  file, or string `reserved` names under editions) is reported as a format
  error on the merged text, not silently corrected.
