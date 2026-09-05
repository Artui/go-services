# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each module in this repository is versioned and tagged independently. Entries
below cover the kernel module, `github.com/Artui/go-services`.

## Adapters

Each adapter is its own module and carries its own tag line. All three were
released at **v0.1.0** on 2026-09-04, against kernel v0.3.0, once the
conformance suite could show they answer the same.

- **`httpx/v0.1.0`** -- mounts a registry on `net/http`'s `ServeMux`, with no
  third-party dependency, so it also reaches chi and echo.
- **`ginx/v0.1.0`** -- the same over a Gin router.
- **`mcpx/v0.1.0`** -- exposes a registry as MCP tools through the official SDK,
  handing it the schema the kernel already reflected rather than a second one.

`httpx` and `ginx` move to **v0.2.0** on 2026-09-05, gaining `Route.Location`
and `WithLocation`. `mcpx` moves to **v0.1.2** the same day with no change but
its floor.

Every adapter floors on the newest kernel, including one that uses nothing the
release added. The alternative leaves that adapter's CI resolving a kernel no
consumer runs: Go takes the maximum across a build, so anyone pairing two
adapters gets the newest kernel regardless, and the low floor only ever applied
to the single-adapter case while the paired case went untested.

All three moved to **v0.1.1** on 2026-09-04, raising their kernel floor from
v0.3.0 to v0.4.0. That raise is the whole release: until it happened, installing
an adapter resolved the old kernel, so the error wording v0.4.0 corrected still
reached every client of every adapter. Nothing else changed in any of them.

`adkx` is unreleased. It publishes a registry as tools for Google's Agent
Development Kit, and it is the same trade `mcpx` makes: `adk-go` reflects its
schemas with `jsonschema-go` at the version this kernel uses, and
`genai.FunctionDeclaration` carries a `ParametersJsonSchema` field, so the
schema the kernel already reflected is handed over as it is.

`conformance` floors at Go 1.26.6 now, the highest of the modules it drives,
which is `adkx`.

`conformance` and `example` are the two modules here that are deliberately never
tagged: they depend on all of the others, and exist to fail when two transports
disagree and when a transaction boundary is wrong.

## [Unreleased]

### Added

- **`adkx` has a column in the conformance suite**, which is what makes it a
  fourth transport rather than a fourth thing that happens to compile. It is
  compared against HTTP on what every transport can express: whether the call
  failed, the value it produced, the per-field messages of a rejected input, and
  that nothing an operator wrote reached the client.

  Falsified on both axes rather than assumed. Removing `adkx`'s redaction fails
  it with `adkx leaked the internal error text`; dropping a field from its
  result fails it with the two maps printed side by side.

  **One thing the column deliberately does not cover** is written into the
  driver: in production ADK hands a tool a `map[string]any` it decoded itself,
  so a large identifier is already a `float64`, while this harness calls the
  tool directly with the case table's own `int64`. Reproducing that would mean
  standing up an agent and a model to demonstrate a rounding `adkx` cannot
  prevent. The limit is recorded so a green suite is not read as its absence.

- **`adkx`, a fourth adapter: a registry as tools for Google's ADK.** The
  declaration carries the kernel's own `*jsonschema.Schema` object, asserted by
  pointer identity rather than by comparison, so the schema a model is shown and
  the schema the kernel enforces cannot drift apart.

  Two facts about that wire are documented on the package because neither is
  fixable here. **Arguments arrive as a map**: `genai.FunctionCall.Args` is a
  `map[string]any`, so a model's tool-call arguments are decoded before any tool
  in any ADK program sees them and an integer past 2^53 is already a float64 --
  the same registry is exact over MCP, whose SDK carries the raw JSON, and lossy
  here. **And a tool's error text is shown to the model**: ADK renders a
  returned error as `map[string]any{"error": err.Error()}`, so this package
  returns the kernel's taxonomy verbatim and replaces everything else with a
  fixed sentence, sending the real error to `WithErrorReporter`.

  It floors at Go 1.26.6, `adk-go`'s own floor and the highest in the
  repository. That is the module-per-boundary layout paying out: a consumer who
  wants an HTTP route and not an agent framework is not dragged onto it.

- **The kernel-import guard knows about ADK.** It matched `net/http`,
  `gin-gonic` and `modelcontextprotocol`, so a kernel file could have imported
  `adk` or `genai` and nothing would have fired. Verified by making it fire.

- **`Route.Location` and `WithLocation` on both HTTP adapters.** A successful
  response carries a `Location` built from a template naming the operation's
  output fields -- `"/loans/{loan_id}"` -- which is what a 201 is specified to
  do and what `Result` had no channel for. `mcpx` gains nothing: MCP has no
  headers, and inventing somewhere to put one would be an HTTP concept on a wire
  that does not want it.

  The filling is the kernel's `ExpandLocation`, so both adapters answer the same
  path for the same output. A template naming a field the output schema does not
  declare is refused when the handler is built.

- **The conformance suite compares the `Location` header.** It is not a
  formality: the two adapters build it at different points -- `httpx` expands
  before marshalling and clears the header if the marshal fails, `ginx` marshals
  first and never expands -- so they reach the same answer by different routes.
  Both are mounted over a value that cannot be encoded, and moving `ginx`'s
  header write before its marshal fails the suite with
  `Location diverges: httpx="" ginx="/unencodable/fixed"`.

### Changed

- **Every module floors on kernel v0.5.0**, `mcpx` included, and that is now the
  repository's rule rather than a judgement per release. A module left behind is
  the one combination nobody runs.

## [0.5.0] - 2026-09-05

### Added

- **`ExpandLocation` and `Entry.CheckLocation`, the kernel half of a `Location`
  header.** A template is a path with `{name}` placeholders naming output fields
  by their JSON name -- `"/loans/{loan_id}"` -- and the two functions are the
  request-time and mount-time halves of the same guarantee, exactly as
  `EncodeParams` and `CheckCaptures` are on the input side.

  It lives in the kernel for the reason `StatusFor` does: both HTTP adapters
  need it, the result is client-visible, and two adapters carrying their own
  copies would be two servers disagreeing about where the thing they just
  created lives. The syntax is `{name}` on both, including Gin, because a
  Location is a string being filled rather than a pattern being matched.

  Values are path-escaped, since a slug carrying a slash would otherwise forge a
  path segment. Numbers go through `UseNumber`, so an identifier past 2^53 is
  not rewritten on its way into the header -- the same float64 round trip that
  cost this project a defect in `EncodeParams`.

  **No adapter reads it yet.** Wiring `Route.Location` into `httpx` and `ginx`
  is the next release, and it cannot happen until this one is tagged.

- **The wire a client actually receives is now asserted end to end.** The
  adapters' own suites build their expectations from the sentinel, so they check
  the composition -- the sentinel's words, then the service's -- and would keep
  passing if a sentinel's wording became nonsense. The kernel's own test checks
  the property. Neither of them reads a response body.
  `example.TestTheWireCarriesNoPackageName` spells all four answers out in full,
  through a real registry over a real database, across all three transports. It
  was falsified by reinstating the prefix, which fails it on each of them.

## [0.4.0] - 2026-09-04

### Changed

- **`ErrPermission`, `ErrNotFound` and `ErrConflict` no longer carry a
  `services: ` prefix.** All three adapters render those three verbatim and
  redact everything else, and the documented way to use them is to wrap -- so
  `fmt.Errorf("%w: no copy of %q is on the shelf", services.ErrConflict, title)`
  was serving `{"error":"services: conflict: no copy of ..."}`, putting the
  implementation's package name in the middle of a sentence about a library
  book. Over MCP the reader is a model that may take "services" for a term of
  art. The rule the change settles on: the prefix is for logs, and a sentinel
  whose words reach a client is not going to a log. `ErrConfiguration` and
  `ErrBodyTooLarge` keep theirs, because both are redacted before any client
  sees them. `TestClientFacingSentinelsCarryNoPackagePrefix` holds the split.

  This is visible on every wire. `errors.Is` and `errors.As` are unaffected, and
  any test comparing a rendered body should build the expectation from the
  sentinel rather than spelling it out -- the adapters' own suites now do, which
  is what let this land without a flag day.

### Added

- **A package doc for the kernel.** Every adapter carried a `doc.go` and the
  package everyone imports first did not, so pkg.go.dev showed it with an empty
  overview. It covers the declaration shape, the three validation layers, the
  ordering rule, the error taxonomy's two audiences, and how to map a driver's
  errors onto it.
- **`WithAtomic` now documents what decides the shape of `D`.** Only an atomic
  entry runs inside the callback, so a `Query` resolves with no transaction in
  its context and a dependency type holding a concrete `*sql.Tx` is empty for
  every read in the registry. It also says why its type argument cannot be
  inferred, so the next reader does not re-open the question.
- **`ginx` documents the two response shapes** as a table, the way `httpx`
  already did. A validation failure answers `errors` with a map and everything
  else answers `error` with a sentence, and nothing in the body says which --
  a client branches on the status.

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

[Unreleased]: https://github.com/Artui/go-services/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/Artui/go-services/releases/tag/v0.5.0
[0.4.0]: https://github.com/Artui/go-services/releases/tag/v0.4.0
[0.3.0]: https://github.com/Artui/go-services/releases/tag/v0.3.0
[0.2.0]: https://github.com/Artui/go-services/releases/tag/v0.2.0
[0.1.0]: https://github.com/Artui/go-services/releases/tag/v0.1.0
