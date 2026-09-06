# What writing this module was like

This file is the output of `example/`. The code is how it was produced.

It was written as a stranger would write it: public API only, nothing reached
into, and each piece of friction written down where it was met instead of
smoothed over in passing. Findings are ordered by what they would cost a
consumer, not by when they turned up.

Everything below was measured against kernel `v0.3.0` and the adapters at
`v0.1.0` on 2026-09-04. Each finding carries what the ergonomics pass then did
with it, added the same week; two were declined, and the reasons are here rather
than in a commit message nobody will find.

---

## 1. `Deps` cannot hold a concrete `*sql.Tx`

The plan for this module said it would. It cannot, and the reason is structural:
only an atomic entry runs inside the transaction callback, so a `Query` spec
resolves its dependencies with a context that has no transaction in it. A
concrete `*sql.Tx` field is nil for every read operation in the registry.

So `Deps.DB` is an interface with the three methods `*sql.DB` and `*sql.Tx`
share, and `resolverOver` picks between them. That is the right answer and
nothing in the kernel needs to change -- but it is the first wall anyone wiring
a database hits, and neither the kernel's documentation nor the plan says it.

`TestMutationGetsTheTransactionAndQueryDoesNot` asserts both halves, so the
behaviour is pinned rather than described.

**Owed: documentation.** The `WithAtomic` doc comment is the place. It already
explains that dependencies resolve with the transactional context; it should say
that a read-only spec resolves without one, because that sentence is what
decides the shape of the consumer's `Deps`.

**Done.** `WithAtomic` now says it, and shows the resolver that picks between a
transaction and a pool.

## 2. The library's own error idiom puts its package name on the client's wire

The documented way to use the taxonomy is `fmt.Errorf("%w: ...", services.ErrX,
...)`. Doing that, over `httpx`, produces exactly this:

```
409 {"error":"services: conflict: no copy of \"Structure and Interpretation\" is on the shelf"}
403 {"error":"services: permission denied: member 2 is suspended"}
404 {"error":"services: not found: no book 999"}
```

and now serves this:

```
409 {"error":"conflict: no copy of \"Structure and Interpretation\" is on the shelf"}
403 {"error":"permission denied: member 2 is suspended"}
404 {"error":"not found: no book 999"}
```

A note already existed for this, and it understated the problem: it described
bare sentinels reaching the wire. What actually reaches the wire is every
domain error a consumer writes, because the sentinel's text is a prefix of the
wrapped message. The client sees the implementation's package name in the middle
of a sentence about a library book.

The internal-error and body-too-large paths are already redacted to fixed text,
so the machinery to answer differently is present; these three arms are the ones
that pass the error through.

**Done: the sentinel texts lost their prefix**, and only those three. The rule
the pass settled on is that the prefix is for logs, and a sentinel whose words
reach a client is not going to a log -- so `ErrConfiguration` and
`ErrBodyTooLarge` keep theirs, because both are redacted before any client sees
them and their only reader is whoever is on call.

Rendering a fixed sentence per status was the alternative and was rejected: it
would have thrown away the service's own words, which are the half a caller can
act on and the half a spec author chose deliberately.

`TestClientFacingSentinelsCarryNoPackagePrefix` holds the split, so a sentinel
added later has to pick a side rather than inherit one.

## 3. `WithAtomic` cannot infer its type parameter

```go
services.New(resolverOver(db), services.WithAtomic[Deps](atomicOver(db)))
```

`D` appears nowhere in `WithAtomic`'s parameter list, so inference has nothing
to work from and every caller writes `[Deps]` by hand -- next to a `New` call
that infers the same type from its first argument. It reads as though the author
forgot something.

**Declined, and documented instead.** All three fixes cost more than the
problem: putting `D` into a signature that has no use for it, or moving the
callback into `New` where it becomes a required argument for the many registries
that want no transaction at all, or a `SetAtomic` method that permits mutating a
registry after its specs are registered. The annoyance is one line per
application -- a compile error, caught immediately -- and the doc comment now
says why it is spelt that way, which is the part that was actually missing.

## 4. A 201 has no way to say where the thing is

`borrow_book` declares `Status: 201` and returns a `loan_id`. There is no channel
on `Result` for a `Location` header, so the one thing a 201 is specified to carry
cannot be carried. The output struct is the only place left, which makes the
address part of the body on every transport including the ones that have no
headers.

**Done, and the design held.** It does not belong on `Result`, which is
transport-neutral by construction -- a `Location` is an HTTP header and MCP has
nowhere to put one. It belongs on the HTTP adapters' `Route`, as a template
filled from the output the dispatch already returned, because the route is the
only thing in the system that knows the URL shape.

`Route.Location` and `WithLocation` are on both HTTP adapters; the *filling* is
`services.ExpandLocation`, in the kernel for the reason `StatusFor` is there --
a client following a Location must not reach a different place depending on
which router the server was built with. This module's own `borrow_book` route
now carries `"/loans/{loan_id}"`, and `TestACreatedLoanSaysWhereItLives` asserts
both adapters answer `/loans/3` -- loan 3 rather than loan 1 since the seed grew
a history, and each adapter still runs against a database of its own.

One thing only writing it revealed: the two adapters build the header at
different points -- `httpx` expands before marshalling and clears it if the
marshal fails, `ginx` marshals first and never expands -- so a value that cannot
be encoded reaches the same answer by two different routes. The conformance
suite mounts them with the same template over exactly that case, and moving
`ginx`'s header write before its marshal makes it fail with
`Location diverges: httpx="" ginx="/unencodable/fixed"`.

## 5. Every consumer will write the same driver-error mapping

`sql.ErrNoRows` to `services.ErrNotFound` appears twice in 170 lines of domain
code here and would appear in every service in a real application. The kernel
cannot ship the mapping without importing a driver, so this is not an API
request.

**Done.** The kernel had **no package documentation at all** -- every adapter
carried a `doc.go` and the package everyone imports first did not, so pkg.go.dev
showed it with an empty overview. That is now written, and this mapping is one
of its sections.

## 6. Two error shapes, and the client has to know which by status

A validation failure answers `{"errors":{"book_id":[...]}}`; everything else
answers `{"error":"..."}`. Both are deliberate -- per-field messages genuinely
need a different shape -- but a client parsing responses must branch on the
status to know which key exists, and nothing on the wire says so.

**Done.** `httpx` already had the table; `ginx` described the same six answers
in prose without showing the two shapes. It has the table now, and both say
outright that nothing in the body distinguishes them and a client branches on
the status.

---

## Mounting on ADK, added 2026-09-05

Two frictions, both small, both only visible from out here.

### 7. Every consumer restates ADK's tool interface to test one

ADK dispatches a tool through an interface it does not export, so a tool is
matched structurally at the point of use. Nothing in the compiler says a method
drifted, and nothing lets a consumer write `var _ adk.FunctionTool = myTool`.

This module needs the shape to drive `borrow_book`, so it writes it out:

```go
type adkTool interface {
	adktool.Tool
	Run(ctx agent.Context, args any) (map[string]any, error)
}
```

`adkx`'s own suite writes the same thing, and so does the conformance driver.
Three copies of one interface, none of which the compiler ties to ADK.

**Done: `adkx.RunnableTool`.** The package exports the interface, and carries
`var _ RunnableTool = (*specTool[struct{}])(nil)` so that a drift in ADK's shape
is a build failure in `adkx` rather than a runtime surprise in somebody's agent.
Verified by renaming a method, which fails that line by name.

All three copies are now aliases of it. There is one declaration of the shape in
the repository, in the package best placed to notice when it stops being right.
It is not a promise about ADK's API, which is not ours to make -- it is a
promise that what `adkx` publishes is runnable, which is.

### 8. Identity is a string on one transport and a number everywhere else

`agent.Context.UserID` returns a string, and this application's member is an
`int64`, so the ADK principal parses one into the other. The HTTP adapters have
the same seam and it reads better there, because a header was always going to be
text.

**Owed: nothing.** The kernel's decision that a principal is opaque is what lets
this be four lines in the application instead of a conversion the library has to
guess at. Recorded because it is the kind of thing that looks like a gap until
you ask what the alternative would be.

---

## Mounting on AG-UI, added 2026-09-05

### 9. The web component does not register itself

Loading `@artooi/ag-ui-web-component` defines nothing. `defineAgUiChat()` is
what upgrades `<ag-ui-chat>` from an unknown element into the chat, and without
it the page renders, the element sits in the DOM, and absolutely nothing
happens -- no error, no warning, no element.

The published `dist/index.js` also carries bare imports for `@ag-ui/client`,
which a browser cannot resolve on its own. A `<script type="module">` pointed
straight at it fails with a module-specifier error that names the dependency
rather than the package you asked for. jsDelivr's `+esm` build rewrites them.

**Owed: nothing here, and possibly a line in the component's README.** Both are
one-line fixes once you know, and both cost an afternoon if you do not. Recorded
because this is the second consumer of that component in the workspace and the
first one to load it from a CDN rather than a bundler.

### 10. A failed server-side tool call cannot be rendered as failed

`TOOL_CALL_RESULT` carries a content string and nothing else -- the protocol has
no error flag on it -- and the web component's `onToolResult` settles every
result it receives as `DONE`. So a refusal arrived as a card reading "done" with
"no copy is on the shelf" folded inside it: a failure wearing the shape of an
outcome, which is the one rendering a tool result must never have.

What is available is a convention the component already uses. When its OWN
browser-side tool handler throws, it sends the result back as `Error: ` followed
by the message. `aguix` now emits the same shape for a server-side failure, so
the model sees one convention whichever side the tool ran on, and a person
reading the transcript sees the word before the reason rather than after it. A
success stays bare JSON, so the two are tellable apart.

**Taken to the protocol, 2026-09-05.** The paragraph above used to end here,
saying the card was not ours to fix and the prefix was all this side could do.
Half of that is still true and half of it was a failure of nerve.

`TOOL_CALL_RESULT` now carries an optional `outcome` from `aguix`, either
`failed` or `denied`. The vocabulary is pydantic-ai's own `ToolReturnPart.outcome`
rather than one invented here, so the family's Python transport forwards a value
it already computes and currently discards; AG-UI's event schemas are
`passthrough`, so the key survives a client's parser whether or not that client
knows it. A success carries no such key, which is what keeps every existing
server correct.

**Still owed and still not by us: the card reads "done".** A client has to read
the field for the rendering to change, which is a change in the component and is
in flight alongside this one. What is no longer true is that a server had nothing
to say -- it had nothing the protocol had *named*, which is a different problem
and a fixable one.

### 11. A scripted agent cannot see its own tool result

Steps run in order and none sees what the previous produced, so a `Say` written
after a `CallTool` is composed before the call has happened. The first version
of this demo ended with "That is done." -- and said it over a refusal, with the
tool result reading "no copy is on the shelf" directly above.

The fix is not to give scripts a branch. A real agent gets the tool result back
as a message and decides, which is a second turn; giving a script that ability
would make it a small agent framework competing with the real ones the package
exists to serve. The fix is that a script must not claim an outcome, and the
`Librarian` rules now end at the call.

**Owed: nothing, and the note is in `aguix/script.go` where someone writing a
script will read it.** Caught by looking at the browser rather than at the
tests, which were green throughout: every assertion held, because none of them
had any opinion about whether the sentence was true.

---

## What was expected to hurt and did not

The plan asked for these to be measured, and a result of "nothing owed" is a
result rather than a step that was skipped.

- **Mounting one registry on three adapters needed only the route tables.** No
  per-transport special case, no spec touched, no branch on which adapter is
  calling. The only divergence is capture syntax -- `{book_id}` against
  `:book_id` -- which both adapters document as the one thing they cannot share.
- **`Permit` running inside the transaction needed no extra API and is worth
  having.** The suspension check reads the same snapshot the write will use.
  It costs a round trip on the transaction's connection, which is the honest
  trade and is written down beside the function.
- **A misspelt capture was refused at mount, not at request time**, exactly as
  `CheckCaptures` claims. `TestAMisspeltCaptureIsRefusedAtMount` holds it.
- **The three-layer validation split did not get in the way once.** Schema for
  shape, `Validate` for which integers are identifiers, `Permit` for who may
  borrow. Each rule had one obvious home.

## What the falsification cost, and one thing it turned up

`TestRollbackAssertionHasTeeth` runs the same dispatch against a registry whose
dependencies resolve outside the boundary, and asserts the orphan loan row
appears. Without it, the rollback assertion beside it would pass against a
service that never wrote anything and nobody would know.

Building it turned up a trap worth keeping: with a plain `:memory:` DSN, every
test in this module still passes **except the two falsifications**. They are the
only ones that touch two connections at once -- a transaction on one, writes on
another -- so a broken boundary stops reporting an orphan row and starts
reporting `no such table`, which reads like a broken test rather than a proven
one. `cache=shared` is load-bearing for the falsification and for nothing else
here, which is the opposite of how it would be summarised without measuring.

The companion `TestBrokenBoundaryIsInvisibleWhenNothingFails` is the other half:
on the happy path, the correct registry and the broken one are indistinguishable
on every assertion. That is why the ordering rule needs a test at all.

---

## Whether an audience layer is owed, measured 2026-09-06

Measured against the working tree rather than a tag, as this module always is:
the kernel at `v0.5.0` plus whatever is uncommitted beside it, and every adapter
through the `replace` directives in `go.mod`.

Every finding above this line already carries a resolution -- `Done`, `Declined,
and documented instead`, `Owed: nothing` -- written as prose in its closing
paragraph rather than as a labelled field. That is a real record and it was
maintained; it is simply not greppable, which is a different complaint and a
smaller one. Findings below state theirs on their own line so that a reader, or
a script, can tell without reading the argument.

One resolution above has since gone stale, and it is worth knowing how. Finding
10 closes with "still owed and still not by us ... a change in the component and
is in flight alongside this one" -- and that change shipped, as web component
0.36.0, which settles a tool card to error or declined on the outcome field. So
the finding is closed and still reads as open. A resolution written while
something is in flight becomes a dangling reference the moment it lands, and
nothing points back from a release to the note that was waiting on it.

### Why the silence above was not evidence

Not one of the eleven findings above mentions output shaping, and that proved
nothing. This module's whole domain was `int64` and `string`: no enum, no
timestamp, no money, no opaque token. A testbed with none of those cannot record
friction about any of them, so its silence was structural rather than a result.

So the domain was given the field kinds that could hurt, kept plausible for a
lending library rather than shaped as a probe:

- a **due date** on a loan, `time.Time`, encoded RFC3339;
- a **status enum**, `LoanStatus`, one of `on_loan`, `overdue`, `returned`;
- a **fine in currency**, `fine_cents`, minor units in an `int64` because money
  in a float is a rounding error waiting to happen;
- an **opaque token**, `next_cursor`, added for a reason worth stating outright:
  the first three cannot test the handle question at all. A marking that
  distinguishes "an identifier another tool consumes" from "content a model may
  read out" has nothing to bite on in a domain whose only identifiers are small
  integers, so the catalogue was paged, which is the least contrived opaque
  token any list API has.

The seed grew the history that makes those readable: one loan still out and
late, which is why book 11 has no copy on the shelf, and one returned late,
which is why a fine can be non-zero without anything having to happen first.

`audience_test.go` holds every payload below as a literal and asserts it against
a real session on each transport, with the clock stopped. Nothing in this section
was reasoned about without being run.

## 12. Nothing shapes an output for its audience

**Status: OPEN. The measurement below stands; the verdict it carried does not.**

This finding was written as "and nothing needs to", closed, on the strength of
the captures below. The owner reopened it the same day on an argument the
experiment was not scoped to test, and the argument is right.

What was measured is still true: every transport serves one encoding of one
value, and read aloud, nothing goes wrong. What was concluded from it does not
follow. The question asked here was whether a *marking* is earned -- whether a
field needs to be labelled as a handle or a label or plumbing -- and the answer
to that is still no, in finding 13. The formatters were excluded from this
section's scope before it was written, and then the evidence landed on them:
`fine_cents: 550` names its unit and not its currency, and `due_at` is UTC.

Those are not read-aloud problems, which is why reading the payloads aloud found
nothing. They are missing information. An API is read by code that was written
knowing the units; a model is a reader nobody told, and it cannot obtain the
currency or the reader's timezone from anywhere in the payload. That is data loss
at the boundary rather than a formatting preference, and it is why the same raw
value is right for one audience and wrong for the other.

⇒ *an experiment that lands on the thing you excluded from its scope is telling
you the scope was wrong, not that the answer is no.*

Left open rather than re-answered here, because the fork it turns on -- whether a
struct tag should render the value or enrich the schema description that MCP and
ADK already carry -- is a question these captures cannot settle. They are the
before picture for whoever settles it.

Every transport serves one encoding of one value. `list_loans` over MCP, over
AG-UI, and over plain HTTP to a browser, byte for byte:

```json
{"loans":[{"loan_id":1,"book_id":11,"title":"Structure and Interpretation","status":"overdue","due_at":"2026-08-15T09:00:00Z","fine_cents":550},{"loan_id":2,"book_id":10,"title":"The Mythical Man-Month","status":"returned","due_at":"2026-07-15T09:00:00Z","fine_cents":125}]}
```

MCP's `structuredContent` and ADK's required map answer carry the same fields
with the keys sorted, because both have been through a Go map on the way out:

```json
{"loans":[{"book_id":11,"due_at":"2026-08-15T09:00:00Z","fine_cents":550,"loan_id":1,"status":"overdue","title":"Structure and Interpretation"},{"book_id":10,"due_at":"2026-07-15T09:00:00Z","fine_cents":125,"loan_id":2,"status":"returned","title":"The Mythical Man-Month"}]}
```

That is an ordering difference and nothing else: same fields, same values, same
encoding of each, and neither reader is told anything the other is not.

The paged catalogue, where the opaque token lives, and a created loan:

```json
{"books":[{"id":10,"title":"The Mythical Man-Month","author":"Brooks","available":2}],"next_cursor":"YWZ0ZXI6MTA"}
{"loan_id":3,"book_id":10,"member_id":1,"remaining":1,"status":"on_loan","due_at":"2026-09-20T12:00:00Z"}
```

**Read them aloud and nothing goes wrong.** `"overdue"` and `"on_loan"` are
already the words a person would use. `"2026-08-15T09:00:00Z"` is a date any
reader renders correctly. `"loan_id":1` and `"member_id":1` are identifiers a
librarian would say out loud anyway.

Two things in there could be read imprecisely, and neither is what a handle
marking is for. `"fine_cents":550` names its unit and not its currency, so a
reader has to supply one; and the timestamp is UTC, so a library that is not on
UTC is one conversion away from an hour that is wrong on the wall clock. Both
are the value-formatter half of the sibling Python library's audience layer,
which is already declined -- and the honest note is that this is where the
declining costs something, not in the markings.

**The sharpest thing in the whole capture that reads badly to a user is not a
field at all.** It is this, which every agent transport serves verbatim:

```
permission denied: member 2 is suspended
```

An internal member id and an account state, written by a spec author, handed
straight to a model. Finding 2 above settled that deliberately -- the words are
the service's and a caller can act on them -- and no marking on an output field
reaches a sentence in an error. If the worry is internal values reaching a
person, the exposure this module actually has is there, and it is already a
decision rather than a gap.

## 13. A handle marking is expressible today, which is why it is not earned

**Status: DECIDED. No. The answer to "is HANDLE earned" is no, and the evidence
is that the mechanism it would need already exists and already arrives.**

The question was whether a field needs a way to say "this is an identifier
another tool consumes, never something to read out". It has one. `LoanStatus`
declares its own schema through `services.SchemaFor`, and the kernel reflects
that on the way OUT as well as on the way in, so what an MCP client is told
about `list_loans` includes the three values as a list it can check:

```json
"status":{"description":"where the loan stands","enum":["on_loan","overdue","returned"],"type":"string"}
```

and `next_cursor` carries the field's own words, written once on the struct tag:

```json
"next_cursor":{"description":"an opaque token; pass it back as cursor to fetch the next page, and do not show it to a person or try to read it","type":"string"}
```

Both reach an MCP client and an ADK model unchanged, because `mcpx` publishes
`OutputSchema` and `adkx` fills `ResponseJsonSchema` from the same object the
kernel reflected.

A *machine-readable* marking is available on the same channel, and
`TestAFieldMarkingReachesTheWireWithNoKernelChange` is the probe that shows it
rather than argues it. A named type declaring `jsonschema.Schema.Extra` with an
unnamed keyword arrives on the wire untouched:

```json
{"additionalProperties":false,"properties":{"token":{"description":"an opaque token","type":"string","x-audience":"handle"}},"required":["token"],"type":"object"}
```

So the whole of what a marking system would add, over what a consumer can write
today, is a **vocabulary** -- and nothing in this repository reads a vocabulary.
Adding one would be a second place the truth about a field lives, which is the
argument `mcpx.toolFor` already makes about hints it refuses to infer.

The last thing worth writing down, because it is what makes the two options
equal rather than merely similar: **a marking has exactly the enforcement power
a description has, which is none.** `YWZ0ZXI6MTA` decodes to `after:10` with no
key, so calling a field opaque is a convention on both sides of the wire. A
marking would not stop a model reading a token aloud any more than a sentence
does; it would only be terser about asking.

## 14. `aguix` publishes no output schema, so nothing written on an output field reaches an AG-UI model

**Status: OPEN, and it belongs to `aguix` rather than to this module. Reported,
not fixed -- this module does not touch an adapter.**

The definition an AG-UI agent is given for `list_loans` is the whole of what
that transport says about the operation:

```json
{"name":"list_loans","description":"List the authenticated member's own loans, with what each one owes.","parameters":{"type":"object","properties":{"include_returned":{"type":"boolean","description":"also list loans that have already been returned"}},"additionalProperties":false}}
```

`Definitions` builds `Name`, `Description` and `Parameters` from `entry.Input`,
and reads `entry.Output` nowhere. So the sentence on `next_cursor` that MCP and
ADK both carry -- and any marking put beside it -- cannot arrive here at all. On
this transport a model is handed `"next_cursor":"YWZ0ZXI6MTA"` with the field
name as its only clue.

This is the one place the measurement found a real hole, and it is worth being
precise about what would fill it: **publishing the output schema, not inventing a
marking.** A marking would be equally unable to reach an AG-UI model, because
the channel that would carry it is the one that is missing.

The only reason the AG-UI definition mentions the token at all is that the
*input* field's description names it -- the author writing the same fact twice,
on the one field this transport does publish. The test named
`TestOnlySomeTransportsAdvertiseTheOutputSchema` pins all three answers, so if
`aguix` starts publishing an output schema, the assertion that says it does not
is what fails.

## 15. `Validate` cannot hand `Run` the value it just parsed

**Status: OPEN as an observation, no change requested. Recorded because the
workaround is invisible and the next person will re-derive it.**

`Validator` is `Validate() error`. A check that merely accepts or rejects fits
it exactly. A check that *produces* something -- decoding a cursor into the id
it pages from -- has nowhere to put the result, so the choice is to decode the
token twice or to decode it once inside `Run` and return `services.Invalid` from
there.

`listBooks` does the second, and it works: the kernel maps a `ValidationError`
to the same answer wherever it is raised, so the client sees a 400 naming
`cursor` exactly as it would from `Validate`. What is lost is the ordering
guarantee -- `Validate` runs before any transaction is opened, and a check moved
into `Run` no longer does. For a read that costs nothing. For a mutation it
would mean a transaction opened for a payload that was never going to be
accepted.

Nothing is asked for here. The alternative shape -- a validator that returns a
parsed value -- would put a second type parameter on the interface for the sake
of the minority of specs that need one. It is written down because the reason
`listBooks` validates where it does is not visible from the code, and the
comment on `ListIn.Validate` now says it.
