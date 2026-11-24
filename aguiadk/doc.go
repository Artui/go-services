// Package aguiadk serves an ADK agent over AG-UI: one aguix run is one ADK
// invocation, rebuilt from the conversation the client sends.
//
// A call that ADK's tool confirmation gates ends the run on an AG-UI interrupt,
// and the client's answer in the next run's resume runs or denies it; Approval
// gates the specs that declare themselves destructive.
//
// The client's shared state seeds the session's state, and the agent's writes
// to it stream back as STATE_SNAPSHOT and STATE_DELTA.
//
// A model's thinking streams as REASONING frames, each message sealed with the
// signed blocks it holds, and the next run hands every block back unchanged.
// PublishInvalidation tells the page which of its resources a call changed.
package aguiadk
