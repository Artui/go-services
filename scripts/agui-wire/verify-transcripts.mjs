// Checks every SSE transcript the aguiadk tests record against the client that
// reads them.
//
// The Go tests write each run's response body to aguiadk/testdata/agui/transcripts
// and fail when a run's bytes drift from it, so these files are what the server
// sends. This script reads them as @ag-ui/client 1.0.4 does: each frame through
// @ag-ui/core's EventSchemas (the parse must not change the frame, as in
// generate.mjs), then the whole run through the client's own verifyEvents stage,
// which rejects arguments before their call starts, content outside a message,
// and a run that finishes with something still open.
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

import { verifyEvents } from "@ag-ui/client";
import { EventSchemas } from "@ag-ui/core/schemas";
import { from, lastValueFrom, tap, toArray } from "rxjs";

const here = dirname(fileURLToPath(import.meta.url));
const dir = join(here, "..", "..", "aguiadk", "testdata", "agui", "transcripts");

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

const files = readdirSync(dir).filter((name) => name.endsWith(".sse")).sort();
if (files.length === 0) {
  console.error(`no transcripts in ${dir}`);
  process.exit(1);
}
let failed = 0;
for (const name of files) {
  try {
    await verify(frames(readFileSync(join(dir, name), "utf8")));
    console.log(`ok   ${name}`);
  } catch (err) {
    failed++;
    console.error(`FAIL ${name}: ${err.message}`);
  }
}
process.exit(failed === 0 ? 0 : 1);
