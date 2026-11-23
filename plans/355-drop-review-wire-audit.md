# Plan 355: Remove the built-in `review` skill and wire the `/run-skill-generator` recommendation into `audit`

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/skill/system_assets pkg/skill/system_test.go` and
> `git diff --stat 6541b8ab -- pkg/skill/system_assets pkg/skill/system_test.go`.
> The baseline **is** HEAD, so a `6541b8ab..HEAD` range diff is always empty and
> sees no uncommitted work — use the two commands above. If any in-scope file
> changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED — it deletes a shipped skill (user-visible: `/review` disappears) and edits the harness's densest prompt asset
- **Depends on**: `plans/354-run-skill-generator-system-skill.md` (the command being recommended must exist) and `plans/362-verify-system-skill.md` (A4 names `/verify`). Plan 353 is a transitive dependency. **This plan runs last in the batch.**
- **Category**: tech-debt (dedup) + feature (skill system)
- **Planned at**: commit `9b5c4891`, 2026-11-22; revised at `6541b8ab`, 2026-11-23 (HEAD moved when plan 352-config-subagents-page merged — re-run the drift check against `6541b8ab`)
- **Owner decision**: 2026-11-22 — "remove review skill, no need any more, in favor of audit skill. wire into audit skill." This supersedes the earlier decision to add the recommendation to `review`.
- **Owner decision**: 2026-11-23 — "撤销 Plan 355 的裁决，补齐 A runtime-verification skill 这个缺口." The runtime-verification skill this plan originally declined to build is now plan 362, and A4 names it. The three reasons for the original refusal (no browser tool, no screenshot tool, no agreed capture location) are addressed by plans 357–360.
- **Provenance**: the *behavior* added to `audit` was studied from how other agent tools structure runtime verification. **All text in this plan and in the assets it produces is original and authored for this repository.** Do not consult, download, decompile, `strings`-dump or transcribe any third-party binary or product; nothing here requires it, and there is no carve step or hash gate because there is no source text to match. Where this plan says "the text must state X", that is a requirement, not an invitation to source X from elsewhere.

- **Review 2026-11-23 (`review-plan plans/353-365`, baseline `6541b8ab`)**: five
  defects fixed in place, all forced by existing tests or git semantics —
  ① A1/A3 named `.claude/skills` and "repository", which
  `TestAuditSkillIsSelfContainedAndSourceNeutral` (`system_test.go:668-683`)
  forbids in this asset; reworded neutrally. ② The playbook's four-directory
  probe named `.claude`, which `TestAuditSkillReferencesShip` (`:693-725`)
  forbids in audit's references; replaced with a neutral glob and the
  "byte-identical across three assets" requirement dropped for this copy.
  ③ `BLOCKED = 0` was false — the word is already a backlog status at
  `audit/SKILL.md:50`; gates now expect 1. ④ "additions only, no deleted line"
  is impossible for inline insertions; the gate is now "only lines 34/42/44
  change". ⑤ The package-wide `"review"` grep can never be empty while the
  fixtures this plan keeps survive; narrowed. Also fixed: assertion numbering
  (1/6/8, not 1/5/7), the drift-check form, the 65-vs-66 line cap, the
  `run-skill-generator` line-vs-occurrence count, and four unlisted `/review`
  references that would have tripped this plan's own STOP.
  **Owner 2026-11-23: 采纳中立改写**（不放宽中立性测试）。

## Why this matters

Two things happen here, and they belong in one plan because the second replaces
the first.

**The removal.** `review` is a 48-line static skill: read `git diff`, look for
correctness/security/performance/style/edge-case problems, print them ranked
P0–P3. `audit` (`pkg/skill/system_assets/audit/SKILL.md`, 54 lines plus three
files under `references/`) covers the same ground and far more: nine audit
categories including correctness and security, a vetting pass that rejects
duplicate/misattributed/unsupported findings, an evidence standard requiring
2–5 cited locations per finding, an ordering rule, and a `branch` variant that
does precisely what `review` does — "audit files changed since the merge-base
with the default branch plus their direct importers or callers, tagging each
finding introduced or pre-existing" (line 30). `review` is the subset of
`audit branch` with the evidence discipline removed. Two skills that answer the
same request, one of them weaker, is exactly the drift problem the repo already
guards against with `TestSkillAuthoringRulesLiveInOnePlace`
(`pkg/skill/system_test.go:509-552`), whose comment states the reason: two
copies drift, and a drifted copy is how work gets done to rules that were
replaced.

**The rewire.** The recommendation semantic now appears twice in forebrain: in the
`run` skill's closing paragraphs (plan 353), where it fires when getting the app to
run took work the project had not recorded, and in its Stale bullet, where it fires
when an existing run skill broke on mechanics unrelated to the task. Both live in a
skill the model reaches only when it is already trying to *run* something.

That leaves a gap. A session doing static work — auditing a branch, reviewing a diff,
planning a change — can hit the same wall without ever invoking `run`: a finding it
cannot settle without seeing the app behave, fifteen minutes lost working out how to
build the project, and no trace of that work left behind for the next session. The
third copy of the semantic exists to cover exactly that session.

`audit` is where it belongs, and it is a better host than `review` was, because
`audit` already contains the two moments where the semantic actually fires:
recon ("note … exact build/test/check commands", line 42) and `execute` ("re-run
the done criteria", line 34).

## Current state

### What the removal touches — the complete list

Verified by grepping the whole repository (excluding `node_modules`, `dist`,
`.git`, `plans`, `.worktrees`) for the review *skill*. The asset and its four
pins are the entire blast radius:

- `pkg/skill/system_assets/review/SKILL.md` — the asset. **One file**; the
  directory has no `references/`, no scripts, no examples
  (`ls -R pkg/skill/system_assets/review` returns only `SKILL.md`).
- `pkg/skill/system_test.go:46-48` — asserts the skill is extracted:
  ```go
  reviewSkill := filepath.Join(dest, "review", "SKILL.md")
  if _, err := os.Stat(reviewSkill); err != nil {
      t.Fatalf("review/SKILL.md not extracted: %v", err)
  ```
- `pkg/skill/system_test.go:282-287` — compares embedded bytes against on-disk
  bytes (an install-fidelity check):
  ```go
  embedData, err := assetsFS.ReadFile("system_assets/review/SKILL.md")
  ...
  diskData, err := os.ReadFile(filepath.Join(tmp, "review", "SKILL.md"))
  ```
- `pkg/skill/system_test.go:325` — `"review",` in the `topLevelDirs` list inside
  `TestInstall_AllSkillDirsExtracted` (`:314-380`).

**Nothing else.** Specifically verified absent: no slash-command registry entry
(`pkg/turn/slash.go` does not mention it — skills auto-derive `/<name>` from
discovery, so deleting the directory deletes `/review`); no `pkg/skill/system.go`
registration (the embed plus fingerprint is the whole mechanism); no web/frontend
reference; no e2e spec; no `docs/` or `README.md`/`FOREBRAIN.md` reference.

Every other "review" hit in the repository is a **different thing and must not be
touched**:

| Hit | What it actually is |
|---|---|
| `caveman-review` (`pkg/skill/system_test.go:369`) | a skill in the `caveman` bundle under `system_assets/` — unrelated, stays |
| `review-plan` (`pkg/skill/system_test.go:594,607,633,645,653`; `audit/SKILL.md:4,33,38`) | an `audit` **variant keyword** — unrelated, stays |
| `"review"` as an agent ID (`pkg/config/permission_profiles_test.go:28,283,309,342,365`; `pkg/tui/reducer_test.go:1143`; `pkg/home/workspace_bootstrap_test.go:125`; `pkg/skill/skillrt_test.go:15`; `frontend/src/lib/api.test.ts:16`) | a test agent named "review" and its `workspaces/review` directory — unrelated, stays |
| `review-agent` (`frontend/src/composables/useChatStream.test.ts:2963+`) | a fixture skill path in a frontend test — unrelated, stays |
| `case "review"` fanout verb (`pkg/tui/reducer.go`, cited in `docs/plan/PLAN_REVIEW_DUPLICATE_PEER_REPORT_PLAN.md:42`) | a TUI fan-out card verb — unrelated, stays |
| `"review": 45053` (`pkg/llm/testdata/deepseek_v3_tokenizer.json:52482`) | tokenizer data — unrelated, stays |
| `workspace-review/SKILL.md` (`pkg/skill/skillrt_test.go:39`) | the same agent-ID fixture as `:15` — unrelated, stays |
| `INDEPENDENT_MODES = {"commit", "review", "compress"}` (`pkg/skill/system_assets/caveman/caveman-stats/scripts/caveman-mode-tracker.py:31`) | the caveman bundle's own mode set — unrelated, stays |
| `/review` in `pkg/tui/run.go:1462` (a comment) and synthetic `/review` payloads in `pkg/tui/chat_session_test.go` (`:16211`, `:16499-16523`, `:18181`, `:22324`) | a comment about inject-prompt commands, and test fixtures that build their own composer/skill payloads — none reads the installed skill, so none breaks. The comment goes stale once the skill is deleted; it is out of scope here — report it, do not edit it |

### Migration is automatic — no code needed

`Install` (`pkg/skill/system.go:24-65`) compares the stored marker against
`Fingerprint()` (`:69-84`, first 16 hex of SHA-256 over all embedded paths and
contents); on a mismatch it stages into `.<dir>.seed-*`, extracts, writes the
marker, then `RemoveAll(dest)` + `Rename`. Deleting `system_assets/review/`
changes the fingerprint, so **every existing install drops the skill on next
start**. There is no registry, no migration table, no user-facing upgrade step.

Two consequences worth stating in the commit message: `/review` stops existing
for users on their next start, and a user who installed their *own* skill named
`review` under `.forebrain/skills`, `.agents/skills`, `.claude/skills` or
`.codex/skills` is unaffected — those are separate roots, and removing a built-in
only ever raises what a same-named user skill resolves to (built-ins are the
lowest-priority root; see the comment at `pkg/skill/discover.go:36-41`).

### The file being edited — `pkg/skill/system_assets/audit/SKILL.md`

54 lines. Frontmatter at 1–5 (`name: audit`, a long description listing every
variant, and `argument-hint`). Then:

- `# Audit and planning workflows` (7) and a one-line scope statement (9)
- `## Boundaries and evidence` (11–20) — eight bullets
- `## Variants` (22–38) — one bullet per variant keyword; line 34 is `execute`,
  line 30 is `branch`; line 38 is the effort/modifier paragraph
- `## Recon and investigation` (40–46) — three paragraphs (42, 44, 46)
- `## Reconcile a plan backlog` (48–50)
- `## Publish plans as issues` (52–54)
- `references/audit-playbook.md`, `references/closing-the-loop.md`,
  `references/plan-template.md`

**Its voice is the constraint.** This asset is written in dense compressed prose:
whole workflows per bullet, no code fences anywhere in the file, hyphens used as
parenthetical dashes (`- not direct implementation -`, line 9), curly quotes at
line 44 (`“Not worth doing”`), and no trailing explanations. The `review` skill
it replaces was bulleted and conversational. **Do not paste `review`'s style, or
the five-bullet block from the superseded version of this plan, into `audit`.**
Match line 42's register: one sentence that carries the rule and its reason.

Line 42, verbatim, is the insertion site:

```
Read project guidance, manifests, verification configuration, design documents and decision records; note deliverable types, structure, exact build/test/check commands, conventions and active areas, and carry documented intent forward. references/audit-playbook.md carries the per-category checklists, the investigator briefing and the finding format; read it before the audit pass. Brief each read-only investigator with its absolute scope, evidence standard, decided tradeoffs and security handling, and have it return findings only; open every cited location yourself before presenting a finding. For documents and data, apply the categories to sources, calculations, structure, usability, confidentiality, reproducibility and deliverable quality.
```

Line 44's ordering rule, verbatim, is the second site:

```
A finding records category, title, 2–5 evidence locations in the material's own coordinates (path and line, page or section, sheet and cell), impact, S/M/L effort, fix risk, HIGH/MED/LOW confidence and a fix sketch. Order by impact over effort, discounted by confidence and fix risk, with verification prerequisites and HIGH-confidence security issues first. “Not worth doing” is a valid, recorded verdict.
```

Line 34's `execute` variant, verbatim, is the third:

```
- execute: dispatch one executor subagent on a single plan in an isolated workspace, then review its changes like a tech lead - re-run the done criteria, check scope, read every change - and render a verdict; reject any out-of-scope change, however plausible. The plan, and optionally an executor model name, follows the keyword. You never edit source yourself, and never merge or publish to the user's own branch. Read references/closing-the-loop.md before the first dispatch.
```

Note the vocabulary collision this plan must respect: `audit` uses "review" as a
**verb** throughout ("review its changes like a tech lead", line 34;
"review-plan", line 33). Removing the `review` *skill* does not remove that verb,
and the new text must not confuse the two.

### forebrain's project-skill discovery, for the probe text

`projectSkillDirs` (`pkg/skill/roots.go:21-26`) is `.forebrain/skills`,
`.agents/skills`, `.claude/skills`, `.codex/skills`, highest priority first, with
the comment "A team that has committed skills for Claude Code or Codex has
committed skills for this repository". `projectSkillRootsForRoot` (`:125-136`)
builds exactly those four at the project root — **no upward walk, no descent into
subdirectories**. `.drafts/` is inert (`pkg/skill/discover.go:77` skips any leaf
beginning with `.`). This repository's own live instance of the artifact is
`.claude/skills/run-forebrain/`.

The four-directory probe below is the same snippet plans 353 (R2) and 354 (G5)
install. **Keep all three byte-identical** so the repo has one discovery snippet.
Note that `audit/SKILL.md` contains no code fences today; adding one is a
deliberate, single exception — if you would rather keep the file fence-free,
express the four directories as prose in the same sentence and record that choice
in your report. Either form is acceptable; a *fifth* variant is not.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Format | `gofmt -l pkg/skill` | no output |
| Vet | `go vet ./pkg/skill/...` | exit 0 |
| Skill tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` | ok |
| Full suite | `make test` | exit 0 |

`make test` is `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`
(Makefile:103-104). On macOS a bare `go test ./...` fails on pdfium — the
`CGO_ENABLED=1 -tags fts5` combination is required.

## Scope

**In scope** (the only files you should modify):

- `pkg/skill/system_assets/review/SKILL.md` — **delete** (and the now-empty `review/` directory)
- `pkg/skill/system_test.go` — remove the three `review` pins (`:46-48`, `:282-287`, `:325`) and add one new test
- `pkg/skill/system_assets/audit/SKILL.md` — three surgical edits
- `pkg/skill/system_assets/audit/references/audit-playbook.md` — one addition (Step 4)

**Out of scope** (do NOT touch, even though they look related):

- `caveman-review`, `review-plan`, every `"review"` agent-ID fixture, the TUI
  fanout verb, the tokenizer JSON — all listed in the table above. Deleting any
  of them breaks unrelated tests.
- `pkg/skill/system_assets/run/**` and `pkg/skill/system_assets/run-skill-generator/**` — plans 353/354.
- `pkg/skill/system_assets/audit/references/{closing-the-loop.md,plan-template.md}` — only `audit-playbook.md` changes.
- `audit/SKILL.md`'s frontmatter, `## Boundaries and evidence`, `## Variants` (except line 34's single clause), `## Reconcile a plan backlog`, `## Publish plans as issues`.
- `pkg/skill/system.go` — no registration change; the embed and fingerprint handle it.
- Every other Go file. No production logic changes in this plan at all.
- `NOTICE`, `LICENSE`, `README.md`, `FOREBRAIN.md`, `docs/**`.

## Git workflow

- Branch: `advisor/355-drop-review-wire-audit`
- Two commits, because they are two changes: one
  `refactor(skill): drop the built-in review skill in favor of audit branch`,
  one `feat(skill): audit points at run-skill-generator when a finding needs the app`.
  Conventional Commits, lowercase imperative, matching recent history
  (`refactor(config): drop the create form's unused name-check parameter`,
  `test(config): pin the empty create form leaving on a tab arrow`).
- Do NOT push and do NOT open a PR unless the operator instructed it.

## Steps

### Step 1: Confirm the dependency landed

```bash
ls pkg/skill/system_assets/run-skill-generator/SKILL.md pkg/skill/system_assets/run/SKILL.md
grep -n "model-invocation: false" pkg/skill/system_assets/run-skill-generator/SKILL.md
```

**Verify**: both files listed; the grep prints a line. If the generator skill is
absent, STOP — Step 3 would recommend a command that does not exist.

### Step 2: Delete the `review` skill and its three pins

```bash
git rm -r pkg/skill/system_assets/review
```

Then edit `pkg/skill/system_test.go`:

1. **`:46-48`** — delete the `reviewSkill` variable, its `os.Stat` and the
   `t.Fatalf`. Read the enclosing test first: if it asserts on other skills too,
   delete only the review clause and leave the rest intact. If the review clause
   is the entire body of the test, delete the whole test function and say so in
   your report.
2. **`:282-287`** — `TestExtractEmbedFS_FileContents` (`:275-297`) compares an
   embedded file against the same file extracted to disk. **Repoint it at
   `audit/SKILL.md`; do not delete it.** Change `assetsFS.ReadFile` to
   `"system_assets/audit/SKILL.md"` and the disk path to
   `filepath.Join(tmp, "audit", "SKILL.md")`; the test keeps its name and its
    meaning ("what the embed says is what lands on disk"). It is the repository's
    only embedded-vs-disk fidelity check **for a SKILL.md**
    (`TestAuditSkillReferencesShip`, `:693-728`, already does `bytes.Equal` for the
    three `audit/references/*` files), and audit survives this plan — owner
    decision 2026-11-23: keep the check, move it off the deleted skill.
3. **`:325`** — delete the `"review",` entry from `topLevelDirs`. The
   `allowedEntries` map at `:345-351` is derived from that list, so no second
   edit is needed. `TestInstall_AllSkillDirsExtracted` fails on any **unlisted**
   directory and on any listed directory that is missing, which is why the entry
   must go in the same commit as the asset.

**Verify**:

```bash
test ! -e pkg/skill/system_assets/review && echo GONE
grep -n '"review"\|system_assets/review\|review/SKILL.md' pkg/skill/system_test.go
grep -rn 'caveman-review\|review-plan' pkg/skill/system_test.go | wc -l
CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1 -run TestInstall -v
```

Expected: `GONE`; the second grep prints **nothing**; the third prints a count
**≥ 5** (proof you deleted the right things and left `caveman-review` and
`review-plan` alone); `TestInstall` PASSes.

### Step 3: Wire the recommendation into `audit/SKILL.md`

Three surgical edits. Match the file's compressed prose; add no new section and no
new bullet list.

**A1 — recon, line 42.** Insert one paragraph immediately after
`…conventions and active areas, and carry documented intent forward.` and before
`references/audit-playbook.md carries the per-category checklists…`:

> A project run skill - a `run-*` skill in one of the four skill directories the
> product discovers at the project root - is the project's own record of how it
> builds, launches and is driven; check for one and follow it before working the
> build out from the README, even when you think you already know. Project skills
> load only in a trusted project, so one that is on disk but absent from the
> catalog means untrusted, not missing.

**Wording constraint (review 2026-11-23, test-pinned — not a style preference):**
`TestAuditSkillIsSelfContainedAndSourceNeutral`
(`pkg/skill/system_test.go:668-683`) lowercases `Description + Body` and fails on
any of `".claude"`, `"claude"`, `"git"`, `"repository"`, `"monorepo"`, `"diff"`,
`"refactor"`, `"codebase"`, `"skill.md"`, the provider names and the builtin tool
names. The earlier draft of A1 enumerated `.claude/skills` and said "the
repository's own record"; both fail that test, so the text above names no
directory and says "project". Keep it that way: the four directories are named in
`references/audit-playbook.md`'s source-of-truth pointer (Step 4), not here.

Why this wording: the rule being added is *prefer the project's own recorded handle
over rediscovering the build*, and the "even when you think you already know" clause
is what makes it fire — an auditor that already read the README will otherwise skip
the check. The trust sentence is forebrain-specific and prevents a wrong conclusion:
`AgentSkillRoots` (`pkg/skill/roots.go:52-60`) includes project roots only when
`launch.IsTrusted()`, so "the probe found a skill but my catalog has none" has a
benign explanation that must not be reported as a broken skill.

**A2 — finding format, line 44.** Append one sentence to the paragraph, after
`“Not worth doing” is a valid, recorded verdict.`:

> A finding that only a running app can confirm - a race, a rendering fault, a
> wrong status code, a migration that breaks on real data - says so instead of
> guessing a severity, and says where it stopped when the app could not be
> reached: the command, the error, and a filled-in `/run-skill-generator` prompt
> the user can paste.

Why this wording: it extends `audit`'s existing evidence discipline (line 44 already
requires 2–5 cited locations per finding and already ranks "verification
prerequisites" first) to the one kind of evidence a static pass cannot produce.
**Do not add a PASS/FAIL/BLOCKED/SKIP verdict vocabulary to this asset** — `audit`
has no verdict set for findings; its `execute` variant renders a verdict on an
executor's *diff*, which is a different thing, and introducing one verdict name
implies the other three exist. The verdict vocabulary lives in the separate
`verify` skill (plan 362), which observes the **running app** rather than the diff.
`audit` reaches it by name (A4) and never by vocabulary.

**A3 — the recommendation itself, and where it fires.** Add to the **same
paragraph (line 44)** as A2 — not to line 46. Placement is pinned so the count
gates below are deterministic: A2 and A3 then share one line, and
`grep -c "run-skill-generator"` counts **1 line / 2 occurrences**.

> When reaching a running app took work the project did not already record -
> installing packages, setting environment variables, patching configuration,
> writing a driver or a stub provider - recommend `/run-skill-generator` in the
> report so that work becomes a project skill; when a run skill existed and just
> worked, do not, and when an existing run skill fails on mechanics unrelated to
> the audit, report it as stale and offer to refresh it rather than charging the
> failure to the code under audit.

("the project", not "the repository" — the same neutrality test as A1.)

That one sentence has to carry three distinct triggers, because they fire in different
sessions and collapsing any of them loses coverage: the cold-start case (no run skill,
and getting up took real work), the restraint case (a run skill existed and worked —
say nothing), and the stale case (a run skill existed and broke on mechanics unrelated
to the audit — report the skill, not the change). **The restraint clause is
load-bearing**: without it every audit ends in a sales pitch for the generator and users
learn to ignore it. The "driver or a stub provider" phrasing is deliberate — a fake
provider is the most common handle forebrain projects need, per plan 353's asset and
`scripts/acceptance/`.

**A4 — `execute`, line 34.** Insert after `- re-run the done criteria,`:

> using the project's run skill as the handle where one exists, and `/verify` when a
> criterion is only observable in the running app,

so the executor-review path reuses the same discovery instead of rediscovering the
build, and hands the runtime-only criteria to the skill built for them. One clause, no
new sentence. **`/verify` is plan 362's skill** — if 362 has not landed, insert only the
run-skill half of the clause and note the omission in your report; a recommendation
naming a command that does not exist is worse than no recommendation. Do not reorder the
rest of the plan around it.

**Verify**:

```bash
f=pkg/skill/system_assets/audit/SKILL.md
grep -c "run-skill-generator" "$f"; grep -o "run-skill-generator" "$f" | wc -l
grep -c "run-\*" "$f"; grep -c "just worked" "$f"
grep -c "trusted project" "$f"; grep -c "stub provider" "$f"
grep -c "/verify" "$f"; grep -c "BLOCKED" "$f"; grep -c "^## " "$f"; grep -c '^```' "$f"; wc -l "$f"
git diff --stat -- "$f"
```

Expected: `run-skill-generator` = **1 line / 2 occurrences** (A2 and A3 share
line 44 — `grep -c` counts lines, `grep -o … | wc -l` counts occurrences);
`run-*` = 1; `just worked` = 1;
`trusted project` = 1 (the forebrain trust gate — A1's second sentence);
`stub provider` = 1 (A3, the handle forebrain projects most often need);
`/verify` = 1 (A4 — or 0 if plan 362 has not landed and you inserted only the
run-skill half of the clause, in which case say so in your report);
`BLOCKED` = **1**, not 0: the word is already there as a backlog status in
"Reconcile a plan backlog" (line 50), a paragraph this plan does not touch. A1–A4
add no verdict vocabulary; `^## ` = **5** (unchanged — no new section);
`^``` ` = **0** (the probe lives in the playbook, Step 4, not in this asset);
line count 54–66. The diff touches only lines 34, 42 and 44 — A1 and A4 are
**inline** insertions, so git renders each as one replaced line (`-old +new`);
"additions only, no deleted line" is impossible for them and is not the gate.
No line outside those three may change.

### Step 4: Put the mechanism in `references/audit-playbook.md`

Line 42 states that `references/audit-playbook.md` "carries the per-category
checklists, the investigator briefing and the finding format". The detail behind
A1–A3 belongs there, not in the terse SKILL.md.

I have not read that file, so **do not trust any line number for it**. Locate the
sites yourself:

```bash
grep -n '^#' pkg/skill/system_assets/audit/references/audit-playbook.md
grep -n -i 'finding format\|investigator\|verification' pkg/skill/system_assets/audit/references/audit-playbook.md
```

Then add, in the section that defines the finding format, the discovery probe and
the two rules that do not fit in SKILL.md's compressed prose:

- The discovery probe, in the **neutral form this asset is required to use**:

  ```bash
  grep -Hm1 '^description:' .*/skills/*/SKILL.md 2>/dev/null
  ```

  With the note that the four directories are the ones `projectSkillDirs` lists
  (`pkg/skill/roots.go:21-26`), highest priority first, and that forebrain
  resolves project skills at the session's project root only: it does not walk
  up to the checkout root and does not descend into subdirectories, and anything
  under `.drafts/` is inert until the user publishes it.

  **Why this is not the snippet plans 353/354 carry** (review 2026-11-23,
  test-pinned): `TestAuditSkillReferencesShip`
  (`pkg/skill/system_test.go:693-725`) lowercases each of audit's three reference
  files and fails on `".claude"` / `"claude"` among others. The explicit
  `for d in .forebrain .agents .claude .codex` form cannot go into this file. The
  glob covers the same four dot-directories without naming any of them. The two
  forms are deliberately **not** byte-identical; `projectSkillDirs` is the single
  source of truth, and a fifth directory would be picked up by the glob for free.
- That a run skill which is *stale* is a finding against the skill, not against
  the code under audit, and never a reason to distrust the change.
- That the recommendation names a **user-only** command: `/run-skill-generator`
  carries `model-invocation: false`, so it is absent from the skills catalog and
  the model cannot load it on its own initiative. Recommending it to the user is
  therefore the *only* way it ever runs — which is precisely why the report line
  matters and why the restraint clause matters.

Match `audit-playbook.md`'s own heading levels and voice. If the file turns out to
have no section that defines the finding format, STOP and report its actual
headings rather than inventing a section.

**Verify**:

```bash
p=pkg/skill/system_assets/audit/references/audit-playbook.md
grep -c '\.\*/skills/\*/SKILL.md' "$p"; grep -c "run-skill-generator" "$p"
grep -ic 'claude' "$p"
grep -c "for d in .forebrain .agents .claude .codex" pkg/skill/system_assets/run/SKILL.md pkg/skill/system_assets/run-skill-generator/SKILL.md
git diff --stat -- "$p"
```

Expected: the playbook's glob probe = 1 and `run-skill-generator` ≥ 1;
`grep -ic 'claude' "$p"` = **0** (the neutrality gate above); the explicit
four-directory form is present in the two plan-353/354 assets (1 each) and is
**not** expected in the playbook; the diff touches only the finding-format
section.

### Step 5: Pin the contract

Add `TestAuditSkillCarriesTheRunSkillRecommendation` to
`pkg/skill/system_test.go`, modeled on `TestSystemAssetsCarryTheGeneratorSkill`
(`:443-478`): `Install` into a `t.TempDir()`, read
`<home>/skills/.system/audit/SKILL.md`, assert on its bytes.

It must assert:

1. `<home>/skills/.system/review` does **not** exist, and no entry returned by
   `skill.CatalogEntries([]string{<home>/skills/.system}, "")` is named `review`
   — the removal, proven end to end through the same path the model sees.
2. An entry named `audit` **is** still in that catalog, and `LoadActivation` for
   it reports `SlashCommand == true` and `ModelInvocation == true` — `audit` stays
   both user- and model-invocable, unlike the generator.
3. The `audit` body contains `/run-skill-generator` **twice** (A2 and A3), and
   `run-*` and the restraint phrase `just worked` once each.
4. The `audit` body contains `trusted project` and `stub provider` — one
   occurrence each, pinning the two forebrain-specific clauses added by A1 and
   A3 so they cannot be dropped in a later tidy-up.
5. The `audit` body contains `/verify` exactly once (A4), and its only `BLOCKED`
   is the backlog-status word in the "Reconcile a plan backlog" paragraph — one
   occurrence, present before this plan and untouched by it. A1–A4 add no verdict
   vocabulary: that belongs to `verify`'s own asset (plan 362); `audit` names the
   skill and never a verdict, because naming one verdict implies the other three
   exist here.
6. The `audit` body still contains `references/audit-playbook.md`,
   `review-plan`, and `“Not worth doing”` — proof the edits were surgical and the
   file's own use of "review" as a verb survived.
7. `references/audit-playbook.md` on disk contains the neutral probe
   `grep -Hm1 '^description:' .*/skills/*/SKILL.md` and **no** occurrence of
   `claude` (case-insensitive) — the form `TestAuditSkillReferencesShip` allows.
8. The `caveman` bundle's entries are still in the catalog and still include
   `caveman-review` — proof Step 2 deleted the right skill only.

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1 -run 'TestAuditSkill|TestInstall|TestSystemAssets|TestSkillAuthoring' -v`
→ PASS, no failures.

### Step 6: Full gates

1. `gofmt -l pkg/skill` → no output
2. `go vet ./pkg/skill/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... ./pkg/turn/... ./pkg/run/... -count=1` → ok
4. `make test` → exit 0
5. `git status --porcelain` → only the four in-scope paths (one deleted, three modified)

## Test plan

- Deleted: the `review` clause at `pkg/skill/system_test.go:46-48` and the
  `"review"` entry at `:325`. **Repointed**: `:282-287` now compares
  `audit/SKILL.md` embedded against the same file on disk.
- New: `TestAuditSkillCarriesTheRunSkillRecommendation`, eight assertions per
  Step 5. Structural pattern: `TestSystemAssetsCarryTheGeneratorSkill`
  (`pkg/skill/system_test.go:443-478`).
- Unchanged and expected to keep passing:
  `TestInstall_AllSkillDirsExtracted` (`:314`),
  `TestSkillAuthoringRulesLiveInOnePlace` (`:509-552` — it loops only
  `skill-workshop` and `skill-generator`, so neither the removal nor the `audit`
  edit touches it), the `caveman` bundle assertions (`:369`), and — the two the
  added text must satisfy — `TestAuditSkillIsSelfContainedAndSourceNeutral`
  (`:668-683`, SKILL.md) and `TestAuditSkillReferencesShip` (`:693-725`, the three
  references). Both neutrality gates are why A1/A3 say "project" and the playbook
  probe is a glob.

Assertions 1, 6 and 8 carry the review weight: 1 proves the removal is visible
through the catalog the model actually reads, 6 proves the `audit` edits were
surgical rather than a rewrite, and 8 proves the deletion did not take
`caveman-review` with it.

Verification: `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` → all
pass, including 1 new test and 0 review pins.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `test ! -e pkg/skill/system_assets/review` succeeds
- [ ] `grep -rn 'system_assets/review\|review/SKILL.md' pkg/skill/` returns nothing,
      and `grep -rn '"review"' pkg/skill/` returns only the unrelated survivors
      this plan lists (`skillrt_test.go:15`'s agent-ID fixture and
      `system_assets/caveman/caveman-stats/scripts/caveman-mode-tracker.py:31`'s
      mode set). A package-wide grep for the bare word can never be empty while
      those stay — do not "fix" it by deleting them
- [ ] `grep -c "system_assets/audit/SKILL.md" pkg/skill/system_test.go` returns `2` (the `auditSkill` helper plus the repointed fidelity test)
- [ ] `grep -rc 'caveman-review' pkg/skill/system_test.go` returns ≥ 1 and `grep -rc 'review-plan' pkg/skill/system_assets/audit/SKILL.md` returns ≥ 1 (both survivors intact)
- [ ] `grep -c "run-skill-generator" pkg/skill/system_assets/audit/SKILL.md` returns 1 **line**, and `grep -o "run-skill-generator" … | wc -l` returns 2 (A2 + A3 on line 44)
- [ ] `grep -c "just worked" pkg/skill/system_assets/audit/SKILL.md` returns 1
- [ ] `grep -c "BLOCKED" pkg/skill/system_assets/audit/SKILL.md` returns **1** — the pre-existing backlog-status word at line 50, unchanged
- [ ] `grep -c "/verify" pkg/skill/system_assets/audit/SKILL.md` returns 1 (or 0 with the 362-not-landed omission recorded in the report)
- [ ] `grep -c "^## " pkg/skill/system_assets/audit/SKILL.md` returns 5 (no new section)
- [ ] `grep -icE 'claude|repository|monorepo|git|refactor|codebase' pkg/skill/system_assets/audit/SKILL.md` returns 0, and `grep -ic 'claude' pkg/skill/system_assets/audit/references/audit-playbook.md` returns 0 (substring, case-insensitive — the same semantics as the two neutrality tests, which are authoritative)
- [ ] `git diff -- pkg/skill/system_assets/audit/SKILL.md` changes only lines 34, 42 and 44; inline insertions render as one replaced line each — no other line differs
- [ ] `grep -c '\.\*/skills/\*/SKILL.md' pkg/skill/system_assets/audit/references/audit-playbook.md` returns 1
- [ ] `gofmt -l pkg/skill` produces no output
- [ ] `go vet ./pkg/skill/...` exits 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` exits 0, including `TestAuditSkillCarriesTheRunSkillRecommendation`
- [ ] `make test` exits 0
- [ ] `git status --porcelain` lists only the four in-scope paths
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- `pkg/skill/system_assets/run-skill-generator/SKILL.md` does not exist (plan 354
  has not landed). The recommendation would name a nonexistent command.
- The greps in "Current state" find a reference to the review skill that this plan
  did not list — especially anything under `web/`, `frontend/src/`, `docs/`, an
  e2e spec, or a gateway route. The removal is safe *because* the blast radius is
  four sites; a fifth means the analysis is stale. Report it and wait.
- `pkg/skill/system_test.go:46-48` or `:282-287` turns out to assert on skills
  other than `review` in a way that cannot be separated, or `:282-287` is no longer
  `TestExtractEmbedFS_FileContents` (the embedded-vs-disk comparison). Report the
  test's name and what else it covers — the repoint assumes it reads exactly two
  files, one from the embed and one from disk.
- `audit/SKILL.md` does not match the line-42/44/34 excerpts quoted above, or
  `grep -c "^## " pkg/skill/system_assets/audit/SKILL.md` is not 5. The asset has
  drifted; re-derive the insertion points before editing.
- `references/audit-playbook.md` has no section defining the finding format, the
  investigator briefing, or verification. Report its actual headings.
- Adding the recommendation makes `audit/SKILL.md` exceed 66 lines, or forces a
  new `## ` section. That means the text is too long for this asset's register —
  shorten it and move the detail into `audit-playbook.md` rather than
  restructuring the skill.
- `TestAuditSkillIsSelfContainedAndSourceNeutral` or
  `TestAuditSkillReferencesShip` fails after your edit. The added text named
  something the neutrality gates forbid (a product directory, "repository",
  "git", a tool name). Reword it neutrally — do **not** relax the test.
- `TestSkillAuthoringRulesLiveInOnePlace` fails. Neither change should touch it;
  a failure means something was edited outside scope. Report which assertion.
- You find yourself adding a PASS/FAIL/BLOCKED verdict set or a `## Verdicts`
  section **to this asset**. That vocabulary is plan 362's `verify` skill, not
  `audit`'s: `audit` renders a verdict on a diff, `verify` on observed behavior,
  and collapsing the two is exactly the duplication this plan exists to remove.
  Report the impulse instead.
- Any instruction to build or launch the project as a matter of course. `audit`
  reaches the app through `verify` (A4) and says when it could not; it does not
  grow a build procedure of its own.

## Maintenance notes

- **`/review` disappears for users on their next start.** That is intended, but it
  is user-visible: anyone with `/review` in muscle memory or in a committed
  workflow gets "unknown command". The replacement to tell them is `/audit branch`
  (or `/audit` for the full pass). If the owner wants a deprecation window rather
  than a clean break, that is a separate plan — the mechanism would be keeping the
  directory with a body that redirects, which this plan deliberately does not do.
- **Three copies of the recommendation, three triggers, on purpose.** After plans
  353–355: the `run` skill's closing paragraph (fallback launch needed extra
  work), the `run` skill's Stale bullet (an existing run skill broke), and `audit`
  (a finding needed a running app, or reaching one took unrecorded work). The
  reference design this batch borrows the *behavior* from keeps two copies; we
  need three because `audit` runs without ever loading `run`. Before adding a
  fourth, ask whether it belongs in an existing copy — the failure mode is
  recommendation spam.
- **The discovery probe lives in three places, in two forms**: `run/SKILL.md`
  and `run-skill-generator/SKILL.md` carry the explicit four-directory
  `for d in …` loop; `audit/references/audit-playbook.md` carries the neutral
  glob `grep -Hm1 '^description:' .*/skills/*/SKILL.md`, because
  `TestAuditSkillReferencesShip` forbids naming a third-party product directory
  in audit's references. `audit/SKILL.md` itself names no directory (its own
  neutrality gate). Step 5's assertion 7 pins the playbook's form.
  `pkg/skill/roots.go:21-26` (`projectSkillDirs`) is the source of truth: a fifth
  directory must be added to the two explicit copies; the glob picks it up for
  free.
- **`audit` must stay model-invocable.** Unlike the generator it has no
  `model-invocation: false`, and Step 5's assertion 2 pins that. `audit` is the
  skill a model *should* reach for on its own initiative when asked to review or
  plan; the generator is the one it must never reach for, because its deliverable
  is committed files.
- **The runtime-verification skill is no longer a non-goal — it is plan 362.**
  This plan originally declined to build one, for three reasons: no browser tool,
  no screenshot tool, and no agreed storage location for a capture. The owner
  reversed that ruling on 2026-11-23, and all three blockers are now addressed by
  this batch: 357 makes a local server reachable from a sandboxed command, 358
  holds a process across turns and waits for readiness, 360 produces an image (a
  tool result could already carry one — `read_file` has returned image parts since
  `19239c1d`, which is why plan 359 was rejected), and the storage question is answered by the
  spool convention `shell` already uses (`pkg/tool/shell_tool.go:592-595`),
  extended to `state/verify/`. Plan 362's `verify` skill renders
  PASS/FAIL/BLOCKED/SKIP on **observed behavior**; this plan's A4 is the only
  place `audit` reaches it, by name and never by vocabulary. A2's requirement — a
  finding that needs a running app says so, and says where it stopped — stays: it
  is what `audit` does when `verify` is not available or the app would not come
  up, and it reports the gap instead of papering over it with a guessed severity.
- **Reviewer scrutiny**: read the `audit/SKILL.md` diff as prose, not as a
  checklist. This asset is the densest prompt text in the repository and its
  compression is the point — a bullet list pasted into it will read as foreign
  tissue and will be the first thing a future editor deletes.
