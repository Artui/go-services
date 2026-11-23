// Writes the AG-UI wire fixtures the Go tests read, from the producer itself.
//
// Each inbound fixture is the body @ag-ui/client's HttpAgent posts for a run:
// the agent is driven through runAgent with a fetch that records the request and
// answers with an empty run, so the bytes are the ones a browser sends, after
// the client's own outgoing enforcement, rather than JSON written by hand.
//
// One exception, because the agent cannot produce it: HttpAgent drops every
// activity message from the history it sends (prepareRunAgentInput filters
// role "activity" at 1.0.4 and 1.0.5), so the web component never sends one.
// "activity-history" records that drop; "activity" is the same history passed
// through enforceOutgoingInput, the client's last stage before the wire, which
// is what a producer that does keep activity messages would send.
//
// The outbound fixtures are the frames a server sends, one file each, in
// aguix/testdata/agui/outbound. Each is written by @ag-ui/core's own EventSchemas
// (the client's parser) and must come back unchanged: a key the schema would
// strip, or a required one missing, stops the script rather than becoming a
// fixture, so the Go tests compare against what a 1.0 client keeps.
//
// The bytes are written exactly as sent, unindented, so a test reads what a
// server receives. The one addition is a final newline, which the repository's
// end-of-file hook requires and a JSON decoder skips.
//
// Run it with `npm ci && node generate.mjs` in this directory; CI does the same
// and fails when the committed fixtures differ from what it wrote.

import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import assert from "node:assert/strict";

import { HttpAgent, enforceOutgoingInput } from "@ag-ui/client";
import { EventSchemas } from "@ag-ui/core/schemas";

const here = dirname(fileURLToPath(import.meta.url));
const inbound = join(here, "..", "..", "aguix", "testdata", "agui", "inbound");
const outbound = join(here, "..", "..", "aguix", "testdata", "agui", "outbound");

const tools = [
  {
    name: "fill_field",
    description: "Fill a form field on the page.",
    parameters: {
      type: "object",
      properties: { field: { type: "string" }, value: { type: "string" } },
      required: ["field", "value"],
    },
  },
];

const fixtures = {
  // A user turn sent as content parts: text beside an inline image.
  parts: {
    messages: [
      {
        id: "u1",
        role: "user",
        content: [
          { type: "text", text: "What is in " },
          {
            type: "image",
            source: { type: "data", value: "aGVsbG8=", mimeType: "image/png" },
          },
          { type: "text", text: "this picture?" },
        ],
      },
    ],
  },

  // A browser tool round across runs: the assistant's call, then the page's
  // answer to it, one success and one failure.
  "tool-round": {
    messages: [
      { id: "u1", role: "user", content: "Fill in my name and email." },
      {
        id: "a1",
        role: "assistant",
        content: "Filling both fields.",
        toolCalls: [
          {
            id: "call-1",
            type: "function",
            function: { name: "fill_field", arguments: '{"field":"name","value":"Ada"}' },
          },
          {
            id: "call-2",
            type: "function",
            function: { name: "fill_field", arguments: '{"field":"email","value":"x"}' },
          },
        ],
      },
      { id: "t1", role: "tool", toolCallId: "call-1", content: "filled" },
      {
        id: "t2",
        role: "tool",
        toolCallId: "call-2",
        content: "",
        error: "no field named email",
      },
    ],
    params: {
      tools,
      context: [{ description: "The page the user is on", value: "/signup" }],
      forwardedProps: { locale: "en" },
    },
  },

  // An activity a server sent earlier, kept in the thread with object content.
  "activity-history": {
    messages: [
      { id: "u1", role: "user", content: "Summarise the thread." },
      {
        id: "act-1",
        role: "activity",
        activityType: "compaction",
        content: { removed: 12, before: 30, after: 18 },
      },
      { id: "u2", role: "user", content: "Go on." },
    ],
  },

  // Reasoning and its signature, carried back so a model can see its own
  // prior thinking.
  reasoning: {
    messages: [
      { id: "u1", role: "user", content: "Why is the sky blue?" },
      {
        id: "r1",
        role: "reasoning",
        content: "Rayleigh scattering favours short wavelengths.",
        encryptedValue: "sig-abc",
      },
      {
        id: "a1",
        role: "assistant",
        content: "Because of Rayleigh scattering.",
        metadata: { model: "test" },
        subagentRunId: "subagent-call-9",
      },
      { id: "u2", role: "user", content: "And at sunset?" },
    ],
    state: { draft: "sunset", turns: 2 },
  },

  // A run continuing from two interrupts: one approved with edited arguments,
  // one cancelled.
  resume: {
    messages: [{ id: "u1", role: "user", content: "Delete the draft." }],
    params: {
      resume: [
        {
          interruptId: "int-call-1",
          status: "resolved",
          payload: { approved: true, editedArgs: { id: 4 } },
        },
        { interruptId: "int-call-2", status: "cancelled" },
      ],
    },
  },
};

async function capture(name, { messages, params = {}, state }) {
  let body;
  const fetch = async (_url, init) => {
    body = init.body;
    const frames = [
      { type: "RUN_STARTED", threadId: "thread-1", runId: `run-${name}` },
      { type: "RUN_FINISHED", threadId: "thread-1", runId: `run-${name}` },
    ];
    const sse = frames.map((f) => `data: ${JSON.stringify(f)}\n\n`).join("");
    return new Response(sse, { headers: { "Content-Type": "text/event-stream" } });
  };
  const agent = new HttpAgent({
    url: "http://example.invalid/agui",
    threadId: "thread-1",
    initialMessages: messages,
    initialState: state,
    fetch,
  });
  await agent.runAgent({ runId: `run-${name}`, ...params });
  if (body === undefined) {
    throw new Error(`${name}: the client sent no request`);
  }
  return body;
}

function enforced(name, { messages }) {
  const input = enforceOutgoingInput({
    threadId: "thread-1",
    runId: `run-${name}`,
    messages,
    tools: [],
    context: [],
    state: {},
    forwardedProps: {},
  });
  // JSON.stringify, as HttpAgent's request body; unlike HttpAgent, this adds
  // no protocolVersion.
  return JSON.stringify(input);
}

const written = {};
for (const [name, spec] of Object.entries(fixtures)) {
  written[name] = await capture(name, spec);
}
written.activity = enforced("activity", fixtures["activity-history"]);

// The directory is emptied first, so a fixture this script no longer writes is
// gone rather than left behind looking like client output.
rmSync(inbound, { recursive: true, force: true });
mkdirSync(inbound, { recursive: true });
for (const [name, body] of Object.entries(written)) {
  writeFileSync(join(inbound, `${name}.json`), `${body}\n`);
}

const call = { id: "c1", type: "function", function: { name: "borrow_book", arguments: '{"book_id":4}' } };

const frames = {
  "state-snapshot": { type: "STATE_SNAPSHOT", snapshot: { count: 1, items: ["a", "b"] } },
  "state-delta": {
    type: "STATE_DELTA",
    delta: [
      { op: "replace", path: "/count", value: 2 },
      { op: "add", path: "/items/-", value: "c" },
      { op: "remove", path: "/items/0" },
      { op: "move", from: "/items/1", path: "/last" },
    ],
  },
  "messages-snapshot": {
    type: "MESSAGES_SNAPSHOT",
    messages: [
      { id: "u1", role: "user", content: "Borrow book 4." },
      { id: "a1", role: "assistant", toolCalls: [call] },
      { id: "t1", role: "tool", content: "{\"loan_id\":1}", toolCallId: "c1" },
      { id: "a2", role: "assistant", content: "Done." },
    ],
  },
  "reasoning-start": { type: "REASONING_START", messageId: "r1" },
  "reasoning-message-start": { type: "REASONING_MESSAGE_START", messageId: "r1", role: "reasoning" },
  "reasoning-message-content": { type: "REASONING_MESSAGE_CONTENT", messageId: "r1", delta: "Checking stock." },
  "reasoning-message-end": { type: "REASONING_MESSAGE_END", messageId: "r1" },
  "reasoning-end": { type: "REASONING_END", messageId: "r1" },
  "reasoning-encrypted-value": {
    type: "REASONING_ENCRYPTED_VALUE", subtype: "message", entityId: "r1", encryptedValue: "sig-abc==",
  },
  "activity-snapshot": {
    type: "ACTIVITY_SNAPSHOT", messageId: "act1", activityType: "chart",
    content: { kind: "bar", series: [{ name: "loans", points: [1, 2, 3] }] }, replace: true,
  },
  "activity-snapshot-default": {
    type: "ACTIVITY_SNAPSHOT", messageId: "act1", activityType: "chart", content: { kind: "bar" },
  },
  "activity-delta": {
    type: "ACTIVITY_DELTA", messageId: "act1", activityType: "chart",
    patch: [{ op: "replace", path: "/kind", value: "line" }],
  },
  "custom-object": { type: "CUSTOM", name: "capability.load", value: { id: "books", n: 2 } },
  "custom-scalar": { type: "CUSTOM", name: "ping", value: "pong" },
  "custom-null": { type: "CUSTOM", name: "cleared", value: null },
  "run-finished-success": {
    type: "RUN_FINISHED", threadId: "t1", runId: "r1",
    outcome: { type: "success", pendingToolCallIds: ["c1", "c2"] },
  },
  "run-finished-interrupt": {
    type: "RUN_FINISHED", threadId: "t1", runId: "r1",
    outcome: {
      type: "interrupt",
      interrupts: [{
        id: "int-1", reason: "tool_approval", message: "Delete the draft?", toolCallId: "c1",
        responseSchema: { type: "object", properties: { approved: { type: "boolean" } } },
        expiresAt: "2026-12-01T00:00:00Z", metadata: { risk: "high" },
      }],
    },
  },
  "run-finished-interrupt-minimal": {
    type: "RUN_FINISHED", threadId: "t1", runId: "r1",
    outcome: { type: "interrupt", interrupts: [{ id: "int-1", reason: "input_required" }] },
  },
};

rmSync(outbound, { recursive: true, force: true });
mkdirSync(outbound, { recursive: true });
for (const [name, input] of Object.entries(frames)) {
  const parsed = EventSchemas.parse(input);
  assert.deepStrictEqual(parsed, input, `${name}: the schema changed the frame`);
  writeFileSync(join(outbound, `${name}.json`), `${JSON.stringify(parsed)}\n`);
}
