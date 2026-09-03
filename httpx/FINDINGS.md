# Kernel findings from building the httpx adapter

Written as the kernel's first HTTP consumer, which is the only position some of
these were visible from. Kept as a ledger rather than a wish list: what was
found, what happened to it, and what this module does about it now.

## Closed

**Parameter merge and precedence.** `EncodeParams` takes body, query and
captures separately and owns the ordering, so an adapter cannot get it
backwards.

**The method rule.** `Kind.AllowsMethod` states the whole rule. `checkRoute`
surfaces it and holds none of it.

**The never-nil field map.** `ValidationError.FieldMap`.

**The body ceiling.** `ErrBodyTooLarge` and `DefaultMaxBodyBytes`.

**The status table and the fixed sentences.** `services.StatusFor`,
`InternalErrorText`, `UnreadableBodyText`, `BodyTooLargeText` and
`StatusBodyTooLarge`. `httpx/errors.go` now holds only the shape of the response
body, which is a wire format and genuinely an adapter's business. Recognising
`*http.MaxBytesError` stays here, correctly: the type belongs to this transport.

**A well-formed non-object body.** No longer reported as malformed, and no
longer answered differently depending on whether the route captured anything.

**`StatusBodyTooLarge`'s doc contradicted `StatusFor`.** The comment said the
constant was "not in the StatusFor table"; the function returned it for both a
bare and a wrapped `ErrBodyTooLarge`. Fixed. The consequence was the point: the
obvious move on reading that comment is to add a redundant 413 branch before
calling `StatusFor`, written on the strength of the documentation rather than
the code.

**An undeclared capture is refused, not dropped -- and it is not the caller's
fault.** This ran over two rounds and is worth reading as one item.

A capture with no matching schema property used to vanish in silence, so
mounting `/tenants/{tenant}/invoices` onto a spec with no `tenant` field ran
unscoped with every layer agreeing and nothing failing. `EncodeParams` now
refuses it and still drops an undeclared query key, which is the right
asymmetry: a query string carries analytics noise nobody declared, a capture was
written into the pattern by hand.

The refusal was a `ValidationError` at first, which put an operator's diagnostic
on the client's channel beside genuine field errors an adapter could not tell it
from. It is `ErrConfiguration` now, and `StatusFor` answers 500: the deployment
is wrong, no change the caller makes will help, and 400 would tell them to fix a
request that was never the problem. The diagnostic reaches the deployment
through `WithOnError`.

## Corrected

**This file previously recorded that deleting `checkCaptures` "was the right
call".** It was not, and the correction is the coordinator's. Deleting it was
right about the guarantee -- the kernel does enforce it at dispatch -- and wrong
about where it should fire. A route table naming a capture the operation cannot
receive is broken in every request it will ever serve, so refusing it at
start-up is strictly better than answering 500 forever, and fail-fast
configuration checking is a stated value of this library.

The check is back, in `checkRoute`, through `services.Entry.CheckCaptures`. The
split is: this adapter extracts `{name}` and `{name...}` segments from the
pattern, because path syntax is the one thing two HTTP adapters genuinely cannot
share; the kernel decides whether the input can receive them, and names every
undeclared one at once.

Both halves are tested, and they are not duplicates:

- `TestMountRejectsBadConfiguration` covers the start-up refusal, including
  that every undeclared capture is reported at once and sorted.
- `TestMountBindsAMultiSegmentCapture` covers the seam -- `{tags...}` has to be
  handed over as `tags`, or every multi-segment wildcard would be reported as
  undeclared.
- `TestAnUndeclaredCaptureFromAForeignRouterIsRefused` covers the half that
  Mount can never reach: a Handler behind a router whose captures arrive
  through `WithPathValues`, where there is no pattern to inspect. It asserts the
  500, the redacted body, that the real `ErrConfiguration` reached the observer,
  and -- the assertion that actually falsifies the old behaviour -- that the
  response carries no `"viewer"`, so the operation did not merely fail but never
  ran unscoped.

## Deferred, by the coordinator, and recorded in the project plan

**`Result` carries no transport-neutral hint channel.** `Result` is
`{Value, Status, Input}`, so an adapter can choose a status but cannot say where
the thing it just created lives. A `201` therefore ships without `Location`, and
the same gap covers `ETag` and `Retry-After` on a rate-limited conflict.
Deriving any of them would mean guessing which field of `Value` is the
identifier and which route to build a URL from, which is the guess this library
exists to prevent. Nothing in `httpx` works around it; it simply does not set
those headers.

## Open

None.
