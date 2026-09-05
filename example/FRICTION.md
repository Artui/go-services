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
both adapters answer `/loans/1`.

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

**Owed: nothing in the kernel, and possibly something in `adkx`.** It could
export that interface itself -- a consumer would then have one name to assert
against, and `adkx` would be the single place that notices if ADK's shape
changes. Worth deciding before there are consumers rather than after.

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
