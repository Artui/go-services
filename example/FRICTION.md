# What writing this module was like

This file is the output of `example/`. The code is how it was produced.

It was written as a stranger would write it: public API only, nothing reached
into, and each piece of friction written down where it was met instead of
smoothed over in passing. Findings are ordered by what they would cost a
consumer, not by when they turned up.

Everything below was measured against kernel `v0.3.0` and the adapters at
`v0.1.0` on 2026-09-04.

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

## 2. The library's own error idiom puts its package name on the client's wire

The documented way to use the taxonomy is `fmt.Errorf("%w: ...", services.ErrX,
...)`. Doing that, over `httpx`, produces exactly this:

```
409 {"error":"services: conflict: no copy of \"Structure and Interpretation\" is on the shelf"}
403 {"error":"services: permission denied: member 2 is suspended"}
404 {"error":"services: not found: no book 999"}
```

A note already existed for this, and it understated the problem: it described
bare sentinels reaching the wire. What actually reaches the wire is every
domain error a consumer writes, because the sentinel's text is a prefix of the
wrapped message. The client sees the implementation's package name in the middle
of a sentence about a library book.

The internal-error and body-too-large paths are already redacted to fixed text,
so the machinery to answer differently is present; these three arms are the ones
that pass the error through.

**Owed: a kernel decision.** Either the sentinel texts lose their prefix, or the
adapters render a fixed sentence per status the way they already do for 500, or
the taxonomy grows a "client-facing message" channel. This is the finding with
the widest blast radius, because it is visible to every consumer of every
transport and nobody has to do anything unusual to hit it.

## 3. `WithAtomic` cannot infer its type parameter

```go
services.New(resolverOver(db), services.WithAtomic[Deps](atomicOver(db)))
```

`D` appears nowhere in `WithAtomic`'s parameter list, so inference has nothing
to work from and every caller writes `[Deps]` by hand -- next to a `New` call
that infers the same type from its first argument. It reads as though the author
forgot something.

**Owed: ergonomics.** Options, roughly in order of how much they disturb: accept
the callback in `New` alongside `resolve`; give `Registry` a method; or leave it
and say why in the doc comment. Worth deciding in the 0.5.0 pass rather than
after there are consumers.

## 4. A 201 has no way to say where the thing is

`borrow_book` declares `Status: 201` and returns a `loan_id`. There is no channel
on `Result` for a `Location` header, so the one thing a 201 is specified to carry
cannot be carried. The output struct is the only place left, which makes the
address part of the body on every transport including the ones that have no
headers.

**Owed: a kernel decision**, already on the 0.5.0 list. Writing an actual 201
is what makes it feel like a gap rather than a nicety.

## 5. Every consumer will write the same driver-error mapping

`sql.ErrNoRows` to `services.ErrNotFound` appears twice in 170 lines of domain
code here and would appear in every service in a real application. The kernel
cannot ship the mapping without importing a driver, so this is not an API
request.

**Owed: documentation**, or an example that is easy to copy. This module is now
that example.

## 6. Two error shapes, and the client has to know which by status

A validation failure answers `{"errors":{"book_id":[...]}}`; everything else
answers `{"error":"..."}`. Both are deliberate -- per-field messages genuinely
need a different shape -- but a client parsing responses must branch on the
status to know which key exists, and nothing on the wire says so.

**Owed: documentation at most.** Recorded because it is invisible from inside
the library and obvious the first time you write a client.

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
