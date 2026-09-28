// Semantic shell replacements: grep, wc, ls, env, diff, paths, and log dedup.
package tool

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SemanticFilter is a Go-native compressor for one command family.
type SemanticFilter struct {
	// Name is the capability id reported in metadata (without the "go:" prefix).
	Name string
	// Version tracks behavior changes for retrieval bookkeeping.
	Version string
	// Match decides whether this filter handles the command line.
	Match func(cmd string) bool
	// Apply compresses the output. ok=false means passthrough.
	Apply func(output string) (out string, ok bool)
}

// semanticFilters is the ordered registry: first match wins.
var semanticFilters = []*SemanticFilter{
	{Name: "diff", Version: "1", Match: matchDiffCommand, Apply: CompactDiff},
	{Name: "wc", Version: "1", Match: matchWcCommand, Apply: FilterWc},
	{Name: "env", Version: "1", Match: matchEnvCommand, Apply: FilterEnv},
	{Name: "paths", Version: "1", Match: matchPathListCommand, Apply: applyPathListing},
	{Name: "grep", Version: "1", Match: matchGrepCommand, Apply: FilterGrep},
	{Name: "log-dedup", Version: "1", Match: matchLogCommand, Apply: FilterLogDedup},
}

// MatchSemantic returns the first semantic filter that handles cmd, or nil.
func MatchSemantic(cmd string) *SemanticFilter {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	for _, f := range semanticFilters {
		if f.Match != nil && f.Match(cmd) {
			return f
		}
	}
	return nil
}

// ApplySemantic runs the matching semantic filter, if any. It recovers from
// panics so a parser bug can never break a tool call.
func ApplySemantic(cmd, output string) (res Result, handled bool) {
	f := MatchSemantic(cmd)
	if f == nil || f.Apply == nil {
		return Result{}, false
	}
	defer func() {
		if r := recover(); r != nil {
			res, handled = Result{}, false
		}
	}()
	out, ok := f.Apply(output)
	if !ok || out == "" || len(out) >= len(output) {
		// No gain (or a degenerate result): let the TOML pipeline try.
		return Result{}, false
	}
	res = Result{
		Output:        out,
		Matched:       true,
		Applied:       true,
		FilterName:    f.Name,
		FilterVersion: f.Version,
		BeforeBytes:   len(output),
		AfterBytes:    len(out),
		Semantic:      true,
	}
	res.SavedTokens = EstimateTokens(res.BeforeBytes - res.AfterBytes)
	res.CompressedPct = 100 * (res.BeforeBytes - res.AfterBytes) / res.BeforeBytes
	return res, true
}

// --- command matching ---------------------------------------------------
//
// Matchers look at the leading utility of each pipeline segment. A command like
// `find . -name '*.go' | head -50` is still a find; a command whose *last*
// stage rewrites the output (sort -u, awk) is not, because our parse would be
// operating on text the user already reshaped.

var reDiffCmd = regexp.MustCompile(`(^|[|&;]\s*)(git\s+(-[^\s]+\s+)*(diff|show|log\s+-p)|diff\b|hg\s+diff|svn\s+diff)`)

func matchDiffCommand(cmd string) bool {
	if !reDiffCmd.MatchString(cmd) {
		return false
	}
	// --stat/--name-only already produce a summary; nothing structural to strip.
	return !strings.Contains(cmd, "--stat") &&
		!strings.Contains(cmd, "--name-only") &&
		!strings.Contains(cmd, "--name-status") &&
		!strings.Contains(cmd, "--numstat")
}

func matchWcCommand(cmd string) bool { return leadingUtilIs(cmd, "wc") }

func matchEnvCommand(cmd string) bool {
	seg := lastPipelineSegment(cmd)
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "env", "printenv":
		// `env VAR=x cmd` runs a command; only a bare listing is parseable.
		return len(fields) == 1
	case "export":
		return len(fields) == 1
	}
	return false
}

func matchPathListCommand(cmd string) bool {
	return leadingUtilIs(cmd, "find", "fd", "tree", "ls", "rg") && !hasGrepPattern(cmd)
}

// applyPathListing dispatches on the shape of the output rather than the flags,
// because `ls -l`, `ls -lR`, and a bare path list are three different formats and
// a command line can produce any of them. Trying the long-listing parser first
// and falling back keeps both paths available without parsing argv.
func applyPathListing(output string) (string, bool) {
	if out, ok := FilterLsLong(output); ok {
		return out, true
	}
	return FilterPathList(output)
}

func matchGrepCommand(cmd string) bool {
	return leadingUtilIs(cmd, "grep", "egrep", "fgrep", "ack") ||
		(leadingUtilIs(cmd, "rg") && hasGrepPattern(cmd))
}

func matchLogCommand(cmd string) bool {
	return leadingUtilIs(cmd, "journalctl", "dmesg") ||
		regexp.MustCompile(`\b(docker|kubectl|podman)\s+logs\b`).MatchString(cmd) ||
		regexp.MustCompile(`\b(tail|cat)\b[^|]*\.log\b`).MatchString(cmd)
}

// hasGrepPattern reports whether an rg invocation carries a search pattern
// (as opposed to `rg --files`, which is a path lister).
func hasGrepPattern(cmd string) bool {
	return strings.Contains(cmd, "--files-with-matches") ||
		strings.Contains(cmd, "-l ") ||
		(!strings.Contains(cmd, "--files") && leadingUtilIs(cmd, "rg"))
}

// leadingUtilIs reports whether the last pipeline segment's first word is one of
// names, ignoring env-var assignments and common wrappers.
func leadingUtilIs(cmd string, names ...string) bool {
	seg := lastPipelineSegment(cmd)
	fields := strings.Fields(seg)
	for len(fields) > 0 {
		word := fields[0]
		// Skip `VAR=value` prefixes and simple wrappers.
		if strings.Contains(word, "=") && !strings.HasPrefix(word, "-") {
			fields = fields[1:]
			continue
		}
		if word == "sudo" || word == "command" || word == "time" {
			fields = fields[1:]
			continue
		}
		break
	}
	if len(fields) == 0 {
		return false
	}
	base := fields[0]
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	for _, n := range names {
		if base == n {
			return true
		}
	}
	return false
}

// lastPipelineSegment returns the final stage of a pipeline, which is what
// actually produced the text we are compressing.
func lastPipelineSegment(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	// Split on | but not ||.
	parts := []string{}
	start := 0
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '|' {
			continue
		}
		if i+1 < len(cmd) && cmd[i+1] == '|' {
			i++
			continue
		}
		if i > 0 && cmd[i-1] == '|' {
			continue
		}
		parts = append(parts, cmd[start:i])
		start = i + 1
	}
	parts = append(parts, cmd[start:])
	last := strings.TrimSpace(parts[len(parts)-1])
	// A trailing pager/limiter still shows the upstream tool's format.
	for _, passthrough := range []string{"head", "tail", "cat", "more", "less", "tee", "sort", "uniq"} {
		if fields := strings.Fields(last); len(fields) > 0 && fields[0] == passthrough && len(parts) > 1 {
			return strings.TrimSpace(parts[len(parts)-2])
		}
	}
	return last
}

const (
	// grepMinLines is where the per-file header starts paying for itself.
	grepMinLines = 8
	// grepFileSampleLimit caps hits shown per file; the rest become a count.
	grepFileSampleLimit = 20
	// grepTotalHitLimit caps hit lines across all files. A repo-wide grep can
	// match tens of thousands of lines, which no amount of grouping makes small
	// enough to belong in context.
	grepTotalHitLimit = 300
)

// FilterGrep groups `file:line:text` hits under per-file headers. It returns
// ok=false unless the output is predominantly in that form.
func FilterGrep(output string) (string, bool) {
	lines := splitNonEmptyLines(output)
	if len(lines) < grepMinLines {
		return output, false
	}
	type hit struct {
		lineNo string
		text   string
	}
	order := []string{}
	byFile := map[string][]hit{}
	parsed := 0
	for _, line := range lines {
		file, rest, ok := strings.Cut(line, ":")
		if !ok || file == "" {
			return output, false
		}
		lineNo, text, ok := strings.Cut(rest, ":")
		if !ok || !isAllDigits(lineNo) {
			// No line numbers (grep without -n): nothing to group safely.
			return output, false
		}
		if _, seen := byFile[file]; !seen {
			order = append(order, file)
		}
		byFile[file] = append(byFile[file], hit{lineNo: lineNo, text: text})
		parsed++
	}
	if parsed != len(lines) || len(byFile) == 0 {
		return output, false
	}
	// One file per hit means no repetition to remove.
	if len(byFile) == parsed {
		return output, false
	}
	sort.Strings(order)

	// A repo-wide grep can match tens of thousands of lines. Grouping alone only
	// removes the repeated paths, so the result can still be far too large to put
	// in context. Past a global budget, show whole hits for the first files and
	// reduce the tail to per-file counts: the agent still learns which files match
	// and how often, and the untouched original is one retrieve_output away.
	globalBudget := grepTotalHitLimit
	if parsed <= grepTotalHitLimit {
		globalBudget = parsed
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d matches in %d files\n", parsed, len(byFile))
	shown, truncatedFiles, truncatedHits := 0, 0, 0
	for _, file := range order {
		hits := byFile[file]
		if shown >= globalBudget {
			truncatedFiles++
			truncatedHits += len(hits)
			continue
		}
		fmt.Fprintf(&sb, "%s (%d)\n", file, len(hits))
		limit := len(hits)
		if limit > grepFileSampleLimit {
			limit = grepFileSampleLimit
		}
		if remaining := globalBudget - shown; limit > remaining {
			limit = remaining
		}
		for _, h := range hits[:limit] {
			fmt.Fprintf(&sb, "  %s: %s\n", h.lineNo, strings.TrimSpace(h.text))
		}
		shown += limit
		if rest := len(hits) - limit; rest > 0 {
			fmt.Fprintf(&sb, "  … +%d more matches\n", rest)
		}
	}
	if truncatedFiles > 0 {
		fmt.Fprintf(&sb, "… +%d more files with %d matches (use retrieve_output for full results)\n",
			truncatedFiles, truncatedHits)
	}
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return output, false
	}
	return out, true
}

// FilterWc compresses `wc` output. It returns ok=false when the text does not
// look like a wc table.
func FilterWc(output string) (string, bool) {
	lines := splitNonEmptyLines(output)
	if len(lines) == 0 {
		return output, false
	}
	type row struct {
		counts []string
		path   string
	}
	rows := make([]row, 0, len(lines))
	width := -1
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return output, false
		}
		// Trailing non-numeric fields form the path; leading fields are counts.
		n := 0
		for n < len(fields) && isAllDigits(fields[n]) {
			n++
		}
		if n == 0 {
			return output, false // not a wc row
		}
		if width == -1 {
			width = n
		} else if width != n {
			return output, false // inconsistent shape; don't guess
		}
		rows = append(rows, row{counts: fields[:n], path: strings.Join(fields[n:], " ")})
	}

	// Single file, single metric: the number is the entire signal.
	if len(rows) == 1 && width == 1 {
		out := rows[0].counts[0]
		if len(out) >= len(strings.TrimSpace(output)) {
			return output, false
		}
		return out, true
	}

	// Separate the `total` row: it must not influence the common prefix.
	body := rows
	hasTotal := false
	var total row
	if last := rows[len(rows)-1]; last.path == "total" {
		hasTotal = true
		total = last
		body = rows[:len(rows)-1]
	}

	paths := make([]string, 0, len(body))
	for _, r := range body {
		paths = append(paths, r.path)
	}
	prefix := findWcCommonPrefix(paths)

	var sb strings.Builder
	for _, r := range body {
		sb.WriteString(strings.Join(r.counts, " "))
		if p := strings.TrimPrefix(r.path, prefix); p != "" {
			sb.WriteString(" ")
			sb.WriteString(p)
		}
		sb.WriteByte('\n')
	}
	if hasTotal {
		// Σ marks the aggregate without spending a word on it.
		fmt.Fprintf(&sb, "Σ %s\n", strings.Join(total.counts, " "))
	}
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return output, false
	}
	return out, true
}

// findWcCommonPrefix returns the longest shared directory prefix (including its
// trailing separator) across paths. Rows keep only the part that differs.
func findWcCommonPrefix(paths []string) string {
	if len(paths) < 2 {
		return ""
	}
	prefix := paths[0]
	for _, p := range paths[1:] {
		prefix = commonStringPrefix(prefix, p)
		if prefix == "" {
			return ""
		}
	}
	// Only cut on a directory boundary so filenames stay intact.
	if i := strings.LastIndexByte(prefix, '/'); i >= 0 {
		return prefix[:i+1]
	}
	return ""
}

func commonStringPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func splitNonEmptyLines(s string) []string {
	raw := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

const (
	// lsLongMinLines is where compaction beats its own header overhead.
	lsLongMinLines = 10
	// lsLongDirSampleLimit is how many entries one directory block shows before
	// the remainder becomes a count.
	lsLongDirSampleLimit = 12
	// lsLongTotalLimit caps emitted entry lines across all directory blocks.
	lsLongTotalLimit = 300
)

// lsEntry is one parsed row of a long listing.
type lsEntry struct {
	name  string
	size  int64
	isDir bool
}

// FilterLsLong compacts `ls -l` style output. It returns ok=false unless the
// input really is a long listing, so `ls` without -l falls through to the plain
// path-list filter instead.
func FilterLsLong(output string) (string, bool) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) < lsLongMinLines {
		return output, false
	}

	type block struct {
		header  string
		entries []lsEntry
	}
	var blocks []*block
	current := &block{}
	rows, other := 0, 0

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		if line == "" {
			continue
		}
		// `ls -lR` emits "path:" before each directory block.
		if strings.HasSuffix(line, ":") && !strings.ContainsAny(line, " \t") {
			if current.header != "" || len(current.entries) > 0 {
				blocks = append(blocks, current)
			}
			current = &block{header: strings.TrimSuffix(line, ":")}
			continue
		}
		// "total 1234" precedes each block's rows and carries no information the
		// per-block summary does not.
		if strings.HasPrefix(line, "total ") {
			continue
		}
		entry, ok := parseLsLongRow(line)
		if !ok {
			other++
			// A few stray lines are tolerable; mostly-unparsed input is not a
			// long listing and must pass through.
			if other > 3 {
				return output, false
			}
			continue
		}
		current.entries = append(current.entries, entry)
		rows++
	}
	if current.header != "" || len(current.entries) > 0 {
		blocks = append(blocks, current)
	}
	if rows < lsLongMinLines {
		return output, false
	}

	var sb strings.Builder
	shown, skippedDirs, skippedEntries := 0, 0, 0
	for _, b := range blocks {
		dirs, files := 0, 0
		var total int64
		for _, e := range b.entries {
			if e.isDir {
				dirs++
			} else {
				files++
				total += e.size
			}
		}
		header := b.header
		if header == "" {
			header = "."
		}
		if shown >= lsLongTotalLimit {
			skippedDirs++
			skippedEntries += len(b.entries)
			continue
		}
		fmt.Fprintf(&sb, "%s/ (%d dirs, %d files, %s)\n", header, dirs, files, humanBytes(total))

		limit := len(b.entries)
		if limit > lsLongDirSampleLimit {
			limit = lsLongDirSampleLimit
		}
		if remaining := lsLongTotalLimit - shown; limit > remaining {
			limit = remaining
		}
		for _, e := range b.entries[:limit] {
			if e.isDir {
				fmt.Fprintf(&sb, "  %s/\n", e.name)
			} else {
				fmt.Fprintf(&sb, "  %s  %s\n", e.name, humanBytes(e.size))
			}
		}
		shown += limit
		if rest := len(b.entries) - limit; rest > 0 {
			fmt.Fprintf(&sb, "  … +%d more entries\n", rest)
		}
	}
	if skippedDirs > 0 {
		fmt.Fprintf(&sb, "… +%d more directories with %d entries (use retrieve_output for the full listing)\n",
			skippedDirs, skippedEntries)
	}

	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return output, false
	}
	return out, true
}

// parseLsLongRow parses one `ls -l` row. The layout is
// mode links owner group size <date fields...> name, where the date occupies
// three fields on both BSD and GNU ls.
func parseLsLongRow(line string) (lsEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 9 {
		return lsEntry{}, false
	}
	mode := fields[0]
	// A mode string is 10-11 chars: type char plus 9 permission bits, optionally
	// a trailing ACL/xattr marker.
	if len(mode) < 10 || len(mode) > 11 {
		return lsEntry{}, false
	}
	switch mode[0] {
	case '-', 'd', 'l', 'c', 'b', 'p', 's':
	default:
		return lsEntry{}, false
	}
	for _, c := range mode[1:10] {
		switch c {
		case 'r', 'w', 'x', '-', 's', 'S', 't', 'T', 'l':
		default:
			return lsEntry{}, false
		}
	}
	if !isAllDigits(fields[1]) {
		return lsEntry{}, false
	}
	size, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return lsEntry{}, false
	}
	// The name is everything after the three date fields, so names containing
	// spaces survive intact.
	name := strings.Join(fields[8:], " ")
	if name == "" {
		return lsEntry{}, false
	}
	// A symlink's "a -> b" is worth keeping whole.
	return lsEntry{name: name, size: size, isDir: mode[0] == 'd'}, true
}

// humanBytes renders a byte count compactly, since exact sizes rarely drive a
// decision but orders of magnitude do.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fK", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1fG", float64(n)/(1024*1024*1024))
	}
}

const (
	// envValueMaxLen is where boost cuts a long value (verified: values of 970
	// and 65 chars both truncate at exactly 50).
	envValueMaxLen = 50
	// envMaskThreshold is the shortest value that still shows edge characters;
	// below it the whole value is masked.
	envMaskThreshold = 8
)

// envCategoryOrder fixes the print order so output is deterministic.
var envCategoryOrder = []string{"Path", "Cloud", "Tool", "Interesting", "Other"}

// FilterEnv compresses `env`/`printenv` output. It returns ok=false when the
// text is not a KEY=VALUE listing.
func FilterEnv(output string) (string, bool) {
	lines := splitNonEmptyLines(output)
	if len(lines) == 0 {
		return output, false
	}
	groups := map[string][]string{}
	count := 0
	for _, line := range lines {
		name, value, found := strings.Cut(line, "=")
		if !found || name == "" || strings.ContainsAny(name, " \t") {
			// Multi-line values (functions, embedded newlines) make the whole
			// dump unparseable; fail open rather than mangle it.
			return output, false
		}
		count++
		rendered := value
		if isSensitiveEnvVar(name) {
			rendered = maskEnvValue(value)
		} else {
			rendered = truncateEnvValue(value)
		}
		cat := categorizeEnvVar(name)
		groups[cat] = append(groups[cat], "  "+name+"="+rendered)
	}
	if count == 0 {
		return output, false
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "env: %d variables\n", count)
	for _, cat := range envCategoryOrder {
		entries := groups[cat]
		if len(entries) == 0 {
			continue
		}
		sort.Strings(entries)
		fmt.Fprintf(&sb, "\n%s (%d):\n", cat, len(entries))
		for _, e := range entries {
			sb.WriteString(e)
			sb.WriteByte('\n')
		}
	}
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return output, false
	}
	return out, true
}

// categorizeEnvVar buckets a variable name. Path-like and cloud/tool credentials
// get their own groups so a reader can skip straight to what matters.
func categorizeEnvVar(name string) string {
	upper := strings.ToUpper(name)
	switch upper {
	case "PATH", "MANPATH", "INFOPATH", "LD_LIBRARY_PATH", "PYTHONPATH",
		"GOPATH", "GOROOT", "CLASSPATH", "NODE_PATH", "PKG_CONFIG_PATH",
		"DYLD_LIBRARY_PATH", "DYLD_FALLBACK_LIBRARY_PATH":
		return "Path"
	case "HOME", "USER", "LOGNAME", "SHELL", "TERM", "LANG", "LC_ALL", "PWD",
		"TZ", "EDITOR", "VISUAL", "HOSTNAME":
		return "Interesting"
	}
	for _, p := range []string{"AWS_", "AZURE_", "GOOGLE_", "GCP_", "GCLOUD_", "ALIYUN_", "OSS_"} {
		if strings.HasPrefix(upper, p) {
			return "Cloud"
		}
	}
	for _, p := range []string{
		"GIT", "GITHUB_", "GITLAB_", "DOCKER_", "KUBE", "NPM_", "YARN_", "PNPM_",
		"CARGO_", "RUST", "JAVA_", "MAVEN_", "GRADLE_", "PYTHON", "PIP_", "CONDA_",
		"NODE_", "GO111", "GOFLAGS", "GOPROXY", "TERRAFORM_", "TF_", "ANSIBLE_",
	} {
		if strings.HasPrefix(upper, p) {
			return "Tool"
		}
	}
	if strings.HasSuffix(upper, "_HOME") || strings.HasSuffix(upper, "_ROOT") {
		return "Interesting"
	}
	return "Other"
}

// isSensitiveEnvVar reports whether a name suggests the value is a credential.
// It errs toward masking: a masked non-secret costs nothing, a leaked secret is
// unrecoverable.
func isSensitiveEnvVar(name string) bool {
	upper := strings.ToUpper(name)
	for _, needle := range []string{
		"SECRET", "TOKEN", "PASSWORD", "PASSWD", "APIKEY", "API_KEY", "KEY",
		"CREDENTIAL", "AUTH", "PRIVATE", "SESSION", "COOKIE", "SIGNATURE",
		"CERT", "SALT", "CIPHER", "PASSPHRASE", "ACCESS_ID", "DSN",
	} {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}

// maskEnvValue keeps two characters at each end so a reader can tell which
// credential is set without learning its value.
func maskEnvValue(value string) string {
	if value == "" {
		return ""
	}
	if len(value) < envMaskThreshold {
		return "****"
	}
	return value[:2] + "****" + value[len(value)-2:]
}

// truncateEnvValue cuts an over-long value and states the original length, so
// the reader knows something was elided and by how much.
func truncateEnvValue(value string) string {
	if len(value) <= envValueMaxLen {
		return value
	}
	return value[:envValueMaxLen] + fmt.Sprintf("… (%d chars)", len(value))
}

// CompactDiff removes context lines from a unified diff and appends an added/
// removed tally. It returns ok=false for text that is not a unified diff.
func CompactDiff(output string) (string, bool) {
	if output == "" {
		return output, false
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	var (
		kept       []string
		added      int
		removed    int
		hunks      int
		contextHit bool
		pendingOld string
	)
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--- "):
			// Hold it: the following +++ carries the authoritative path.
			pendingOld = line
		case strings.HasPrefix(line, "+++ "):
			// Emit the new path under a `---` marker, dropping the old line.
			kept = append(kept, "--- "+strings.TrimPrefix(line, "+++ "))
			pendingOld = ""
		case strings.HasPrefix(line, "@@"):
			hunks++
			kept = append(kept, line)
		case line == `\ No newline at end of file`:
			// Structural note with no content value.
		case strings.HasPrefix(line, "+"):
			added++
			kept = append(kept, line)
		case strings.HasPrefix(line, "-"):
			removed++
			kept = append(kept, line)
		case line == "" || strings.HasPrefix(line, " "):
			// Context (or blank context): the compressible bulk.
			contextHit = true
		default:
			// Metadata git emits around hunks: diff --git, index, mode, similarity.
			// Keep rename/mode facts, drop blob hashes which are unusable here.
			if isKeepableDiffMeta(line) {
				kept = append(kept, line)
			}
			if pendingOld != "" {
				pendingOld = ""
			}
		}
	}
	// Not a diff: no hunk headers means we have no idea what we parsed.
	if hunks == 0 || (added == 0 && removed == 0) {
		return output, false
	}
	// Nothing was actually redundant.
	if !contextHit {
		return output, false
	}
	kept = append(kept, fmt.Sprintf("  +%d -%d", added, removed))
	return strings.Join(kept, "\n"), true
}

// isKeepableDiffMeta keeps the git metadata lines that carry information a
// model cannot reconstruct from the working tree, and drops the gateway.
func isKeepableDiffMeta(line string) bool {
	for _, prefix := range []string{
		"diff --git ",
		"rename from ",
		"rename to ",
		"copy from ",
		"copy to ",
		"new file mode ",
		"deleted file mode ",
		"old mode ",
		"new mode ",
		"Binary files ",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

const (
	// pathListMinLines is the point where folding beats the tree's own overhead.
	pathListMinLines = 12
	// pathDirSampleLimit is how many entries a large directory shows before the
	// rest become a count.
	pathDirSampleLimit = 8
	// pathTotalLimit caps total emitted entry lines; beyond it directories are
	// summarized rather than listed.
	pathTotalLimit = 400
	// pathTruncatedDirLimit caps count-only directory headers. A dependency tree
	// can hold tens of thousands of directories, where even one line each would
	// swamp the summary.
	pathTruncatedDirLimit = 200
)

// FilterPathList folds a newline-separated list of paths into a directory tree.
// It returns ok=false unless the input really is a path listing with meaningful
// shared structure.
func FilterPathList(output string) (string, bool) {
	lines := splitNonEmptyLines(output)
	if len(lines) < pathListMinLines {
		return output, false
	}
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		p := strings.TrimSpace(line)
		// `ls -l` rows, tree art, and grep hits are not bare paths.
		if p == "" || strings.ContainsAny(p, "\t") {
			return output, false
		}
		if !strings.Contains(p, "/") {
			// Tolerate a few bare names (find's own root, ls output) but a
			// listing with no directories has nothing to fold.
			paths = append(paths, p)
			continue
		}
		paths = append(paths, p)
	}
	// Require real nesting: at least half the entries must live in a directory.
	nested := 0
	for _, p := range paths {
		if strings.Contains(strings.TrimPrefix(p, "./"), "/") {
			nested++
		}
	}
	if nested*2 < len(paths) {
		return output, false
	}

	groups := map[string][]string{}
	for _, p := range paths {
		clean := strings.TrimPrefix(p, "./")
		dir, base := path.Split(clean)
		if base == "" {
			// A trailing slash means the entry is itself a directory.
			dir, base = path.Split(strings.TrimSuffix(clean, "/"))
			base += "/"
		}
		groups[dir] = append(groups[dir], base)
	}
	if len(groups) < 2 {
		return output, false
	}

	dirs := make([]string, 0, len(groups))
	for d := range groups {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d paths in %d directories\n", len(paths), len(groups))
	emitted := 0
	truncatedDirs := 0
	collapsedDirs, collapsedEntries := 0, 0
	for _, dir := range dirs {
		entries := groups[dir]
		sort.Strings(entries)
		label := dir
		if label == "" {
			label = "./"
		}
		if emitted >= pathTotalLimit {
			// Budget spent. A tree like node_modules has tens of thousands of
			// directories, so even one header line each would dominate the
			// output; past a cap on headers too, collapse the rest into a single
			// tally and leave the original to retrieve_output.
			if truncatedDirs < pathTruncatedDirLimit {
				fmt.Fprintf(&sb, "%s (%d)\n", label, len(entries))
			} else {
				collapsedDirs++
				collapsedEntries += len(entries)
			}
			truncatedDirs++
			continue
		}
		fmt.Fprintf(&sb, "%s (%d)\n", label, len(entries))
		limit := len(entries)
		if limit > pathDirSampleLimit {
			limit = pathDirSampleLimit
		}
		for _, e := range entries[:limit] {
			sb.WriteString("  ")
			sb.WriteString(e)
			sb.WriteByte('\n')
			emitted++
		}
		if rest := len(entries) - limit; rest > 0 {
			fmt.Fprintf(&sb, "  … +%d more\n", rest)
		}
	}
	if truncatedDirs > collapsedDirs {
		fmt.Fprintf(&sb, "… %d more directories listed by count only\n", truncatedDirs-collapsedDirs)
	}
	if collapsedDirs > 0 {
		fmt.Fprintf(&sb, "… +%d further directories with %d paths (use retrieve_output for the full listing)\n",
			collapsedDirs, collapsedEntries)
	}
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return output, false
	}
	return out, true
}

const (
	// logDedupMinLines avoids reshaping short logs where every line is read.
	logDedupMinLines = 20
	// logDedupMinRepeat is the run length that justifies a collapse note.
	logDedupMinRepeat = 2
)

var (
	// Order matters: timestamps first, then ids, then bare numbers, so a
	// timestamp is not shredded into separate number placeholders.
	reLogTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	reLogClock     = regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(?:[.,]\d+)?\b`)
	reLogUUID      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reLogHex       = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b|\b[0-9a-fA-F]{12,}\b`)
	reLogQuoted    = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	reLogNumber    = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
)

// FilterLogDedup collapses runs of structurally identical log lines. It returns
// ok=false when there is not enough repetition to matter.
func FilterLogDedup(output string) (string, bool) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) < logDedupMinLines {
		return output, false
	}
	var (
		sb        strings.Builder
		prevNorm  string
		runCount  int
		collapsed int
		started   bool
	)
	flush := func() {
		if runCount > logDedupMinRepeat-1 && runCount > 1 {
			// The first line of the run was already written verbatim.
			fmt.Fprintf(&sb, "  [x%d similar]\n", runCount)
			collapsed += runCount - 1
		}
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		norm := normalizeLogLine(line)
		if started && norm == prevNorm {
			runCount++
			continue
		}
		flush()
		sb.WriteString(line)
		sb.WriteByte('\n')
		prevNorm = norm
		runCount = 1
		started = true
	}
	flush()
	if collapsed == 0 {
		return output, false
	}
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return output, false
	}
	return out, true
}

// normalizeLogLine replaces the parts of a log line that vary between otherwise
// identical events, so repetition becomes detectable.
func normalizeLogLine(line string) string {
	s := StripANSI(line)
	s = reLogTimestamp.ReplaceAllString(s, "<ts>")
	s = reLogClock.ReplaceAllString(s, "<ts>")
	s = reLogUUID.ReplaceAllString(s, "<uuid>")
	s = reLogHex.ReplaceAllString(s, "<hex>")
	s = reLogQuoted.ReplaceAllString(s, "<str>")
	s = reLogNumber.ReplaceAllString(s, "<n>")
	return strings.Join(strings.Fields(s), " ")
}
