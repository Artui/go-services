// Package example is a small library-lending service, declared once and served
// over net/http, Gin and MCP from a single registry.
//
// It exists to be read and to fail. Two things it does are not decoration:
//
//   - It runs against a real database, so the kernel's ordering rule --
//     dependencies resolve INSIDE the transaction, never before it -- has a
//     composed proof. The conformance suite cannot supply one: it runs with an
//     empty Deps and no database, so nothing there can tell a correct boundary
//     from a broken one.
//   - It carries its own falsification. TestRollbackAssertionHasTeeth builds
//     the same registry with dependencies resolved OUTSIDE the boundary and
//     asserts the orphan row appears, which is what proves the rollback test
//     beside it is not passing vacuously.
//
// It is mounted on every adapter this repository publishes -- net/http, Gin,
// MCP and ADK -- from one registry, so a transport that starts answering
// differently fails a test here as well as in the conformance suite. The
// difference between the two is what they hold: conformance compares the
// transports' own answers, and this compares the rows they left behind.
//
// aguix, when it exists, will not join that list. AG-UI streams an agent's turn
// rather than calling one spec, so there is no single call whose database state
// could be compared with the others'. It needs its own harness here, driving a
// run and asserting what the run committed, and putting it in the mounts table
// would be claiming the two are the same shape when they are not.
//
// It is deliberately written the way a stranger would write it: only the public
// API, no reaching into the kernel, and every piece of friction met along the
// way recorded in FRICTION.md rather than smoothed over in passing. That file
// is the actual output of this module. The code is how it was produced.
package example
