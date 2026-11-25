# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each module in this repository is versioned and tagged independently, and the
`## Adapters` section below carries their tag lines.

The dated version headings are the **kernel's** versions, and they cover a
release ROUND rather than only the kernel module: a kernel tag is followed by a
sweep raising every adapter's floor, so the adapter work that ships alongside it
is written up under the same heading. The alternative -- six changelogs, or an
adapter's entry filed under a version it does not have -- is worse than one
heading meaning "the round that kernel version opened".

Corrected 2026-09-08. This paragraph read "entries below cover the kernel
module", which the entries themselves had not obeyed since `aguix` was added,
and a rule nothing follows is worse than no rule.

## Adapters

Each adapter is its own module and carries its own tag line. All three were
released at **v0.1.0** on 2026-09-04, against kernel v0.3.0, once the
conformance suite could show they answer the same.

- **`httpx/v0.1.0`** -- mounts a registry on `net/http`'s `ServeMux`, with no
  third-party dependency, so it also reaches chi and echo.
- **`ginx/v0.1.0`** -- the same over a Gin router.
- **`mcpx/v0.1.0`** -- exposes a registry as MCP tools through the official SDK,
  handing it the schema the kernel already reflected rather than a second one.

All three moved to **v0.1.1** on 2026-09-04, raising their kernel floor from
v0.3.0 to v0.4.0. That raise is the whole release: until it happened, installing
an adapter resolved the old kernel, so the error wording v0.4.0 corrected still
reached every client of every adapter. Nothing else changed in any of them.

`httpx` and `ginx` then moved to **v0.2.0** on 2026-09-05, gaining
`Route.Location` and `WithLocation`. `mcpx` moved to **v0.1.2** the same day
with no change but its floor.

Every adapter floors on the newest kernel, including one that uses nothing the
release added. The alternative leaves that adapter's CI resolving a kernel no
consumer runs: Go takes the maximum across a build, so anyone pairing two
adapters gets the newest kernel regardless, and the low floor only ever applied
to the single-adapter case while the paired case went untested.

`adkx/v0.1.0` on 2026-09-05 publishes a registry as tools for Google's Agent
Development Kit, and it is the same trade `mcpx` makes: `adk-go` reflects its
schemas with `jsonschema-go` at the version this kernel uses, and
`genai.FunctionDeclaration` carries a `ParametersJsonSchema` field, so the
schema the kernel already reflected is handed over as it is.

`conformance` floors at Go 1.26.6 now, the highest of the modules it drives,
which is `adkx`.

`aguix/v0.1.0` on 2026-09-06 serves an agent over AG-UI -- Server-Sent Events
over one POST -- and bridges a registry into it, so a spec becomes a tool an
agent can call and the client watches the call happen. `aguix/v0.2.0` publishes
the output schema of every tool it announces, which the other two adapters
already carried; AG-UI's `Tool` has no field for one, so it rides as an addition
its models tolerate, spelt MCP's way because that is the vocabulary a model
already knows.

Every adapter moved again on 2026-09-07 for the renderer: `mcpx/v0.1.4`,
`adkx/v0.1.2` and `aguix/v0.2.2` gained `WithRenderer` / `WithToolboxRenderer`,
and `httpx/v0.2.2` and `ginx/v0.2.2` moved for the floor alone -- deliberately
gaining nothing, because an HTTP client wants the value it can compute with and
"raw" is best delivered by having nothing to turn on.

`conformance` and `example` are the two modules here that are deliberately never
tagged: they depend on all of the others, and exist to fail when two transports
disagree and when a transaction boundary is wrong.

Every module moved again on 2026-09-08 for kernel `v0.7.1`. `aguix/v0.3.0` is
the only one with a change of its own -- `WhenUserSaysAny`, a new exported
symbol, hence the minor. `httpx/v0.2.3`, `ginx/v0.2.3`, `mcpx/v0.1.5` and
`adkx/v0.1.3` change nothing but their floor.

That is not the same as changing nothing. `mcpx` and `adkx` are where the map
rendering actually reaches a client, and they gain it entirely through the
floor, because the walk that was wrong lives in the kernel. `httpx` and `ginx`
gain nothing and are tagged anyway, which is the rule rather than an oversight:
Go takes the maximum kernel across a build, so a module left behind is the one
combination nobody runs.

`adkx/v0.1.4` on 2026-09-14 carries one thing: `google.golang.org/grpc` raised
to v1.83.2 for GHSA-2v4p-qf9q-27wj (high). Only `adkx` moves, because it is the
only tagged module that resolves grpc at all; its kernel floor was already
v0.7.1 and nothing else changed, so this is a patch.

A tag is the whole delivery here, and that is worth saying plainly: the fix was
on `main` and in no tag for several hours, which means `go get` still resolved
the vulnerable version from the module proxy the entire time. There is no
publish step to forget in this repository -- there is only the tag, which is
easier to forget.

`adkx/v0.2.0` raises `google.golang.org/adk/v2` from v2.6.0 to v2.8.0, which
puts the native Claude backend (`model/anthropicmodel`, added in v2.7.0) within
reach of a consumer. The Go floor of `adkx` rises from 1.26.6 to 1.26.10, ADK
v2.8.0's own, so a consumer on an older toolchain must move.

It also fixes a defect that was in every earlier tag: a tool published by `adkx`
could not run inside an ADK agent. ADK's flow requires every tool to implement
`ProcessRequest`, which `specTool` lacked, so the first model call failed with
`tool "..." does not implement RequestProcessor() method`. Every `adkx` test
called `Run` directly and none went through the flow, so nothing noticed. The
method is added, `RunnableTool` now lists it, and a test drives a scripted model
through `llmagent` and `runner` to assert success, a verbatim refusal and a
redacted internal error. The package example also named an `llmagent.New` shape
that does not exist in ADK v2 and now matches the one the flow test builds.

ADK's breaking changes between the two are in code `adkx` does not touch:
v2.7.0's telemetry schema, its cap of 500 model calls per invocation, its
placeholder errors and its Go floor, and v2.8.0's path containment in
`configurable`. A consumer's agent is affected by the first two. `conformance`
and `example` follow to 1.26.10 and ADK v2.8.0; they are never tagged.

`aguix/v0.4.0` on 2026-11-23 decodes what `@artooi/agents-web-components`
0.4.0 sends, on `@ag-ui/client` 1.0, and makes its outcome visible to it. The
kernel floor stays v0.7.1, still the newest kernel tag.

- **Breaking: `Message.Content` is `json.RawMessage`, read with
  `Message.Text()`.** A user turn sent as content parts -- text beside an
  image -- was an array where a string was expected, so the whole run was a
  400. `Text()` reads content the way `@ag-ui/core`'s `contentToText` does,
  and `TextContent` builds it in Go. The change fails at compile time rather
  than at run time, which is the reason it is a type change and not a second
  field.
- **`Message` models every role of `@ag-ui/core` 1.0**: `toolCalls` on an
  assistant turn, `toolCallId` and `error` on a tool result, `activityType`,
  `encryptedValue`, `metadata` and `subagentRunId`, with the roles `reasoning`
  and `activity` among the ones it keeps. A browser tool's call and its result
  arrive on different runs, and these ids are what pair them. A role this package has
  never heard of is kept as sent.
- **`RunInput` gains `Context` and `Resume`**, the protocol's ambient context
  and the answers to the interrupts a previous run ended on.
- **A denied or failed `TOOL_CALL_RESULT` carries its outcome in `metadata`
  as well as at the top level.** The 1.0 client strips every undeclared key
  before a subscriber sees the event, and the web component reads the outcome
  from `metadata` alone, so until now a refused call rendered as done. A
  success still carries neither key.
- The wire fixtures the tests decode are written by `@ag-ui/client` itself,
  pinned with a lockfile under `scripts/agui-wire/`, and CI regenerates them
  and fails on a difference. They found that the client never sends an
  `activity` message back, so object content reaches a server only from other
  producers.

`aguix/v0.5.0` writes the state, messages-snapshot, reasoning, activity and
custom frames `@artooi/agents-web-components` reads, and lets an agent decide
how its run ends. The sub-agent frames (`SUBAGENT_STARTED`, `SUBAGENT_FINISHED`,
`SUBAGENT_ERROR`) are not written yet. The kernel floor stays v0.7.1.

- **New constructors for the frames the flat `Event` could not carry:**
  `StateSnapshot`, `StateDelta`, `MessagesSnapshot`, `ReasoningStart` through
  `ReasoningEnd` with `ReasoningEncryptedValue`, `ActivitySnapshot` and
  `ActivityDelta` (with `Event.WithReplace`), and `Custom`. Each carries object
  content, a snapshot or a CUSTOM value, which the string fields of `Event`
  cannot: the payload is written under the key its type names, with the bytes
  the caller gave (`json.RawMessage` keeps a number past 2^53 whole). A payload
  that cannot be encoded fails `Emit` instead of writing a frame without it.
  `Event`'s string fields and every existing frame are unchanged byte for byte.
- **An agent can end its own run.** `Stream.Finish(aguix.Success(ids...))` ends
  it with the tool calls the client still has to answer, as
  `pendingToolCallIds`; `Stream.Finish(aguix.Interrupted(aguix.Interrupt{...}))`
  ends it asking for a decision, which the next run answers in
  `RunInput.Resume`. The handler writes it as `RUN_FINISHED`'s `outcome`, where
  it used to write the event itself with none. `Finish` returns an error for an outcome
  the client's parser would reject (no interrupts, a `responseSchema` or
  `metadata` that is not a JSON object) and for this package's own rule that an
  interrupt id and reason, and a pending id, are not empty. An agent that returns an error after `Finish` still
  ends the run in `RUN_ERROR`. `Agent` is unchanged.
- **Each new frame is asserted against bytes `@ag-ui/core` 1.0.4 wrote.**
  `scripts/agui-wire/generate.mjs` writes `aguix/testdata/agui/outbound`
  through the package's own `EventSchemas` and refuses a frame the schema would
  change; CI regenerates the directory with the inbound one and fails on a
  difference. The Go test compares each constructor's frame to its fixture by
  value, so a stripped key or a missing required one fails.

`aguiadk/v0.1.0` on 2026-11-23 is a new module that serves an ADK agent
over AG-UI. `aguiadk.New` takes the root agent and returns the `aguix.Agent`
that `aguix.Handler` serves, so `@artooi/agents-web-components` can talk to an
agent built on `adkx` and `model/anthropicmodel`. It floors on Go 1.26.10,
`adkx`'s, and on kernel v0.7.1, still the newest kernel tag, `aguix` v0.5.0 and
`adkx` v0.2.0.

- **The client's history is the only state.** Each run replays
  `RunInput.messages` into a fresh in-memory session: assistant tool calls
  become function calls with the client's ids, the tool messages that answer
  them become one function-response turn, and reasoning, activity, system and
  developer messages are left out. The trailing user message, or the trailing
  run of tool results, is what the agent answers; a conversation that ends on
  an assistant message finishes without calling the model.
- **Text and tool calls stream as AG-UI frames.** Under ADK's SSE mode the
  model's text arrives as partial events and is repeated in the final one;
  it reaches the client once. Server tools, `adkx`'s among them, report
  `TOOL_CALL_RESULT` with `outcome` `failed` when the result is ADK's lone
  `{"error": ...}`. A model or runner error ends the run in `RUN_ERROR`
  through `aguix`'s redaction, after any open message is closed.
- **Browser tools end the run pending.** `ClientToolset()` turns the run's
  `RunInput.tools` into long-running tools; a run that calls one finishes
  with `pendingToolCallIds`, and the next run carries its result. A response
  that calls a server and a browser tool together runs the server tool,
  streams its result and stops, where ADK alone would call the model again
  with the browser call unanswered. A server tool of the same name wins.
- `WithUserID` sets the ADK user id from the request context; `New` refuses
  to start without it, and `aguiadk.Anonymous` is the explicit choice for an
  unauthenticated mount.
- Its tests record whole runs under `aguiadk/testdata/agui/transcripts`,
  and CI passes each through `@ag-ui/core`'s schemas and `@ag-ui/client`'s
  event verifier, pinned under `scripts/agui-wire/`.

`aguiadk/v0.2.0` adds server-side tool approval, so the web component's
approval card works against a Go server. Its floors are unchanged.

- **`aguiadk.NewApproval(reg.Entries(), ...)`** gates every spec whose
  `Destructive` is true; `Require(names...)` gates more, `Exempt(names...)`
  un-gates, and exempt wins. `Gate(toolset)` wraps the toolset with ADK's
  `tool.WithConfirmation`, which ADK marks experimental; `WithApproval` hands
  the same value to `New`. A gated tool must be on the root agent.
- **A gated call ends the run on an interrupt.** `RUN_FINISHED` carries
  outcome `interrupt` with `id` `int-<callId>`, `reason` `tool_call`,
  `toolCallId`, the message `Approve name(args)?`, a `responseSchema` for
  `{approved, editedArgs?}`, and the spec's `Metadata["x-confirm"]` under
  `metadata`. ADK's placeholder result and its `adk_request_confirmation`
  call are not streamed. A step that also called a browser tool finishes on
  the interrupt, and the client answers the browser call as not finished.
- **The next run's `resume` runs or denies it.** The bridge rebuilds the
  confirmation ADK resumes from, so the approved call runs inside ADK's flow
  and its `TOOL_CALL_RESULT` streams on the original call id. `editedArgs`
  replace the proposed arguments and go through the kernel's validation. A
  denial, a `cancelled` status, or any payload whose `approved` is not `true`
  reports the call with outcome `denied`, and it never runs. A resume that
  names no call left waiting, a call already answered or followed by a user
  message, a call the approval does not gate, or one call twice fails the run
  instead, as does a history that carries an `adk_request_confirmation` call.
  The bridge keeps no state, so the same resume sent twice runs the call twice.
- **`aguiadk.ToolCatalog(reg.Entries())`** serves the `data-tools-url`
  catalog, `[{name, summary, description}]`, with the summary from
  `Metadata["x-summary"]` or the readable name. Mount it behind the agent's
  own authentication.
- A tool wrapped with `tool.WithConfirmation` directly also ends the run on an
  interrupt, where v0.1.0 sent ADK's confirmation request to the browser as a
  pending tool call.
- The resume bodies its tests read are written by `@ag-ui/client` under
  `aguiadk/testdata/agui/inbound`.

`aguiadk/v0.3.0` carries AG-UI shared state through a run, so a client's
`state` reaches the agent and the agent's writes reach the client. Its floors
are unchanged.

- **`RunInput.state` seeds the run's session state.** A JSON object becomes
  the session's initial state, with numbers kept as `json.Number`, so a tool
  reading a seeded number gets a `json.Number` rather than a `float64`, and an
  instruction template such as `{draft}` sees the client's value. A value the
  run writes keeps its Go type until the next run seeds it back. A `temp:` key
  the client sends is dropped. A state that is present but not an object fails
  the run; absent and `null` are no state.
- **A non-empty state is echoed as a `STATE_SNAPSHOT`** right after
  `RUN_STARTED`. `{}`, which `@ag-ui/client` sends by default, adds no frame.
  A conversation that ends on an assistant message still runs nothing and
  sends no snapshot.
- **Every write to the session state is a `STATE_DELTA`.** Each event's
  `Actions.StateDelta` becomes one frame after that event's own frames, with
  one RFC 6902 `add` per key, in key order, the key escaped as a JSON Pointer
  token. A write of `nil` sends `"value":null`, and a value JSON cannot encode
  fails the run. A `temp:` key is never sent,
  since ADK does not keep it past the invocation. When the client sent no
  state, the run's first write is a `STATE_SNAPSHOT` instead, so there is an
  object to patch.
- **No `MESSAGES_SNAPSHOT` is sent.** The bridge rewrites history only inside
  ADK's session: reused call ids are renamed and a resume adds the
  confirmation ADK resumes from. Neither change reaches the client's thread.
- State written on a resumed run, or by an agent or tool callback, streams the
  same way.
- A run's two transcripts, a seeded run and the next run seeded from the
  state the first produced, are recorded under
  `aguiadk/testdata/agui/transcripts` and checked by the wire script. The
  seeded input is the body `@ag-ui/client` wrote for `aguix`'s reasoning
  fixture.

`aguix/v0.6.0` says how a run ended in the fields the web component reads off
its last frame. Its floors are unchanged.

- **`RUN_ERROR` carries a `code`**, in the vocabulary django-ag-ui uses:
  `timed_out` for an error that reports itself a timeout
  (`context.DeadlineExceeded`, a net or os deadline), `error` for anything
  else. The message is redacted as before; the code is not. `Event.WithCode`,
  `CodeError` and `CodeTimedOut` are exported.
- **`Stream.SetUsage`** reports token usage, written on `RUN_FINISHED` as
  AG-UI 1.0's `usage` list of `TokenUsage`. It is absent unless the agent sets
  it and never sent on `RUN_ERROR`. A later call replaces an earlier one, an
  entry with neither input nor output tokens is dropped, a zero count is
  omitted, and a count below zero or above 2^53-1 is refused.
- **`WithRunTimeout(d)`** bounds a run. When the agent fails after the
  handler's deadline passed, the run ends as a cancel, `RUN_FINISHED` with
  outcome `cancelled`, never `RUN_ERROR`. Text, reasoning and tool-call spans
  still open are closed first, since the client's verifier rejects a finish
  over them, and the usage is kept. The agent's error still reaches
  `WithOnError`. A deadline the handler did not set is the agent's
  `RUN_ERROR` `timed_out`; a client that hung up gets no terminal frame.
- **`Cancelled()`** is accepted by `Stream.Finish`. A cancelled outcome goes
  only to a client that declares `protocolVersion` 1.0 or newer, now read
  into `RunInput.ProtocolVersion`; a value outside `MAJOR.MINOR` counts as
  newer. An older client's schema rejects the outcome, so that client gets a
  bare `RUN_FINISHED`, as pydantic-ai does.
- `Stream.Emit` is now safe to call from several goroutines.
- Its tests record timed-out runs under `aguix/testdata/agui/transcripts`, and
  `scripts/agui-wire/verify-transcripts.mjs` checks them beside aguiadk's.

`aguiadk/v0.4.0` hands the end of an ADK run to the fields `aguix/v0.6.0`
added, and marks its interrupts the way the web component reads them. Its aguix
floor rises to `v0.6.0`, so a failed run's `RUN_ERROR` now carries a `code`.

- **An interrupt's `metadata` carries `kind: "approval"` and `destructive`.**
  The web component restores a waiting thread only for an approval, and its
  inbox approves in place only when `destructive` is `false`. `destructive` is
  `true` when the `Approval`'s own gate asks about the tool, by a `Destructive`
  spec or by `Require`, as django-ag-ui decides it. A call some other
  `tool.WithConfirmation` gates is `false` only for a spec the `Approval` knows
  does not declare itself destructive; an exempted destructive spec, a tool that
  is no spec, and any call without `WithApproval` stay `true`, since the inbox
  approves a `false` one in a single click. `x-confirm` sits beside them as
  before.
- **`RUN_FINISHED` carries the run's token usage**, summed over every model
  call the run made, one entry per model in the order they first answered. Input
  is the prompt plus any tool-use prompt. Output is the total less the input,
  since ADK's OpenAI models count reasoning inside the candidates while its
  Gemini and Anthropic models do not; without a usable total it is the
  candidates plus the thoughts. Reasoning and cache counts are parts of them, as
  AG-UI counts them, and cache writes are read from the metadata ADK's
  Anthropic model sets. Only complete events count, so a
  provider that repeats its usage on SSE partials is not added twice. The sum is
  set after each call, so a run that ends on the handler's timeout keeps it. A
  run with no usage sends none, a negative count fails the run, and a
  `RUN_ERROR` never carries usage.
- **`WithMaxLLMCalls(n)`** sets ADK's `RunConfig.MaxLLMCalls`, with ADK's
  meanings: zero is its default and a negative value is no limit. A run that
  would pass it ends in a `RUN_ERROR` with code `error`.
- Two transcripts are added under `aguiadk/testdata/agui/transcripts`, a
  two-call run's usage and an interrupt that is not destructive, and the wire
  script checks them.

`aguiadk/v0.5.0` streams a model's thinking and lets a tool tell the page what
it changed. It needs no new aguix type, so its floors stay where `v0.4.0` set
them.

- **Thinking streams as `REASONING_*` frames.** `Thought` parts are sent as
  `REASONING_START`, `REASONING_MESSAGE_START`, the deltas, `REASONING_MESSAGE_END`
  and `REASONING_END`; text and reasoning never interleave, each ending the
  other, and a run that fails mid-thought still closes its span before
  `RUN_ERROR`. The reasoning message is never a tool call's parent.
- **Each reasoning message is sealed with the blocks it holds**, as its
  `REASONING_ENCRYPTED_VALUE` of subtype `message`, after the span: a JSON
  object listing every thinking block in order, each as the bytes of the text
  it covers and its signature in base64. `anthropicmodel` streams a response's
  thinking blocks with no boundary between them and signs them only in the
  complete response, so they stream as one reasoning message, and its seal
  lists them all, a redacted block as one that covers no text. Thought parts
  that did not stream are each a span of their own, a redacted block an empty
  one. A stream whose text is not the response's thought parts joined, which
  `anthropicmodel` does not produce, is not sealed.
- **The next run hands Claude its thinking back.** Claude requires the final
  assistant turn of a thinking-enabled request to start with its thinking
  blocks, which is the turn a tool result or an approval resume answers. A
  `reasoning` message with an `encryptedValue` is split back into the blocks
  its seal lists, each replayed as a signed `Thought` part, unchanged and in
  order, on the model turn it sits in, ahead of that turn's text and calls;
  the client files a block sealed after the text started behind the assistant
  message, so the turn is found on either side. A `reasoning`
  message with no `encryptedValue`, one whose seal does not split its text
  exactly, or one with a user or tool message before the next model turn, is
  still dropped.
- **`PublishInvalidation(ctx, reason, keys...)`** queues a CUSTOM
  `ag_ui.invalidate` event, `{keys, reason}` with an empty reason sent as
  `null`, as django-ag-ui sends it. A spec's handler calls it with its
  `Ctx.Context`. What is queued is sent after each event of the run: within one
  agent that is the event carrying the call's result, so after a spec's
  transaction has committed, but under a `ParallelAgent` a sibling branch's
  event can send it before the commit. Whatever is still queued when a run
  ends, a failed one included, is sent before it finishes. It returns
  `false`, and queues nothing, outside a bridge run or with no keys.
  `InvalidateEventName` names the event.
- Five transcripts are added under `aguiadk/testdata/agui/transcripts`, and
  `scripts/agui-wire/generate.mjs` writes three inbound fixtures from three of
  them: the client's request after the page answers a browser tool the model
  thought before, the same after a Claude response with two signed thinking
  blocks and a redacted one, served to `anthropicmodel` as the Messages API
  streams it, and the client's resume of an approval the model thought before.

`aguiadk/v0.6.0` sends what a tool publishes after that tool's own result, adds
activity helpers, and reports a delegated sub-agent's calls. It needs no new
aguix type, so its floors stay where `v0.5.0` set them.

- **An invalidation now follows its own call's `TOOL_CALL_RESULT`, even under a
  `ParallelAgent`.** What a tool call publishes is held under that call's id and
  sent right after the event that carries its result, so a sibling branch's
  event no longer carries it out before the spec's transaction commits. The call
  is found from a context the call derives, a spec's transaction included. A
  call made by a sub-agent that `agenttool` runs counts as the delegating call,
  whose result is the one the client sees. A streaming tool, which ADK runs
  without the tool callbacks, keeps `v0.5.0`'s rule: after the run's next event.
  What is published outside a call, from a model callback say, now goes out
  ahead of the run's next event rather than after it. Whatever is still held
  when a run ends is sent before it finishes, as before. The bridge fans a
  step's calls out through its own `platform.TaskRunner`, still one goroutine
  per call, so a runner installed on the request's context is replaced for the
  run.
- **`PublishSuggestions(ctx, id, prompts...)`** sends an `ACTIVITY_SNAPSHOT` of
  type `suggestions`, `{prompts}`, with `replace: true`: at most
  `MaxSuggestions` (4) prompts of at most `MaxSuggestionChars` (120) characters,
  trimmed, none blank. An empty id is `suggestions-<uuid>`; the same id replaces
  a row already drawn.
- **`PublishChart(ctx, id, ChartSpec)`** sends one of type `chart`, the content
  django-ag-ui's `ChartSpec.as_content` builds: `kind` (empty is `bar`),
  `labels`, `series` of `{label, points}`, a `title` when set, and `Metadata`
  merged beneath them. A spec the web component would drop is refused: no labels
  or series, more than `MaxChartLabels` labels or `MaxChartPoints` points, a
  series of the wrong length, or a point that is not finite or is past
  `MaxChartMagnitude`. An empty id is `chart-<uuid>`.
- **`PublishCompaction(ctx, before, after)`** sends one of type `compaction`,
  `{removed, before, after}`, when `after` is below `before`.
- These follow the invalidation's rule: published inside a call, after its
  result; anywhere else, ahead of the next event. Each returns `false` outside a
  bridge run, and the first two an error for what they refuse.
- **A sub-agent that `agenttool` runs reports each call it makes** as a CUSTOM
  `ag_ui.subagent` event, `{delegationId, agent, phase, status, tool:
  {toolCallId, name, ok}}`, as django-ag-ui sends it: `phase` is `tool_call`,
  with `ok` null, then `tool_result`, with `ok` false for a failed call or one
  to a tool the agent lacks. `delegationId` is the delegating call's id. These
  go out as they happen, while the delegating call runs, rather than after a
  result; a run's own calls are on the wire already and send none.
  `SubagentEventName` names the event. The `SUBAGENT_*` lifecycle events need
  fields aguix does not have yet, and are not in this release.
- Three transcripts are added under `aguiadk/testdata/agui/transcripts`: an
  invalidation under a `ParallelAgent`, activities from a tool and a model
  callback, and a delegated sub-agent's progress.

## [Unreleased]

### Added

- **`aguiadk.WithRunContext` delivers the run input's context to the model**, opt-in:
  the entries are fenced as `<untrusted-client-context>` (marker defused inside
  values, 20000 characters, visible truncation) and appended to the system
  instruction of every model call of the run's own agents, never to the messages.
- **`aguiadk.WithSkills` and `aguiadk.SkillCatalog` serve server-resolved skills**: a
  run whose latest user message opens with `/name` gets `# Skill: <name>` and the
  body in its instructions, `/name` left as sent; the catalog answers GET with the
  web component's `data-skills-url` entries, never a body.
- **`make release` and `make release-verify` cut and check a module's tag.**
  `make release MODULE=<dir or kernel> VERSION=<version>` refuses a dirty tree,
  a checkout that is not `origin/main` (or `SHA`), a version already on the
  remote, locally or in sum.golang.org, a version with no CHANGELOG paragraph on
  that commit, and an adapter whose kernel floor is not the newest kernel tag;
  it creates the annotated tag locally and pushes only with `PUSH=1`.
  `make release-verify` exits non-zero until the remote tag, the module proxy
  and the sum database all serve the version. A `release` workflow tags on
  merge when the merge adds a kernel heading or a new module token to the
  Adapters section, through the same script.

### Changed

- **`adkx` moves to ADK v2.8.0 and Go 1.26.10, and its tools now run in an ADK
  agent.** See the `adkx/v0.2.0` paragraph under Adapters; `conformance` and `example` follow, and `make check-floors`
  names the new floors.
- **Dependabot now watches every module in the repository, in one grouped
  entry.** Four were missing from the list -- `adkx`, `aguix`, `conformance`
  and `example` -- so nothing was ever proposed for them, which is precisely
  what the comment above that list warned would happen without anything failing
  to say so. The grouped `directories` form replaces one entry per module
  because the `replace` edges mean these modules are not independently
  updatable: they have to move in a single pull request or none of them can be
  green.

### Fixed

- **The coverage gate measured only the kernel in CI.** `scripts/coverage.sh`
  ran `go tool cover -func` from the repo root, which cannot resolve a
  submodule's packages without a `go.work` (gitignored, so absent in CI); the
  failure was discarded, each adapter printed an empty total and the gate
  exited 0. It now runs inside each module and a failure is fatal. The
  exclusions match the whole line, so an uncovered function whose name is a
  prefix of an excluded one is no longer exempted.

- **The release workflow and `make release` failed intermittently on a long
  Adapters section.** The token check piped `awk` into `grep -q`, which quits at its first match and
  kills `awk` with SIGPIPE; under `pipefail` that read as a missing token. The
  section is now captured first.

### Documentation

- **`WithAtomic` now names the write-lock consequence of its own ordering.**
  Resolving dependencies inside the transaction is deliberate and is what lets a
  `Permit` function read the row it gates -- but resolving is a read, so on
  SQLite every write transaction opens as a reader and then tries to upgrade,
  which it refuses rather than queues as soon as two mutations overlap.

  The doc carries the DSN that survives it and the measurement behind it: over
  eight concurrent mutations, `_txlock=immediate` alone failed 8 of 8, adding
  `busy_timeout` left 3 of 8, and only WAL plus the timeout plus immediate
  failed none. The obvious-looking DSN is the worst of them.

  Written down because a single-threaded suite structurally cannot find it. The
  consumer that did had 40 of its 41 tests green under the broken DSN.


## [0.7.1] - 2026-09-08

### Fixed

- **The renderer walked straight past a map-valued field.** `Render` looked up
  each member of an object in `schema.Properties`, which a `map[string]T` does
  not have: its value schema is the open tail, `additionalProperties`, and every
  entry fell through carrying its `x-render` keyword untouched.

  It failed silently, which is what makes it worth a changelog entry longer than
  the fix. The schema still declared the field, the adapter still forwarded it,
  and the reader got the raw integer -- so one MCP payload carried the same
  amount twice, rendered inside a list of line items and raw in the map of
  totals beside it. Named properties still win where a schema declares both.

- **`aguix.WhenUserSays` is a conjunction, and `WhenUserSaysAny` is now beside
  it.** The doc comment always said "every one of these"; the name reads as "any
  of these" to most people, and nothing distinguishes them at the call site --
  an unmatched rule simply loses to the next one, so a script with a fallback
  answers something plausible and nothing fails.

  Every call in this repository passes a single word, which is the one case
  where the two readings agree, so nothing here could have shown the difference.
  A consumer wrote four rules listing synonyms and got the fallback for all of
  them.

  Note the empty case differs on purpose: `WhenUserSays()` matches every run,
  because a conjunction over nothing holds, and `WhenUserSaysAny()` matches
  none.

Both were found by an outside consumer resolving the published tags rather than
this working tree -- the gap `example/` and `conformance/` cannot cover, because
both carry `replace` directives and have never read a published module.

## [0.7.0] - 2026-09-07

### Added

- **`Renderer.RenderValue`**, which does the JSON round trip `Render` needs its
  input already to have had. `Render` walks decoded JSON -- maps, slices and
  scalars -- because that is the shape a formatter can change freely: replacing
  an integer with a string is ordinary there and impossible in the Go value.
  So something has to marshal first, and every adapter would otherwise write
  the same three lines. The cost is one encode/decode per rendered call, paid
  only where a renderer is configured.

## [0.6.0] - 2026-09-07

### Added

- **A renderer, so an agent-facing transport can show a value to the reader who
  asked for it.** `RenderKeyword` (`x-render`) is a schema extension a field
  uses to ask for formatting; `Formatter` is the function that does it, handed
  the request context and the principal; `NewRenderer` and `Renderer.Render`
  walk a decoded value against its schema and apply what the schema asks for.

  The split is the whole design. A declaration is a fact about the FIELD, true
  of every row and every reader -- "this integer is money in minor units" -- and
  it is made once, at registration, with no reader in front of it. A formatter
  is a fact about the READER: it runs adapter-side, per call, and it is what
  knows the currency and the timezone. A description can say "in minor units"
  and can never say "half past two on Tuesday", and a struct tag has the
  identical limit for the identical reason.

  Nothing in `Dispatch` calls it. An HTTP transport gets raw values by doing
  nothing, which is what lets an API route and an agent tool share one `Spec`.

  A formatter is handed the principal rather than `Deps`, and that is a safety
  property rather than a convenience: `Deps` is resolved inside the transaction
  and its handle is dead by the time a `Result` exists, so a renderer reaching
  for it would be reaching into a closed transaction.

- **`Register` refuses a `SchemaFor` declaration the declaring type cannot
  serve.** A type that advertises a string and marshals an object was accepted
  and then contradicted itself on every wire. The check compares the JSON
  *kind* only -- full validation would refuse enums whose zero value is not a
  member, which is legal and common.

  This is what makes `type Instant time.Time` fail loudly instead of quietly:
  a defined type does not inherit `MarshalJSON`, and `time.Time`'s fields are
  unexported, so it would advertise a date-time string and serve `{}`.


- **`aguix`, a fifth adapter: an agent over AG-UI.** An HTTP handler that
  decodes `RunAgentInput` and streams the protocol's events, a `Toolbox` that
  turns registry operations into tool calls the client can watch, and a
  `Scripted` agent so an endpoint can be exercised end to end with no model.

  It **does not** get a conformance column, and that is a decision rather than
  an omission: AG-UI streams an agent's turn where the other four call one spec,
  so there is no single call whose answer could be lined up against theirs. What
  it gets instead is the worked example driving a run and then reading the
  database -- `TestAnAgentBorrowRollsBack` asserts that a refused borrow through
  a transport with no status code still leaves no loan row behind.

  The event types are written from the published protocol rather than taken from
  the community Go SDK, which carries no tags -- the pins found in the wild span
  nine months with no shared floor, and roughly seventy repositories
  reimplement these types rather than depend on it. The cost of that choice is
  the obligation to be right about the wire, so `TestEventFrames` asserts the
  encoded bytes against field names read out of the `@ag-ui/core` package the
  web component actually resolves.

- **A browser demo, `example/agentdemo`.** It serves the library registry as an
  AG-UI agent and a page that drives it through the real `<ag-ui-chat>` web
  component, loaded from a CDN at a pinned version. There is no model and no API
  key: the script decides which tool to call, and everything under that is the
  kernel every other adapter runs, against the same SQLite database.

  Three findings came out of pointing a real client at it, all in
  `example/FRICTION.md`. One is that **a failed server-side tool call cannot be
  rendered as failed**: as published, `TOOL_CALL_RESULT` carries a content
  string and no field saying how the call ended, and the component settles every
  result as done, so a refusal arrived as a card reading "done" with "no copy is
  on the shelf" folded inside it. `aguix` marks a failure twice. The `Error: `
  prefix on the content is the convention the component already emits for its
  own browser-side tool failures, so the model sees one shape whichever side the
  tool ran on. Beside it the event now carries an optional `outcome`, either
  `failed` or `denied` -- pydantic-ai's own vocabulary rather than one invented
  here, proposed upstream and emitted ahead of standardisation. A success
  carries no such key at all, so a client that has not adopted the field sees
  exactly the stream it saw before, the card reading "done" included, and that
  part remains the component's to fix. The sharper one is that **the tests were
  green while
  the demo was lying**: the script ended with "That is done." and said it over a
  refusal, with the tool result reading "no copy is on the shelf" directly
  above. Every assertion held, because none of them had an opinion about whether
  the sentence was true.

- **`adkx.RunnableTool`**, the shape ADK actually dispatches on.

  ADK's own version is unexported and matched structurally at the point of use,
  so nothing in the compiler tells a consumer that a method drifted and there is
  no name to assert against. Every consumer that wants to drive a tool -- which
  mostly means every consumer that wants to test one -- was writing the
  interface out; three copies existed here before this.

  All three are now aliases, so there is one declaration of the shape in the
  repository, in the package best placed to notice when it stops being right.
  `adkx` carries `var _ RunnableTool = (*specTool[struct{}])(nil)`, verified by
  renaming a method, which fails that line by name.

  It is not a promise about ADK's API, which is not ours to make. It is a
  promise that what `adkx` publishes is runnable, which is.

- **The worked example is mounted on all four adapters.** `example/` drives one
  library-lending registry through `net/http`, Gin, MCP and now ADK, and asserts
  they leave the same rows behind. That is a different question from the one the
  conformance suite asks: conformance compares the transports' own answers, and
  this compares what they committed.

  Verified by breaking it -- swallowing the ADK leg's error makes
  `TestEveryTransportRollsBackTheSameWay` fail with
  `httpx produced true, adkx produced false`.

  Two frictions came out of writing it, and are in `example/FRICTION.md`. The
  one worth acting on: **ADK's tool interface is unexported**, so every consumer
  that wants to drive a tool in a test restates it -- this module, `adkx`'s own
  suite and the conformance driver all carry a copy, and none of the three is
  tied to ADK by the compiler. `adkx` exporting that interface would give
  consumers one name and make `adkx` the single place that notices a change.

- **`adkx` has a column in the conformance suite**, which is what makes it a
  fourth transport rather than a fourth thing that happens to compile. It is
  compared against HTTP on what every transport can express: whether the call
  failed, the value it produced, the per-field messages of a rejected input, and
  that nothing an operator wrote reached the client.

  Falsified on both axes rather than assumed. Removing `adkx`'s redaction fails
  it with `adkx leaked the internal error text`; dropping a field from its
  result fails it with the two maps printed side by side.

  **One thing the column deliberately does not cover** is written into the
  driver: in production ADK hands a tool a `map[string]any` it decoded itself,
  so a large identifier is already a `float64`, while this harness calls the
  tool directly with the case table's own `int64`. Reproducing that would mean
  standing up an agent and a model to demonstrate a rounding `adkx` cannot
  prevent. The limit is recorded so a green suite is not read as its absence.

- **`adkx`, a fourth adapter: a registry as tools for Google's ADK.** The
  declaration carries the kernel's own `*jsonschema.Schema` object, asserted by
  pointer identity rather than by comparison, so the schema a model is shown and
  the schema the kernel enforces cannot drift apart.

  Two facts about that wire are documented on the package because neither is
  fixable here. **Arguments arrive as a map**: `genai.FunctionCall.Args` is a
  `map[string]any`, so a model's tool-call arguments are decoded before any tool
  in any ADK program sees them and an integer past 2^53 is already a float64 --
  the same registry is exact over MCP, whose SDK carries the raw JSON, and lossy
  here. **And a tool's error text is shown to the model**: ADK renders a
  returned error as `map[string]any{"error": err.Error()}`, so this package
  returns the kernel's taxonomy verbatim and replaces everything else with a
  fixed sentence, sending the real error to `WithErrorReporter`.

  It floors at Go 1.26.6, `adk-go`'s own floor and the highest in the
  repository. That is the module-per-boundary layout paying out: a consumer who
  wants an HTTP route and not an agent framework is not dragged onto it.

- **The kernel-import guard knows about ADK.** It matched `net/http`,
  `gin-gonic` and `modelcontextprotocol`, so a kernel file could have imported
  `adk` or `genai` and nothing would have fired. Verified by making it fire.

- **`Route.Location` and `WithLocation` on both HTTP adapters.** A successful
  response carries a `Location` built from a template naming the operation's
  output fields -- `"/loans/{loan_id}"` -- which is what a 201 is specified to
  do and what `Result` had no channel for. `mcpx` gains nothing: MCP has no
  headers, and inventing somewhere to put one would be an HTTP concept on a wire
  that does not want it.

  The filling is the kernel's `ExpandLocation`, so both adapters answer the same
  path for the same output. A template naming a field the output schema does not
  declare is refused when the handler is built.

- **The conformance suite compares the `Location` header.** It is not a
  formality: the two adapters build it at different points -- `httpx` expands
  before marshalling and clears the header if the marshal fails, `ginx` marshals
  first and never expands -- so they reach the same answer by different routes.
  Both are mounted over a value that cannot be encoded, and moving `ginx`'s
  header write before its marshal fails the suite with
  `Location diverges: httpx="" ginx="/unencodable/fixed"`.

### Changed

- **Every module floors on kernel v0.5.0**, `mcpx` included, and that is now the
  repository's rule rather than a judgement per release. A module left behind is
  the one combination nobody runs.

## [0.5.0] - 2026-09-05

### Added

- **`ExpandLocation` and `Entry.CheckLocation`, the kernel half of a `Location`
  header.** A template is a path with `{name}` placeholders naming output fields
  by their JSON name -- `"/loans/{loan_id}"` -- and the two functions are the
  request-time and mount-time halves of the same guarantee, exactly as
  `EncodeParams` and `CheckCaptures` are on the input side.

  It lives in the kernel for the reason `StatusFor` does: both HTTP adapters
  need it, the result is client-visible, and two adapters carrying their own
  copies would be two servers disagreeing about where the thing they just
  created lives. The syntax is `{name}` on both, including Gin, because a
  Location is a string being filled rather than a pattern being matched.

  Values are path-escaped, since a slug carrying a slash would otherwise forge a
  path segment. Numbers go through `UseNumber`, so an identifier past 2^53 is
  not rewritten on its way into the header -- the same float64 round trip that
  cost this project a defect in `EncodeParams`.

  **No adapter reads it yet.** Wiring `Route.Location` into `httpx` and `ginx`
  is the next release, and it cannot happen until this one is tagged.

- **The wire a client actually receives is now asserted end to end.** The
  adapters' own suites build their expectations from the sentinel, so they check
  the composition -- the sentinel's words, then the service's -- and would keep
  passing if a sentinel's wording became nonsense. The kernel's own test checks
  the property. Neither of them reads a response body.
  `example.TestTheWireCarriesNoPackageName` spells all four answers out in full,
  through a real registry over a real database, across all three transports. It
  was falsified by reinstating the prefix, which fails it on each of them.

## [0.4.0] - 2026-09-04

### Changed

- **`ErrPermission`, `ErrNotFound` and `ErrConflict` no longer carry a
  `services: ` prefix.** All three adapters render those three verbatim and
  redact everything else, and the documented way to use them is to wrap -- so
  `fmt.Errorf("%w: no copy of %q is on the shelf", services.ErrConflict, title)`
  was serving `{"error":"services: conflict: no copy of ..."}`, putting the
  implementation's package name in the middle of a sentence about a library
  book. Over MCP the reader is a model that may take "services" for a term of
  art. The rule the change settles on: the prefix is for logs, and a sentinel
  whose words reach a client is not going to a log. `ErrConfiguration` and
  `ErrBodyTooLarge` keep theirs, because both are redacted before any client
  sees them. `TestClientFacingSentinelsCarryNoPackagePrefix` holds the split.

  This is visible on every wire. `errors.Is` and `errors.As` are unaffected, and
  any test comparing a rendered body should build the expectation from the
  sentinel rather than spelling it out -- the adapters' own suites now do, which
  is what let this land without a flag day.

### Added

- **A package doc for the kernel.** Every adapter carried a `doc.go` and the
  package everyone imports first did not, so pkg.go.dev showed it with an empty
  overview. It covers the declaration shape, the three validation layers, the
  ordering rule, the error taxonomy's two audiences, and how to map a driver's
  errors onto it.
- **`WithAtomic` now documents what decides the shape of `D`.** Only an atomic
  entry runs inside the callback, so a `Query` resolves with no transaction in
  its context and a dependency type holding a concrete `*sql.Tx` is empty for
  every read in the registry. It also says why its type argument cannot be
  inferred, so the next reader does not re-open the question.
- **`ginx` documents the two response shapes** as a table, the way `httpx`
  already did. A validation failure answers `errors` with a map and everything
  else answers `error` with a sentence, and nothing in the body says which --
  a client branches on the status.

- **A worked example, `example/`, running against a real database.** One
  registry -- a library lending service over `database/sql` and SQLite -- mounted
  on all three adapters at once, so the kernel's ordering rule has a composed
  proof for the first time. The conformance suite cannot supply one: it runs
  with an empty `Deps` and no database, so nothing in it can tell a correct
  transaction boundary from a broken one. Like `conformance`, the module is
  never published and carries `replace` directives.
- **The example carries its own falsification.** `TestRollbackAssertionHasTeeth`
  runs the same dispatch against a registry whose dependencies resolve *outside*
  the boundary and asserts the orphan row appears, which is what makes the
  rollback assertion beside it mean something. Its companion asserts the other
  half: on the happy path the correct registry and the broken one are
  indistinguishable.
- `example/FRICTION.md`, which is the module's actual output -- six findings
  written down where they were met, and four things that were expected to hurt
  and did not.

## [0.3.0] - 2026-09-03

Everything here came from three independent reviews, one per adapter, by
reviewers who had not written the code they read.

### Fixed

- **A request body's numbers are no longer rewritten.** `EncodeParams`
  re-encodes the body it is given, and decoding into an `any` turns every number
  into a `float64`: an identifier of 9007199254740993 was re-encoded as ...992,
  and the literal `1.0` as `1`. The path only ran when a request carried a
  parameter, so the same payload behaved differently depending on the shape of
  the route it arrived at -- exact with no captures, rounded with a query
  string, and in the `1.0` case a 400 in one and a 201 in the other. This is the
  hazard `DispatchValue`'s own documentation warns callers about, reintroduced
  one function away from that warning.
- **A nil `*ValidationError` no longer panics.** A helper typed to return
  `*ValidationError`, assigned into an `error`, produces a non-nil error holding
  a nil pointer, and `errors.As` matches it -- so an adapter's validation arm
  handed a nil receiver to a renderer. `FieldMap` and `Error` now tolerate one.
  On a transport whose server recovers per connection this was a 500; on the MCP
  adapter, whose handler runs on a goroutine nobody recovers, it ended the
  process.
- **An explicit JSON `null` argument set is accepted.** Absent and empty already
  meant "nothing was sent" and `null` did not, so a client rendering no
  arguments as `null` was refused with a schema error naming a type and no field
  to correct.
- **A `Spec.Status` outside 200-599 is refused at registration.** A 1xx is an
  interim response: the server writes it, does not commit, and the next write
  commits an implicit 200 behind it, so the status was a promise nothing could
  keep. Both HTTP adapters had grown their own range check after finding this
  independently, and for a while the two disagreed.

### Added

- **`ValidSuccessStatus`**, because three places needed the same answer and two
  of them had already diverged. Its documentation carries the evidence, gathered
  over a real server with `httptrace`: an `httptest.ResponseRecorder` reports the
  1xx faithfully while hiding what the wire does, which is how this survived
  review on both adapters.

### Changed

- The coverage gate keys on the full import path rather than the base name. One
  exclusions file serves every module, so the single kernel entry would have
  silently exempted a function of that name in any module that grew one.

## [0.2.0] - 2026-09-03

Everything here came from building the first three adapters against v0.1.0.
Most of it is places the kernel described a rule instead of applying it, which
is what a first consumer is for.

### Changed

- **`EncodeParams` takes its two parameter sources separately and merges them
  itself**: `EncodeParams(schema, body, query, captures)`. It previously took
  one already-merged map and stated the precedence in a comment, which makes a
  security rule the adapter's job. An operation mounted at
  `/tenants/{tenant}/invoices` can be rescoped by a client with `?tenant=other`
  if an adapter merges the other way, and nothing fails when it does.
  **Breaking.**
- **A route capture the schema does not declare is refused** rather than
  silently dropped. That same operation, against a spec declaring no `tenant`
  field, previously ran completely unscoped and returned no error. Query
  parameters are still dropped: a query string carries analytics noise nobody
  declared, while a capture was written into the pattern by hand and is always
  load-bearing.
- **A body that is valid JSON but not an object passes through** for the schema
  to reject. It was reported as malformed, and only when a parameter happened to
  trigger the parse, so one client mistake drew two explanations depending on
  whether the route captured anything.
- **`Register` detaches a spec's declared `Idempotent` and `Destructive`** from
  the caller's variables. It cloned `Tags` but stored the bool pointers as
  given, so a later write rewrote what adapters had already advertised.
- **`DispatchValue`'s documentation no longer names a caller that does not
  exist.** It claimed to be the shape the MCP SDK hands a non-generic tool
  handler; the SDK carries those arguments as a `json.RawMessage`. Following the
  old comment lost data, because a `map[string]any` decoded from JSON has
  already rounded every integer past 2^53 into a float64.

### Added

- **`Spec.Destructive`**, three-state like `Idempotent`. `Kind` cannot tell a
  create from a delete, and MCP's `destructiveHint` defaults to true for
  anything not read-only, so every mutation was advertised as possibly
  destructive and approval policies keyed on that hint over-prompted.
- **`Kind.AllowsMethod`**, applying the rule `Kind`'s own documentation had been
  promising. Left to the adapters, the agreed rule accepted both a query on
  DELETE and a mutation on HEAD.
- **`ValidationError.FieldMap`**, which never returns nil, so `{"errors": null}`
  can no longer reach a client.
- **`ErrBodyTooLarge` and `DefaultMaxBodyBytes`.** An unbounded read is a denial
  of service, and the ceiling plus the status a breach answers with is one
  decision rather than a per-adapter knob.
- **`StatusFor`, `StatusBodyTooLarge`, `InternalErrorText`,
  `UnreadableBodyText` and `BodyTooLargeText`** -- the HTTP projection of the
  error taxonomy. The taxonomy was shared and its projection was not, so two
  adapters carried their own copies of a client-visible contract.
- **`ErrConfiguration`**, for a fault in how an operation was mounted rather
  than in the request. An undeclared route capture was reported as a
  `ValidationError`, which put an operator's diagnostic on the client's channel
  where an adapter could not tell it from a genuine client error. `StatusFor`
  answers 500: no change the caller makes would help.
- **`Entry.CheckCaptures`**, moving that same guarantee to configuration time.
  A route table naming a capture the operation cannot receive is broken in
  every request it will serve, so an adapter that knows its patterns refuses it
  at startup. The dispatch-time check remains for a handler placed on a router
  the adapter cannot inspect.

## [0.1.0] - 2026-09-03

### Added

- The kernel: `Spec`, `Registry`, `Register`, `MustRegister`, `Dispatch`,
  `DispatchValue`, `Entry` and `Result`.
- Three validation layers, applied by the kernel on every transport: the
  reflected JSON Schema, an optional `Validate() error` on the input type, and
  `Spec.Permit` for rules that need resolved dependencies.
- `Optional[T]`, which distinguishes an omitted field from a field sent as its
  zero value, and composes with pointers for the explicitly-null case.
- `SchemaFor`, letting a type declare the schema it marshals to. Reflection does
  not consult `json.Marshaler` for arbitrary types, so without this a wrapper
  advertises its own internals.
- `WithAtomic`, which takes a transaction callback so the kernel names no
  database driver. Dependencies resolve inside the transaction.
- `EncodeParams`, schema-directed coercion of flat string parameters, so a query
  string reaches a service as the types its schema declares.
- Framework-agnostic errors: `ErrNotFound`, `ErrConflict`, `ErrPermission` and
  `ValidationError`.

[Unreleased]: https://github.com/Artui/go-services/compare/v0.7.1...HEAD
[0.7.1]: https://github.com/Artui/go-services/releases/tag/v0.7.1
[0.7.0]: https://github.com/Artui/go-services/releases/tag/v0.7.0
[0.6.0]: https://github.com/Artui/go-services/releases/tag/v0.6.0
[0.5.0]: https://github.com/Artui/go-services/releases/tag/v0.5.0
[0.4.0]: https://github.com/Artui/go-services/releases/tag/v0.4.0
[0.3.0]: https://github.com/Artui/go-services/releases/tag/v0.3.0
[0.2.0]: https://github.com/Artui/go-services/releases/tag/v0.2.0
[0.1.0]: https://github.com/Artui/go-services/releases/tag/v0.1.0
