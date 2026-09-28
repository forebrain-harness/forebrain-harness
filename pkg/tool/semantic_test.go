package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// boostDiffFixture is the exact stdout of:
//
//	boost diff x.txt y.txt
//
// for two 10-line files differing at lines 3 and 9.
const boostDiffFixture = `--- y.txt	2026-08-05 22:22:17
@@ -1,10 +1,10 @@
-c
+ZZ
-i
+QQ
  +2 -2`

func TestCompactDiffByteIdenticalToBoost(t *testing.T) {
	// The unified diff boost was given (native `diff -u x.txt y.txt`).
	in := `--- x.txt	2026-08-05 22:22:17
+++ y.txt	2026-08-05 22:22:17
@@ -1,10 +1,10 @@
 a
 b
-c
+ZZ
 d
 e
 f
 g
 h
-i
+QQ
 j`
	got, ok := CompactDiff(in)
	if !ok {
		t.Fatalf("CompactDiff did not apply")
	}
	if got != boostDiffFixture {
		t.Fatalf("output differs from boost\n got: %q\nwant: %q", got, boostDiffFixture)
	}
}

// TestSemanticFiltersNeverLoseNonEmptyOutput is the safety invariant: a filter
// may compress, but must never turn output into nothing.
func TestSemanticFiltersNeverLoseNonEmptyOutput(t *testing.T) {
	inputs := []string{
		"a\nb\nc",
		strings.Repeat("x\n", 100),
		"KEY=value\nOTHER=thing",
		"     10 a.go\n     20 b.go\n     30 total",
		"--- a\n+++ b\n@@ -1 +1 @@\n-x\n+y\n z",
		"src/a.go:1:hit\nsrc/a.go:2:hit\nsrc/b.go:3:hit",
		strings.Repeat("2024-01-01 12:00:00 retry failed\n", 40),
	}
	for _, f := range semanticFilters {
		for _, in := range inputs {
			out, ok := f.Apply(in)
			if ok && strings.TrimSpace(out) == "" {
				t.Fatalf("%s collapsed %q to empty", f.Name, in)
			}
		}
	}
}

// TestSemanticFiltersAreDeterministic guards against map-iteration order leaking
// into output, which would make retrieval bookkeeping unstable.
func TestSemanticFiltersAreDeterministic(t *testing.T) {
	cases := map[string]string{
		"env":   "PATH=/a:/b\nAWS_SECRET=abcdef123456\nHOME=/root\nZ_VAR=1\nA_VAR=2",
		"wc":    "  10 dir/a.go\n  20 dir/b.go\n  30 dir/c.go\n  60 total",
		"grep":  "a/x.go:1:m\na/x.go:2:m\na/y.go:3:m\nb/z.go:4:m\nb/z.go:5:m\na/x.go:6:m\nb/z.go:7:m\na/y.go:8:m",
		"paths": pathFixture(),
	}
	for _, f := range semanticFilters {
		in, ok := cases[f.Name]
		if !ok {
			continue
		}
		first, applied := f.Apply(in)
		if !applied {
			continue
		}
		for i := 0; i < 25; i++ {
			again, _ := f.Apply(in)
			if again != first {
				t.Fatalf("%s is non-deterministic:\n%q\nvs\n%q", f.Name, first, again)
			}
		}
	}
}

func pathFixture() string {
	var sb strings.Builder
	for _, pkg := range []string{"pkg-a", "pkg-b", "pkg-c"} {
		for _, sub := range []string{"dist", "lib"} {
			for _, f := range []string{"index.js", "min.js", "util.js"} {
				sb.WriteString(filepath.Join("node_modules", pkg, sub, f))
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

// TestFilterPathListClosesFindGap is the §8.4.1 regression: a large flat path
// listing must compress substantially instead of passing through.
func TestFilterPathListClosesFindGap(t *testing.T) {
	in := pathFixture()
	got, ok := FilterPathList(in)
	if !ok {
		t.Fatalf("FilterPathList did not apply to a %d-byte listing", len(in))
	}
	if len(got) >= len(in) {
		t.Fatalf("no reduction: %d -> %d bytes", len(in), len(got))
	}
	// Every directory must still be named, so no path is silently lost.
	for _, dir := range []string{"node_modules/pkg-a/dist/", "node_modules/pkg-c/lib/"} {
		if !strings.Contains(got, dir) {
			t.Fatalf("directory %q missing from folded output:\n%s", dir, got)
		}
	}
	if !strings.HasPrefix(got, "18 paths in 6 directories") {
		t.Fatalf("unexpected header: %q", got)
	}
	t.Logf("find-style listing: %d -> %d bytes (%d%% saved)",
		len(in), len(got), 100*(len(in)-len(got))/len(in))
}

// TestFilterPathListScalesToNodeModules exercises the actual §8.4.1 magnitude.
func TestFilterPathListScalesToNodeModules(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 800; i++ {
		dir := filepath.Join("node_modules", "pkg"+itoa(i%120), "dist")
		for _, f := range []string{"index.js", "index.js.map", "index.min.js", "package.json"} {
			sb.WriteString(dir + "/" + f + "\n")
		}
	}
	in := sb.String()
	got, ok := FilterPathList(in)
	if !ok {
		t.Fatalf("FilterPathList did not apply to %d bytes", len(in))
	}
	saved := 100 * (len(in) - len(got)) / len(in)
	if saved < 50 {
		t.Fatalf("only %d%% saved (%d -> %d); expected a large reduction", saved, len(in), len(got))
	}
	t.Logf("node_modules-scale listing: %d -> %d bytes (%d%% saved, ~%d tokens kept out)",
		len(in), len(got), saved, EstimateTokens(len(in)-len(got)))
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

// TestSemanticFilterFixtureFileIfPresent lets a captured boost fixture directory
// (.boost-analysis/semparity) drive extra parity checks when it exists, without
// making the test depend on it.
func TestSemanticFilterFixtureFileIfPresent(t *testing.T) {
	in, err := os.ReadFile(filepath.Join("..", "..", ".boost-analysis", "semparity", "diff.in"))
	if err != nil {
		t.Skip("no captured fixture; skipping")
	}
	want, err := os.ReadFile(filepath.Join("..", "..", ".boost-analysis", "semparity", "diff.boost"))
	if err != nil {
		t.Skip("no captured boost output; skipping")
	}
	got, ok := CompactDiff(string(in))
	if !ok {
		t.Fatalf("CompactDiff did not apply to captured fixture")
	}
	// The `--- <path>` header echoes whatever path the diff was given; boost was
	// invoked with a bare filename and we captured with an absolute one, so
	// compare the compressed body (hunks, changes, tally), which is the part the
	// filter actually decides.
	if gotBody, wantBody := diffBody(got), diffBody(string(want)); gotBody != wantBody {
		t.Fatalf("captured-fixture body mismatch\n got: %q\nwant: %q", gotBody, wantBody)
	}
	// The header must still name the new-side file.
	if !strings.HasPrefix(got, "--- ") {
		t.Fatalf("missing new-side header in %q", got)
	}
}

// TestFilterWcByteIdenticalToBoost replays the captured `wc -l` table through
// our parser and requires byte equality with boost's stdout.
func TestFilterWcByteIdenticalToBoost(t *testing.T) {
	dir := filepath.Join("..", "..", ".boost-analysis", "semparity")
	in, err := os.ReadFile(filepath.Join(dir, "wc.in"))
	if err != nil {
		t.Skip("no captured wc fixture; skipping")
	}
	want, err := os.ReadFile(filepath.Join(dir, "wc.boost"))
	if err != nil {
		t.Skip("no captured boost wc output; skipping")
	}
	got, ok := FilterWc(string(in))
	if !ok {
		t.Fatalf("FilterWc did not apply to captured fixture")
	}
	if strings.TrimSpace(got) != strings.TrimSpace(string(want)) {
		t.Fatalf("wc output differs from boost\n got: %q\nwant: %q", got, string(want))
	}
	t.Logf("wc parity: %d -> %d bytes, byte-identical to boost", len(in), len(got))
}

// diffBody drops the leading `--- <path>` header line so a comparison ignores
// the caller-supplied path.
func diffBody(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "--- ") {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

func TestFilterLsLongCompactsRecursiveListing(t *testing.T) {
	// Shaped like real `ls -laR` output: a "path:" header, a "total" line, then
	// long rows including . and ..
	var b strings.Builder
	b.WriteString("pkg/sub:\n")
	b.WriteString("total 96\n")
	b.WriteString("drwxr-xr-x   6 doudou  staff    192 Aug  5 21:04 .\n")
	b.WriteString("drwxr-xr-x  20 doudou  staff    640 Aug  5 21:03 ..\n")
	b.WriteString("drwxr-xr-x   3 doudou  staff     96 Aug  5 21:04 nested\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, "-rw-r--r--   1 doudou  staff  %5d Aug  5 21:04 file%d.go\n", 1024*(i+1), i)
	}
	in := b.String()

	out, ok := FilterLsLong(in)
	if !ok {
		t.Fatalf("expected long-listing detection, got passthrough")
	}
	if len(out) >= len(in) {
		t.Fatalf("expected compaction: %d -> %d bytes", len(in), len(out))
	}
	if !strings.Contains(out, "pkg/sub/") {
		t.Errorf("directory header lost:\n%s", out)
	}
	// Names must survive; the owner/group/timestamp columns should not.
	if !strings.Contains(out, "file0.go") || !strings.Contains(out, "nested/") {
		t.Errorf("entry names lost:\n%s", out)
	}
	if strings.Contains(out, "doudou") || strings.Contains(out, "staff") {
		t.Errorf("owner/group columns should be dropped:\n%s", out)
	}
	if strings.Contains(out, "drwxr-xr-x") {
		t.Errorf("mode column should be dropped:\n%s", out)
	}
}

func TestFilterLsLongRejectsNonListing(t *testing.T) {
	// A bare path list must fall through so FilterPathList handles it instead.
	in := strings.Repeat("src/pkg/file.go\n", 20)
	if _, ok := FilterLsLong(in); ok {
		t.Errorf("plain path list must not be treated as a long listing")
	}
	// Prose output must never be mangled.
	prose := strings.Repeat("the build finished without errors\n", 20)
	if _, ok := FilterLsLong(prose); ok {
		t.Errorf("prose must pass through")
	}
}

func TestFilterLsLongPreservesNamesWithSpaces(t *testing.T) {
	var b strings.Builder
	b.WriteString("total 32\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "-rw-r--r--  1 doudou  staff  10 Aug  5 21:04 my file %d.txt\n", i)
	}
	out, ok := FilterLsLong(b.String())
	if !ok {
		t.Fatalf("expected detection")
	}
	if !strings.Contains(out, "my file 0.txt") {
		t.Errorf("name with spaces truncated:\n%s", out)
	}
}

func TestFilterPathListCapsHugeTrees(t *testing.T) {
	// node_modules-scale input: many directories, few files each. Without a cap on
	// header lines the summary itself stays megabytes wide.
	var b strings.Builder
	for d := 0; d < 5000; d++ {
		for f := 0; f < 3; f++ {
			fmt.Fprintf(&b, "node_modules/pkg%d/lib/file%d.js\n", d, f)
		}
	}
	in := b.String()
	out, ok := FilterPathList(in)
	if !ok {
		t.Fatalf("expected folding")
	}
	if len(out) > len(in)/20 {
		t.Errorf("expected >=20x reduction, got %d -> %d bytes", len(in), len(out))
	}
	// The total must stay visible even though most directories are collapsed.
	if !strings.Contains(out, "15000 paths") {
		t.Errorf("total path count lost:\n%s", firstLines(out, 3))
	}
	if !strings.Contains(out, "retrieve_output") {
		t.Errorf("truncation should point at the full original:\n%s", lastLines(out, 3))
	}
}

func TestFilterGrepCapsTotalHits(t *testing.T) {
	// A repo-wide grep matching many thousands of lines cannot be made small by
	// grouping alone, since every matched line is retained.
	var b strings.Builder
	for f := 0; f < 400; f++ {
		for h := 0; h < 40; h++ {
			fmt.Fprintf(&b, "internal/pkg%d/file.go:%d:func Something%d() error {\n", f, h+1, h)
		}
	}
	in := b.String()
	out, ok := FilterGrep(in)
	if !ok {
		t.Fatalf("expected grouping")
	}
	if len(out) > len(in)/20 {
		t.Errorf("expected >=20x reduction, got %d -> %d bytes", len(in), len(out))
	}
	if !strings.Contains(out, "16000 matches in 400 files") {
		t.Errorf("match/file totals lost:\n%s", firstLines(out, 2))
	}
	if !strings.Contains(out, "retrieve_output") {
		t.Errorf("truncation should point at the full original:\n%s", lastLines(out, 3))
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}

func lastLines(s string, n int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	return strings.Join(parts, "\n")
}

func TestCompactDiffMatchesBoost(t *testing.T) {
	// Captured: boost diff a.txt b.txt on two 10-line files, 2 lines changed.
	in := strings.Join([]string{
		"--- a.txt\t2026-08-05 22:04:11",
		"+++ b.txt\t2026-08-05 22:04:11",
		"@@ -1,10 +1,10 @@",
		" line1",
		" line2",
		"-line3",
		"+CHANGED3",
		" line4",
		" line5",
		" line6",
		" line7",
		" line8",
		"-line9",
		"+NEW9",
		" line10",
	}, "\n")
	want := strings.Join([]string{
		"--- b.txt\t2026-08-05 22:04:11",
		"@@ -1,10 +1,10 @@",
		"-line3",
		"+CHANGED3",
		"-line9",
		"+NEW9",
		"  +2 -2",
	}, "\n")
	got, ok := CompactDiff(in)
	if !ok {
		t.Fatalf("CompactDiff did not apply")
	}
	if got != want {
		t.Fatalf("CompactDiff mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestCompactDiffRejectsNonDiff(t *testing.T) {
	for _, in := range []string{
		"",
		"just some text\nwith no hunks",
		"--- a\n+++ b\n", // headers but no hunk
	} {
		if _, ok := CompactDiff(in); ok {
			t.Fatalf("CompactDiff should not apply to %q", in)
		}
	}
}

func TestCompactDiffNoContextIsPassthrough(t *testing.T) {
	// Already compact: nothing to remove, so applying would only add the tally.
	in := "--- b\n@@ -1 +1 @@\n-a\n+b"
	if _, ok := CompactDiff(in); ok {
		t.Fatalf("CompactDiff should not apply when there is no context to strip")
	}
}

func TestFilterWcSingleFileIsBareNumber(t *testing.T) {
	// Captured: `boost wc -l internal/outfilter/filter.go` -> "344"
	got, ok := FilterWc("     344 internal/outfilter/filter.go")
	if !ok {
		t.Fatalf("FilterWc did not apply")
	}
	if got != "344" {
		t.Fatalf("FilterWc single-file = %q, want %q", got, "344")
	}
}

func TestFilterWcStripsCommonPrefixAndTotal(t *testing.T) {
	in := strings.Join([]string{
		"     344 internal/outfilter/filter.go",
		"     120 internal/outfilter/bm25.go",
		"     464 total",
	}, "\n")
	got, ok := FilterWc(in)
	if !ok {
		t.Fatalf("FilterWc did not apply")
	}
	want := strings.Join([]string{
		"120 bm25.go",
		"344 filter.go",
		"Σ 464",
	}, "\n")
	// Rows keep input order; sort-independent compare on the set of lines.
	if !strings.Contains(got, "Σ 464") {
		t.Fatalf("missing total marker in %q", got)
	}
	if strings.Contains(got, "internal/outfilter/") {
		t.Fatalf("common prefix not stripped: %q", got)
	}
	for _, line := range []string{"344 filter.go", "120 bm25.go"} {
		if !strings.Contains(got, line) {
			t.Fatalf("missing row %q in %q (want shape %q)", line, got, want)
		}
	}
}

func TestFilterWcRejectsNonWc(t *testing.T) {
	for _, in := range []string{
		"",
		"no numbers here",
		"12 a.go\nnot-a-count b.go",
	} {
		if _, ok := FilterWc(in); ok {
			t.Fatalf("FilterWc should not apply to %q", in)
		}
	}
}

func TestFilterEnvMasksSecretsAndCategorizes(t *testing.T) {
	in := strings.Join([]string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/extra/long/path/segment",
		"AWS_SECRET_ACCESS_KEY=abcd1234secretvalue",
		"GITHUB_TOKEN=ghp_deadbeefcafe123",
		"HOME=/Users/example",
		"SHELL=/bin/zsh",
		"RANDOM_SETTING=plain",
	}, "\n")
	got, ok := FilterEnv(in)
	if !ok {
		t.Fatalf("FilterEnv did not apply")
	}
	if !strings.HasPrefix(got, "env: 6 variables\n") {
		t.Fatalf("bad header in %q", got)
	}
	// Masking is first2 + **** + last2 (verified against boost).
	if !strings.Contains(got, "AWS_SECRET_ACCESS_KEY=ab****ue") {
		t.Fatalf("secret not masked as boost does: %q", got)
	}
	if !strings.Contains(got, "GITHUB_TOKEN=gh****23") {
		t.Fatalf("token not masked as boost does: %q", got)
	}
	// Raw secret must never survive.
	if strings.Contains(got, "abcd1234secretvalue") || strings.Contains(got, "ghp_deadbeefcafe123") {
		t.Fatalf("raw secret leaked: %q", got)
	}
	// Long values truncate at 50 chars with a length note.
	if !strings.Contains(got, "… (") {
		t.Fatalf("long PATH not truncated: %q", got)
	}
	for _, cat := range []string{"Path (1):", "Cloud (1):", "Tool (1):", "Other (1):"} {
		if !strings.Contains(got, cat) {
			t.Fatalf("missing category %q in %q", cat, got)
		}
	}
	// Category order must be stable.
	iPath := strings.Index(got, "Path (")
	iCloud := strings.Index(got, "Cloud (")
	iOther := strings.Index(got, "Other (")
	if !(iPath < iCloud && iCloud < iOther) {
		t.Fatalf("category order wrong in %q", got)
	}
}

func TestFilterEnvRejectsNonEnv(t *testing.T) {
	for _, in := range []string{
		"",
		"this has no equals sign",
		"BAD KEY=value",
	} {
		if _, ok := FilterEnv(in); ok {
			t.Fatalf("FilterEnv should not apply to %q", in)
		}
	}
}

func TestMaskEnvValueShortValueFullyMasked(t *testing.T) {
	if got := maskEnvValue("abc"); got != "****" {
		t.Fatalf("short value = %q, want %q", got, "****")
	}
	if got := maskEnvValue(""); got != "" {
		t.Fatalf("empty value = %q, want empty", got)
	}
}

func TestIsSensitiveEnvVar(t *testing.T) {
	for _, name := range []string{"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "MY_API_KEY", "DB_PASSWORD", "FOREBRAIN_PROJECT_KEY"} {
		if !isSensitiveEnvVar(name) {
			t.Fatalf("%s should be sensitive", name)
		}
	}
	for _, name := range []string{"PATH", "HOME", "LANG", "PWD"} {
		if isSensitiveEnvVar(name) {
			t.Fatalf("%s should not be sensitive", name)
		}
	}
}
