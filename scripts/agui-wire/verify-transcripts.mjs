// Checks every SSE transcript the aguix and aguiadk tests record against the
// client that reads them.
//
// The Go tests write each run's response body to <module>/testdata/agui/transcripts
// and fail when a run's bytes drift from it, so these files are what the server
// sends. This script reads them as @ag-ui/client 1.0.4 does: each frame through
// @ag-ui/core's EventSchemas (the parse must not change the frame, as in
// generate.mjs), then the whole run through the client's own verifyEvents stage,
// which rejects arguments before their call starts, content outside a message,
// and a run that finishes with something still open.
//
// A transcript with a <name>.state.json beside it is then applied by the client's
// own default applier (defaultApplyEvents), starting from the state the Go run was
// sent, and the state it reaches must equal the state the Go run ended with. That
// file is written by the Go test from the run's session, never by hand. Every
// state-* transcript must have one.
//
// An aguiadk transcript must also have an entry in aguiadk/testdata/agui/messages.json,
// the messages the bridge stores for the run, written by the Go test from the
// frames. The client's own default applier, starting from no messages, must file
// the same ones, activities aside: the bridge does not store those.
//
// A transcript is stored without the body's final blank line, which the
// repository's end-of-file hook would strip; it is put back before splitting.
//
// Run it with `npm ci --ignore-scripts && node verify-transcripts.mjs` in this
// directory. It exits non-zero on the first failure in each file, naming the
// file and the frame.

import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";

import { HttpAgent, defaultApplyEvents, verifyEvents } from "@ag-ui/client";
import { EventSchemas } from "@ag-ui/core/schemas";
import { from, lastValueFrom, tap, toArray } from "rxjs";

const here = dirname(fileURLToPath(import.meta.url));
const dirs = ["aguix", "aguiadk"].map((module) =>
  join(here, "..", "..", module, "testdata", "agui", "transcripts"),
);

function frames(body) {
  const chunks = `${body}\n`.split("\n\n");
  if (chunks.pop() !== "") {
    throw new Error("the body does not end with a blank line");
  }
  return chunks.map((chunk, i) => {
    const where = `frame ${i + 1}`;
    if (!chunk.startsWith("data: ") || chunk.includes("\n")) {
      throw new Error(`${where} is not one data line: ${chunk}`);
    }
    const frame = JSON.parse(chunk.slice("data: ".length));
    const parsed = EventSchemas.safeParse(frame);
    if (!parsed.success) {
      throw new Error(`${where} (${frame.type}) fails the schema: ${parsed.error.message}`);
    }
    if (!isDeepStrictEqual(parsed.data, frame)) {
      throw new Error(`${where} (${frame.type}) is changed by the schema`);
    }
    return parsed.data;
  });
}

async function verify(events) {
  let accepted = 0;
  try {
    await lastValueFrom(from(events).pipe(verifyEvents(), tap(() => accepted++), toArray()));
  } catch (err) {
    throw new Error(`frame ${accepted + 1} (${events[accepted]?.type}) is rejected: ${err.message}`);
  }
}

async function applied(events, sent) {
  const start = input([], structuredClone(sent ?? {}));
  const agent = new HttpAgent({ url: "http://unused.invalid" });
  const mutations = await lastValueFrom(
    defaultApplyEvents(start, from(events), agent, []).pipe(toArray()),
  );
  return mutations.filter((m) => m.state !== undefined).at(-1)?.state ?? start.state;
}

const stored = JSON.parse(
  readFileSync(join(here, "..", "..", "aguiadk", "testdata", "agui", "messages.json"), "utf8"),
);

async function checkMessages(name, events) {
  const key = name.replace(/\.sse$/, "");
  if (!(key in stored)) {
    throw new Error("no entry in aguiadk/testdata/agui/messages.json");
  }
  const mutations = await lastValueFrom(
    defaultApplyEvents(input([]), from(events), new HttpAgent({ url: "http://unused.invalid" }), []).pipe(
      toArray(),
    ),
  );
  const got = (mutations.filter((m) => m.messages !== undefined).at(-1)?.messages ?? []).filter(
    (m) => m.role !== "activity",
  );
  if (!isDeepStrictEqual(JSON.parse(JSON.stringify(got)), stored[key])) {
    throw new Error(
      `the client files ${JSON.stringify(got)}, the bridge stores ${JSON.stringify(stored[key])}`,
    );
  }
}

function input(messages, state = {}) {
  return {
    threadId: "thread",
    runId: "run",
    state,
    messages,
    tools: [],
    context: [],
    forwardedProps: {},
  };
}

async function checkState(dir, name, events) {
  const path = join(dir, name.replace(/\.sse$/, ".state.json"));
  let record;
  try {
    record = JSON.parse(readFileSync(path, "utf8"));
  } catch (err) {
    if (err.code === "ENOENT" && !name.startsWith("state-")) return false;
    throw new Error(`no readable final state beside it: ${err.message}`);
  }
  const got = await applied(events, record.sent);
  if (!isDeepStrictEqual(got, record.final)) {
    throw new Error(
      `the client applies it to ${JSON.stringify(got)}, the Go run ended with ${JSON.stringify(record.final)}`,
    );
  }
  return true;
}

let failed = 0;
for (const dir of dirs) {
  const files = readdirSync(dir).filter((name) => name.endsWith(".sse")).sort();
  if (files.length === 0) {
    console.error(`no transcripts in ${dir}`);
    process.exit(1);
  }
  for (const name of files) {
    try {
      const events = frames(readFileSync(join(dir, name), "utf8"));
      await verify(events);
      const state = await checkState(dir, name, events);
      if (dir.includes("aguiadk")) {
        await checkMessages(name, events);
      }
      console.log(`ok   ${name}${state ? " (state applied)" : ""}`);
    } catch (err) {
      failed++;
      console.error(`FAIL ${name}: ${err.message}`);
    }
  }
}
process.exit(failed === 0 ? 0 : 1);
