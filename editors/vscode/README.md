# Forebrain for VS Code

The full Forebrain Harness Web UI as a VS Code side-bar view.

## What it is

This extension does not reimplement a chat panel: it embeds the Web UI the gateway already serves, so
everything the Web UI has is here — session list, streaming replies, approval cards, diffs, subagent
views, plan mode, `user_interaction` question cards and the `session_todo` checklist. The view lives in
the Secondary Side Bar (`Forebrain` › `Chat`) and fills it with that page.

On top of that page the extension adds a thin native layer — a checklist view, a status bar line, an
approval notification and two commands that send editor context into the conversation. It mirrors and
opens doors; the conversation itself stays in the panel. See [Native integration](#native-integration).

The one problem it solves is authentication. The gateway's session cookie is `SameSite=Strict`, and a
page inside a VS Code webview is a cross-site frame, so Chromium neither stores nor sends that cookie
there — the Web UI would land on the sign-in page and loop. Instead of changing the gateway, the
extension host runs a small loopback relay that adds `Authorization: Bearer <token>` to every request
the page makes. The token never enters the page.

## Install

```bash
bash scripts/build-vscode-forebrain-ext.sh
code --install-extension editors/vscode/out/forebrain-vscode-0.2.0.vsix
```

The extension needs the Harness CLI for its "start a gateway for me" path:

```bash
npm i -g @forebrain-harness/forebrain
```

## First connection

The panel finds a token in this order, and stops at the first one the gateway accepts:

1. **Secret storage** — a token entered earlier (the extension keeps it, and forgets it when the gateway
   rejects it).
2. **`FOREBRAIN_GATEWAY_TOKEN`** — read from the environment VS Code was started in.
3. **A gateway this extension starts** — if nothing answers at `forebrain.gatewayUrl` and
   `forebrain.autoStartGateway` is on, it runs `forebrain gateway start` and reads the token from the
   banner. That token dies with the process, so it is never stored.
4. **You paste one** — copy the `Token …` line from a `forebrain gateway start` banner (or the token in
   `forebrain.yaml`) and run `Forebrain: Enter Gateway Token`.

If the panel says it needs a token and you already started a gateway in a terminal, option 4 is the
quickest: paste the banner's token, and the Web UI appears without reloading the window.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `forebrain.gatewayUrl` | `http://127.0.0.1:6060` | The gateway's HTTP(S) origin, without a path. |
| `forebrain.binaryPath` | empty | The `forebrain` binary to start. Empty: the first one on `PATH`. |
| `forebrain.autoStartGateway` | `true` | Start a gateway when the configured origin does not answer. |
| `forebrain.relayLogRequests` | `false` | Log each relayed request's method and path (never headers) to the Output channel. |
| `forebrain.selectionTarget` | `current` | Where `Ask About Selection`/`Ask About File` sends: the session the panel is showing, or always a new one (`always-new`). |
| `forebrain.includeDiagnostics` | `true` | Append the file's current diagnostics to the prompt of `Ask About Selection`/`Ask About File`. |
| `forebrain.approvalNotifications` | `true` | Show a notification for an approval that arrives while the chat panel is not visible. |

## Native integration

### Checklist view

The agent's `session_todo` list, mirrored: `Forebrain` › `Checklist` shows one section per agent — the
primary agent's list first, then one `Subagent: <agent>` section for each subagent's own list — and the
items update as the agent works through them (`in progress` spins, `completed` gets a check, `cancelled`
an ✗). It is **read-only**: the state belongs to the agent, and a tree the user could tick would fight
the next `session_todo` call for the truth. Nothing here is a second checklist — it is the same list the
panel's plan card shows.

### Status bar

One line, left-aligned, always telling you what the host's own link knows:

| Text | Meaning |
|---|---|
| `$(radio-tower) Forebrain` | Connected; no turn running. |
| `$(sync~spin) Forebrain` | A turn is running. |
| `$(check-all) 3/7` | Appended: how far the agent's checklist is, `done/total`. |
| `$(bell) 2` | Appended: approvals waiting, and how many. |
| `$(debug-disconnect) Forebrain` | The link to the gateway is down — the panel may still look fine, but nothing is streaming into these views. |

Hovering shows the session id and the same counts; clicking focuses the panel.

### Sending a selection or a file

- **`Forebrain: Ask About Selection`** — `Ctrl+Cmd+K` on macOS, `Ctrl+Alt+K` on Windows/Linux, or the
  editor's context menu (it needs a selection). The prompt starts with `In <path>:<start>-<end>:` and a
  fenced block of the selected code.
- **`Forebrain: Ask About File`** — the editor's context menu with nothing selected, or a file in the
  Explorer's context menu. The prompt starts with `File <path>:` and carries up to 64 KiB of the file;
  if it is longer, the rest is left to the agent's own file tools.

Both need the panel connected (they send through the same gateway token), and both ask the question in
an input box first — **Esc cancels and creates nothing**. `forebrain.includeDiagnostics` (on by default)
adds the file's current diagnostics to the prompt.

Where the turn lands is the owner's rule from 2026-11-24: **the conversation the panel is showing**.
When the panel has not loaded one yet, the conversation this extension last sent into is used; failing
that, the newest conversation of the workspace folder's project; failing that, a new conversation is
created and titled `VS Code: <file>`. `forebrain.selectionTarget: "always-new"` makes every send start a
new conversation instead.

### Approval notifications

An approval that arrives while the panel is **not visible** raises a VS Code notification —
`Forebrain: allow shell?` with the command underneath — and its buttons `Approve`, `Deny` and
`Open panel` answer it without switching back to the panel. Approve/Deny go through the REST endpoint,
so no panel reload is needed; the fine choices (a rule, a prefix, an "always allow") stay in the panel's
card. A question rather than a tool approval (`user_interaction` and the like) only offers `Open panel`,
because it needs the card. With the panel on screen there is no notification: the card is already there.

A decision made elsewhere — in the panel, in a terminal, in another window — clears the waiting count
within a few seconds: the extension reconciles the pending list with the gateway every 4 seconds
(`GET /api/actions?status=pending`). Nothing pushes that news, so polling is what keeps the bell honest.

### Delivered files

When a turn ends having delivered files, a notification says `Forebrain delivered N file(s)` with
`Open…` (a picker, then the editor) and `Reveal` (the Explorer). `Forebrain: Open Delivered File` lists
the current session's delivered files directly. **Only paths inside a workspace folder open**; anything
else is named in a message and left alone — the agent names those paths, so they are not trusted.

### How the native layer learns anything

The extension host opens its **own** WebSocket to the gateway's `/ws/chat` (bearer token, no browser
origin) and binds it to the conversation the panel is showing — the relay tells it which one, because
the Web UI reads `GET /api/chat/sessions/<id>/messages` through the relay. That connection is an
observer: it receives `run_event` frames (turn, tool, subagent, `plan_updated` from `session_todo`,
`approval_requested`) and folds them into the checklist, the status bar, the approval list and the
delivery list. It is not a replacement for the panel's own connection — the gateway keeps each
connection's subscription independent, so both can be bound to the same conversation at once.

A dropped link reconnects with exponential backoff (0.5 s, 1 s, 2 s, 4 s, then every 8 s) and resumes
from the last event sequence it saw; a different conversation starts from zero. Killing the gateway
shows up in the status bar within a second or two, long before anything else notices.

## How it works

```
VS Code Secondary Side Bar > Forebrain > Chat
  iframe src = http://fb-<nonce>.localhost:<port>/?boot=<boot>
        |  the page's own fetch and WebSocket (same origin, no cookie, no token)
        v
  extension host (Node)  relay: 127.0.0.1:<port>, admits one Host only
        |  adds Authorization: Bearer <token>; drops Cookie, Origin, Referer
        v
  gateway http://127.0.0.1:6060  (the harness itself is unchanged)
```

The relay's origin is its first capability: the hostname carries a 128-bit nonce
(`fb-<nonce>.localhost`), and a request is admitted only when its `Host` header names exactly that
origin — so another page, or a process that never saw the iframe URL, cannot use the relay to act as
the token holder. The second is the `boot` secret in the iframe URL: the origin is written to the
Output channel for you to read, so a navigation additionally needs `?boot=<boot>`, which means a page
that learned the origin still cannot embed the Web UI and click-jack it. On top of both, Fetch
Metadata is enforced: a navigation must be an iframe load carrying the boot secret, everything else
must be `Sec-Fetch-Site: same-origin`.

The token lives in the extension host's memory and in secret storage. It is never written into the
panel's HTML, the iframe URL, the log or the Output channel; the two banner lines that print it are
buffered and dropped rather than forwarded. The browser's `Cookie`/`Origin`/`Referer` and the
gateway's `Set-Cookie` never cross the relay; a WebSocket handshake must come from the relay's own
origin and is forwarded with the token and without `Origin`. The relay listens on `127.0.0.1` for as
long as the panel is open, and closes on deactivate. A gateway the extension started is the only one
it ever stops — `forebrain gateway stop` has no server-side handler, so it is not used.

## Known limitations

- **Remote windows are not supported**: in Remote-SSH, WSL and container windows the extension runs on
  the remote side (`extensionKind: ["ui"]` keeps it local, but the relay's loopback is then not the
  loopback your window can reach). Use a local window.
- **The sandbox has no `allow-popups`**, so a vendor OAuth flow that calls `window.open` (the
  subscription sign-in in the Web UI) cannot open your system browser from the panel. Run
  `Forebrain: Open Web UI in Browser` and finish the sign-in there; the session is shared.
- **Local preferences reset when the port changes**: the Web UI stores its theme and last session in
  `localStorage`, which is keyed by origin, and the origin includes the relay's port. The port is
  sticky while it is free; if it is taken, the extension falls back to another one and those
  preferences start from their defaults.
- **The checklist view is a mirror, not a control**: there is no way to tick an item from the tree. The
  agent owns that state (`session_todo`); change it by asking, not by clicking.
- `forebrain gateway stop` does nothing server-side today (`/admin/shutdown` has no handler). The
  extension works around it by only ever stopping the gateway it started itself; stopping a gateway
  you started in a terminal is your job.

## Troubleshooting

- `View › Output › Forebrain` — connection decisions, the relay origin, and why a start failed. It
  never contains the token.
- `Help › Toggle Developer Tools › Console` — for a blank panel: the webview's own errors, including
  the CSP or relay diagnostics.
- `curl http://127.0.0.1:6060/health` — whether the gateway listens where the extension expects.
- `forebrain gateway status` — what the CLI knows about a running gateway.

## Development

```bash
cd editors/vscode
npm install
npm run compile     # dist/extension.js and dist-test/*.test.js (esbuild)
npm run typecheck   # tsc --noEmit
npm test            # node --test on the built tests
```

Packaging (from the repository root) writes a VSIX under `editors/vscode/out/`, which is git-ignored:

```bash
bash scripts/build-vscode-forebrain-ext.sh
```

Source layout: `src/relay.ts` is the loopback relay, `src/connection.ts` the token ladder and relay
lifecycle, `src/gatewayProcess.ts` the optional gateway child, `src/panelHtml.ts` the panel document
and `src/panel.ts` its `WebviewViewProvider`. The native layer is `src/sessionSocket.ts` (the host's
own chat socket, speaking the gateway protocol) and, above it, `src/sessionState.ts` — the pure reducer
that folds every frame into one state object. `src/promptBuilder.ts`, `src/sessionTarget.ts`,
`src/approvalText.ts` and `src/deliverableGate.ts` are the other pure decisions; `src/checklistTree.ts`,
`src/statusBar.ts`, `src/approvals.ts`, `src/deliverables.ts` and `src/commands.ts` are the thin parts
that draw them and read the editor. Everything unit-tested avoids importing `vscode`, so the tests run
under plain `node --test`; the thin parts are covered by the acceptance checklist instead.