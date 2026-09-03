# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each module in this repository is versioned and tagged independently. Entries
below cover the kernel module, `github.com/Artui/go-services`.

## [Unreleased]

Everything here came from building the first three adapters against v0.1.0.
Seven of the eight are places the kernel described a rule instead of applying
it, which is what a first consumer is for.

### Changed

- **`EncodeParams` takes its two parameter sources separately and merges them
  itself**: `EncodeParams(schema, body, query, captures)`. It previously took
  one already-merged map and stated the precedence in a comment, which makes a
  security rule the adapter's job. An operation mounted at
  `/tenants/{tenant}/invoices` can be rescoped by a client with `?tenant=other`
  if an adapter merges the other way, and nothing fails when it does.
  **Breaking.**
- **A route capture the schema does not declare is refused** rather than
  silently dropped. That same operation, against a spec declaring no `tenant`
  field, previously ran completely unscoped and returned no error. Query
  parameters are still dropped: a query string carries analytics noise nobody
  declared, while a capture was written into the pattern by hand and is always
  load-bearing.
- **A body that is valid JSON but not an object passes through** for the schema
  to reject. It was reported as malformed, and only when a parameter happened to
  trigger the parse, so one client mistake drew two explanations depending on
  whether the route captured anything.
- **`Register` detaches a spec's declared `Idempotent` and `Destructive`** from
  the caller's variables. It cloned `Tags` but stored the bool pointers as
  given, so a later write rewrote what adapters had already advertised.
- **`DispatchValue`'s documentation no longer names a caller that does not
  exist.** It claimed to be the shape the MCP SDK hands a non-generic tool
  handler; the SDK carries those arguments as a `json.RawMessage`. Following the
  old comment lost data, because a `map[string]any` decoded from JSON has
  already rounded every integer past 2^53 into a float64.

### Added

- **`Spec.Destructive`**, three-state like `Idempotent`. `Kind` cannot tell a
  create from a delete, and MCP's `destructiveHint` defaults to true for
  anything not read-only, so every mutation was advertised as possibly
  destructive and approval policies keyed on that hint over-prompted.
- **`Kind.AllowsMethod`**, applying the rule `Kind`'s own documentation had been
  promising. Left to the adapters, the agreed rule accepted both a query on
  DELETE and a mutation on HEAD.
- **`ValidationError.FieldMap`**, which never returns nil, so `{"errors": null}`
  can no longer reach a client.
- **`ErrBodyTooLarge` and `DefaultMaxBodyBytes`.** An unbounded read is a denial
  of service, and the ceiling plus the status a breach answers with is one
  decision rather than a per-adapter knob.
- **`StatusFor`, `StatusBodyTooLarge`, `InternalErrorText`,
  `UnreadableBodyText` and `BodyTooLargeText`** -- the HTTP projection of the
  error taxonomy. The taxonomy was shared and its projection was not, so two
  adapters carried their own copies of a client-visible contract.

## [0.1.0] - 2026-09-03

### Added

- The kernel: `Spec`, `Registry`, `Register`, `MustRegister`, `Dispatch`,
  `DispatchValue`, `Entry` and `Result`.
- Three validation layers, applied by the kernel on every transport: the
  reflected JSON Schema, an optional `Validate() error` on the input type, and
  `Spec.Permit` for rules that need resolved dependencies.
- `Optional[T]`, which distinguishes an omitted field from a field sent as its
  zero value, and composes with pointers for the explicitly-null case.
- `SchemaFor`, letting a type declare the schema it marshals to. Reflection does
  not consult `json.Marshaler` for arbitrary types, so without this a wrapper
  advertises its own internals.
- `WithAtomic`, which takes a transaction callback so the kernel names no
  database driver. Dependencies resolve inside the transaction.
- `EncodeParams`, schema-directed coercion of flat string parameters, so a query
  string reaches a service as the types its schema declares.
- Framework-agnostic errors: `ErrNotFound`, `ErrConflict`, `ErrPermission` and
  `ValidationError`.

[Unreleased]: https://github.com/Artui/go-services/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Artui/go-services/releases/tag/v0.1.0
