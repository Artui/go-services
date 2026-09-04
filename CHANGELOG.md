# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each module in this repository is versioned and tagged independently. Entries
below cover the kernel module, `github.com/Artui/go-services`.

## [Unreleased]

### Added

- **A worked example, `example/`, running against a real database.** One
  registry -- a library lending service over `database/sql` and SQLite -- mounted
  on all three adapters at once, so the kernel's ordering rule has a composed
  proof for the first time. The conformance suite cannot supply one: it runs
  with an empty `Deps` and no database, so nothing in it can tell a correct
  transaction boundary from a broken one. Like `conformance`, the module is
  never published and carries `replace` directives.
- **The example carries its own falsification.** `TestRollbackAssertionHasTeeth`
  runs the same dispatch against a registry whose dependencies resolve *outside*
  the boundary and asserts the orphan row appears, which is what makes the
  rollback assertion beside it mean something. Its companion asserts the other
  half: on the happy path the correct registry and the broken one are
  indistinguishable.
- `example/FRICTION.md`, which is the module's actual output -- six findings
  written down where they were met, and four things that were expected to hurt
  and did not.

## Adapters

Each adapter is its own module and carries its own tag line. All three were
released at **v0.1.0** on 2026-09-04, against kernel v0.3.0, once the
conformance suite could show they answer the same.

- **`httpx/v0.1.0`** -- mounts a registry on `net/http`'s `ServeMux`, with no
  third-party dependency, so it also reaches chi and echo.
- **`ginx/v0.1.0`** -- the same over a Gin router.
- **`mcpx/v0.1.0`** -- exposes a registry as MCP tools through the official SDK,
  handing it the schema the kernel already reflected rather than a second one.

`conformance` is a fifth module and is deliberately never tagged: it depends on
all of the others and exists only to fail when two of them disagree.

## [0.3.0] - 2026-09-03

Everything here came from three independent reviews, one per adapter, by
reviewers who had not written the code they read.

### Fixed

- **A request body's numbers are no longer rewritten.** `EncodeParams`
  re-encodes the body it is given, and decoding into an `any` turns every number
  into a `float64`: an identifier of 9007199254740993 was re-encoded as ...992,
  and the literal `1.0` as `1`. The path only ran when a request carried a
  parameter, so the same payload behaved differently depending on the shape of
  the route it arrived at -- exact with no captures, rounded with a query
  string, and in the `1.0` case a 400 in one and a 201 in the other. This is the
  hazard `DispatchValue`'s own documentation warns callers about, reintroduced
  one function away from that warning.
- **A nil `*ValidationError` no longer panics.** A helper typed to return
  `*ValidationError`, assigned into an `error`, produces a non-nil error holding
  a nil pointer, and `errors.As` matches it -- so an adapter's validation arm
  handed a nil receiver to a renderer. `FieldMap` and `Error` now tolerate one.
  On a transport whose server recovers per connection this was a 500; on the MCP
  adapter, whose handler runs on a goroutine nobody recovers, it ended the
  process.
- **An explicit JSON `null` argument set is accepted.** Absent and empty already
  meant "nothing was sent" and `null` did not, so a client rendering no
  arguments as `null` was refused with a schema error naming a type and no field
  to correct.
- **A `Spec.Status` outside 200-599 is refused at registration.** A 1xx is an
  interim response: the server writes it, does not commit, and the next write
  commits an implicit 200 behind it, so the status was a promise nothing could
  keep. Both HTTP adapters had grown their own range check after finding this
  independently, and for a while the two disagreed.

### Added

- **`ValidSuccessStatus`**, because three places needed the same answer and two
  of them had already diverged. Its documentation carries the evidence, gathered
  over a real server with `httptrace`: an `httptest.ResponseRecorder` reports the
  1xx faithfully while hiding what the wire does, which is how this survived
  review on both adapters.

### Changed

- The coverage gate keys on the full import path rather than the base name. One
  exclusions file serves every module, so the single kernel entry would have
  silently exempted a function of that name in any module that grew one.

## [0.2.0] - 2026-09-03

Everything here came from building the first three adapters against v0.1.0.
Most of it is places the kernel described a rule instead of applying it, which
is what a first consumer is for.

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
- **`ErrConfiguration`**, for a fault in how an operation was mounted rather
  than in the request. An undeclared route capture was reported as a
  `ValidationError`, which put an operator's diagnostic on the client's channel
  where an adapter could not tell it from a genuine client error. `StatusFor`
  answers 500: no change the caller makes would help.
- **`Entry.CheckCaptures`**, moving that same guarantee to configuration time.
  A route table naming a capture the operation cannot receive is broken in
  every request it will serve, so an adapter that knows its patterns refuses it
  at startup. The dispatch-time check remains for a handler placed on a router
  the adapter cannot inspect.

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

[Unreleased]: https://github.com/Artui/go-services/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/Artui/go-services/releases/tag/v0.3.0
[0.2.0]: https://github.com/Artui/go-services/releases/tag/v0.2.0
[0.1.0]: https://github.com/Artui/go-services/releases/tag/v0.1.0
