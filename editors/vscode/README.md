# Forebrain for VS Code

The native Forebrain chat workbench in VS Code's side bar, plus a Sessions view and a thin native
layer (status bar, approval notifications, editor-to-chat commands, delivered files).

## What it is

The panel runs the same Vue components the gateway's Web UI runs — the conversation thread, the
composer, approval cards, diffs, subagent and peer drawers, plan mode, `user_interaction` question
cards, the `session_todo` checklist, the workbench column — assembled into a workbench shaped for the
side bar. It is not an iframe of the Web UI: the app is bundled into the extension and reaches the
gateway only through the extension host.

Three views make up the alignment contract:

- **Forebrain** (chat) in the Secondary Side Bar on VS Code ≥ 1.106, falling back to the Activity Bar
  below that — one app, shown by the `forebrain.doesNotSupportSecondarySidebar` capability key.
- **Sessions** in its own Activity Bar container: the conversation list, new chat, search and switch.

The one problem the host solves is authentication. The gateway's session cookie is `SameSite=Strict`,
and a VS Code webview is a cross-site frame, so Chromium neither stores nor sends that cookie there.
Instead of changing the gateway, the extension host proxies the app's requests: the webview posts each
HTTP call and WebSocket frame to the host over `postMessage`, and the host adds
`Authorization: Bearer <token>` before forwarding it to the gateway. **The token never enters the
webview.** The host also opens its own observer WebSocket for the status bar, approvals and delivered
files.

## Install

```bash
bash scripts/build-vscode-forebrain-ext.sh
code --install-extension editors/vscode/out/forebrain-vscode-0.2.1.vsix
```

The extension needs the Harness CLI for its "start a gateway for me" path:

```bash
npm i -g @forebrain-harness/forebrain
```

## First connection

The host finds a token in this order, and stops at the first one the gateway accepts:

1. **Secret storage** — a token entered earlier (the extension keeps it, and forgets it when the gateway
   rejects it).
2. **`FOREBRAIN_GATEWAY_TOKEN`** — read from the environment VS Code was started in.
3. **A gateway this extension starts** — if nothing answers at `forebrain.gatewayUrl` and
   `forebrain.autoStartGateway` is on, it runs `forebrain gateway start` and reads the token from the
   banner. That token dies with the process, so it is never stored.
4. **You paste one** — copy the `Token …` line from a `forebrain gateway start` banner (or the token in
   `forebrain.yaml`) and run `Forebrain: Enter Gateway Token`.

Until the host holds a token the views show a connection card (enter the token, reconnect, or open the
Web UI in a browser); once it connects, the app loads.

## Workspace trust

The extension declares `capabilities.untrustedWorkspaces.supported: false`: in an untrusted window the
views stay off until you trust the folder. Trusting a folder is VS Code's own decision; on top of it,
the first send into a directory nobody has judged asks once, through the host:

- a directory inside a trusted workspace folder is recorded automatically (the same record the
  terminal writes);
- a directory outside the workspace asks in a native modal — confirm records it, cancel sends nothing;
- an untrusted workspace refuses without asking.

A failure to record the decision still lets the send proceed: the prompt is an added gate, not the
mechanism.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `forebrain.gatewayUrl` | `http://127.0.0.1:6060` | The gateway's HTTP(S) origin, without a path. |
| `forebrain.binaryPath` | empty | The `forebrain` binary to start. Empty: the first one on `PATH`. |
| `forebrain.autoStartGateway` | `true` | Start a gateway when the configured origin does not answer. |
| `forebrain.selectionTarget` | `current` | Where `Ask About Selection`/`Ask About File` sends: the session the panel is showing, or always a new one (`always-new`). |
| `forebrain.includeDiagnostics` | `true` | Append the file's current diagnostics to the prompt of `Ask About Selection`/`Ask About File`. |
| `forebrain.approvalNotifications` | `true` | Show a notification for an approval that arrives while the chat panel is not visible. |

## Native integration

### The checklist

The agent's `session_todo` list lives in the panel: a chip above the composer toggles the checklist,
and an update lands unread until you acknowledge it. It is the agent's state — change it by asking,
not by clicking. (The independent read-only tree view of earlier versions is removed; the list is in
the conversation now.)

### Status bar

One line, left-aligned, always telling you what the host's own link knows:

| Text | Meaning |
|---|---|
| `$(radio-tower) Forebrain` | Connected; no turn running. |
| `$(sync~spin) Forebrain` | A turn is running. |
| `$(check-all) 3/7` | Appended: how far the agent's checklist is, `done/total`. |
| `$(bell) 2` | Appended: approvals waiting, and how many. |
| `$(debug-disconnect) Forebrain` | The link to the gateway is down. |

Hovering shows the session id and the same counts; clicking focuses the chat view.

### Sending a selection or a file

- **`Forebrain: Ask About Selection`** — `Ctrl+Cmd+K` on macOS, `Ctrl+Alt+K` on Windows/Linux, or the
  editor's context menu (it needs a selection). The prompt starts with `In <path>:<start>-<end>:` and a
  fenced block of the selected code.
- **`Forebrain: Ask About File`** — the editor's context menu with nothing selected, or a file in the
  Explorer's context menu. The prompt starts with `File <path>:` and carries up to 64 KiB of the file.

Both need the host connected, and both ask the question in an input box first — **Esc cancels and
creates nothing**. `forebrain.includeDiagnostics` (on by default) adds the file's current diagnostics.

Where the turn lands: **the conversation the panel is showing**; failing that, the conversation this
extension last sent into; failing that, the newest conversation of the workspace folder's project;
failing that, a new conversation titled `VS Code: <file>`. `forebrain.selectionTarget: "always-new"`
makes every send start a new conversation instead.

### Approval notifications

An approval that arrives while the panel is **not visible** raises a VS Code notification with
`Approve`, `Deny` and `Open panel`. Approve/Deny go through the REST endpoint, so no panel reload is
needed; the fine choices stay in the panel's card. A question rather than a tool approval only offers
`Open panel`. A decision made elsewhere clears the waiting count within a few seconds: the host
reconciles the pending list with the gateway every 4 seconds.

### Delivered files

When a turn ends having delivered files, a notification says `Forebrain delivered N file(s)` with
`Open…` and `Reveal`. In the panel, a delivered file's eye opens it by kind — code in its diff and the
editor, anything else with the OS. **Only paths inside a workspace folder open**; anything else is
named and left alone. A `file:line` link in a reply opens that file in the editor at the named lines,
with a native selection.

### How the native layer learns anything

The host opens its **own** WebSocket to the gateway's `/ws/chat` (bearer token, no browser origin) and
binds it to the conversation the chat view reports through the RPC `bind` call. That connection is an
observer: it receives `run_event` frames and folds them into the status bar, the approval list and the
delivery list. It is independent of the webview's own proxied connection — the gateway keeps each
connection's subscription separate, so both can be bound to the same conversation at once.

A dropped link reconnects with exponential backoff and resumes from the last event sequence it saw. A
webview that is rebuilt (the view was hidden and restored) announces itself ready and is handed the
conversation it had, so it re-binds and replays its history.

## How it works

```
VS Code side bar > Forebrain (chat) / Sessions
  bundled Vue app, fetch + WebSocket replaced by a postMessage transport
        |  rpc: http / ws.open / ws.send / bind / openFileRange / … (whitelisted)
        v
  extension host (Node)  WebviewRpc: adds Authorization: Bearer <token>
        |  drops client authorization/cookie; response headers allowlisted
        v
  gateway http://127.0.0.1:6060  (the harness itself is unchanged)
```

The RPC surface is a whitelist: a method the host does not know is refused. The bearer header is added
only by the host, on the `/api/` boundary; a client-supplied `authorization` or `cookie` is dropped, and
only a small set of response headers cross back. Each proxied call has a 60 s ceiling, so a dead gateway
never leaves a panel hanging. `run_event` frames are coalesced into at most one `postMessage` per turn
of the event loop, in order.

The token lives in the extension host's memory and in secret storage. It is never written into the
webview's HTML, the log or the Output channel. The only place it leaves the extension is the sign-in
link `Forebrain: Open Web UI in Browser` opens in your own browser (the same link the gateway's banner
prints), and a settings page the panel has no native surface for (logs, models), which opens the Web UI
at that page. A gateway the extension started is the only one it ever stops.

## Known limitations

- **Remote windows are not supported**: use a local window.
- **Background batch projects are out of scope**: the panel is a coding conversation surface; a batch
  execution's read-only acts live in the Web UI's project space.
- **A vendor OAuth flow that calls `window.open`** cannot open your system browser from the webview.
  Run `Forebrain: Open Web UI in Browser` and finish the sign-in there; the session is shared.
- `forebrain gateway stop` does nothing server-side today. The extension only ever stops the gateway it
  started itself; stopping a gateway you started in a terminal is your job.

## Troubleshooting

- `View › Output › Forebrain` — connection decisions and why a start failed. It never contains the token.
- `Help › Toggle Developer Tools › Console` — for a blank panel: the webview's own errors, including CSP.
- `curl http://127.0.0.1:6060/health` — whether the gateway listens where the extension expects.
- `forebrain gateway status` — what the CLI knows about a running gateway.

## Development

```bash
cd editors/vscode
npm install
npm run compile     # dist/extension.js and dist-test/*.test.js (esbuild)
npm run typecheck   # tsc --noEmit
npm test            # node --test on the built tests

# The webview app (from frontend/, the pnpm workspace root):
corepack pnpm --filter @forebrain/vscode-webview typecheck
corepack pnpm --filter @forebrain/vscode-webview test
corepack pnpm --filter @forebrain/vscode-webview build
```

Packaging (from the repository root) writes a VSIX under `editors/vscode/out/`, which is git-ignored:

```bash
bash scripts/build-vscode-forebrain-ext.sh
```

Source layout: `src/connection.ts` is the token ladder and gateway-child lifecycle, `src/gatewayProcess.ts`
the optional gateway child, `src/panelHtml.ts` the connection document and the native app loader,
`src/panel.ts` the `WebviewViewProvider`, and `src/webviewRpc.ts` the host's whitelisted proxy. The
webview app is `webview/` (its own Vite build, reusing `frontend/src` components and composables over an
injected transport). The native layer is `src/sessionSocket.ts` (the host's own chat socket) and
`src/sessionState.ts` (the pure reducer). `src/promptBuilder.ts`, `src/sessionTarget.ts`,
`src/approvalText.ts` and `src/deliverableGate.ts` are the other pure decisions; `src/statusBar.ts`,
`src/approvals.ts`, `src/deliverables.ts` and `src/commands.ts` draw them and read the editor.
`src/relay.ts` is the frozen loopback relay of the earlier iframe architecture, kept but no longer
called. Everything unit-tested avoids importing `vscode`, so the tests run under plain `node --test`.