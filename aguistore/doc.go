// Package aguistore keeps AG-UI threads for a server: each user's
// conversations, listed, read back, renamed and deleted over the HTTP routes
// the web component's data-threads-url reads.
//
// A Store holds a thread's messages under the ids the wire gave them, keyed by
// user and thread, so one user is never answered with another's thread. Memory
// keeps them for the life of the process; SQL keeps them in a database/sql
// table and outlives it, written in the SQL SQLite 3.35 and later runs (?
// placeholders, ON CONFLICT and RETURNING). Both satisfy aguiadk's ThreadStore,
// so the bridge saves each run through either, error handling left out here:
//
//	store, _ := aguistore.OpenSQL(ctx, db)
//	agent, _ := aguiadk.New(root, aguiadk.WithUserID(userID), aguiadk.WithThreadStore(store))
//	runs, _ := aguix.Handler(agent)
//	mux.Handle("/agent/", runs)
//	mux.Handle("/threads/", http.StripPrefix("/threads", aguistore.Handler(store, userID)))
//
// Pass the same userID function to both, so the routes answer for the user the
// runs were saved under.
//
// Both are also a RunStore, so the bridge records each run's status through
// them: the list then carries each thread's status, the reason a failed one
// failed, the interrupts a waiting one parked on as RUN_FINISHED sent them, and
// last_read_at, which POST /<id>/read/ stamps. A parked approval therefore
// survives a reload, and a resume claims the park, so the same answer sent twice
// runs the call once. The list is also a watch: a GET that accepts
// text/event-stream is answered with the list again each time it changes (see
// WithWatch). Each stored activity's last snapshot is in the thread, where the
// run sent it.
package aguistore
