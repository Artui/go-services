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
// PublishInvalidation tells the page which of its resources a call changed,
// and PublishSuggestions, PublishChart and PublishCompaction send the activities
// the web component draws; what a call publishes goes out after its own result.
// A sub-agent that agenttool runs is announced with SUBAGENT_STARTED, ends in
// SUBAGENT_FINISHED or SUBAGENT_ERROR, and reports its calls as they happen.
//
// # Running a resumed call once
//
// The bridge on its own keeps no state, so an identical resume sent twice runs
// the call twice, under the same tool-call id both times. WithThreadStore
// refuses the second once the first has finished, since the stored thread then
// answers the call, but two sent at once can still both run. A spec that must
// not repeat its effect keys idempotency on that id: an adkx.Principal returns
// agent.Context's FunctionCallID with the user, the registry's resolver puts
// them in Deps, and the spec records the pair under a unique constraint in the
// transaction that does the work. Key on the user too, because without a thread
// store a resume's id comes from the history the client posts: it is only as
// trustworthy as the client, and a client that changes it gets a new key. This
// guards against a repeated request, not against a hostile client.
//
// # Prompt caching and large results
//
// ADK's anthropicmodel (v2.8.0) caches only when ClientConfig.PromptCache is
// set. It then marks the last tool, the system block and the last cacheable
// message block on every model call, so each round's new tool output, a large
// page of results included, is written to the cache once and read by later
// rounds. At Anthropic's 5-minute prices (a write costs 1.25 times the input
// price, a read 0.1 times) that beats no caching for all but the output no
// later round reads. A consumer observed about 160k cache-write tokens, which
// they put at $0.44, in a run that paged for five rounds; both figures are
// their observation, not reproduced here.
//
// To check yours, compare cachedInputTokens with cacheWriteInputTokens in the
// usage of RUN_FINISHED, which sums the run's model calls. Writes that stay
// near the input while reads stay near zero mean the prefix is being rewritten
// every round. Rule out an expired cache lifetime and an instruction or tool
// list that changes between calls; what is left means this bridge is not
// rebuilding a byte-stable prefix, which is a defect worth reporting with a
// recorded request.
package aguiadk
