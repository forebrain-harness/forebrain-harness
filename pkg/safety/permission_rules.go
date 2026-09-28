// Permission rule parsing and matching, plus guardrail scanning.
package safety

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// RuleContentDisplay is the one text that names what a rule covers, whichever
// field carries it. Every surface that shows a rule reads it from here: the
// three fields are not interchangeable, and a reader that knew only
// rule_content would print a bare tool name for the two typed ones.
func RuleContentDisplay(v PermissionRuleValue) string {
	if content := strings.TrimSpace(v.RuleContent); content != "" {
		return content
	}
	if len(v.CommandPrefix) > 0 {
		return joinShellTokens(v.CommandPrefix) + ":*"
	}
	// A literal command has no pattern spelling — that is why it is a field —
	// so it is shown as the command it authorizes.
	return strings.TrimSpace(v.Command)
}

func FormatRuleString(v PermissionRuleValue) string {
	tool := strings.TrimSpace(v.ToolName)
	content := RuleContentDisplay(v)
	if tool == "" {
		return ""
	}
	out := tool
	if strings.TrimSpace(content) != "" {
		out = tool + "(" + escapeRuleContent(content) + ")"
	}
	if v.BypassSandbox {
		out += " [bypass_sandbox]"
	}
	return out
}

func escapeRuleContent(s string) string {
	out := strings.Builder{}
	for _, ch := range s {
		switch ch {
		case '\\', '(', ')':
			out.WriteRune('\\')
			out.WriteRune(ch)
		default:
			out.WriteRune(ch)
		}
	}
	return out.String()
}

// pathWildcardMatch performs path-aware glob matching with strict semantics:
//
//   - `*` matches any run of characters within a single path segment.
//   - `**` matches zero or more path segments (including any `/` characters).
//   - All other characters (including `/`) are literals.
//
// Matching is anchored: the pattern must consume the entire candidate path.
func pathWildcardMatch(pattern, input string) bool {
	patSegs := strings.Split(pattern, "/")
	inSegs := strings.Split(input, "/")
	return matchPathSegments(patSegs, inSegs)
}

// matchPathSegments matches pattern segments against input segments where any
// segment equal to "**" represents zero or more path segments.
func matchPathSegments(pat, in []string) bool {
	pi, ii := 0, 0
	starPi, starIi := -1, -1
	for ii < len(in) {
		switch {
		case pi < len(pat) && pat[pi] == "**":
			starPi = pi
			starIi = ii
			pi++
		case pi < len(pat) && segmentGlobMatch(pat[pi], in[ii]):
			pi++
			ii++
		case starPi != -1:
			pi = starPi + 1
			starIi++
			ii = starIi
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == "**" {
		pi++
	}
	return pi == len(pat)
}

// segmentGlobMatch matches one path component. An unclosed character class is
// treated as a literal `[`.
func segmentGlobMatch(pattern, segment string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return pattern == segment
	}
	p := []rune(pattern)
	s := []rune(segment)
	memo := make(map[uint64]bool, len(p)*len(s)+1)
	var rec func(pi, si int) bool
	rec = func(pi, si int) bool {
		key := uint64(pi)<<32 | uint64(uint32(si))
		if v, ok := memo[key]; ok {
			return v
		}
		var res bool
		switch {
		case pi == len(p):
			res = si == len(s)
		case p[pi] == '*':
			for k := si; k <= len(s); k++ {
				if rec(pi+1, k) {
					res = true
					break
				}
			}
		case p[pi] == '?' && si < len(s):
			res = rec(pi+1, si+1)
		case p[pi] == '[':
			end := pi + 1
			for end < len(p) && p[end] != ']' {
				end++
			}
			if end == len(p) {
				res = si < len(s) && s[si] == '[' && rec(pi+1, si+1)
			} else if si < len(s) && runeInGlobClass(s[si], p[pi+1:end]) {
				res = rec(end+1, si+1)
			}
		case si < len(s) && p[pi] == s[si]:
			res = rec(pi+1, si+1)
		default:
			res = false
		}
		memo[key] = res
		return res
	}
	return rec(0, 0)
}

func runeInGlobClass(value rune, class []rune) bool {
	negated := false
	if len(class) > 0 && (class[0] == '!' || class[0] == '^') {
		negated = true
		class = class[1:]
	}
	matched := false
	for i := 0; i < len(class); i++ {
		if i+2 < len(class) && class[i+1] == '-' {
			if class[i] <= value && value <= class[i+2] {
				matched = true
			}
			i += 2
			continue
		}
		if class[i] == value {
			matched = true
		}
	}
	if negated {
		return !matched
	}
	return matched
}

func ExpandDeniedReadPatterns(patterns []string, cwd string, maxDepth *int) ([]string, error) {
	var matches []string
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(cwd, pattern)
		}
		pattern = filepath.Clean(pattern)
		root := globStaticScanRoot(pattern)
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); errorsIsMissing(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		matcher := FileSystemPermissionPath{
			Type: FileSystemPermissionPathTypeGlobPattern, Pattern: pattern,
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path != root && maxDepth != nil {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				depth := pathComponentCount(relative)
				if depth > *maxDepth {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			if matcher.Matches(path, cwd) {
				matches = appendUniquePath(matches, path)
				if entry.IsDir() {
					return filepath.SkipDir
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func globStaticScanRoot(pattern string) string {
	index := strings.IndexAny(pattern, "*?[]")
	if index < 0 {
		return filepath.Clean(pattern)
	}
	prefix := pattern[:index]
	if prefix == "" {
		return ""
	}
	if strings.HasSuffix(prefix, string(filepath.Separator)) {
		return filepath.Clean(prefix)
	}
	return filepath.Dir(prefix)
}

func pathComponentCount(path string) int {
	path = filepath.Clean(path)
	if path == "." || path == "" {
		return 0
	}
	return len(strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}))
}

func errorsIsMissing(err error) bool {
	return err != nil && os.IsNotExist(err)
}

const webFetchDomainPrefix = "domain:"

// WebFetchRuleMatches reports whether a WebFetch permission rule content
// matches a candidate URL. Rule content beginning with "domain:" matches the
// URL host (host wildcards via "*" are supported, e.g. "domain:*.github.com").
// All other content is treated as a literal URL or wildcard URL pattern.
func WebFetchRuleMatches(ruleContent string, urlInput string) bool {
	rule := strings.TrimSpace(ruleContent)
	candidate := strings.TrimSpace(urlInput)
	if rule == "" {
		return false
	}
	if strings.HasPrefix(rule, webFetchDomainPrefix) {
		hostPattern := strings.ToLower(strings.TrimSpace(rule[len(webFetchDomainPrefix):]))
		if hostPattern == "" {
			return false
		}
		host := webFetchHost(candidate)
		if host == "" {
			return false
		}
		if hasUnescapedStar(hostPattern) {
			return wildcardMatch(hostPattern, host)
		}
		return host == hostPattern
	}
	if hasUnescapedStar(rule) {
		return wildcardMatch(rule, candidate)
	}
	return candidate == rule
}

func webFetchHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(u.Hostname()))
}

func isWebFetchTool(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "web_fetch", "webfetch":
		return true
	default:
		return false
	}
}

// CanonicalToolName maps internal tool kinds and public tool aliases to a
// single canonical form so rules written against either name match. The
// canonical form preserves PascalCase user-facing names where they exist,
// falling back to the original input otherwise.
func CanonicalToolName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if canonical, ok := canonicalToolAliases[lower]; ok {
		return canonical
	}
	return trimmed
}

var canonicalToolAliases = map[string]string{
	// Shell
	"shell": "Bash",
	"bash":  "Bash",

	// File I/O
	"read_file":  "Read",
	"read":       "Read",
	"write_file": "Write",
	"write":      "Write",
	"edit_file":  "Edit",
	"edit":       "Edit",
	"multi_edit": "MultiEdit",
	"multiedit":  "MultiEdit",
	// Discovery
	"list_directory": "LS",
	"list_files":     "LS",
	"ls":             "LS",

	// Web
	"web_fetch":  "WebFetch",
	"webfetch":   "WebFetch",
	"web_search": "WebSearch",
	"websearch":  "WebSearch",

	// Session/meta
	"ask_user":        "AskUserQuestion",
	"askuserquestion": "AskUserQuestion",
	"enter_plan_mode": "EnterPlanMode",
	"exit_plan_mode":  "ExitPlanMode",

	// Subagent (canonical: Agent alias for subagent_run)
	"agent":        "Agent",
	"subagent_run": "Agent",
}

func toolInputMap(toolInput any) map[string]any {
	switch v := toolInput.(type) {
	case map[string]any:
		return v
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(v), &m) == nil && m != nil {
			return m
		}
	default:
		b, err := json.Marshal(v)
		if err == nil {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil && m != nil {
				return m
			}
		}
	}
	return nil
}

// MessageGuard checks the outbound message list before it is sent to the model.
// Returning a non-nil error blocks execution.
type MessageGuard func(ctx context.Context, messages []llm.Message) error

// ResultGuard checks the model result after the call completes successfully.
// Returning a non-nil error rejects the result and returns that error to the caller.
type ResultGuard func(ctx context.Context, result *llm.Result) error

// GuardrailError wraps a named guard failure.
type GuardrailError struct {
	// Stage identifies whether the failure happened on input or output.
	Stage string
	// Name is the guard name provided at middleware construction time.
	Name string
	// Err is the underlying guard error.
	Err error
}

// Error implements the error interface.
func (e *GuardrailError) Error() string {
	return fmt.Sprintf("middleware: %s guard %q failed: %v", e.Stage, e.Name, e.Err)
}

// Unwrap returns the underlying guard error.
func (e *GuardrailError) Unwrap() error {
	return e.Err
}

// GuardrailOption configures a Guardrails middleware.
type GuardrailOption func(*guardrailConfig)

type namedMessageGuard struct {
	name  string
	guard MessageGuard
}

type namedResultGuard struct {
	name  string
	guard ResultGuard
}

type guardrailConfig struct {
	messageGuards []namedMessageGuard
	resultGuards  []namedResultGuard
}

// WithMessageGuard adds a named input guard that inspects messages before the
// model call is executed.
func WithMessageGuard(name string, guard MessageGuard) GuardrailOption {
	return func(cfg *guardrailConfig) {
		cfg.messageGuards = append(cfg.messageGuards, namedMessageGuard{name: name, guard: guard})
	}
}

// WithResultGuard adds a named output guard that inspects the result after the
// model call completes successfully.
func WithResultGuard(name string, guard ResultGuard) GuardrailOption {
	return func(cfg *guardrailConfig) {
		cfg.resultGuards = append(cfg.resultGuards, namedResultGuard{name: name, guard: guard})
	}
}

// NewGuardrails returns an llm.LLMMiddleware that executes message guards
// before the model call and result guards after a successful response.
//
// Guards are executed in the order they are added. The first failing guard stops
// the request and its error is wrapped in GuardrailError.
//
//	mw := safety.NewGuardrails(
//		safety.WithMessageGuard("prompt-size", func(ctx context.Context, messages []llm.Message) error {
//			return nil
//		}),
//	)
func NewGuardrails(opts ...GuardrailOption) llm.LLMMiddleware {
	cfg := &guardrailConfig{}
	for _, o := range opts {
		o(cfg)
	}

	return func(next llm.LLM) llm.LLM {
		return &guardrailsLLM{inner: next, cfg: cfg}
	}
}

// guardrailsLLM is the concrete LLM produced by the Guardrails middleware.
type guardrailsLLM struct {
	inner llm.LLM
	cfg   *guardrailConfig
}

// Execute validates input messages, delegates to inner, and validates the result.
func (g *guardrailsLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	for _, guard := range g.cfg.messageGuards {
		if err := guard.guard(ctx, messages); err != nil {
			return nil, &GuardrailError{Stage: "input", Name: guard.name, Err: err}
		}
	}

	result, err := g.inner.Execute(ctx, messages, tools)
	if err != nil {
		return nil, err
	}

	for _, guard := range g.cfg.resultGuards {
		if err := guard.guard(ctx, result); err != nil {
			return nil, &GuardrailError{Stage: "output", Name: guard.name, Err: err}
		}
	}

	return result, nil
}

// ErrInputBlocked is returned when guardrails reject user input.
var ErrInputBlocked = errors.New("guardrails: input blocked")

// RunInputRules applies length and blocked-substring checks to user input.
// The input rail is always on; only its tunables are configurable.
func RunInputRules(r *Resolved, text string) error {
	if r == nil {
		return nil
	}
	if r.InputMaxRunes > 0 && utf8.RuneCountInString(text) > r.InputMaxRunes {
		incBlocked("input_max_runes")
		return fmt.Errorf("%w: message exceeds max length", ErrInputBlocked)
	}
	lt := strings.ToLower(text)
	for _, sub := range r.InputBlockSubs {
		s := strings.TrimSpace(sub)
		if s == "" {
			continue
		}
		if strings.Contains(lt, strings.ToLower(s)) {
			incBlocked("input_substring")
			return fmt.Errorf("%w: blocked pattern", ErrInputBlocked)
		}
	}
	return nil
}

// OutputBlockedError is returned when guardrails reject model output.
// The Reason field indicates which rule triggered the block.
type OutputBlockedError struct {
	Reason string // output_sys_leak | output_js_url | output_url_scheme
}

// Error implements the error interface.
func (e *OutputBlockedError) Error() string {
	return fmt.Sprintf("guardrails: output blocked: %s", e.Reason)
}

// Is allows errors.Is(err, &OutputBlockedError{}) to work as a sentinel check.
func (e *OutputBlockedError) Is(target error) bool {
	_, ok := target.(*OutputBlockedError)
	return ok
}

var (
	grSysLeakRe   = regexp.MustCompile(`(?i)(ignore\s+(all\s+)?previous\s+instructions|disregard\s+(the\s+)?(above|prior)|you\s+are\s+now\s+(in\s+)?developer\s+mode)`)
	grJSURLRe     = regexp.MustCompile(`(?i)javascript\s*:`)
	grOpaqueURLRe = regexp.MustCompile(`(?i)\b(?:mailto:[^\s<>\[\]()"']+|data:[^\s<>\[\]()"']+)`)
)

// ApplyOutputRail scans model output for system-leak phrases and unsafe URLs.
// The output rail is always on.
func ApplyOutputRail(r *Resolved, text string) (string, error) {
	if r == nil {
		return text, nil
	}
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	if grSysLeakRe.MatchString(text) {
		incBlocked("output_sys_leak")
		return "", &OutputBlockedError{Reason: "output_sys_leak"}
	}
	if grJSURLRe.MatchString(text) {
		incBlocked("output_js_url")
		return "", &OutputBlockedError{Reason: "output_js_url"}
	}
	for _, raw := range grExtractURLs(text) {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" {
			continue
		}
		s := strings.ToLower(u.Scheme)
		if s != "http" && s != "https" && s != "mailto" {
			incBlocked("output_url_scheme")
			return "", &OutputBlockedError{Reason: "output_url_scheme"}
		}
	}
	return text, nil
}

func grIsSchemeFirst(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func grIsSchemeChar(b byte) bool {
	return grIsSchemeFirst(b) || (b >= '0' && b <= '9') || b == '+' || b == '-' || b == '.'
}

func grTrimTrailingURLPunct(s string) string {
	for len(s) > 0 {
		switch s[len(s)-1] {
		case '.', ',', ';', ':', '!', '?', ')', ']', '}', '"', '\'':
			s = s[:len(s)-1]
		default:
			return s
		}
	}
	return s
}

func grExtractHierarchicalURLs(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		j := strings.Index(s[i:], "://")
		if j < 0 {
			break
		}
		colon := i + j
		start := colon
		for start > 0 && grIsSchemeChar(s[start-1]) {
			start--
		}
		if start >= colon || !grIsSchemeFirst(s[start]) {
			i = colon + 3
			continue
		}
		end := colon + 3
		for end < len(s) {
			c := s[end]
			if c <= ' ' || c == ')' || c == ']' || c == '}' || c == '"' || c == '\'' || c == '<' || c == '>' || c == ',' || c == ';' {
				break
			}
			end++
		}
		out = append(out, grTrimTrailingURLPunct(s[start:end]))
		i = colon + 3
	}
	return out
}

func grExtractURLs(s string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(raw string) {
		raw = strings.TrimSpace(grTrimTrailingURLPunct(raw))
		if raw == "" {
			return
		}
		if _, ok := seen[raw]; ok {
			return
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
	}
	for _, m := range grOpaqueURLRe.FindAllString(s, -1) {
		add(m)
	}
	for _, m := range grExtractHierarchicalURLs(s) {
		add(m)
	}
	return out
}

// OutputRailEnabled reports whether answers pass the output guardrail, which
// judges an answer whole: a surface that would otherwise stream it must hold
// it until it is complete.
func OutputRailEnabled(cfg *appcfg.Root) bool {
	r := ResolveFromRoot(cfg)
	return r != nil && r.OutputEnabled
}

// SanitizeOutbound applies the output rail to text destined for an external
// channel, returning a withheld placeholder when the rail blocks it.
// Returns text unchanged when the output rail is disabled via config.
func SanitizeOutbound(cfg *appcfg.Root, text string) string {
	r := ResolveFromRoot(cfg)
	if r == nil || !r.OutputEnabled {
		return text
	}
	out, err := ApplyOutputRail(r, text)
	if err != nil {
		return "[withheld: output guardrails]"
	}
	return out
}

var guardrailBlockedTotal int64

func incBlocked(string) {
	atomic.AddInt64(&guardrailBlockedTotal, 1)
}
