<div align="center">

# Forebrain Harness

**One open-source agent runtime for your terminal, your browser and your team's chat apps.**

A terminal coding agent and a self-hosted agent gateway in a single Go binary —
any model, a real OS sandbox, durable multilingual memory, and long sessions
that stay cheap.

[![CI](https://github.com/forebrain-harness/forebrain-harness/actions/workflows/ci.yml/badge.svg)](https://github.com/forebrain-harness/forebrain-harness/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)
[![Docs](https://img.shields.io/badge/docs-forebrain--harness.github.io-2ea44f.svg)](https://forebrain-harness.github.io)

[Why Forebrain Harness](#why-forebrain-harness) · [Quick start](#quick-start) · [Features](#features) · [Docs](https://forebrain-harness.github.io) · [Contributing](#contributing)

</div>

---

## Why Forebrain Harness

Coding agents usually live in one terminal window. Bot gateways usually route
messages without an agent behind them. Forebrain Harness is both, built on one engine, so
the agent you trust in your terminal is the same agent your browser and your
team's chat apps talk to.

- **One runtime, every surface.** The interactive TUI and the web UI served by
  `forebrain gateway` run the same engine. Sessions, slash commands, approvals,
  permissions and memory behave the same way in both, because they are the same
  code — not two products kept roughly in sync.
- **Bring any model.** 362 models from 33 vendors are ready to use, or sign in
  with your ChatGPT plan instead of an API key. Switch model and reasoning
  effort mid-session with `/model`.
- **Lower bills on long sessions.** An agent sends its whole conversation to the
  model on every turn. Forebrain Harness is built so that as much of it as possible is
  served from the provider's prompt cache, which keeps long sessions cheap.
- **Your agent in your chat apps.** The gateway connects the same agent — tools,
  sandbox, memory, approvals — to Telegram, Discord, Slack, WhatsApp,
  Feishu/Lark, DingTalk, WeCom, WeChat, QQ, Matrix, Mattermost, iMessage (via
  BlueBubbles), Home Assistant, and generic HTTP/WebSocket bridges.
- **A real sandbox, and you stay in charge.** Commands run inside an OS-level
  sandbox on macOS, Linux and Windows. Anything that needs to leave it asks you
  first; `/permissions` switches between presets, and Plan mode (`/plan`) lets
  the agent investigate and propose before it may change anything.
- **Memory that lasts, in any language.** Forebrain Harness keeps per-project and global
  memory, distills it from past sessions, and finds the right memory whether it
  is written in English, Chinese, Japanese or Korean.
- **Built for long-running work.** `/goal` keeps working toward an objective
  until it is done; subagents take delegated work in sessions of their own;
  `/fork`, `/compact` and `/resume` manage long conversations; the web UI
  schedules recurring tasks.
- **Extensible.** Skills (bundled ones cover Word, Excel, PowerPoint and PDF
  documents, code review, frontend design, image editing and writing new
  skills), MCP servers, and hooks.
- **Come as you are.** `/migrate` imports your Claude Code or Codex history —
  sessions you can keep chatting in, memories, skills, plans, MCP servers and
  input history.
- **One binary, Apache-2.0 licensed.** A single Go binary with the web UI embedded,
  installed through npm on macOS, Linux and Windows.

### At a glance

|                                                              | Terminal coding agents | Bot gateways | **Forebrain Harness** |
| ------------------------------------------------------------ | :--------------------: | :----------: | :--------: |
| Interactive terminal UI with approvals, plan mode and diffs  |           ✓            |      —       |     ✓      |
| Web UI and HTTP/WebSocket API                                |         varies         |      ✓       |     ✓      |
| Reachable from Slack, Telegram, WeChat, Feishu, …            |           —            |      ✓       |     ✓      |
| A full agent (tools, sandbox, memory) behind every channel   |     terminal only      |    varies    |     ✓      |
| Any model vendor, switchable mid-session                     |         varies         |    varies    |     ✓      |

## Quick start

Install (Node.js 18+; macOS arm64/x64, Linux x64/arm64, Windows x64):

```bash
npm install -g @forebrain-harness/forebrain
```

Start it in your project:

```bash
cd your-project
forebrain
```

On first run Forebrain Harness asks whether you trust the directory, then walks you through
choosing a provider: sign in with ChatGPT, or use an API key for OpenAI,
Anthropic, Google Gemini, DeepSeek, Qwen, xAI, Mistral, Moonshot, Z.AI or
MiniMax. Type `/` to see every command, `@` to mention a file.

Open the web UI:

```bash
forebrain gateway start      # serves the web UI and API on http://127.0.0.1:6060
forebrain gateway status
forebrain gateway stop
```

Pick up where you left off with `/resume`, or `forebrain resume <session id>`.
Coming from Claude Code or Codex? Run `/migrate`.

## Features

### In the terminal

| Command        | What it does                                                           |
| -------------- | ---------------------------------------------------------------------- |
| `/model`       | Choose the model and reasoning effort                                  |
| `/connect`     | Configure or switch LLM provider                                       |
| `/permissions` | Choose what Forebrain Harness is allowed to do                                    |
| `/plan`        | Plan mode: investigate and propose before changing anything            |
| `/goal`        | Keep working toward an objective until it is done                      |
| `/subagents`   | Open one of this chat's subagent sessions                              |
| `/skills`      | Run, add, create, improve and toggle skills                            |
| `/mcp`         | MCP servers: status, tools, authentication                             |
| `/memories`    | Configure memory use and generation                                    |
| `/init`        | Analyze the repo and write `FOREBRAIN.md` guidance                        |
| `/diff`        | Show the git diff, untracked files included                            |
| `/context`     | Inspect the context window and compaction state                        |
| `/compact`     | Summarize the conversation before it hits the context limit            |
| `/resume`      | Resume a saved chat · `/fork` fork it · `/new` start fresh             |
| `/migrate`     | Import sessions, memories, skills and MCP servers from another agent   |
| `/help`        | List every command and skill                                           |

### In the browser

`forebrain gateway start` serves a web UI in English and Chinese with the same chat
and slash commands as the terminal, plus pages for agents, projects, providers,
permissions, MCP servers, tools, hooks, memories, channels, scheduled tasks and
configuration. Every primary agent is a separate tenant with its own workspace,
sessions and settings. The gateway requires a token by default.

### In your chat apps

Channel adapters connect an agent to Telegram, Discord, Slack, WhatsApp,
Feishu/Lark, DingTalk, WeCom, WeChat, QQ, Matrix, Mattermost, iMessage (via
BlueBubbles) and Home Assistant, or to anything else through the HTTP and
WebSocket bridges. A channel message starts or continues a real agent session.
See [Runtime, gateway and channels](https://forebrain-harness.github.io/config/runtime-gateway-and-channels).

### Safety

Permissions, approvals and the sandbox are separate layers. The sandbox bounds
what a command can touch; permission presets decide what runs without asking;
approvals put everything else in front of you, in full. See the
[safety model](https://forebrain-harness.github.io/guide/safety-model).

## Configuration

Forebrain Harness keeps its configuration, sessions and memory under `~/.forebrain`
(`FOREBRAIN_HOME` moves it). The main file is `~/.forebrain/forebrain.yaml`; secrets are
never stored in it in plain text — reference an environment variable as
`${NAME}` instead. Project-level settings live in `.forebrain/` at the project root,
and `FOREBRAIN.md` carries guidance for the agent (`/init` writes one).

The [configuration reference](https://forebrain-harness.github.io/config/overview)
covers agents and models, sandbox and permissions, memory, MCP, hooks, gateway
and channels.

## Documentation

**[forebrain-harness.github.io](https://forebrain-harness.github.io)** — getting started,
architecture, the [CLI reference](https://forebrain-harness.github.io/guide/cli-command-reference),
[memory systems](https://forebrain-harness.github.io/guide/memory-systems), subagents,
skills and tools, and configuration.

## Build from source

Requirements: Go 1.26 with CGO enabled (a C toolchain), and Node.js 22 with pnpm
10 for the web UI.

```bash
git clone https://github.com/forebrain-harness/forebrain-harness.git
cd forebrain-harness
make build            # web UI + binary + dictionaries → build/bin/forebrain
./build/bin/forebrain
```

For day-to-day Go work, skip the UI rebuild:

```bash
CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
scripts/install-dictionary.sh ./build/bin   # memory search reads dict/ beside the binary
```

The `fts5` tag is required: memory search needs SQLite's full-text index.

<details>
<summary>Testing the npm packaging</summary>

Build the platform package for your machine and link the launcher:

```bash
./npm/scripts/build-platform-packages.sh   # → npm/dist/@forebrain-harness/forebrain-<platform>-<arch>/
cd npm && npm link
forebrain --version
```

The script builds only the host platform by default; set
`FOREBRAIN_BUILD_TARGETS="darwin/arm64 linux/amd64"` to cross-build (each target
needs its C toolchain, because CGO is required). `cd npm && npm unlink` when
done.

For a full install as users get it:

```bash
cd npm && npm pack
npm i -g forebrain-harness-forebrain-<version>.tgz
```

| Scenario                            | Use                                                             |
| ----------------------------------- | --------------------------------------------------------------- |
| Iterating on Go code                | `go build` + `scripts/install-dictionary.sh`                    |
| Iterating on the launcher / package | `build-platform-packages.sh` + `npm link`                       |
| Verifying a release before publish  | `npm pack` + global install                                     |
| Publishing                          | `build-platform-packages.sh` for every target + `npm publish`   |

</details>

## Roadmap

Forebrain Harness is built in the open, and the roadmap is shaped by the people who use it.
Open items where help is especially welcome:

- [ ] **Memory search: word segmentation for Thai, Lao, Khmer and Myanmar.** These scripts write no spaces between words, but memory search currently treats their letters like a spaced script, so each space-delimited phrase is indexed as one term and a memory is found only by querying that whole phrase. Chinese, Japanese and Korean are already segmented, each by its own rules (`segmentRun` in `pkg/memory/tokenize.go`). A new language needs a segmenter of its own there; any dictionary it uses ships beside the binary (add it to `memory.DictionaryFiles`, never `go:embed`), and `memory.TermsRevision` must be bumped so existing indexes are rebuilt.

Have an idea that is not here? [Open an issue](https://github.com/forebrain-harness/forebrain-harness/issues/new) — that is how
the next roadmap item starts.

## Contributing

Forebrain Harness grows through its community. Bug reports, feature ideas, docs fixes,
translations, new channel adapters, provider updates and code are all welcome —
a first-time contribution as much as a large one.

### Report a bug or suggest a feature

[Open an issue](https://github.com/forebrain-harness/forebrain-harness/issues/new). For a bug,
include:

- `forebrain --version`, your OS, and the provider and model you were using;
- what you did, what you expected, and what happened instead;
- the relevant lines from `~/.forebrain/logs/` (remove anything private first).

For a larger change, open an issue to talk about the approach before you write
the code — it saves everyone a rewrite.

### Send a pull request

1. Fork the repository and create a branch from `main`.
2. Make your change, with tests.
3. Run the checks CI runs:

   ```bash
   go vet ./...
   CGO_ENABLED=1 go test -tags fts5 ./... -count=1
   cd frontend && pnpm install && pnpm test && pnpm build   # when you touch the web UI
   ```

4. Open the pull request, describe what changed and why, and link the issue it
   addresses.

A few ground rules keep the codebase healthy:

- **Fix the root cause.** Trace a bug to the invariant that broke and repair
  that, rather than guarding the place where it shows.
- **Protect the prompt cache.** A change that lowers the prompt-cache hit rate
  will not be merged; `FOREBRAIN.md` explains what that means in practice.
- **One engine, two surfaces.** Behavior shared by the terminal and the web
  belongs in the shared engine (`pkg/turn`, `pkg/run`), not in one surface.
- **Respect the architecture tests.** `pkg/architecture` enforces the package
  layering, at most 20 production files per package, a `doc.go` in every
  package, and test files named after the file they cover. If you change
  imports between packages, regenerate the graph with `scripts/package-graph.sh`.
- **CGO is required** on every platform, and tests run with `-tags fts5`.

`FOREBRAIN.md` at the repository root describes the architecture and conventions in
more detail.

### Security

If you find a security vulnerability, please do not open a public issue. Report
it privately through
[GitHub security advisories](https://github.com/forebrain-harness/forebrain-harness/security/advisories/new).

## Support the project

If Forebrain Harness is useful to you, **star the repository** — it is the simplest way to
help other developers find it. Sharing it with your team, writing about it, and
answering questions in issues help just as much.

[![Star History Chart](https://api.star-history.com/svg?repos=forebrain-harness/forebrain-harness&type=Date)](https://star-history.com/#forebrain-harness/forebrain-harness&Date)

## License

Forebrain Harness is released under the [Apache License 2.0](LICENSE).
