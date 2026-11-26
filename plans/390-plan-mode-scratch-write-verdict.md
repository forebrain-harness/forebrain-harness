# Plan 390: plan mode proves scratch writes clause by clause (heredoc bodies, expansions, `go test` flags, `sh -c`, and the walls around them)

> **Executor instructions**: follow this plan step by step; run every verification
> command and confirm the expected result before moving on. If a STOP condition
> occurs, stop and report — do not improvise. **Owner rulings for this round:
> 直接在当前源码实施，不用 worktree/快照；改动留工作区未提交；批准前不得改任何源码；
> 计划文件不 commit；`sh -c` 的墙洞本期一并修（改分类器）；只读形态（`{ ls; }`、
> `ls ~/…`、`(cd pkg && go test …)`）维持今天「跑」的行为；但分组/`!`/`exec`/保留字
> 后面藏着的真写（`(rm -rf pkg/tool)`、`{ rm -rf pkg/tool; }`、`! sed -i …`、
> `exec go test -c …`、`(find . -delete)`、`if true; then rm -rf pkg/tool; fi`）
> 本期要拦。**
> The reviewer (this session) maintains `plans/README.md` and this file.

**Baseline**: `041a9627` (dev when the excerpts were verified). In-scope files are
unchanged since `bfc3ac17` (`git status --porcelain -- pkg/safety pkg/tool pkg/run` is
empty as this plan is written). `dev` may have moved since — at approval time it was
`6341ce52` (plan 389's `pkg/tui` commit), which touches nothing in this plan's scope —
so the block below, not the SHA, is the check. Drift check, run first:

```
git log --oneline -1
git status --porcelain -- pkg/safety pkg/tool pkg/run
git diff --stat bfc3ac17 -- pkg/safety/shell.go pkg/safety/shell_scratch.go pkg/tool/shell_tool.go
```

A non-empty `git status` on those paths, or a non-empty `git diff --stat`, means the
excerpts below may have drifted: compare before editing and STOP on a content
mismatch (moved line numbers alone are not drift — re-locate by symbol).

## Why this matters

Owner report 2026-11-26, two real TUI transcripts (plan mode + full access), both
refused with `plan mode: "…" modifies files; call exit_plan_mode first`:

1. `cat > /tmp/fb-repro-389/zz_probe2_test.go <<'EOF' … EOF` + `printf '{…}' "$PWD" "$PWD" > /tmp/fb-repro-389/overlay.json`;
2. `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"; CGO_ENABLED=1 go test -tags fts5 -overlay=/tmp/fb-repro-389/overlay.json ./pkg/tui … | grep … | tail -20`.

Every write in both lands in `/tmp`. `plans/387` (commit `bfc3ac17`) made scratch
writes free, so these are holes in that fix. The reviewer hit the refusal twice more
while investigating (`mkdir -p /tmp/x && … go test …`, `go test -overlay ARG`).

## Root causes (measured at `bfc3ac17`; all values are probe output)

### Cause 1 — `go test` flag matching is a prefix test; `-overlay` is not `-o`

`pkg/safety/shell.go:2212-2219`:

```go
case "test":
        for _, arg := range rest {
                a := strings.TrimSpace(arg)
                if a == "-c" || a == "-o" || strings.HasPrefix(a, "-o=") || (strings.HasPrefix(a, "-o") && len(a) > 2) {
                        return ShellMutationWrites
                }
        }
        return ShellMutationUnproven
```

`HasPrefix(a, "-o")` matches `-overlay=…`; `go help build` documents `-overlay file`
as reading a JSON config for build operations. Measured:

```
go test -tags fts5 -overlay=/tmp/x/overlay.json ./pkg/tui -count=1  writes    → Refuse  (wrong)
go test -overlay /tmp/x/overlay.json ./pkg/tui                     writes    → Refuse  (wrong)
go test ./pkg/tool                                                 unproven  → Ask (runs)
go test -c ./pkg/tool                                              writes    → Refuse  (right)
go test -o /tmp/t ./pkg/tool                                       writes    → Refuse  (right)
go test -o=/tmp/t ./pkg/tool                                       writes    → Refuse  (right)
go test -outputdir ./out ./...                                     writes    → Refuse  (right)
go test -coverprofile pkg/tool/c.out ./pkg/tool                    unproven  → Ask (runs; writes inside the project)
go test -cpuprofile pkg/tool/cpu.out ./pkg/tool                    unproven  → Ask (same)
```

Before `bfc3ac17` every `go test` form was `Writes` (`git show bfc3ac17^:pkg/safety/shell.go`),
so both the false `-overlay` refusal and the profile-flag loosening are regressions
from `plans/387` Step 3.

### Cause 2 — the clause walker does not know heredoc syntax

`pkg/safety/shell.go:1822` splits on every newline and `;`; the `<<` branch
(`:1860-1875`) only raises a flag:

```go
if i+1 < len(runes) && runes[i+1] == '<' {
        // A heredoc: what feeds the command arrives after the line, so
        // neither this walker nor a caller can read it here.
        heredoc = true
}
```

No delimiter is read, no body is skipped, so the body parses as commands. Measured on
`cat > /tmp/x/a.go <<'EOF'` + `package a` / `func f() { g(1, 2) }` / `EOF`:
`tier=writes doubt=true clauses=4`, clause[0] `heredoc=true redirects=[/tmp/x/a.go]`.
`pkg/safety/shell_scratch.go:93-98` rejects any clause carrying `Heredoc`, `:84`
rejects the line on doubt ⇒ `confined=false`; the redirect makes it `Writes` ⇒ Refuse.

### Cause 3 — one boolean and one tier for the whole line

`pkg/tool/shell_tool.go:1924-1944` decides Allow from a line-wide boolean and Refuse
from a line-wide tier, so (a) a provable scratch write mixed with an unprovable
command (`mkdir -p /tmp/x && go test …`) is refused, and (b) a line whose only write
is a readable confined redirect is refused because something else could not be read.

### Cause 4 (wall holes, fixed here)

- **Command read from polluted text**: `pkg/safety/shell_scratch.go` derives the
  command from the clause **text**, which still holds redirect targets
  (`plans/387` kept them there on purpose). Measured:
  `> /tmp/cat sh -c 'rm -rf pkg/tool'` ⇒ `confined=true` ("command" = `cat`);
  `> /tmp/x sh -c …` ⇒ `confined=false` only because the basename misses the table.
- **No basename/lowercase normalisation**: `classifyCommand("/bin/rm", ["-rf","pkg/tool"]) = unproven`,
  so `/bin/rm -rf pkg` and `RM -rf pkg` slip past the whitelist.
- **`ln -t`**: `ln -t pkg/tool /tmp/x` is admitted (`confined=true`) because the `ln`
  rule (`shell_scratch.go:209`) ignores `-t/--target-directory` while `mv`:211 and
  `cp`:221 honour it.
- **Writers behind `(`, `{`, `!`, `exec`, reserved words**: `(rm -rf pkg/tool)`,
  `{ rm -rf pkg/tool; }`, `! sed -i '' s/a/b/ main.go`, `exec go test -c ./pkg/tool`,
  `(find . -delete)`, `if true; then rm -rf pkg/tool; fi` give a `Command` of
  `(rm` / `{` / `!` / `exec` / `then`, so they are `Unproven` and run today. Owner
  ruling: refuse them, while `{ ls; }`, `ls ~/…`, `(cd pkg && go test …)` keep running.

### Cause 5 (owner ruling: fix now) — `sh -c` is not classified at all

`classifyCommand("sh", ["-c","rm -rf pkg/tool"]) = unproven`,
`classifyCommand("bash", ["-c","rm -rf pkg/tool"]) = unproven`: `sh -c 'rm -rf pkg/tool'`
in plan mode is `Unproven` ⇒ Ask ⇒ (full access) runs — the wall does not cover a
script in `-c`. The repository already has `shellWrapperScript` (`shell.go:411-423`)
and `shellDashCScript` (`shell.go:578-594`, shared with the dangerous-command
scanner); the tier classifier never uses them, and `shellDashCScript` matches the
exact token `-c` only, so `bash -lc '…'` / `sh -ec '…'` need cluster support.

### Ask is a no-op under full access (why this plan does not re-tier)

`planModeShellGate` only acts when `dangerFullAccess` is on (`shell_tool.go:1931-1933`);
elsewhere it answers Allow and the OS sandbox enforces. Under full access the
approval hook returns "approved, nothing pending" before raising a card
(`pkg/run/runner.go:584-590`, gated by `r.fullAccessEnabled` —
`pkg/run/permissions.go:816-828`: YOLO env **or** the session's danger-full-access
preset). `Ask` and `Allow` are therefore the same behaviour wherever the gate
matters; the only lever is **what it Refuses**.

## Reproduction (the exact probe behind every number above)

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
mkdir -p /tmp/fb390
cat > /tmp/fb390/probe_test.go <<'EOF'
package safety

import ("fmt"; "testing")

func TestZZProbe390(t *testing.T) {
        project := "/Users/doudou/workspace/unionj-cloud/forebrain-harness"
        roots := ShellScratchRoots()
        for _, c := range []string{
                "go test -tags fts5 -overlay=/tmp/x/overlay.json ./pkg/tui -count=1",
                "go test -overlay /tmp/x/overlay.json ./pkg/tui",
                "go test -coverprofile pkg/tool/c.out ./pkg/tool",
                "cat > /tmp/x/a.go <<'EOF'\npackage a\n\nfunc f() { g(1, 2) }\nEOF",
                "mkdir -p /tmp/x && go test ./pkg/tool",
                "cd /tmp && curl -s -o x.zip 'https://example.com/x.zip' && unzip -q x.zip",
                "printf '%s' \"$PWD\" > /tmp/x",
                "> /tmp/cat sh -c 'rm -rf pkg/tool'",
                "sh -c 'rm -rf pkg/tool'",
                "bash -lc 'rm -rf pkg/tool'",
                "ln -t pkg/tool /tmp/x",
                "/bin/rm -rf pkg/tool",
                "(rm -rf pkg/tool)",
                "exec rm -rf pkg/tool",
                "cd /tmp && pushd /etc && echo hi > x",
                "ls ~/.forebrain",
                "cat >> /tmp/x <<EOF\n$(rm -rf pkg/tool)\nEOF",
                "echo $(rm -rf pkg/tool) > /tmp/x",
        } {
                clauses, floor, doubt := splitShellClassifyClauses(c)
                cmds := make([]string, 0, len(clauses))
                for _, cl := range clauses {
                        name, _, _ := shellScratchCommandWords(SplitShellWords(cl.Text))
                        cmds = append(cmds, fmt.Sprintf("%s(h=%v,r=%v)", name, cl.Heredoc, cl.Redirects))
                }
                fmt.Printf("PROBE %-10s tier=%-9s floor=%-9s doubt=%-5v confined=%-5v %v | %q\n",
                        ClassifyShellCommand(c, roots, project), ClassifyShellCommand(c, roots, project),
                        floor, doubt, ShellWritesConfinedToRoots(c, roots, project), cmds, c)
        }
        fmt.Printf("PROBE sh -c = %s\n", classifyCommand("sh", []string{"-c", "rm -rf pkg/tool"}, roots, project))
        fmt.Printf("PROBE /bin/rm = %s\n", classifyCommand("/bin/rm", []string{"-rf", "pkg/tool"}, roots, project))
}
EOF
cat > /tmp/fb390/overlay.json <<'JSON'
{"Replace":{"/Users/doudou/workspace/unionj-cloud/forebrain-harness/pkg/safety/zz_probe390_test.go":"/tmp/fb390/probe_test.go"}}
JSON
GOFLAGS=-overlay=/tmp/fb390/overlay.json \
PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" \
CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run TestZZProbe390 -count=1 -v
```

Use `GOFLAGS=-overlay=…`, not a `-overlay` argument (the argument form is refused by
Cause 1). Expected: the tables above, verbatim. Any difference ⇒ drift: STOP.

## The four states

`ShellScratchWriteVerdict` replaces `ShellWritesConfinedToRoots` (deleted):

```go
type ShellScratchWriteVerdict int

const (
        ShellScratchWritesClean       ShellScratchWriteVerdict = iota // every clause was read; writes stay inside the roots
        ShellScratchWritesUnread                                      // nothing proven to write, but a clause is an unreadable command
        ShellScratchWritesOutside                                     // a write is proven to land outside the roots
        ShellScratchWritesUnplaceable                                 // a write exists whose target cannot be read
)
```

Line verdict = worst clause verdict, `Outside`/`Unplaceable` > `Unread` > `Clean`.
Gate tail (`pkg/tool/shell_tool.go`; the early answers — `ReadOnly`, not plan mode,
`!dangerFullAccess`, approved replay — stay):

```go
        switch safety.ShellScratchWriteVerdict(cmd, safety.ShellScratchRoots(), cwd) {
        case safety.ShellScratchWritesClean:
                return PlanModeShellAllow
        case safety.ShellScratchWritesUnread:
                return PlanModeShellAsk
        default: // Outside, Unplaceable
                return PlanModeShellRefuse
        }
```

## Fix design

### Change A — `go test` flag precision (`pkg/safety/shell.go`)

Exact flag names (bare or `=value`), never a prefix test: `-c`, `-o`, `-outputdir`,
`-coverprofile`, `-cpuprofile`, `-memprofile`, `-mutexprofile`, `-blockprofile`,
`-trace`, `-modfile` (that last one writes a modified `go.mod` copy). `-outputdir`
keeps `plans/387`'s behaviour; the profile/`-modfile` flags return to the pre-`387`
`Writes`; `-overlay` and every other `-o…` lookalike fall through to `Unproven`.

### Change D first — record the facts a clause needs (`pkg/safety/shell.go`)

`shellClause` (`shell.go:1712-1722`) gains:

```go
type shellClause struct {
        Text          string
        Redirects     []string
        Heredoc       bool     // a body this walker did NOT skip
        Command       string   // the clause's command word: basename, lowercased
        Args          []string // the words after it, without redirect targets or heredoc delimiters
        Substitutions []string // the source of each $(…) and `…`
        Grouping      bool     // ( ) { } or a word-start ~ that is not part of a substitution
}
```

Recording rules (inside the existing scan; no second parser):

- `Command`/`Args`: the first completed word that is **not** a redirect target, a
  heredoc/herestring delimiter, an env assignment (`shellEnvAssignment`), a
  grouping/negation/`exec` token (`(`, `)`, `{`, `}`, `!`, `exec`), or a reserved
  word (`containsShellControlKeyword`, `shell.go:400-401`). Then `filepath.Base` +
  `ToLower`, unquoted with `SplitShellWords`. A redirect target is identified **by
  the rune range `redirectTarget` just consumed**, never by "the first equal word"
  (`cmp /tmp/x /etc > /tmp/x` keeps `cmp`).
- `Substitutions`: the inner source of `$( … )` (balanced, quote-aware — reuse the
  scanner around `shell.go:232`), of `` ` … ` ``, of any `$( … )`/backtick nested
  inside `${ … }` and `$(( … ))`, and **the action operand of `trap`**. A parameter
  expansion (`$NAME`, `${NAME}`, `${NAME:-word}`) is a value, not a substitution.
- `Grouping`: `(`, `)`, `{`, `}`, or a word-start `~`, excluding what belongs to a
  recorded substitution and the braces of `${ … }`; also set for an unterminated
  quote/escape.
- `Text` keeps every word exactly as today, so no classifier tier moves except the
  ones Change A and Change E name. The `doubt` return stays for `ClassifyShellCommand`.
- `hasShellHeredocOperator` (`shell.go:1217`, also used from `runtime.go:915`): leave
  it alone (raw-string detector for approval prefixes, does not know `<<<`); report.

### Change C — heredoc bodies are data when the whole line is data readers

In `splitShellClassifyClauses`:

1. `<<<` is a herestring: no flag, nothing to skip (its operand is still not a
   command word).
2. On `<<`: consume `-` **only when it directly follows** `<<`; read the delimiter
   with `consumeShellWord`, unquote with `SplitShellWords` — exactly one word and no
   `$`, backtick or glob ⇒ *plain*. Record whether it was quoted (`<<'EOF'`,
   `<<"EOF"`, `<<\EOF` are quoted in bash; `<<EOF` is not).
3. Collect the line's heredocs in order. At a **physical newline that ends the
   logical command line** (not `;`, not `|`, not a `\`-continuation, not inside a
   quote) decide once for the whole line: skip the bodies **iff**
   (a) every delimiter is plain, **and**
   (b) every clause of the line is a data-only clause — `shellReadOnlyCommands`
   ∪ `shellScratchCommands` ∪ `cd` ∪ a pure assignment clause, **and**
   (c) for an **unquoted** delimiter the body contains no `$` and no backtick
   (the shell expands an unquoted body before the reader sees it:
   `cat >> /tmp/x <<EOF` + `$(rm -rf pkg/tool)` + `EOF` executes it — verified in bash).
   Otherwise fall back to today's behaviour exactly.
4. Skipping: from the character after the newline, for each delimiter in order,
   consume lines until one equals it (leading **tabs** stripped only for `<<-`),
   consume that line too, resume scanning after it. No terminator ⇒ the rest of the
   command is the body and the scan ends (bash semantics).
5. `Heredoc` is set on the clause **only when the body was not skipped**.

### Change E — `sh -c <script>` is classified by its script (owner ruling)

```go
// shellScriptMutation classifies the script operand of `sh -c <script>`;
// nesting deeper than the cap is a write, because the proof stops reading.
func shellScriptMutation(script string, allowedRoots []string, cwd string, depth int) ShellMutation
```

- `shellDashCScript` must accept `-c` **inside a short-option cluster** (`-lc`,
  `-ec`, `-xc`) and keep allowing flags before it. It is shared with the
  dangerous-command scanner, so `sh -lc 'rm -rf /tmp/x'` becomes *Dangerous* too: add
  that row to `TestDangerousCommandUnwrapsWrappers` (`pkg/safety/shell_test.go:507`)
  and say so in the report.
- `classifyCommand` (`shell.go:2034`) gains a depth-aware sibling
  (`classifyCommandDepth(name, args, allowedRoots, cwd, depth)`); `ClassifyShellCommand`
  enters at depth 0 (`classifyCommandWordsDepth`, `shell.go:1965`), and
  `case "sh", "bash", "zsh", "dash", "ksh":` calls `shellScriptMutation(…, depth+1)`.
  Depth cap 4 ⇒ `ShellMutationWrites`. Existing wrappers (`env`, `sudo`, `xargs`,
  `command`, `timeout`, …) already recurse through `classifyCommand`.
- Expected tier changes: `sh -c 'ls -la'` `ReadOnly`; `sh -c 'rm -rf pkg/tool'`
  `Writes`; `bash -lc 'go test ./pkg'` `Unproven`; `sh -ec 'rm -rf pkg'` `Writes`;
  `sh -c "sh -c 'rm -rf pkg'"` `Writes`; five levels deep `Writes`. This also affects
  `ShellCommandIsKnownSafe` for `sh -c` scripts — intended; update any assertion it
  moves and say why.

### Change B — the verdict, per clause

Per clause, in this order (first answering rule wins; the line takes the worst):

0. `stripDevNullRedirects` first. **Zero clauses**: an empty line is **Clean** (nothing
   to run), and a line the walker cannot split at all (`splitShellClassifyClauses`
   returns nil for an unterminated quote/escape, `shell.go:1898-1902`) is **Unread** —
   do not make the walker flush that partial clause, which would move
   `cat 'unterminated` from `Unproven` to `ReadOnly`.
1. **Substitutions, on every clause, before anything else**: each
   `clause.Substitutions` source is classified with `nil` roots; a source whose tier
   is `Writes` ⇒ **Unplaceable** (`echo $(rm -rf pkg/tool) > /tmp/x`,
   `touch /tmp/f <<<$(rm -rf pkg)`, `mkdir -m $(rm -rf pkg) /tmp/x`,
   `echo "${x:-$(rm -rf pkg)}" > /tmp/y`, `trap 'rm -rf pkg/tool' EXIT`,
   `cd $(rm -rf pkg/tool)`). `ReadOnly`/`Unproven` sources are fine.
2. **Directory builtins**: a `cd` with exactly one plain operand is **followed** —
   an absolute operand as written, a relative one from the tracked directory (starting
   at the caller's `cwd`) — and answers **Clean** — *unless* `CDPATH` is in play
   (assigned on this line, or non-empty in the environment: bash searches it first, so
   a relative operand would land somewhere else), in which case a **relative** operand
   leaves the directory **unknown**. A `cd` with no operand, `-`, a flag or an
   expansion, and **`pushd`/`popd` in any form**, also leave the directory **unknown**;
   all of these answer **Clean** (they write nothing themselves). This must precede
   rule 3, because `cd` is in neither table and rule 3 would otherwise call it
   `Unread` (`plans/387`'s transcript, which contains `cd claude-x`, would regress).
3. Is a **write visible** on this clause? A redirect, or `Command` ∈
   `shellScratchCommands`, or **the clause's own tier, computed from `Command`/`Args`
   by `classifyCommandDepth(clause.Command, clause.Args, roots, dir, 0)`**, is
   `Writes`. Never the clause text's tier (the text starts with the redirect target in
   `> f cmd`). A clause that rule 6's `shellScriptFromClause` reduces to a script is
   **not** decided here — rule 6 answers it.
   No visible write ⇒ `Command` ∈ `shellReadOnlyCommands` or empty (pure assignments)
   ⇒ **Clean**; otherwise ⇒ **Unread** (`python3`, `go test`, `curl` without `-o`,
   `export`).
4. Visible write: `clause.Grouping`, `clause.Heredoc`, or an unknown tracked
   directory ⇒ **Unplaceable**.
5. Every redirect target of this clause: literal — no `$`, backtick, `~`, `*?[` ⇒
   otherwise Unplaceable; place with `shellScratchWalkPath`; unresolvable ⇒
   Unplaceable; the root itself or outside the roots ⇒ **Outside**.
6. `sh`-style `-c` clause, found by `shellScriptFromClause(command, args)`:
   `shellScriptFromClause` unwraps the wrapper commands the classifier already knows
   (`env`, `sudo`, `command`, `timeout`, `nice`, `nohup`, `stdbuf`, `setsid`, `xargs`,
   `time`, skipping their flags/assignments) and then matches the `sh`/`bash`/`zsh`/
   `dash`/`ksh` `-c` form (cluster-aware). When it yields a script, this clause's
   verdict is the **worst of the verdict already reached in rules 4-5 and**
   `ShellScratchWriteVerdict(script, roots, dir)` (depth cap ⇒ Unplaceable). Ordering
   matters: the clause's own redirect must be placed before the script's verdict gets
   a say, so `> pkg/tool/x.txt sh -c 'true'` and `sh -c 'true' > $OUT` stay refused
   while `sh -c 'echo hi > /tmp/x'`, `env X=1 sh -c 'echo hi > /tmp/x'` and
   `timeout 5 sh -c 'tar -xzf /tmp/a.tgz -C /tmp/x'` are Clean.
7. `Command` containing `$` or a backtick ⇒ **Unplaceable**.
8. `git clone` ⇒ `shellScratchGitCloneConfined` (unchanged, `shell_scratch.go:322-337`).
9. `Command` ∈ `shellScratchCommands` ⇒ `shellScratchOperandsConfined` decides, with
   two repairs: every written operand **and every flag value** must be literal
   (rule 5's test) — `mkdir /tmp/$X`, `mkdir /tmp/x-$(date +%s)`, `echo hi > $OUT` —
   and `ln` also honours `-t/--target-directory` (`shell_scratch.go:209`).
10. `Command` ∈ `shellReadOnlyCommands` ⇒ Clean.
11. Otherwise ⇒ the tier from rule 3: `Writes` ⇒ **Unplaceable**; `Unproven` + this
    clause carries a redirect ⇒ **Unplaceable** (keeps `go test ./pkg > /tmp/log`
    refused); `Unproven` without one ⇒ Unread; `ReadOnly` ⇒ Clean.

## Behavior matrix (cwd = project, plan mode, full access; ✎ = deliberate revision)

| Command | Before (measured) | After |
|---|---|---|
| `cat > /tmp/x <<'EOF' … EOF` (report 1) | Refuse | **Allow** ✎ |
| `cat <<'EOF' … EOF` (no redirect) | Ask | **Allow** ✎ (tier becomes ReadOnly) |
| `cat > /tmp/x <<'EOF'` + body with `>`, `rm`, `$` | Refuse | **Allow** ✎ (quoted body is literal) |
| `cat <<'EOF' | head -1` + body | Refuse | **Allow** ✎ |
| `cd /tmp && cat <<'EOF' … EOF` | Refuse | **Allow** ✎ |
| `cd /tmp && curl -s -o x.zip … && unzip -q x.zip` (387 flagship) | Allow | Allow |
| `cat >> /tmp/x <<EOF` + body with `$(rm -rf pkg/tool)` | Refuse | Refuse (unquoted body expands) |
| `cat <<'EOF' | sh` + `rm -rf pkg/tool` | Refuse | Refuse |
| `sh <<'EOF'` + `rm -rf "$d"/pkg/tool` | Refuse | Refuse |
| `python3 - <<'EOF' … EOF` | Ask | Ask |
| `cat <<< hi > /tmp/x` | Refuse | **Allow** ✎ |
| `go test -overlay=…`, `go test -overlay …` | Refuse | **Ask → runs** ✎ |
| `go test -c`, `-o`, `-o=`, `-outputdir[=]` | Refuse | Refuse |
| `go test -coverprofile pkg/tool/c.out ./pkg/tool` | Ask (runs, writes the project) | **Refuse** ✎ |
| `printf '%s' "$PWD" > /tmp/x` | Refuse | **Allow** ✎ |
| `echo $(date) > /tmp/x`, `echo $((1+2)) > /tmp/x` | Refuse | **Allow** ✎ |
| `echo $(rm -rf pkg/tool) > /tmp/x`, `trap 'rm -rf pkg/tool' EXIT`, `cd $(rm -rf pkg/tool)` | Refuse / Ask / Ask | **Refuse** ✎ (rule 1) |
| `export A="$PWD/$(go env GOOS)"` (report 2) | Refuse | **Ask → runs** ✎ |
| `mkdir -p /tmp/x && go test ./pkg/tool` | Refuse | **Ask → runs** ✎ (Unread) |
| `mkdir /tmp/$X`, `mkdir /tmp/x-$(date +%s)`, `echo hi > $OUT` | Refuse | Refuse (target not literal) |
| `sh -c 'rm -rf pkg/tool'`, `bash -lc 'rm …'`, `sh -ec 'rm …'` | Ask (runs) | **Refuse** ✎ |
| `sh -c 'ls -la'`, `sh -c 'echo hi > /tmp/x'`, `env X=1 sh -c 'echo hi > /tmp/x'` | Ask | **Allow** ✎ |
| `> /tmp/cat sh -c 'rm -rf pkg/tool'`, `> /tmp/x sh -c …` | Refuse / Refuse | Refuse (kept) |
| `> pkg/tool/x.txt sh -c 'true'`, `sh -c 'true' > $OUT` | Refuse | **Refuse** (kept) |
| `(rm -rf pkg/tool)`, `{ rm -rf pkg/tool; }`, `! sed -i '' s/a/b/ main.go`, `exec go test -c ./pkg/tool`, `(find . -delete)`, `if true; then rm -rf pkg/tool; fi` | Ask (runs) | **Refuse** ✎ (owner ruling) |
| `ln -t pkg/tool /tmp/x` | **Allow (hole)** | **Refuse** ✎ |
| `/bin/rm -rf pkg/tool`, `RM -rf pkg/tool` | Refuse | Refuse (whitelist, not luck) |
| `cd /tmp && pushd /etc && echo hi > x` | Refuse | **Refuse** (kept; unknown dir) |
| `cd /tmp && CDPATH=/workspace/forebrain-harness/pkg cd tool && echo pwned > x.go` | **Allow** today (tracks `/tmp/tool`) | **Refuse** ✎ (relative `cd` under CDPATH ⇒ unknown dir) |
| `git clone /tmp/a /tmp/b`, `mkdir /tmp/x`, `curl -o /tmp/x.zip` | Allow | Allow |
| `{ ls; }`, `ls ~/.forebrain`, `(cd pkg && go test ./...)`, `cd $HOME && ls` | Ask (runs) | Ask or Allow (both run) — owner ruling |
| `(cd pkg && echo hi > state.go)`, `cd /tmp && cd - && echo hi > state.go` | Refuse | Refuse |
| `echo "${x:-$(rm -rf pkg)}" > /tmp/y` | Refuse | Refuse |
| `sed -i '' s/a/b/ main.go`, `rm -rf pkg/tool`, `echo hi > pkg/tool/x.txt` | Refuse | Refuse |
| `python3 /tmp/x.py`, `go test ./pkg/tool` | Ask (runs) | Ask (runs) |

## Scope

**In scope**
- `pkg/safety/shell.go` — Changes A, C, D, E.
- `pkg/safety/shell_scratch.go` — `ShellScratchWriteVerdict` (replacing the deleted
  `ShellWritesConfinedToRoots`), `shellScriptFromClause`, literal-operand rule,
  `ln -t`, doc comments.
- `pkg/safety/shell_scratch_test.go` — verdict table + the invariant test.
- `pkg/safety/shell_test.go` — `go test` tiers, `sh -c` tiers, clause facts, the
  heredoc assertions Change C flips (`shell_test.go:1077-1083`), and
  `TestDangerousCommandUnwrapsWrappers` (`shell_test.go:507`).
- `pkg/safety/permission_rules_test.go` — only if the `sh -c` change moves an
  assertion; say why.
- `pkg/tool/shell_tool.go` — gate tail + doc comment.
- `pkg/tool/shell_output_test.go` — gate cases.
- `plans/README.md` — one index row (the reviewer appends it).

**Out of scope (do NOT touch)**
- `pkg/run/*` — the approval path stays (owner ruling).
- `pkg/tool/process_tool.go` — same gate, inherits the fix; tests must pass unmodified.
- `classifyBase64Command` (`shell.go:2151-2159`) — same loose `-o` prefix, only
  over-classifies: report, leave it.
- Pagers (`less`/`more`/`bat`/`batcat`) escaping to a shell through a tty: report.
- `sh script.sh` (no `-c`), `eval`, `source`, `python3 -c`, `node -e`: unchanged
  (`Unproven`), report as follow-ups.
- `ShellMutation` values/order, tool schemas, surfaces, sandbox profiles.

## Steps

Each step names the tests it must turn green; anything not named stays red until its
own step.

### Step 1: pin everything red

`pkg/safety/shell_scratch_test.go` — `TestShellScratchWriteVerdict` (four states; every
`plans/387` case assigned deliberately with a one-line reason). The rows whose state
moves, so "deliberately" is checkable: **Clean** — `echo $(date) > /tmp/x`, the backtick
row (``echo `date` > /tmp/x``), `(cd /tmp && echo hi > x)`, and the heredoc-body row
(`cat <<EOF > /tmp/x` + `mkdir /tmp/y` + `EOF`): each is truly confined to `/tmp`;
**Unread** — `python3 /tmp/x.py`, `frobnicate /tmp/x`; **non-Clean, unchanged** — "git
subcommand beyond clone" (`git -C … checkout` is `Writes` in
`classifyGitCommand`). Plus:

```go
func TestShellScratchWriteVerdictNeverCleanForUnplacedWrites(t *testing.T) {
        roots := ShellScratchRoots()
        project := "/workspace/forebrain-harness" // absolute, outside the roots; need not exist
        for _, cmd := range []string{
                `> /tmp/x sh -c 'rm -rf pkg/tool'`,
                `> /tmp/cat sh -c 'rm -rf pkg/tool'`,
                `> /tmp/cat rm -rf pkg/tool`,
                `> pkg/tool/x.txt sh -c 'true'`,
                `sh -c 'true' > $OUT`,
                "cat >> /tmp/x <<EOF\n$(rm -rf pkg/tool)\nEOF",
                "echo $(rm -rf pkg/tool) > /tmp/x",
                "trap 'rm -rf pkg/tool' EXIT",
                "cd $(rm -rf pkg/tool)",
                "cd /tmp && cd - && echo hi > state.go",
                "cd /tmp && pushd /etc && echo hi > x",
                "cd /tmp && CDPATH=/workspace/forebrain-harness/pkg cd tool && echo pwned > x.go",
                "ln -t pkg/tool /tmp/x",
                "echo hi > $OUT",
                "mkdir /tmp/$X",
                "mkdir /tmp/x-$(date +%s)",
                "sed -i '' s/a/b/ main.go",
                "rm -rf pkg/tool",
                "/bin/rm -rf pkg/tool",
                "echo hi > pkg/tool/x.txt",
                "sh <<'EOF'\nrm -rf \"$d\"/pkg/tool\nEOF",
                "cat <<'EOF' | sh\nrm -rf pkg/tool\nEOF",
                "sh -c 'rm -rf pkg/tool'",
                "bash -lc 'rm -rf pkg/tool'",
                "sh -c \"sh -c 'rm -rf pkg/tool'\"",
                "(rm -rf pkg/tool)",
                "{ rm -rf pkg/tool; }",
                "! sed -i '' s/a/b/ main.go",
                "exec go test -c ./pkg/tool",
                "(find . -delete)",
                "if true; then rm -rf pkg/tool; fi",
        } {
                if got := ShellScratchWriteVerdict(cmd, roots, project); got == ShellScratchWritesClean {
                        t.Fatalf("%q = Clean; a write the proof cannot place must not be admitted", cmd)
                }
        }
        for _, cmd := range []string{
                "git clone /tmp/a /tmp/b",
                "mkdir /tmp/x",
                "sh -c 'ls -la'",
                "sh -c 'echo hi > /tmp/x'",
                "env X=1 sh -c 'echo hi > /tmp/x'",
                "timeout 5 sh -c 'tar -xzf /tmp/a.tgz -C /tmp/x'",
                "printf '%s' \"$PWD\" > /tmp/x",
                "cat > /tmp/x <<'EOF'\nrm -rf pkg/tool\nEOF",
                "cd /tmp && curl -s -o x.zip 'https://example.com/x.zip' && unzip -q x.zip",
                "cd /tmp && mkdir x",
                "cd /tmp && cat <<'EOF'\nbody\nEOF",
                "cd /tmp && curl -s -o cc.vsix 'https://example.com/cc.vsix' && rm -rf claude-x && mkdir claude-x && cd claude-x && unzip -q ../cc.vsix",
        } {
                if got := ShellScratchWriteVerdict(cmd, roots, project); got != ShellScratchWritesClean {
                        t.Fatalf("%q = %v, want Clean", cmd, got)
                }
        }
}
```

Use an absolute non-scratch cwd as above (`t.TempDir()` lives inside `$TMPDIR`, itself
a scratch root), and call `t.Setenv("CDPATH", "")` at the top of every verdict and gate
test: rule 2's relative-`cd` answer depends on the environment, and a developer shell
that exports `CDPATH` must not be able to flip a row.

Also in Step 1: `pkg/safety/shell_test.go` — `TestClassifyGoCommandFlagTiers`
(Change A), `TestClassifyShellCommandUnwrapsDashCScripts` (Change E), and
`TestSplitShellClassifyClausesCommandFacts` (Change D; command/args/substitution/
grouping facts only). `pkg/tool/shell_output_test.go` — gate rows for every matrix
entry.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool -count=1` fails
on the new cases only; record the output. Add the `ShellScratchWriteVerdict` stub
(returning `ShellScratchWritesClean`) here so both packages compile.

### Step 2: Change D. **Verify**: `go test -tags fts5 ./pkg/safety -run 'TestSplitShellClassifyClausesCommandFacts' -count=1 -v` ⇒ PASS.

### Step 3: Change C, with its own test. Add
`TestSplitShellClassifyClausesHeredocFacts` (heredoc clause facts + the flipped
assertion at `shell_test.go:1077-1083`) in this step, not Step 2.
**Verify**: `go test -tags fts5 ./pkg/safety -run 'TestSplitShellClassifyClauses' -count=1 -v` ⇒ PASS.

### Step 4: Change A. **Verify**: `go test -tags fts5 ./pkg/safety -run 'TestClassifyGoCommandFlagTiers' -count=1 -v` ⇒ PASS.

### Step 5: Change E. **Verify**: `go test -tags fts5 ./pkg/safety -run 'TestClassifyShellCommandUnwrapsDashCScripts|TestKnownSafe|TestGetSimpleCommandPrefix|TestDangerousCommand' -count=1 -v` ⇒ PASS. (The verdict table and the gate's Refuse rows stay red until Step 6.)

### Step 6: Change B + the gate. **Verify**: `go test -tags fts5 ./pkg/safety ./pkg/tool -count=1 -v` ⇒ PASS including `TestShellScratchWriteVerdictNeverCleanForUnplacedWrites`; `grep -rn "ShellWritesConfinedToRoots" pkg/` ⇒ no matches.

### Step 7: broader checks
- `gofmt -l pkg/safety pkg/tool` ⇒ nothing.
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run` ⇒ exit 0.
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run -count=1` ⇒ 3× ok.
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` ⇒ only the
  pre-existing `TestTestFilesCorrespondToProductionFiles` red; anything NEW is a STOP.
- `CGO_ENABLED=1 go build -tags fts5 -o /tmp/fb390-bin ./cmd/forebrain` ⇒ ok.

### Step 8: acceptance (tool-level, all seven)

The acceptance is the gate's answer through the real tool, in
`pkg/tool/shell_output_test.go`'s pattern (`:477-571`; the call is
`shellTool.Handle(ctx, inputJSON)`, `:524-535`) with a danger-full-access runtime.
**Build the context explicitly**: `planModeWriteCtx(home, sessionID)`
(`pkg/tool/request_permissions_test.go:2533`) sets no project root, so `WorkingDir`
falls back to `os.Getwd()` (`pkg/tool/state.go:733-742`) — under `go test` that is
`<repo>/pkg/tool`, and a case that ever failed to be refused would write into this
repository. Use `WithProjectRoot(planModeWriteCtx(home, "sess-1"), projectDir)` with
`projectDir` a `t.TempDir()` (`state.go:616`), assert the message and then the
filesystem at `filepath.Join(projectDir, "pkg", "tool", …)`. Case 1's heredoc target
must exist first: create it with `os.MkdirTemp("/tmp", "fb390-acc-")` and write under
that path. Cases 2-3 are Ask, so install an action hook that returns not-pending
(`shell_output_test.go:517-523`) — otherwise they would stop at an approval record
instead of running. One case per row:

1. report 1's heredoc write ⇒ runs, file present under `/tmp`;
2. report 2's command ⇒ runs;
3. `mkdir -p /tmp/fb390-probe && go test ./pkg/tool -count=1` ⇒ runs;
4. `echo probe > pkg/tool/plan-mode-probe.txt` ⇒ refused (`exit_plan_mode` sentence), file absent;
5. `> /tmp/cat sh -c 'echo pwned > pkg/tool/pwned.txt'` ⇒ refused, file absent;
6. `sh -c 'echo pwned > pkg/tool/pwned2.txt'` ⇒ refused, file absent;
7. `(echo pwned > pkg/tool/pwned3.txt)` ⇒ refused, file absent.

Optionally, additionally drive the TUI (`-tags fts5` build, isolated
`FOREBRAIN_HOME`/`FOREBRAIN_RUN_WORK`, `tmux`, `shift+tab` to plan mode, `/permissions`
→ full access) for cases 4-7 and report the captures; if the driver cannot hold plan
mode across calls, say so — the tool-level cases are the acceptance, not a fallback.

## Done criteria (all must hold)

- [ ] Step 1 output recorded; every new/changed case green after Step 6.
- [ ] `TestShellScratchWriteVerdictNeverCleanForUnplacedWrites` passes and is
      load-bearing (red before Step 6).
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run -count=1` ⇒ 3× ok.
- [ ] `gofmt -l pkg/safety pkg/tool` empty; `go vet -tags fts5` on those three ⇒ exit 0.
- [ ] `pkg/architecture` shows no NEW failure against a pre-change run taken at the
      execution-time HEAD (record that run first; `dev` moves while this plan is being
      executed).
- [ ] `grep -rn "ShellWritesConfinedToRoots" pkg/` ⇒ no matches.
- [ ] `grep -rn 'shellDashCScript\|shellScriptMutation\|shellScriptFromClause' pkg/safety/`
      ⇒ the tier classifier and the verdict both use the `-c` unwrapping.
- [ ] `grep -n 'HasPrefix(a, "-o")' pkg/safety/shell.go` ⇒ exactly one match, in
      `classifyBase64Command` (out of scope).
- [ ] Acceptance cases 1-7 pass at the tool level (plus optional TUI captures).
- [ ] `git status --porcelain -- pkg/safety pkg/tool pkg/run` shows only in-scope files.

## STOP conditions

- The excerpts do not match the live code (content drift) — re-locate by symbol, report, no guessing.
- Any of these stops being refused at any point after Step 6:
  `echo hi > pkg/tool/x.txt`, `rm -rf pkg/tool`, `/bin/rm -rf pkg/tool`,
  `sed -i '' s/a/b/ main.go`, `mkdir /tmp/$X`, `echo hi > $OUT`,
  `echo $(rm -rf pkg/tool) > /tmp/x`, `trap 'rm -rf pkg/tool' EXIT`,
  `cd $(rm -rf pkg/tool)`, `ln -t pkg/tool /tmp/x`,
  `cat <<'EOF' | sh` + `rm -rf pkg/tool`,
  `> /tmp/cat sh -c 'rm -rf pkg/tool'`, `> /tmp/x sh -c 'rm -rf pkg/tool'`,
  `> pkg/tool/x.txt sh -c 'true'`, `sh -c 'true' > $OUT`,
  `sh -c 'rm -rf pkg/tool'`, `bash -lc 'rm -rf pkg/tool'`,
  `sh <<'EOF'` + `rm -rf "$d"/pkg/tool`,
  `cat >> /tmp/x <<EOF` + `$(rm -rf pkg/tool)`,
  `(cd pkg && echo hi > state.go)`, `cd /tmp && pushd /etc && echo hi > x`,
  `cd /tmp && CDPATH=/workspace/forebrain-harness/pkg cd tool && echo pwned > x.go`,
  `(rm -rf pkg/tool)`, `{ rm -rf pkg/tool; }`, `! sed -i '' s/a/b/ main.go`,
  `exec go test -c ./pkg/tool`, `(find . -delete)`,
  `if true; then rm -rf pkg/tool; fi`.
- Read-only shapes start being refused (`{ ls; }`, `ls ~/.forebrain`,
  `(cd pkg && go test ./...)`): the owner ruled these keep running.
- Any `plans/387` flagship starts being refused or asked where it was allowed:
  `cd /tmp && curl -s -o x.zip … && unzip -q x.zip`, `cd /tmp && mkdir x`,
  `mkdir /tmp/x`, `curl -o /tmp/x.zip`, `cat <<'EOF'` without a redirect.
- A heredoc body is skipped while any clause of that line is not a data-only clause,
  or while an unquoted body contains `$`/a backtick.
- A step's verification fails twice after a reasonable fix attempt.
- The fix needs a file outside §Scope.

## Review response (for the record)

Rounds 1-3 asked for and got: literal writer operands; basename/lowercase
normalisation; `nil`-rooted substitution classification; per-test step gates; a stated
acceptance recipe; path-scoped `git status`; a baseline SHA; corrected "before" cells;
`t.TempDir()`'s scratch location; `-coverprofile`/`-cpuprofile`/`-modfile`; `ln -t`;
substitutions on every clause; `-c` recursion ordering; writers behind
`(`/`{`/`!`/reserved words; `-c` clusters; the four-state verdict; `cd` tracked before
the read-only rule; rule 4 before rule 6.

Round 4 (approve with changes) is folded in as: rule 3 computes the clause's tier from
`Command`/`Args` (never the clause text) and skips `-c` clauses for rule 6 to answer;
`pushd`/`popd` join the unfollowable-`cd` set; rule 1 now runs before the `cd` rule so
`cd $(rm -rf pkg/tool)` is read; `shellScriptFromClause` unwraps the classifier's
wrapper commands so `env X=1 sh -c …` and `timeout 5 sh -c …` are judged by their
script; the heredoc facts moved to their own Step-3 test; the scope line now names
`TestDangerousCommandUnwrapsWrappers` at `shell_test.go:507`; and the tool-level cases
are the acceptance, with the TUI run as an optional extra.

Round 6-7 (final round: **approve**) added: rule 0's zero-clause answers (empty line
Clean, an unsplittable line Unread, the walker's nil return untouched), the named
`plans/387` rows whose state moves (Clean: `echo $(date) > /tmp/x`, the backtick row,
`(cd /tmp && echo hi > x)`, the heredoc-body row; Unread: `python3 /tmp/x.py`,
`frobnicate /tmp/x`; unchanged non-Clean: `git … checkout`), and the architecture
criterion compared against a pre-change run at the execution-time HEAD rather than a
fixed SHA (recorded `6341ce52` has only the pre-existing red).

Round 5 (approve with changes) is folded in as: a **relative** `cd` (or `pushd`) while
`CDPATH` is assigned on the line or non-empty in the environment leaves the directory
unknown (`cd /tmp && CDPATH=<project>/pkg cd tool && echo pwned > x.go` is Allow today
because the tracker follows the spelling; it is refused now, with rows in matrix,
invariant and STOP); Step 8 builds its context as
`WithProjectRoot(planModeWriteCtx(home, "sess-1"), projectDir)` over a `t.TempDir()`
and asserts `projectDir/pkg/tool/…` — without it the pattern's `WorkingDir` falls back
to `os.Getwd()`, i.e. this repository, and the call is `shellTool.Handle(ctx, …)`;
`FOREBRAIN_HOME` spelling; and rule 3 points at `shellScriptFromClause` for the `-c`
skip.

## Maintenance notes

- **Reviewer scrutiny, by risk**: (1) rule 1 before rule 2 (substitutions first, even
  on a `cd`); (2) rule 3's tier from `Command`/`Args`, not the text, and its `-c`
  skip; (3) rule 5 before rule 6; (4) `shellScriptFromClause` wrapper unwrapping;
  (5) Change C's three skip conditions and `Heredoc == false only when skipped`;
  (6) the literal rule in rule 9; (7) `pushd`/`popd` and `CDPATH` (rule 2 follows a
  plain operand, absolute or relative, and leaves the directory unknown only when
  `CDPATH` is in play for a relative one).
- **One implementation**: `ShellScratchWriteVerdict` is the only answer about where a
  line writes (`ShellWritesConfinedToRoots` deleted, not wrapped);
  `shellScriptMutation` the only tier unwrapper, `shellScriptFromClause` the only
  verdict-side one.
- **Deferred, reported not fixed**: `sh script.sh`, `eval`/`source`,
  `python3 -c`/`node -e`; substitutions inside a write target beyond the literal rule;
  `go -C <dir> test -c`; `classifyBase64Command`'s loose `-o`; pagers on a tty.
- **Interaction**: `plans/387`'s ordering statement becomes "the verdict decides every
  line, then the verdict's own state decides Allow/Ask/Refuse"; `plans/388`'s
  `process` path inherits it through the same gate. If the owner later wants plan mode
  to prompt rather than run for `Unread`, that is the approval-path change in
  `pkg/run/runner.go:584-590` this plan leaves alone.
## Implementation Status

**VERDICT: APPROVE**（2026-11-26；1 轮 REVISE）。执行器 `alibaba/deepseek-v4.1-flash-1017`，
隔离 worktree `/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/plan-mode-scratch-verdict`
（分支同名，BASE `6341ce52`），两个提交 `7ef15483`（主体）＋`45b10529`（评审 REVISE 修正），
6 个文件 +1951/−352，全部在 §Scope 内，**已按 owner 指示 ff-only 落 dev**（2026-11-26：`6341ce52..85e91e89`，三提交 `7ef15483`/`45b10529`/`85e91e89`，未 push；落 dev 门禁复跑绿：gofmt 空、vet exit 0、`go build -tags fts5 ./cmd/forebrain` ok、三包全量 ok 11.3/21.3/42.0s、`pkg/architecture` 仅预存红 `TestTestFilesCorrespondToProductionFiles`；worktree/分支已按 owner「清理掉」删除（`git worktree remove` + `git branch -d`，内容全在 dev 历史内））。

### 任务完成度

| 任务 | 状态 |
|---|---|
| Step 1 全部测试先红（verdict 四状态表 60+ 行 + 不变式测试 + 三个新分类器测试 + gate 矩阵） | 完成 |
| Step 2 Change D（Command/Args/Substitutions/Grouping 记录，含 CDPATH、Unread 两个附加字段） | 完成 |
| Step 3 Change C（heredoc 体跳过三条件 + `<<-`/`<<<`/引号定界符/缺终止符） | 完成 |
| Step 4 Change A（`go test` 精确 flag：`-c -o -outputdir -coverprofile -cpuprofile -memprofile -mutexprofile -blockprofile -trace -modfile`） | 完成 |
| Step 5 Change E（`shellScriptMutation`＋`shellDashCScript` cluster＋depth cap 4，`sh -lc` 进 Dangerous 行） | 完成 |
| Step 6 Change B（四状态 verdict 取代 `ShellWritesConfinedToRoots`，gate 按状态三答） | 完成 |
| Step 7 更广检查 | 完成（见下） |
| Step 8 验收（tool-level 七例） | 完成；可选 TUI 驱动未做（计划允许） |

### 评审自己跑出的证据（不采信执行器报告）

- `gofmt -l pkg/safety pkg/tool` 空；`go vet -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run` exit 0。
- `go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run -count=1` **第四次**连跑全绿（8.5s / 19.9s / 45.2s）；
  验收＋矩阵＋不变式按名选取（`TestShellToolPlanModeDangerFullAccessScratchResearch`、
  `TestPlanModeShellGateScratchVerdictMatrix`、`TestShellScratchWriteVerdict*`、`TestSplitShellClassifyClauses*`、
  `TestClassifyGoCommandFlagTiers`、`TestClassifyShellCommandUnwrapsDashCScripts`、`TestDangerousCommand*`）全 ok。
- `pkg/architecture` 只有两条已知红（预存 `TestTestFilesCorrespondToProductionFiles` ＋ 本 worktree 无
  `pkg/gateway/dist` 的 `TestWebBuildLoadsNoExternalAsset`），与改动前一致。
- `grep -rn ShellWritesConfinedToRoots pkg/` 无命中；`HasPrefix(a, "-o")` 仅剩 `classifyBase64Command` 一处。
- **我自己的 35 行攻击探针**（`go test -overlay` 直接调 `ShellScratchWriteVerdict`）：`cp /etc/passwd … && rm -rf pkg/tool`、
  `tar --to-command=rm …`、`curl --output $X`、`command/nohup/sudo/timeout/xargs rm -rf pkg/tool`、
  `find . -delete`、`sh -c 'rm -rf pkg/tool'`（含 `> /dev/null`、`bash -lc`、3 层嵌套）、`(rm -rf …)`、`{ … }`、`! sed -i …`、
  `exec go test -c`、`if true; then … fi`、`\rm`、`git -C pkg checkout`、`chown/install/tee/shred/truncate`、
  `dd of=pkg/…`、`tar -czf pkg/tool/x.tar`、`unzip /tmp/a.zip`、`nohup (rm -rf …)`、`env nice timeout 5 sudo rm …` **全部非 Clean**；
  `cat > /tmp/x <<'EOF'`＋`rm` 体、`printf '%s' "$PWD" > /tmp/x`、`sh -c 'echo hi > /tmp/x'`、`env X=1 sh -c …`、
  `git clone /tmp/a /tmp/b`、387 旗舰、`tee /tmp/a <<'EOF'`、`ls | head -3`、`tar -czf /tmp/… -C /tmp/x`、`grep -c`、`unzip -c` **全部 Clean**。

### 评审发现并已修（REVISE round 1）

`shellScriptFromClause` 在判断"是不是 shell"之前就读 `-c`，而 cluster 规则接受任何含 `c` 的短选项，导致
`tar -czf` / `tar -cf` / `grep -c` / `unzip -c` / `nohup tar -czf` 被当成"带脚本"（脚本＝下一个词），
状态由 Clean 误变 Unread。修法＝`shellScriptShells` 表＋先判名后读选项（tier 与 verdict 共用同一张壳名表）。
评审在修复后重测：上述五条全部 Clean 且 script 为空；同时新增的 `tar -czf pkg/tool/x.tar .` ⇒ Outside、
`nohup (rm -rf pkg/tool)` ⇒ Unplaceable 证明墙体没有随之放松。

### 已接受的偏差（均记录在代码注释里）

1. Go 不允许类型与函数同名：类型 `ShellScratchWriteState`、函数 `ShellScratchWriteVerdict`（常量名照计划）。
2. 收尾的 `)`/`}` 只有在后面还有非收尾内容时才置 `Grouping`——否则计划 Step 1 的 Clean 行
   `(cd /tmp && echo hi > x)` 与规则 4 自相矛盾；隐藏写者的形态（`(rm -rf pkg/tool)` 等）仍然 Refuse。
3. 两个超出计划 struct 的字段：`CDPATH`（行内赋值使相对 `cd` 不可跟随）与 `Unread`（替换嵌套超过 8 层上限 ⇒ Unplaceable）。
   替换源递归收集（严格化，不放松）。
4. verdict 侧按 `shellWrapperCommands` 解包 wrapper（`nohup`/`sudo`/`timeout`/`env`…，自身重定向先落位）——
   这是"nohup 行必须 Clean、`nohup rm -rf pkg/tool` 必须拒"两行同时成立的必要条件。
5. tar 创建模式只写 `-f`（`-C`/cwd 是读）；unzip 的 `-c/-p/-l/-t/-v` 送 stdout ⇒ Clean；`shellScriptShells` 比
   `engine.go` 的 `shellInterpreters` 窄（后者含 fish/csh/tcsh，本计划的 tier 不读它们的 `-c`）。

### 遗留（报告未修）

- `tar -tzf /tmp/a.tgz`（纯列表）仍按解包处理 ⇒ Refuse（轮 1 即如此，`--list` 一行可放宽）。
- `(cd /tmp) && echo hi > x`：闭合子 shell 里的 `cd` 不回退跟踪目录，仍只是"碰巧"被拒；需要子 shell cwd 栈。
- 执行器报告过一次 3 包合并跑失败、未留下失败行；评审连跑 4 次（含修复前两次）全绿，判为环境 flake，未复现。
- 计划外的既有项：`classifyBase64Command` 的 `-o` 前缀、`sh script.sh`/`eval`/`source`/`python3 -c`、
  pager 经 tty 逃逸、`hasShellHeredocOperator` 不识 `<<<`。
