# Contributing to Forebrain Harness

Thanks for your interest in making Forebrain Harness better. This document
covers how to set up a development environment, which checks to run, and the
commit and pull request conventions the project enforces.

## Ways to contribute

- **Report a bug** or **suggest a feature** —
  [open an issue](https://github.com/forebrain-harness/forebrain-harness/issues/new).
  For a bug, include the output of `forebrain --version`, your OS, the provider
  and model you were using, what you did, what you expected, and what happened
  instead, plus the relevant lines from `~/.forebrain/logs/` (remove anything
  private first). For a larger change, open an issue to talk about the approach
  before writing the code.
- **Improve the docs** — the documentation lives in this repository and on
  [the docs site](https://forebrain-harness.github.io).
- **Contribute code** — the sections below describe the setup and conventions.
  First-time contributions are as welcome as large ones.

## Development setup

You need Go 1.26 with CGO enabled (a C toolchain), and Node.js 22 with pnpm 10
for the web UI.

```bash
git clone https://github.com/forebrain-harness/forebrain-harness.git
cd forebrain-harness
make build            # web UI + binary + dictionaries → build/bin/forebrain
```

For day-to-day Go work, skip the UI rebuild:

```bash
CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
scripts/install-dictionary.sh ./build/bin   # memory search reads dict/ beside the binary
```

Install the repository git hooks once per clone (commit title check and
automatic DCO sign-off):

```bash
make hooks
```

## Checks

Run these before opening a pull request — CI runs the same set:

```bash
go vet ./...
make test                                            # CGO_ENABLED=1 go test -tags fts5 ./... -count=1
scripts/package-graph.sh                             # when you change imports between packages: graph.json must be updated
cd frontend && pnpm install && pnpm test && pnpm build   # when you touch the web UI
```

## Commit and pull request conventions

Pull requests are merged with **squash merge**. The squash commit's title is
the pull request title and its body is the pull request body, so a good pull
request title and body become a good commit. CI checks the title against the
rules below.

- The title has the form `<type>(<scope>)!: <subject>`; the scope and the `!`
  are optional.
  - **type** is one of `feat fix perf refactor test docs build ci chore revert`;
  - **scope** matches `[a-z0-9._/-]+`, usually the package or area of the
    change (`state`, `tui`, `gateway`, `run`, `turn`, `memory`, …);
  - **subject** starts with a lowercase letter or digit and does not end with
    a period;
  - the whole line is at most **100 characters**.
- A complete example:

  ```
  fix(state): keep the exec timing triple whole for sub-ms tools
  ```

- **Breaking changes** are marked with `!` after the type/scope in the title,
  or with a `BREAKING CHANGE: <who is affected + how to migrate>` footer in the
  body.
- The commit body uses the three sections `Why`, `What`, `Verification`
  (see `.gitmessage`, the repository commit template — enable it
  with `git config commit.template .gitmessage`):

  ```
  Why:
  What:
  Verification:
  ```

  `Verification` lists only the commands you actually ran. Optional sections:
  `Schema`, `Refs`, `BREAKING CHANGE`.

The exact rules are enforced by `scripts/check-commit-message.sh`; when in
doubt, run it on your message file.

## Developer Certificate of Origin

This project uses the
[Developer Certificate of Origin](https://developercertificate.org/) (DCO):
every commit you contribute must carry a `Signed-off-by: Name <email>` trailer
matching the commit's author, certifying that you have the right to submit the
work under the project's license. CI checks this for every pull request;
commits made by bots are exempt.

- Sign a single commit with `git commit -s`.
- Add a sign-off to existing commits with `git rebase --signoff main`.
- With `make hooks` installed, the sign-off is added automatically.

## Ground rules

A few ground rules keep the codebase healthy:

- **Fix the root cause.** Trace a bug to the invariant that broke and repair
  that, rather than guarding the place where it shows.
- **Protect the prompt cache.** A change that lowers the prompt-cache hit rate
  will not be merged; `FOREBRAIN.md` explains what that means in practice.
- **One engine, two surfaces.** Behavior shared by the terminal and the web
  belongs in the shared engine (`pkg/turn`, `pkg/run`), not in one surface.
- **Respect the architecture tests.** `pkg/architecture` enforces the package
  layering, at most 30 production files per package, a `doc.go` in every
  package, and test files named after the file they cover. If you change
  imports between packages, regenerate the graph with `scripts/package-graph.sh`.
- **CGO is required** on every platform, and tests run with `-tags fts5`.

`FOREBRAIN.md` at the repository root describes the architecture and
conventions in more detail.

## Releases

Releases are automated with release-please: contributors do not bump version
numbers and do not write CHANGELOG entries. Maintainer-side details live in
[docs/maintainers/RELEASING.md](docs/maintainers/RELEASING.md).
