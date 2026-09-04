// Package services declares an operation once and serves it over more than one
// transport.
//
// A Spec names a typed input, a typed output, a plain function and the
// cross-cutting facts about the call. A Registry holds specs under their names.
// An adapter reads the registry and speaks a wire. The kernel imports no
// transport -- not even net/http -- so nothing here knows how the call arrived.
//
//	registry := services.New(resolve, services.WithAtomic[Deps](inTransaction))
//
//	services.MustRegister(registry, services.Spec[Deps, BorrowIn, BorrowOut]{
//	    Name:   "borrow_book",
//	    Kind:   services.Mutation,
//	    Status: 201,
//	    Permit: []func(services.Ctx[Deps], BorrowIn) error{memberInGoodStanding},
//	    Run:    borrow,
//	})
//
// That declaration is the whole configuration. Kind decides which HTTP methods
// the operation may be mounted on and whether it runs in a transaction; the
// input type is reflected once into the schema a client is shown and the schema
// the kernel enforces, so the advertised contract and the enforced one are the
// same object rather than two that agree today.
//
// # Validation is three layers, and only one of them is a schema
//
// The reflected JSON Schema checks shape: types, required fields, enums. A
// Validate method on the input type checks the rules a schema cannot express --
// which integers are identifiers, which combinations of optional fields make
// sense. Spec.Permit checks what needs the caller and the current state, which
// means it needs dependencies and therefore runs last.
//
// The first two run before any transaction is opened, so an invalid payload
// never costs one. Permit runs inside it.
//
// # Dependencies resolve inside the transaction
//
// This is the ordering rule the rest of the design rests on. WithAtomic hands
// the registry a callback that opens a transaction and returns a context
// carrying it; the registry resolves dependencies with THAT context, so Deps
// holds the transactional handle and a service physically cannot write outside
// its own boundary.
//
// Resolving first and running the service inside looks identical and passes
// every test in which nothing fails. It writes half a mutation outside the
// transaction on rollback. The example module in this repository asserts the
// difference in both directions, including a registry built the wrong way round
// to prove the assertion can fail.
//
// # Errors have two audiences, and the taxonomy splits on that
//
// ErrPermission, ErrNotFound and ErrConflict are addressed to the caller.
// Wrapping one is how a service declines with its own words:
//
//	return fmt.Errorf("%w: no copy of %q is on the shelf", services.ErrConflict, title)
//
// Every adapter renders those three verbatim, so the added words reach the
// client, and they carry no package prefix for exactly that reason. Everything
// else is redacted to a fixed sentence and reported to the mount's observer
// instead: an unexpected error's words name hosts, tables and identifiers, and
// they are written for whoever is on call.
//
// ValidationError is separate again, because it is per-field and adapters
// render it as a map rather than a sentence.
//
// # Mapping your own errors onto it
//
// The kernel names no driver, so translating one is the application's job and
// is usually two lines at the point of the query:
//
//	err := q.QueryRowContext(ctx, `SELECT ...`, id).Scan(&title)
//	if errors.Is(err, sql.ErrNoRows) {
//	    return fmt.Errorf("%w: no book %d", services.ErrNotFound, id)
//	}
//
// Everything not translated is an unexpected error and is redacted, which is
// the safe direction: a failure nobody classified is a bug until someone says
// otherwise.
//
// # What is not here
//
// No ORM, no pagination or filtering vocabulary, no viewsets and no identity
// model. Filters are typed fields on the input, already described by the schema
// and already validated. Identity is a field on the dependency type, because
// the registry's resolver is the one place an application says what a principal
// is -- so there is no Actor type here to disagree with yours.
package services
