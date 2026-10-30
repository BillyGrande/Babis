# wire

The check-in contract between the Rust edge and the Go operator.

## Files

- `checkin.request.schema.json` - body of `POST /api/v1alpha1/checkin`.
- `checkin.response.schema.json` - the `200` response body.
- `testdata/` - golden fixtures. Go (`central/internal/wire`) and Rust
  (`edge/tests/contract.rs`) both run every fixture. The Go test also
  validates each fixture against the schemas, so schema and code cannot drift.

Both schemas are JSON Schema 2020-12 with `additionalProperties: true` at
every level. Every rule is in both implementations (`Validate()` in Go,
`validate()` in Rust) and in the schema, except the label-prefix length
limit (253), which is code-only.

## Fixture naming

The file name prefix sets the category. A file that matches none fails both tests.

| Prefix | Must |
|---|---|
| `request-*.json`, `response-*.json` | decode, validate, pass the schema, and round-trip (decode, encode, decode gives an equal value) |
| `invalid-request-*.json`, `invalid-response-*.json` | be rejected by `Validate()` (or a decode type error) and by the schema. The rest of the name says which rule it breaks. One rule per file. |

Keep response fixtures compact (single line): Go keeps raw JSON bytes for
`desired` and `extensions`, so whitespace would break round-trip equality.

## Rules for changes

1. Add or change a fixture before you change the types or the schema.
   Make the tests fail first, then make them pass.
2. After the first release, changes are additive only. Add optional fields;
   never remove, rename, or tighten an existing one. Old edges must keep
   working against a new operator, and the other way around.
3. Unknown fields are ignored on both sides. Never use
   `deny_unknown_fields` or Go `DisallowUnknownFields`.
4. Run `make contract` for a fast check. `make check` runs everything.
