// Package aguistore keeps AG-UI threads for a server: each user's
// conversations, listed, read back, renamed and deleted over the HTTP routes
// the web component's data-threads-url reads.
//
// A Store holds a thread's messages under the ids the wire gave them, keyed by
// user and thread, so one user is never answered with another's thread. Memory
// keeps them for the life of the process; SQL keeps them in a database/sql
// table and outlives it. Both satisfy aguiadk's ThreadStore, so the bridge
// saves each run through either:
//
//	store, err := aguistore.OpenSQL(ctx, db)
//	agent, err := aguiadk.New(root, aguiadk.WithUserID(userID), aguiadk.WithThreadStore(store))
//	mux.Handle("/agent/", aguix.Handler(agent))
//	mux.Handle("/threads/", http.StripPrefix("/threads", aguistore.Handler(store, userID)))
//
// Pass the same userID function to both, so the routes answer for the user the
// runs were saved under.
package aguistore
