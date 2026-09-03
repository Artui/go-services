# Working in go-services

A Go library: declare an operation once, serve it over more than one transport.
The kernel sits below every adapter and owns validation, permissions,
transaction boundaries and error mapping.

This repo is **outside the Python family** in language as well as dependency. It
shares the workspace's conventions where they transfer and diverges where they
do not; the divergences are named below rather than left to be inferred.

## Commands

```
make check        # everything CI runs
make lint         # gofmt -l, go vet, golangci-lint if installed
make test-race    # the suite under the race detector
make cover        # coverage, with the gate
```

## The one architectural rule

**The kernel must not import any transport.** No `gin-gonic`, no
`modelcontextprotocol`, not even `net/http` -- which is why a success status is
a plain `int` on `Result` rather than an `http.Status*` constant. Adapters live
in their own module directories and read `Registry.Entries()`; the kernel never
learns they exist.

A pre-commit hook enforces this on every `*.go` file at the repo root. If the
hook ever has to be relaxed, the design has changed and the plan should say so.

## Structure

- One module per boundary. The kernel is the repo root; each adapter is its own
  module with its own `go.mod`, because Go has no optional dependencies and
  their Go floors already differ.
- Cohesive file per concept (`spec.go`, `registry.go`, `dispatch.go`), which is
  Go's idiom. **This deliberately differs from the Python siblings'
  one-symbol-per-file layout** -- that convention exists for a language whose
  packages are directories of modules, and it reads as noise here.

## Divergences from the workspace standard, and why

| Standard | Here |
| --- | --- |
| 100% line **and** branch coverage | **Statements only.** See below -- this is weaker, and saying so is part of the deal |
| mkdocs to gh-pages | pkg.go.dev, which needs no build |
| bump-my-version, OIDC to PyPI | git tags. There is no registry and no publish step |
| one symbol per file | cohesive file per concept, per Go idiom |

### The coverage gate is weaker than it looks

Go measures **statement** coverage. It has no branch coverage, so an identical
number here does not mean what it means in the Python repos, and the badge must
not be read as if it did.

The compensating rule: every uncovered statement is named in
`scripts/coverage-exclusions.txt` with a reason, and each entry is a claim that
the statement is unreachable rather than that testing it was inconvenient. The
list is meant to be read and to stay short. `make cover` fails on any gap that
is not on it.

Alongside it, the review rule: **every error return gets a test.** That is the
half branch coverage was actually buying.

## Testing notes

- Two things are load-bearing enough to have tests written specifically to fail
  if they are swapped: dependencies resolving **inside** the transaction, and
  the `Schema` hook enriching the same object the kernel enforces.
- `jsonschema-go` cannot validate against a Go struct -- it says so with a link
  to its own issue. That is why `Dispatch` parses the payload twice: once
  shapelessly to validate, once into `In` to use. Do not try to collapse it.
- A type whose schema is permissive but whose decoder is strict (`time.Time` is
  the reachable example) is the case that proves the second parse needs its own
  error path.

## Conventions inherited unchanged

No emoji or marker glyphs, no absolute local paths, no internal plan-step labels
in anything committed. All three are pre-commit hooks. A `//nolint` must carry a
linter and a reason.
