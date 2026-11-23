// Package aguiadk serves an ADK agent over AG-UI: one aguix run is one ADK
// invocation, rebuilt from the conversation the client sends.
//
// A call that ADK's tool confirmation gates ends the run on an AG-UI interrupt,
// and the client's answer in the next run's resume runs or denies it; Approval
// gates the specs that declare themselves destructive.
package aguiadk
