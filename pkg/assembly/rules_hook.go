package assembly

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// RuleSource names one instruction file the merged rules body is built from,
// with what became of it: whether its text made it into the merged body (a
// project the user has not trusted is skipped, and a file that starts past the
// body's size limit is dropped), and where it was cut when only part of it did
// (the per-file budget, or the body limit running out inside it).
type RuleSource struct {
	Name  string
	Agent bool // a workspace bootstrap file (AGENTS/SOUL/USER.md)
	// Loaded reports that some of the file's text is in the merged body;
	// NotLoadedReason says why not when it is false.
	Loaded          bool
	NotLoadedReason string
	// TruncatedTo is the character limit that cut the file's text, zero when
	// the whole file is in.
	TruncatedTo int
}

// rulesBuild is one cache entry: the merged rules body the hook injects and
// the source facts recorded while building it. They live under the same key so
// the source list can never drift from the bytes it describes.
type rulesBuild struct {
	merged  string
	sources []RuleSource
}

type PreHook struct {
	mu sync.Mutex
	// cache keyed by cwd+config+roots; rules rarely change within a session.
	cache map[string]rulesBuild
	Cfg   *appcfg.Root
	Home  string
	// WorkspaceRoot is the active primary agent's workspace root (its
	// AGENTS.md/SOUL.md/USER.md live here). Empty falls back to
	// $FOREBRAIN_HOME/workspace (the default main agent's root).
	WorkspaceRoot string
}

func NewPreHook(cfg *appcfg.Root, home, workspaceRoot string) *PreHook {
	return &PreHook{
		cache:         make(map[string]rulesBuild),
		Cfg:           cfg,
		Home:          strings.TrimSpace(home),
		WorkspaceRoot: strings.TrimSpace(workspaceRoot),
	}
}

func (p *PreHook) Hook() hook.AgentHook {
	return p.run
}

func (p *PreHook) run(ctx context.Context, phase string, hc hook.HookContext, text string) (hook.PreHookResult, error) {
	out := hook.PreHookResult{Text: text}
	if phase != "pre" {
		return out, nil
	}
	if !hook.InteractiveTrigger(hc.Trigger) {
		return out, nil
	}
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "/") {
		return out, nil
	}
	wd, ok := rulesWorkingDir()
	if !ok {
		return out, nil
	}
	rules := p.loadRules(wd, hc)
	if strings.TrimSpace(rules) == "" {
		return out, nil
	}
	out.SystemAddendum = fmt.Sprintf("<forebrain_rules>\n%s\n</forebrain_rules>", rules)
	return out, nil
}

// workspaceRoot returns the active primary agent's workspace root, falling back
// to $FOREBRAIN_HOME/workspace (the default main agent's root) when unset.
func (p *PreHook) workspaceRoot() string {
	if root := strings.TrimSpace(p.WorkspaceRoot); root != "" {
		return root
	}
	return home.WorkspaceDir(p.Home)
}

func (p *PreHook) loadRules(wd string, hc hook.HookContext) string {
	return p.buildRules(wd).merged
}

// InstructionSources reports where the session's merged rules body comes from,
// for /status-style views. It resolves the working directory exactly as the
// hook does and shares the cache key with the merged body, so the list always
// describes exactly the text the hook injects.
func (p *PreHook) InstructionSources() []RuleSource {
	wd, ok := rulesWorkingDir()
	if !ok {
		return nil
	}
	return p.buildRules(wd).sources
}

// rulesWorkingDir is the directory the rules body is resolved against: the
// process working directory, the one place both the injection and the report
// read it from.
func rulesWorkingDir() (string, bool) {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		return "", false
	}
	return wd, true
}

func (p *PreHook) buildRules(wd string) rulesBuild {
	maxBody := 120000
	// Workspace rules always chain up the directory tree (git-root → CWD).
	chain := true
	if p != nil && p.Cfg != nil {
		ci := p.Cfg.Agents.Defaults.ContextInject
		maxBody = ci.MaxPreHookRulesCharsOr(120000)
	}
	root := p.workspaceRoot()
	project, projectErr := safety.Resolve(wd)
	trusted := false
	if projectErr == nil {
		trusted, projectErr = safety.IsTrusted(p.Home, project)
	}
	projectKey := ""
	if projectErr == nil {
		projectKey = project.Root
	}
	key := wd + "\x00" + strconv.Itoa(maxBody) + "\x00" + strconv.FormatBool(chain) + "\x00" + p.Home + "\x00" + root + "\x00" + projectKey + "\x00" + strconv.FormatBool(trusted)
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.cache[key]; ok {
		return v
	}
	build := rulesBuild{}
	perFile := bootstrapMaxPerFile(maxBody)
	// Source 1: active primary agent's bootstrap files (AGENTS/SOUL/USER.md).
	var parts []string
	var partSource []int // the source index each part came from
	if files, err := home.LoadWorkspaceBootstrapFilesFromRoot(root, home.LoadOptions{
		CWD:             root,
		Chain:           chain,
		MaxBytesPerFile: perFile,
	}); err == nil {
		for _, f := range files {
			if strings.TrimSpace(f.Body) == "" {
				continue
			}
			src := RuleSource{Name: f.Name, Agent: true, Loaded: true}
			if f.Truncated {
				src.TruncatedTo = perFile
			}
			partSource = append(partSource, len(build.sources))
			parts = append(parts, "## "+f.Name+"\n\n"+f.Body)
			build.sources = append(build.sources, src)
		}
	}
	// Source 2: CWD project FOREBRAIN.md, root-first. A project the user has not
	// trusted is skipped for the body, and the source list records it that way
	// so a reader can tell "absent" from "present but not loaded".
	for _, f := range ProjectForebrainFiles(wd, chain, perFile) {
		src := RuleSource{Name: displayRulePath(f.Path, wd), Loaded: trusted}
		if !trusted {
			src.NotLoadedReason = "project not trusted"
			build.sources = append(build.sources, src)
			continue
		}
		if f.Truncated {
			src.TruncatedTo = perFile
		}
		partSource = append(partSource, len(build.sources))
		parts = append(parts, f.Body)
		build.sources = append(build.sources, src)
	}
	merged := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if maxBody > 0 && len(merged) > maxBody {
		// The body limit cuts the merged text, not a file: a file starting at
		// or past the cut contributes nothing, and the one the cut falls
		// inside is truncated there.
		start := 0
		for i, part := range parts {
			end := start + len(part)
			src := &build.sources[partSource[i]]
			switch {
			case start >= maxBody:
				src.Loaded = false
				src.NotLoadedReason = "over the " + FormatTokenCount(maxBody) + "-char rules limit"
				src.TruncatedTo = 0
			case end > maxBody:
				src.TruncatedTo = maxBody
			}
			start = end + len("\n\n")
		}
	}
	build.merged = TruncateString(merged, maxBody)
	p.cache[key] = build
	return build
}

// displayRulePath shortens a project rule path to be relative to the working
// directory when it lives underneath it ("docs/FOREBRAIN.md"), else keeps the
// absolute path.
func displayRulePath(path, wd string) string {
	// resolvePath evals symlinks, so the comparison baseline matches the
	// project root the walker produced (both may live behind /var → /private/var).
	if rel, err := filepath.Rel(resolvePath(wd), resolvePath(path)); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}
