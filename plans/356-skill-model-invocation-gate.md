# Plan 356: A skill can be user-invocable only — `model-invocation: false` keeps it out of the catalog and out of the `skill` tool

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 9b5c4891..HEAD -- pkg/skill/skill.go pkg/skill/activation.go pkg/skill/skillrt.go pkg/skill/catalog.go pkg/run/skills.go pkg/skill/skill_test.go pkg/skill/catalog_test.go pkg/run/skills_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1 (blocking dependency for plan 354)
- **Effort**: S
- **Risk**: LOW (one new optional frontmatter key; absent key = today's behavior)
- **Depends on**: none
- **Category**: tech-debt / feature (skill system)
- **Planned at**: commit `9b5c4891`, 2026-11-22

## Why this matters

Some skills should only ever run because a human asked. A skill whose
deliverable is committed files in the repository — a driver script, a new
SKILL.md, a config change — is the clearest case: an agent that picked it up
mid-task because a description happened to match would leave the user with
uncommitted work they never requested, and possibly a published skill. There
is no way to express that today.

Every enabled skill is rendered into the model-visible `## Skills` catalog
(`pkg/skill/catalog.go:120`), and the catalog protocol at
`pkg/skill/catalog.go:32` actively instructs the model: "If the user names a
skill, or a skill's description matches the task, you **must** use that skill
for this turn." The existing `slash-command: false` key is the *opposite*
axis — it removes the derived slash command and keeps the skill in the catalog
(`pkg/skill/skill.go:56-61`, used by `skill-workshop`).

After this plan a SKILL.md can declare `model-invocation: false` and get the
missing axis: the slash command still works, the skill's bundled files are
still readable, but the model is never told the skill exists and the `skill`
tool refuses to load it by name.

## Current state

Files in scope, with their role:

- `pkg/skill/skill.go` — the `Skill` struct = the SKILL.md frontmatter schema, and `Parse`.
- `pkg/skill/activation.go` — `Activation` (the model-facing render of one skill) and `LoadActivation`, the explicit-user-invocation path.
- `pkg/skill/skillrt.go` — `mergedSkillEntry` and `mergedSkillParser.collect()`, the discovery pass the catalog renders from.
- `pkg/skill/catalog.go` — `CatalogEntries` / `RenderCatalog`, the `## Skills` prompt section.
- `pkg/run/skills.go` — `RegisterSkillTool`, the `skill` tool the model calls to load a skill by name.

The frontmatter schema today (`pkg/skill/skill.go:43-68`):

```go
type Skill struct {
	RootPath      string         `yaml:"-"`
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license,omitempty"`
	Compatibility string         `yaml:"compatibility,omitempty"`
	Metadata      map[string]any `yaml:"metadata,omitempty"`
	AllowedTools []string `yaml:"-"`
	// SlashCommand controls whether this skill is also surfaced as its own
	// auto-derived slash command. Nil (the frontmatter key absent) means yes,
	// which is what every ordinary skill wants. Meta-skills that are reached
	// through a builtin command instead set it to false so the command palette
	// does not offer two entry points for one capability.
	SlashCommand *bool `yaml:"slash-command,omitempty"`
	ArgumentHint string `yaml:"argument-hint,omitempty"`
	Body         string `yaml:"-"`
}
```

`SlashCommand *bool` is the convention to copy: a pointer, so "key absent" is
distinguishable from "key present and false", and absent means the ordinary
behavior.

The nil-means-yes resolution pattern (`pkg/skill/activation.go:83-86`):

```go
	slashCommand := true
	if sk.SlashCommand != nil {
		slashCommand = *sk.SlashCommand
	}
```

The discovery record the catalog renders from (`pkg/skill/skillrt.go:78-85`):

```go
type mergedSkillEntry struct {
	dir         string
	rootPath    string
	name        string
	description string
	body        string
	metadata    map[string]any
}
```

populated in `collect()` at `pkg/skill/skillrt.go:121-128` from
`found.Skill` — which is a full `skill.Skill`, so a new frontmatter field is
already available there with no change to the scanner.

The catalog filter site (`pkg/skill/catalog.go:126-138`):

```go
	out := make([]CatalogEntry, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.name)
		if name == "" {
			continue
		}
		root := canonicalSkillPath(entry.rootPath)
		out = append(out, CatalogEntry{ ... })
	}
```

The `skill` tool's name resolution and refusal (`pkg/run/skills.go:171-210`).
Read lines 171–246 before editing. The shape is:

```go
			item, ok := loadedSkillByName(st, name)          // :189
			if !ok {
				return "", fmt.Errorf("no skill named %q is available in this session", name)   // :191
			}
			// ... activation, err := pinnedSkillLoad(item) ...
			// ... "skill_name": activation.Name ...          // :202
```

**Why filtering only these two places is sufficient and safe** — verified during
recon, do not re-derive:

- Slash-command derivation does **not** go through `CatalogEntries`. `RefreshSkills`
  (`pkg/turn/skills.go:18-42`) uses `skill.Loader{...}.Discover()` and
  `LoadActivation(...).SlashCommand`. Filtering the catalog leaves `/name` working.
- Skill-file read access does **not** go through `CatalogEntries` either.
  `pkg/run/runner.go:1309-1335` builds `loadedSkills` from the same
  `skill.Loader{...}.Discover()` and hands it to `ReplaceLoadedSkillCatalog`.
  So a filtered skill's `references/`, `examples/` and `scripts/` stay readable.
- The user's explicit slash invocation does **not** go through the `skill` tool.
  It injects `Activation.Content` directly (`pkg/skill/activation.go:24-29`,
  `pkg/run/skills.go:319` `WithExplicitSkillActivation`). So a guard inside
  `RegisterSkillTool` cannot break the user path.

Repo conventions to match:

- Comments explain **why**, in prose, and state the invariant — see the
  `SlashCommand` comment above and `catalogUsage` at `pkg/skill/catalog.go:22-31`.
  Match that voice; do not write what-the-code-does comments.
- Test names in this package are sentences: `TestSystemAssetsCarryTheGeneratorSkill`,
  `TestGeneratorSkillRootNeedsATrustedProject` (`pkg/skill/system_test.go:443`, `:484`).
  Model new tests on those.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Format | `gofmt -l pkg/skill pkg/run` | no output |
| Vet | `go vet ./pkg/skill/... ./pkg/run/...` | exit 0 |
| Skill tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` | ok |
| Run tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1` | ok |
| Turn tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... -count=1` | ok |
| Full suite | `make test` | exit 0 |

(`make test` is `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`, Makefile:103-104.
The `-tags fts5` flag is required; without it the suite does not build.)

## Scope

**In scope** (the only files you should modify):

- `pkg/skill/skill.go`
- `pkg/skill/activation.go`
- `pkg/skill/skillrt.go`
- `pkg/skill/catalog.go`
- `pkg/skill/skill_test.go`
- `pkg/skill/catalog_test.go`
- `pkg/run/skills.go`
- `pkg/run/skills_test.go`
- `FOREBRAIN.md` and/or `docs/**` — **only** where frontmatter keys are already
  enumerated (see Step 7; locate them with the grep given there).
- `pkg/memory/templates/consolidation_project.md` — **only** the two frontmatter
  lines Step 7b names. This embedded prompt creates skills, and today it teaches
  two keys the parser does not implement.

**Out of scope** (do NOT touch, even though they look related):

- `pkg/skill/system_assets/**` — no skill declares the new key in this plan.
  Plan 354 adds the first one. Adding an asset here would make this plan's
  tests depend on a plan that has not landed.
- `pkg/skill/discover.go`, `pkg/skill/hub.go`, `pkg/skill/roots.go` — the
  scanner already carries the whole parsed `Skill`; no change is needed.
- `pkg/turn/skills.go` — slash-command derivation must keep working unchanged;
  that is the point of the design.
- The `/skills` TUI panel and the web skills page — they list and toggle skills.
  Whether they surface the new flag is a separate product decision.
- `pkg/skill/versions.go` — drafts, publishing and the write redirect.
- The **46 installed skills** under `.forebrain/skills/**` that carry
  `user-invocable:` (a key nothing implements; `git grep -l "user-invocable" --
  .forebrain/skills | wc -l` → 46 at `6541b8ab`). They are installed third-party
  content, not this repo's assets; making that key mean something is a separate
  decision. Report the count, do not edit them.

## Git workflow

- Branch: `advisor/356-skill-model-invocation-gate`
- Commit style is Conventional Commits with a lowercase imperative subject and
  an optional `(scope)`; recent examples from `git log`:
  `refactor(config): drop the create form's unused name-check parameter`,
  `fix(web): judge a subagent name by the scope it is written to`,
  `feat(config): a footer action that submits a form, and the name hint`.
  Use e.g. `feat(skill): a frontmatter key that keeps a skill out of the model's reach`.
- Do NOT push and do NOT open a PR unless the operator instructed it.

## Steps

### Step 1: Add the frontmatter field

In `pkg/skill/skill.go`, add to `struct Skill` immediately after the
`SlashCommand` field (currently line 61), before `ArgumentHint`:

```go
	// ModelInvocation controls whether the model may load this skill on its own
	// initiative. Nil (the frontmatter key absent) means yes, which is what
	// every ordinary skill wants. A skill that writes and commits files into the
	// repository sets it to false: the user reaches it through its slash
	// command, and the model is never offered it, so a description that happens
	// to match the current task cannot pull a repository-writing skill into the
	// middle of an unrelated turn. This is the other axis from SlashCommand —
	// that one hides the command and keeps the catalog entry, this one keeps the
	// command and hides the catalog entry.
	ModelInvocation *bool `yaml:"model-invocation,omitempty"`
```

`Parse` (`pkg/skill/skill.go:74-109`) decodes the frontmatter with a plain
`yaml.Unmarshal` into `Skill`, so the struct tag is the whole wiring. Do not add
the field to the loose second-pass decode at `:100-105` — that pass exists only
for `allowed-tools`, whose list spelling cannot unmarshal into a scalar.

**Verify**: `gofmt -l pkg/skill` → no output.
`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` → ok (nothing reads
the field yet, so existing tests must still pass unchanged).

### Step 2: Resolve nil-means-yes in `LoadActivation`

In `pkg/skill/activation.go`:

2a. Add to `struct Activation`, immediately after the `SlashCommand bool` field
(currently line 39):

```go
	// ModelInvocation reports whether the model may load this skill itself.
	// False only when SKILL.md sets `model-invocation: false`. The user's own
	// slash invocation does not consult it — that path injects Content directly.
	ModelInvocation bool
```

2b. In `LoadActivation`, hoist the flag resolution **above** the
`entry := mergedSkillEntry{...}` literal (currently `:76-82`), next to where
`name` and `root` are computed, and set it in both places:

```go
	modelInvocation := true
	if sk.ModelInvocation != nil {
		modelInvocation = *sk.ModelInvocation
	}
```

Add `modelInvocation: modelInvocation,` to the `mergedSkillEntry` literal, and
`ModelInvocation: modelInvocation,` to the returned `Activation` literal
(`:87-94`).

The entry literal must carry it too, not only the returned struct: a
`mergedSkillEntry` built here with the field left at its zero value is a
`false`, and `false` is the restrictive answer. `renderSkillActivation` ignores
the field today, but a later caller that renders from the entry would silently
treat an ordinary skill as user-only.

**Verify**: `gofmt -l pkg/skill` → no output.
`go vet ./pkg/skill/...` → exit 0.

### Step 3: Carry the flag through discovery

In `pkg/skill/skillrt.go`:

3a. Add `modelInvocation bool` to `struct mergedSkillEntry` (`:78-85`).

3b. In `collect()`'s `entries = append(entries, mergedSkillEntry{...})`
(`:121-128`), add:

```go
			modelInvocation: found.Skill.ModelInvocation == nil || *found.Skill.ModelInvocation,
```

`found.Skill` is the full parsed `skill.Skill` (see `discoveredSkill` at
`pkg/skill/discover.go:108-114`), so no scanner change is needed.

**Verify**: `go vet ./pkg/skill/...` → exit 0.
`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` → ok.

### Step 4: Filter the prompt catalog

In `pkg/skill/catalog.go`, inside `CatalogEntries`' loop (`:126-138`), add the
skip next to the existing empty-name skip:

```go
		if !entry.modelInvocation {
			continue
		}
```

Then extend the doc comment on `CatalogEntries` (`:115-119`) with the invariant,
in the file's existing voice. It must say, in substance:

- A skill that declares `model-invocation: false` is not described to the model
  at all — the catalog is the model's only view of the skill set, and the
  protocol above it tells the model to load anything whose description matches.
- It keeps its slash command and its bundled files stay readable, because both
  come from `Loader.Discover` (`pkg/turn/skills.go:18-42`,
  `pkg/run/runner.go:1309-1335`), not from here.
- The catalog sits in the cached prompt prefix and must render identically for
  a whole session (see `RenderCatalog`'s comment at `:39-55`); the flag is read
  from disk with the rest of the entry, so it inherits the existing
  freeze-once-per-session guarantee rather than needing a new one.

**Verify**: `gofmt -l pkg/skill` → no output.
`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` → ok.

### Step 5: Refuse the name in the `skill` tool

Read `pkg/run/skills.go:171-210` first. In `RegisterSkillTool`'s handler, after
the activation has been loaded successfully (the `pinnedSkillLoad(item)` call
at `:196-198`), add:

```go
			if !activation.ModelInvocation {
				return "", fmt.Errorf("no skill named %q is available in this session", name)
			}
```

Use that exact message — the same one the unknown-name branch at `:191` returns.
A distinct message ("this skill is user-invocable only") would confirm to the
model that the skill exists and is reachable by another route, which is the
thing the catalog filter exists to hide. A user-only skill must be
indistinguishable from a name that was never installed.

Add a short comment stating why the two refusals are deliberately identical.

Do **not** change `loadedSkillByName` (`:236-247`): it is shared, and the
explicit-invocation path must keep resolving these skills.

**Verify**: `go vet ./pkg/run/...` → exit 0.
`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1` → ok.

### Step 6: Tests

6a. `pkg/skill/skill_test.go` — frontmatter parsing:

- `model-invocation: false` parses to a non-nil pointer to `false`.
- `model-invocation: true` parses to a non-nil pointer to `true`.
- Key absent parses to `nil` (this is what keeps every existing skill's behavior).
- A skill carrying both `slash-command: false` and `model-invocation: false`
  parses both — the two keys are independent.

Model the fixture style on the existing `Parse` tests already in that file.

6b. `pkg/skill/catalog_test.go` — `CatalogEntries` behavior. This needs skills on
disk, so build a temp root; model the fixture on the temp-root helpers already
used in `pkg/skill/discover_test.go` (read it first and reuse its helper rather
than inventing a second one). Cases:

- Two skills in one root, one with `model-invocation: false` → `CatalogEntries`
  returns exactly one entry, the one without the flag.
- The same skill with the key absent → both entries returned (regression guard
  on the nil-means-yes default).
- `RenderCatalog` over the filtered entries does not contain the hidden skill's
  name anywhere in its output.

6c. `pkg/run/skills_test.go` — the tool refusal. Model on the existing tests at
`:879-916`, which build a `tool.NewState(root)` plus
`st.ReplaceLoadedSkillCatalog([]tool.LoadedSkill{...})` and then
`RegisterSkillTool`. Cases:

- A skill whose SKILL.md sets `model-invocation: false` → invoking the `skill`
  tool with its exact name returns an error whose text is **byte-identical** to
  the error for a name that was never installed. Assert on the equality of the
  two messages, not on a substring — the identical wording is the requirement.
- `LoadActivation` on the same file reports `SlashCommand == true` and
  `ModelInvocation == false`, i.e. the user path is untouched.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... ./pkg/run/... -count=1`
→ ok, and the new test names appear in `-v` output.

### Step 7: Documentation

Locate every place that already enumerates SKILL.md frontmatter keys — including
the ones that teach a key this parser does **not** implement:

```bash
grep -rn "slash-command" FOREBRAIN.md README.md docs/ pkg/skill/system_assets/ | grep -v "_test"
grep -rn "disable-model-invocation\|user-invocable" FOREBRAIN.md README.md docs/ pkg/ --include='*.md' | grep -v "_test"
```

For each hit that is **prose documenting the key set** (not a skill's own
frontmatter, not a test), add `model-invocation` beside `slash-command` with a
one-line description matching the prose style of its neighbors. If the first grep
returns only skill frontmatter and tests, say so in your report rather than
inventing a new documentation section — but the second grep is not optional:
see Step 7b.

**Verify**: re-run the greps; every prose hit now mentions the key that is
actually parsed.

### Step 7b: Converge the skill-creating prompt (review 2026-11-23)

`pkg/memory/templates/consolidation_project.md:782-783` is an **embedded, live
prompt** (`pkg/memory/prompts.go:23` `//go:embed`) that tells the memory
consolidation pass how to write a new SKILL.md. Today it teaches:

```
- disable-model-invocation: true for workflows with side effects (push/deploy/delete/etc.)
- user-invocable: false for background/reference-only skills
```

Neither key is implemented anywhere in Go (`grep -rn
'disable-model-invocation\|user-invocable' --include='*.go' pkg/` → zero), so every
skill that pass creates carries two dead keys. Landing `model-invocation` without
fixing this leaves **three** spellings of one concept, two of them inert and one
with the opposite polarity. Replace those two lines with the keys that exist:

```
- model-invocation: false for workflows with side effects (push/deploy/delete/etc.)
- slash-command: false for background/reference-only skills
```

Nothing pins this template's text (`grep -rn consolidation_project
pkg/memory/*_test.go` → zero hits), so the edit is safe; keep the surrounding
lines byte-for-byte. Line 784's `allowed-tools: … Read, Grep, Glob, Bash` example
names another product's tools — a pre-existing defect, **out of scope**: report
it in one line, do not fix it here.

**Verify**: `grep -n "model-invocation\|slash-command" pkg/memory/templates/consolidation_project.md`
→ the two new lines; `grep -c "disable-model-invocation\|user-invocable" pkg/memory/templates/consolidation_project.md` → 0;
`CGO_ENABLED=1 go test -tags fts5 ./pkg/memory/... -count=1` → ok.

## Test plan

New tests, all listed in Step 6:

- `pkg/skill/skill_test.go` — 4 parsing cases (false / true / absent / both keys).
- `pkg/skill/catalog_test.go` — 3 `CatalogEntries` cases (filtered / unfiltered / render omission).
- `pkg/run/skills_test.go` — 2 cases (identical refusal text / user path intact).

Structural patterns to follow: `pkg/skill/discover_test.go` for on-disk skill
fixtures; `pkg/run/skills_test.go:879-916` for skill-tool invocation;
`pkg/skill/system_test.go:443-478` for assertion voice.

Verification: `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... ./pkg/run/... ./pkg/turn/... -count=1`
→ all pass, including the 9 new cases.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `gofmt -l pkg/skill pkg/run` produces no output
- [ ] `go vet ./pkg/skill/... ./pkg/run/...` exits 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... ./pkg/run/... ./pkg/turn/... -count=1` exits 0
- [ ] `make test` exits 0
- [ ] `grep -n "model-invocation" pkg/skill/skill.go` shows the new struct field with a `yaml:"model-invocation,omitempty"` tag
- [ ] `grep -n "modelInvocation" pkg/skill/catalog.go` shows the filter inside `CatalogEntries`
- [ ] `grep -c "no skill named" pkg/run/skills.go` returns `2` — the two refusals
      share one message. If you hoisted the message into a constant instead,
      assert one constant and two call sites and say so in the report: the
      requirement is byte-identical wording, not two copies of the literal.
- [ ] `grep -c "disable-model-invocation\|user-invocable" pkg/memory/templates/consolidation_project.md` returns 0, and the template's two frontmatter lines now name `model-invocation: false` / `slash-command: false` (Step 7b)
- [ ] `git status --porcelain` lists only files from the in-scope list
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The code at the locations in "Current state" does not match the excerpts —
  in particular if `pkg/skill/skill.go:43-68` no longer carries `SlashCommand *bool`,
  or `pkg/skill/catalog.go:120` is no longer `CatalogEntries`.
- `Parse` turns out to use a **strict** YAML decode (`UnmarshalStrict`,
  `KnownFields`, or a decoder with strict mode). A strict decode changes how the
  new key must be added and may mean unknown keys are already rejected
  somewhere. Report what you found.
- Filtering `CatalogEntries` breaks any existing test. That would mean
  something else derives model-visible state from the catalog, which this plan's
  design assumes it does not. Report the failing test name instead of relaxing
  the filter.
- You find that the explicit slash-invocation path (`WithExplicitSkillActivation`,
  `pkg/run/skills.go:319`) routes through `RegisterSkillTool`'s handler. The Step 5
  guard would then break user invocation; report it before proceeding.
- A second caller of `mergedSkillEntry` appears that renders model-visible text
  from the entry (today only `renderSkillActivation` does, and it ignores the
  flag). Report it — it may need the same filter.

## Maintenance notes

- **The two refusals in `pkg/run/skills.go` must stay identical.** If someone
  later adds a friendlier "that skill is user-only" message, the catalog filter
  leaks: the model learns the skill exists. A reviewer should reject that change
  unless the catalog filter is also reconsidered.
- **`nil` means yes, everywhere.** The pointer type is load-bearing: a plain
  `bool` would make every existing SKILL.md in the world user-only by default.
  Any refactor that flattens `ModelInvocation *bool` to `bool` is a
  behavior-breaking change to every third-party skill.
- **This flag is not a security boundary.** It stops the model from being
  *offered* a skill and from *loading* it by name. It does not stop a model that
  has read the SKILL.md by path from following its instructions — `read_file`
  access to skill directories is governed by the loaded-skill catalog
  (`pkg/tool/skill_catalog.go`), deliberately left untouched here.
- **Interaction with plan 354**: `run-skill-generator` will be the first skill
  to set this key. Plan 354 adds a system-asset test asserting
  `ModelInvocation == false` for it; that test is meaningless until this plan
  lands, which is why 354 depends on 356.
- **Deferred out of this plan**: surfacing the flag in the `/skills` TUI panel
  and the web skills page. A user-only skill currently looks identical to any
  other in those lists. Worth doing, but it is UI work and this plan is the
  mechanism.
