# go-services

A service layer for Go: declare an operation once, serve it over more than one
transport.

A `Spec` names a typed input, a typed output, a plain function, and the
cross-cutting facts about the call. A `Registry` holds the specs. Adapters read
the registry and mount it onto an HTTP router or an MCP server. The adapters
translate wire formats and nothing else -- validation, permission checks,
transaction boundaries and error mapping all live in the kernel, below every
transport, so they cannot drift apart.

## Is this for you?

Probably not yet, and it is worth saying so plainly.

The value scales with the number of specs multiplied by the number of
transports. If you have four endpoints and one transport, a handler that calls a
function is genuinely the right answer and this package is overhead. It starts
paying when the same operations have to be reachable two ways at once -- an HTTP
API and an agent tool surface, say -- and every rule that matters has to hold on
both.

## The idea

The usual way to give an existing HTTP API a second transport is to wrap it:
discover the routes, then call them over loopback HTTP from the new transport.
That works, and it puts every rule you care about in the wrong place. A
permission check written as router middleware is the only thing standing between
a caller and the handler, so the rules belong to whichever transport happens to
be in front, and a second one silently gets a different answer.

This package inverts that. The operation is declared below the transports, and
an adapter's whole job is turning a wire into JSON plus an opaque principal:

    HTTP  ---\
              >--- Registry.Dispatch --- decode, validate, begin, resolve, permit, run
    MCP   ---/

If a rule can be forgotten by writing a new adapter, it is in the wrong place.

## Declaring an operation

```go
type CreateAuthorIn struct {
    Name string `json:"name" jsonschema:"the author's display name"`
    Bio  string `json:"bio,omitempty" jsonschema:"optional short biography"`
}

// Validate is the second of three validation layers: format and business rules
// a JSON Schema cannot state.
func (in CreateAuthorIn) Validate() error {
    if strings.TrimSpace(in.Name) == "" {
        return services.Invalid("name", "must not be blank")
    }
    return nil
}

type AuthorOut struct {
    ID   int64  `json:"id"`
    Name string `json:"name"`
}

func createAuthor(ctx services.Ctx[App], in CreateAuthorIn) (AuthorOut, error) {
    // ctx.Deps.DB is the transactional handle; ctx.Deps.User is already typed.
    ...
}

registry := services.New(resolve, services.WithAtomic[App](tx))

services.MustRegister(registry, services.Spec[App, CreateAuthorIn, AuthorOut]{
    Name:        "create_author",
    Description: "Create an author.",
    Kind:        services.Mutation,
    Run:         createAuthor,
    Permit:      []func(services.Ctx[App], CreateAuthorIn) error{authoring.MayWrite},
})
```

Required-ness comes from the struct tags: a field marked `omitempty` or
`omitzero` is optional and every other exported field is required. Unknown
fields are rejected. Both facts come from the reflected schema, which is also
the schema an MCP tool advertises -- there is no second schema that could
disagree with it.

## Serving it

The same registry, mounted three ways. Each adapter is its own module, so you
take only the one you use.

```go
// net/http -- no third-party dependency, so this also reaches chi and echo.
httpx.Mount(mux, registry, map[string]httpx.Route{
    "create_author": {Method: "POST", Pattern: "/authors"},
    "get_author":    {Method: "GET", Pattern: "/authors/{id}"},
}, principal)

// Gin.
ginx.Mount(engine, registry, map[string]ginx.Route{
    "create_author": {Method: "POST", Path: "/authors"},
    "get_author":    {Method: "GET", Path: "/authors/:id"},
}, principal)

// MCP -- every spec becomes a tool, with the schema the kernel enforces.
mcpx.Mount(server, registry, principal)
```

An adapter's whole job is turning a wire into JSON plus an opaque principal, and
turning a result or an error back. Everything a caller can observe -- which
statuses mean what, whether a body is refused, whether an unexpected error is
redacted -- is decided once, in the kernel, so a second transport cannot answer
differently from the first.

What each adapter refuses at mount rather than at request time: a `Query` on a
method that carries a body, a `Mutation` on GET, a route capture the operation
declares no field for, a duplicate route, and a status a response cannot end
with. A misconfigured route table fails at start-up.

## Declaring a destructive operation

`Kind` says whether an operation has side effects. It cannot say whether those
side effects destroy anything, and MCP's `destructiveHint` **defaults to true**
for anything that is not read-only -- so without a declaration every mutation is
advertised to an approval gate as possibly destructive, and a pure create
prompts as loudly as a delete.

```go
additive := false

services.Spec[App, CreateAuthorIn, AuthorOut]{
    Kind:        services.Mutation,
    Destructive: &additive, // nothing is overwritten
}
```

`nil` means undeclared and is the default, which stays distinguishable from a
declared `false`: a transport publishing this as an annotation has to be able to
tell silence from a claim, which is why the field is a `*bool` and not a `bool`.
The variable is the cost of that, and it is worth paying on every additive
mutation you expose to an agent.

## Dependencies arrive per call

`Ctx[D]` carries a context and a `D`, and nothing else. There is no service
struct to construct, so no method pays for a dependency it does not use, and no
test has to wire three collaborators to exercise one function.

There is deliberately no actor on `Ctx`. Identity is a field on your own `D`,
put there by the registry's resolver:

```go
func resolve(ctx context.Context, principal any) (App, error) {
    user, ok := principal.(*auth.User) // the one place you assert your own type
    if !ok {
        return App{}, services.ErrPermission
    }
    return App{DB: db.FromContext(ctx), User: user}, nil
}
```

Every service and `Permit` function downstream gets a typed user with no
assertion at all.

## Transactions, and one ordering rule

`WithAtomic` takes a "run this inside a transaction" callback, so the kernel
names no driver. `database/sql`, `pgx.BeginFunc` and GORM's `db.Transaction` all
fit.

Dependencies resolve **inside** that callback. That is not an implementation
detail: resolving them first and running the service inside looks identical,
passes every happy-path test, and writes half the mutation outside the boundary
on rollback.

## Optional fields, and PATCH

Go's zero value cannot distinguish "the client sent an empty string" from "the
client did not mention this field", so a naive update blanks everything the
caller left out. `Optional[T]` restores the distinction:

```go
type UpdateAuthorIn struct {
    Name Optional[string]  `json:"name,omitzero"`
    Bio  Optional[*string] `json:"bio,omitzero"`
}
```

Nullability composes through the type parameter rather than needing a second
flag: `Optional[string]` may be absent, and `Optional[*string]` may be absent or
explicitly null.

## Status

Pre-1.0. The kernel and all three adapters are here; 1.0 waits on a second real
consumer.

## Modules and requirements

Four modules, taken separately, because Go has no optional dependencies and
their floors genuinely differ.

| Module | Go | Depends on |
| --- | --- | --- |
| `github.com/Artui/go-services` | 1.24 | `google/jsonschema-go` only |
| `.../go-services/httpx` | 1.24 | nothing beyond the kernel |
| `.../go-services/ginx` | 1.26 | `gin-gonic/gin` |
| `.../go-services/mcpx` | 1.25 | `modelcontextprotocol/go-sdk` |

The kernel's 1.24 is `encoding/json`'s `omitzero`, which `Optional[T]` needs.
`mcpx` follows the MCP SDK. `ginx` is highest because clearing Gin's transitive
advisories pulls in `x/crypto` and `quic-go` releases that require 1.26 -- Gin
requires `quic-go` directly for HTTP/3, which is how a router comes to carry a
QUIC stack. None of that reaches the other three.

## License

MIT.
