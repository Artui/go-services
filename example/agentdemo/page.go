package main

// html is the whole client: the real <ag-ui-chat> element, pinned, pointed at
// this server's /agent endpoint.
//
// The version is pinned rather than floating. A demo that silently followed the
// component's latest release would start failing for reasons that have nothing
// to do with this repository, and the failure would look like an aguix bug.
const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>go-services over AG-UI</title>
<style>
  :root { color-scheme: light dark; }
  body {
    margin: 0; min-height: 100vh; display: grid; place-items: center;
    font: 15px/1.55 ui-sans-serif, system-ui, sans-serif;
    background: #f6f7f9; color: #16181d;
  }
  main { max-width: 40rem; padding: 2rem; }
  h1 { font-size: 1.35rem; margin: 0 0 .5rem; }
  p { margin: 0 0 1rem; color: #4a4f58; }
  code { background: #e9ebef; padding: .1em .35em; border-radius: .25rem; }
  ul { color: #4a4f58; padding-left: 1.1rem; }
  @media (prefers-color-scheme: dark) {
    body { background: #14161a; color: #e7e9ee; }
    p, ul { color: #a8adb8; }
    code { background: #23262c; }
  }
</style>
</head>
<body>
<main>
  <h1>go-services over AG-UI</h1>
  <p>
    The chat in the corner is the real <code>&lt;ag-ui-chat&gt;</code> component,
    talking to a Go server. Claude answers and calls the library's specs as
    tools, and everything under them is the same kernel the HTTP and MCP adapters
    run, against a real SQLite database.
  </p>
  <ul>
    <li><code>show me the books</code> lists the catalogue.</li>
    <li><code>borrow book 10</code> writes two tables in one transaction.</li>
    <li><code>borrow book 11</code> is refused: no copy is on the shelf, and the
        loan row it had already written is rolled back.</li>
    <li><code>my loans</code> answers with a status, a due date and a fine in
        cents -- the same bytes every other transport serves, which is what the
        module's payload tests are about.</li>
  </ul>
</main>

<ag-ui-chat
  endpoint="/agent"
  title-text="Librarian"
  placement="bottom-right"></ag-ui-chat>

<script type="module">
  // The component does NOT register itself: importing the bundle defines
  // nothing, and defineChat() is what upgrades <ag-ui-chat> from an unknown
  // element into the chat. The /bundle entry inlines @ag-ui/client, which a
  // browser could not resolve from bare imports on its own.
  import { defineChat } from
    "https://cdn.jsdelivr.net/npm/@artooi/agents-web-components@0.11.0/dist/agents-web-components.bundle.js";
  defineChat();
</script>
</body>
</html>
`
