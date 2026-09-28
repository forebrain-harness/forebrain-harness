# Releasing and repository setup

This document is for maintainers. It explains how a release happens, how the
repository is configured on GitHub, and what the first go-live looked like.

## 1. How a release happens

Releases are fully automated; nobody runs `npm publish` or
`gh release create` by hand.

1. A PR is merged into `main` (squash merge, Conventional Commits title).
2. The Release workflow (`.github/workflows/release.yml`) runs on every push
   to `main`. release-please maintains a single open release PR: it bumps the
   version in `VERSION` and `npm/package.json` (including the five
   `optionalDependencies` pins) and updates `CHANGELOG.md`, all derived from
   the Conventional Commit types of the merged commits.
3. The workflow's `webui-into-release-pr` job rebuilds the web UI
   (`make ui`) on the release PR branch and, when the output differs from what
   is committed, pushes a `chore(release): build the web UI` commit, so the
   commit that gets tagged embeds a UI built from exactly that source.
4. When the maintainer merges the release PR, the next run of the workflow:
   - tags the version and creates the GitHub Release;
   - builds five platforms — darwin/arm64, darwin/amd64 (cross-compiled with
     `clang -arch x86_64`), linux/amd64, linux/arm64, windows/amd64 —
     smoke-testing `forebrain --version` on native builds;
   - packages `forebrain-dict.tar.gz`, the word-segmentation dictionaries
     memory search reads;
   - uploads the five archives, the dictionary bundle and `SHA256SUMS` to the
     Release, and verifies the published dictionary bundle is exactly what
     this version reads;
   - publishes the five platform npm packages and then the launcher
     `@forebrain-harness/forebrain` (environment `npm`, with provenance);
   - runs `go-install`, which installs the just-tagged version the way a user
     does — this is also what makes proxy.golang.org and sum.golang.org
     record the version.

Merging the release PR is the only manual step, and it belongs to the owner.

## 2. Versioning

Versions are semver, decided by release-please from Conventional Commit
titles: `feat` bumps the minor, `fix` the patch, a `BREAKING CHANGE:` footer
or `!` would bump the major. While the project is on 0.x
(`bump-minor-pre-major: true` in `release-please-config.json`), breaking
changes bump the **minor** version only.

Reaching 2.0.0 has a cost specific to Go: from v2 on, the module path must
carry a `/v2` suffix (`github.com/forebrain-harness/forebrain-harness/v2`),
and every import — in this repository and in every user's code — must change
with it. Until the module path is moved, `go install …@latest` cannot even
see a v2 tag. So do not step into 1.0 → 2.0 lightly: stay on 0.x while the
CLI and configuration still move, and treat the move to v2 as a deliberate,
documented migration.

## 3. The first release

The first release is 0.1.0. The baseline commit that imported the codebase
carries a `Release-As: 0.1.0` footer, and release-please reads that footer to
bootstrap the first version — no manual version seed is needed. To pin any
later release to a specific version, add the same footer
(`Release-As: x.y.z`) to a commit body that lands before that release.

## 4. The web UI in the repository

`pkg/gateway/dist` is release-managed: it is written only by the Release
workflow's `webui-into-release-pr` job, so that each tag embeds a web UI built
from exactly the tagged source. A normal PR that changes `dist` fails the
`Web UI build is release-managed` check. If your PR picked up `dist` changes
by accident, restore them before pushing:

```bash
git checkout origin/main -- pkg/gateway/dist
```

## 5. npm publishing

Publishing runs in the `npm` deployment environment and authenticates with the
`NPM_TOKEN` secret (a granular publish token). The five platform packages are
published before the launcher, so the launcher's `optionalDependencies`
resolve the moment it appears. Each package is published with `--provenance`.

### Switching to Trusted Publishing later

`NPM_TOKEN` is a bootstrap: trusted publishing (OIDC) can only be configured
for a package that already exists. After the first release, switch over:

1. For each of the six packages, register this repository's Release workflow
   as a trusted publisher on npm (package settings on npmjs.com, or
   `npm trusted-publishers add`): owner `forebrain-harness`, repository
   `forebrain-harness`, workflow `release.yml`.
2. Remove the `NPM_TOKEN` secret and the `NODE_AUTH_TOKEN` environment
   variable from the workflow — the job already carries the required
   `id-token: write` permission.
3. Run one release to confirm publishing succeeds without the token.

### Setup (GitHub App, variables, secrets)

The Release workflow authenticates as a GitHub App, not as `GITHUB_TOKEN`,
because commits and PRs pushed with `GITHUB_TOKEN` do not trigger other
workflows' CI runs. One-time setup, done by the owner:

1. Create the App: GitHub → Settings → Developer settings → GitHub Apps →
   New GitHub App. Permissions: **Contents read & write, Pull requests read &
   write, Issues read & write, Metadata read-only.** No webhook. Install it
   on this repository only. Do **not** grant the Workflows permission.
2. Generate a private key for the App (App settings → Private keys), and note
   the App ID.
3. Configure the repository:
   - variable `FOREBRAIN_APP_ID` — the App ID;
   - secret `FOREBRAIN_APP_PRIVATE_KEY` — the generated private key;
   - secret `NPM_TOKEN` — the npm granular publish token.

   For example `gh variable set FOREBRAIN_APP_ID --body <app-id>` and
   `gh secret set FOREBRAIN_APP_PRIVATE_KEY < key.pem`; secret values are set
   by the owner and never appear in scripts, logs or this repository.
4. Run `scripts/setup-github.sh --dry-run`, then `scripts/setup-github.sh`.
   The script is idempotent and configures: squash-only merging (commit title
   = PR title, body = PR body), auto-merge, branch deletion and branch
   updates; private vulnerability reporting, vulnerability alerts and
   automated security fixes; the `dependencies` label; the `npm` environment;
   and the `main` ruleset from `scripts/setup-github/ruleset-main.json`. It
   ends by checking that the variable and both secrets exist, printing the
   `gh` commands for anything missing — it never reads or accepts secret
   values itself.

The `main` ruleset requires all eight CI checks and linear history, and sets
`required_approving_review_count` to 0: there is a single maintainer, and
GitHub does not let the author approve their own PR. Every change still goes
through a PR and all required checks, and the merge itself is the human
approval. Raise the count to 1 when a second maintainer joins.

## 6. If something fails

- **A CI or release job failed.** Re-run the failed job from the Actions
  page; jobs are independent and safe to re-run.
- **Publishing stopped halfway.** `npm publish` refuses to republish a
  version, and the workflow's publish step skips packages whose version
  already exists — re-running the job publishes only what is missing.
- **Release assets are wrong.** The upload step runs
  `gh release upload … --clobber`, so re-running it overwrites existing
  assets with the rebuilt ones.
- **A released version is broken.** Never delete or move an already
  published tag: the Go checksum database has recorded it, and rewriting the
  tag breaks `go install` for everyone who already fetched it. Fix forward
  with a new version (`fix: …` or `Release-As:` as needed).

## 7. First go-live checklist

The go-live ran as three gates. Every merge — including the release PR — was
done by the owner.

**G1 — first push (right after push protection is lifted).** The initial
`git push -u origin main` had been blocked by a push-protection false
positive; once the owner marks it as such via the link in the push error:

- Push `main`. Expected: the push succeeds and the CI workflow's jobs are
  all green. The live tests of the automation batch (module installability,
  version identity, CI coverage) run here.
- The Release workflow also triggers on the push, but until the GitHub App
  exists its first job cannot authenticate — that failure is expected and
  disappears at G2.

**G2 — identity, secrets, repository configuration.**

- Owner: create the GitHub App and install it on this repository (see
  [Setup](#setup-github-app-variables-secrets)), then set
  `FOREBRAIN_APP_ID`, `FOREBRAIN_APP_PRIVATE_KEY` and `NPM_TOKEN`.
- Executor: run `scripts/setup-github.sh --dry-run`, review the output, then
  run `scripts/setup-github.sh`. Expected: the script reports no missing
  variable or secret at the end.
- Run the Release workflow (push, or `gh workflow run Release`). Expected: a
  release PR appears, bootstrapped at 0.1.0 by the baseline commit's
  `Release-As` footer, with a `chore(release): build the web UI` commit on
  it whenever the committed `dist` differs from a fresh UI build.
- Open a throwaway PR to prove the ruleset: a non-Conventional title fails
  `Conventional Commits title`; renaming it passes; a commit pushed without
  `-s` fails `DCO sign-off`. Close it afterwards.

**G3 — first release (owner merges the release PR).**

- Release page: expected five platform archives, `forebrain-dict.tar.gz` and
  `SHA256SUMS` attached to the 0.1.0 Release.
- npm: expected all six packages at 0.1.0, each with a provenance badge.
- The `go-install` job passes — proxy.golang.org and sum.golang.org now
  serve the version.
- On your own machine: `CGO_ENABLED=1 go install -tags fts5
  github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest`, then
  `forebrain --version` prints `v0.1.0`.
- Start a session with Chinese content and let it retrieve a memory:
  expected the dictionaries downloaded to `$(go env GOPATH)/bin/dict/`, and
  the session works offline afterwards.
- Docs site: the install page (S2 of the docs-site plan) is published, and
  its two install commands match `README.md` verbatim.
