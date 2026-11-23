# Driver patterns

forebrain gives an agent no browser tool and no screenshot tool. It drives an app
through the `shell` tool and observes it through what a shell can print — and
through `read_file`, which returns a saved png/jpeg/gif/webp as pixels. A driver
exists to turn "look at the app" into "print something readable".

Pick the section for the app's shape. Every pattern below is a sketch to adapt, not
code to paste — the point is the shape, and the point of the shape is that a run
reproduces.

## The shape every driver shares

Subcommands, one per thing an agent needs to do:

- `build` — compile or bundle, so a stale artifact cannot silently show old behavior
- `start [mode]` — launch, with the mode choosing the scenario
- one or more drive steps (`submit`, `send`, `key`, `click`, `request`) — the smallest
  action that reaches the change
- one or more read steps (`screen`, `messages`, `db`, `log`, `provider-log`) — print
  observable state
- `home` / `proj` — print the isolated directories the run is using
- `stop` / `reset` — tear down, and wipe the isolated home; both idempotent

Rules: print what it did, exit non-zero on failure, prefer a mode argument over
environment-variable soup, and never write outside the isolated home.

## Terminal UI

Run the app inside `tmux` and capture the pane:

```bash
tmux new-session -d -s "$S" "$BIN"
tmux send-keys -t "$S" "$TEXT" Enter
sleep 1
tmux capture-pane -p -t "$S" >"$OUT/screen.txt"
```

`capture-pane -p` prints the rendered cells as text, so the read step is `grep`. For
cursor position, cell attributes or wrapped lines, capture with `-e` and parse, or dump
the app's own render state if it exposes one. `screen` is not a substitute for `tmux`
here — it has no equivalent pane dump.

To watch something change over time, capture twice with a sleep between and diff: a line
that grew is proof of streaming, and one capture is not.

## Web frontend

Two paths, and the second is often enough:

- **Through the API behind it.** Start the server, hit the endpoint the page calls, read
  the JSON. This is fast, deterministic and needs no browser.
- **Through a headless browser driven from a script.** Navigate, wait for a selector,
  then print the DOM, the console messages or the network log. Assert on printed text;
  when the question is visual, save a screenshot and `read_file` it — a saved
  png/jpeg/gif/webp comes back as pixels, so the model can look at it.

For a page whose behavior lives in the browser (a key binding, a focus ring, a panel that
opens on a keystroke), only the second path proves anything.

## Server, API or CLI

```bash
"$BIN" --port "$PORT" &>"$OUT/server.log" &
echo $! >"$OUT/pid"
curl -fsS "http://127.0.0.1:$PORT$path" >"$OUT/resp.json"
```

Read the response bytes and the log; do not infer success from the exit code. For a CLI,
capture stdout, stderr and the exit status separately — a tool that prints its error to
stdout and exits 0 is a real failure mode.

## Standing up a fake where the app needs a real service

The single highest-value move in a run skill. A run that needs live credentials, a real
network or a paid account cannot be repeated, and an agent will quietly skip it.

- **Same protocol, scripted responses.** A local HTTP endpoint that speaks the API the
  app already speaks — for a model provider, an OpenAI-compatible `/chat/completions`
  that streams canned deltas. The app cannot tell, and the run needs no key, no network
  and no money.
- **Modes on the stub.** One stub, many scenarios: a normal reply, a slow word-by-word
  stream, a tool call, an approval prompt, a rate limit that clears after N seconds, a
  connection dropped mid-stream, an empty 200. The mode is how the driver *chooses* the
  edge case instead of waiting for it.
- **A stub for the second protocol too.** If the app also speaks MCP, or opens a
  websocket, or polls a queue, stub that as well — a half-stubbed app makes a
  half-verifiable run.
- **A proxy for the network itself.** To test retry, timeout and backoff, put a proxy in
  front that delays, drops or corrupts on command. This is the only reliable way to
  provoke a network failure.
- **Environment knobs for thresholds.** Anything the app waits for or limits — a
  compaction threshold, a retry maximum, a delay — should be settable by an environment
  variable so an edge case that normally takes an hour takes ten seconds.

forebrain's own `scripts/acceptance/` does all of this: `fake_provider.py` (a scripted
model provider with a mode per scenario), `fake_mcp_stdio.py` (a stub MCP server),
`flaky_proxy.py` (the network itself), and `driver.sh` to drive the real TUI against
them. Read it for the shape.

## Isolation

A run that mutates the user's real state is not a test. Give the app a scratch home and a
scratch project directory, print both, and wipe them on `reset`:

```bash
HOME_DIR="$WORK/home"; PROJ_DIR="$WORK/proj"
mkdir -p "$HOME_DIR" "$PROJ_DIR"
(cd "$PROJ_DIR" && FOREBRAIN_HOME="$HOME_DIR" "$BIN")
```

For forebrain itself, `FOREBRAIN_HOME` is the switch that matters: point it at a scratch
directory and the run cannot touch the user's configuration, skills, credentials or state
database. The project is the **working directory** — the binary has no project flag — and
`scripts/acceptance/driver.sh` exposes the two directories as `home` and `proj`
so a failing run can be inspected afterwards.

## The trap that looks like success

A **stale build**. If the app is compiled, a driver that launches a cached binary will
faithfully show the old behavior, and the change under test looks like it did nothing at
all. `reset` and `stop` usually clear state without rebuilding; neither is a substitute
for `build`. Say this in the generated skill's `## Gotchas` — it is the most expensive
false negative a run skill can have.
