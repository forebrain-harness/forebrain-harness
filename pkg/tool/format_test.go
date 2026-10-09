package tool

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

func TestFormatToolStepResult_readFile(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "read_file",
		Input: map[string]any{
			"file_path": "internal/foo.go",
		},
		Output: map[string]any{
			"preview_text": "1|package foo\n2|\n",
			"preview_kind": "file",
		},
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "```go") {
		t.Fatalf("expected go fence: %q", got)
	}
	if !strings.Contains(got, "package foo") {
		t.Fatalf("expected body: %q", got)
	}
}

func TestSkillReadHasSemanticDisplayBodyInsteadOfFileBody(t *testing.T) {
	t.Parallel()
	path := "/repo/.forebrain/skills/review/SKILL.md"
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "read_file",
		Output: map[string]any{
			"preview_text": "1|# Review\n2|Follow the checklist.",
			"preview_kind": "skill",
			"skill_name":   "review",
			"skill_path":   path,
		},
	}
	body, truncated := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	// The body is the shared semantic text naming the source — never the file
	// preview it replaced — and it must not be empty: webchat renders exactly
	// this body.
	if truncated || body != "Loaded from "+path {
		t.Fatalf("skill display body = %q, truncated=%v; want the Loaded-from source line", body, truncated)
	}
	meta := BuildToolMeta(evt)
	if meta.Category != "skill" || meta.SkillName != "review" || meta.SkillPath != path {
		t.Fatalf("skill meta = %+v", meta)
	}
}

func TestFailedSkillReadKeepsErrorDisplayBody(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:      event.RunEventToolCompleted,
		ToolName:  "read_file",
		SkillName: "review",
		SkillPath: "/skills/review/SKILL.md",
		Error:     "open /skills/review/SKILL.md: no such file or directory",
	}
	body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	want := "Failed to load from /skills/review/SKILL.md\nopen /skills/review/SKILL.md: no such file or directory"
	if body != want {
		t.Fatalf("failed skill read body = %q, want the source line plus the verbatim error", body)
	}
	meta := BuildToolMeta(evt)
	if meta.Status != "failed" || meta.Category != "skill" || meta.SkillName != "review" {
		t.Fatalf("failed skill meta = %+v", meta)
	}
}

func TestSkillReadBodyHidesPreviewForEveryOtherSurface(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:      event.RunEventToolCompleted,
		ToolName:  "read_file",
		SkillName: "review",
		SkillPath: "/skills/review/SKILL.md",
		Output: map[string]any{
			"preview_text": "1|# Review\n2|Follow the checklist.",
		},
	}
	body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if strings.Contains(body, "Follow the checklist") || strings.Contains(body, "1|") {
		t.Fatalf("skill body leaked the file preview: %q", body)
	}
}

func TestFormatToolStepResultWebSearchRendersReadableResults(t *testing.T) {
	t.Parallel()
	providerBody := `{"query":"go release","results":[{"title":"Go 1.24 Release Notes","url":"https://go.dev/doc/go1.24","content":"Changes to the language and standard library.","score":0.98},{"title":"Go Blog","url":"https://go.dev/blog/","content":"Official Go news."}]}`
	transport := map[string]any{
		"status":       200,
		"content_type": "application/json",
		"limit":        5,
		"body":         providerBody,
		"truncated":    false,
		"rule_content": "query:go release",
	}
	rawTransport, err := json.Marshal(transport)
	if err != nil {
		t.Fatal(err)
	}
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "web_search",
		Input:    map[string]any{"query": "go release", "limit": 5},
		Output:   map[string]any{"output": string(rawTransport)},
	}

	body, truncated := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	for _, want := range []string{
		"**2 search results**",
		"1. [Go 1.24 Release Notes](https://go.dev/doc/go1.24)",
		"Changes to the language and standard library.",
		"2. [Go Blog](https://go.dev/blog/)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("web search result missing %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{"output:", "```json", "{", `\"status\"`, `\"content_type\"`, `\"rule_content\"`, `\"score\"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("web search result exposed raw JSON field %q:\n%s", forbidden, body)
		}
	}
}

func TestFormatToolStepResultWebFetchRendersOnlyMarkdownBody(t *testing.T) {
	t.Parallel()
	transport := map[string]any{
		"status":       200,
		"content_type": "text/html; charset=utf-8",
		"body":         "# API reference\n\n- first item\n- second item",
		"bytes":        1234,
		"final_url":    "https://example.com/docs",
		"truncated":    false,
		"rule_content": "domain:example.com",
	}
	rawTransport, err := json.Marshal(transport)
	if err != nil {
		t.Fatal(err)
	}
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "web_fetch",
		Input:    map[string]any{"url": "https://example.com/docs"},
		Output:   map[string]any{"output": string(rawTransport)},
	}

	body, truncated := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	want := "# API reference\n\n- first item\n- second item"
	if body != want {
		t.Fatalf("web fetch body = %q, want %q", body, want)
	}
	for _, forbidden := range []string{"output:", "```json", `\"status\"`, `\"content_type\"`, `\"rule_content\"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("web fetch result exposed transport field %q:\n%s", forbidden, body)
		}
	}
}

func TestFormatWebFetchResultSupportsNestedAndDirectPayloads(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "step envelope", raw: `{"output":"{\"body\":\"# Nested\\n\\nBody\",\"status\":200}"}`, want: "# Nested\n\nBody"},
		{name: "direct tool payload", raw: `{"body":"plain body","status":200}`, want: "plain body"},
		{name: "empty response body", raw: `{"body":"","status":204}`, want: ""},
		{name: "generic legacy fence", raw: "output:\n\n```json\n{\"output\":\"{\\\"body\\\":\\\"legacy body\\\",\\\"status\\\":200}\"}\n```", want: "legacy body"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, recognized := FormatWebFetchResult(test.raw)
			if !recognized || got != test.want {
				t.Fatalf("FormatWebFetchResult() = %q, %v; want %q, true", got, recognized, test.want)
			}
		})
	}
}

func TestFormatToolStepResultWebFetchMalformedPayloadFailsReadably(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "web_fetch",
		Output:   map[string]any{"output": `{"status":200,"final_url":"https://example.com"}`},
	}
	body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if body != "Web fetch completed, but its content could not be displayed." {
		t.Fatalf("unexpected fallback: %q", body)
	}
	if strings.Contains(body, "output:") || strings.Contains(body, "final_url") || strings.Contains(body, "```json") {
		t.Fatalf("malformed web fetch exposed its transport envelope: %q", body)
	}
}

func TestFormatWebSearchResultSupportsProviderShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "brave",
			raw:  `{"status":200,"body":"{\"web\":{\"results\":[{\"title\":\"Go Documentation\",\"url\":\"https://go.dev/doc/\",\"description\":\"The <strong>Go</strong> programming language.\",\"page_age\":\"2 days ago\"}]}}"}`,
			want: []string{"**1 search result**", "[Go Documentation](https://go.dev/doc/)", "The Go programming language.", "_Published: 2 days ago_"},
		},
		{
			name: "baidu",
			raw:  `{"status":200,"body":"{\"references\":[{\"title\":\"图尔坤项目\",\"url\":\"https://example.cn/forebrain\",\"content\":\"项目说明\",\"date\":\"2026-08-01\"}]}"}`,
			want: []string{"**1 search result**", "[图尔坤项目](https://example.cn/forebrain)", "项目说明", "_Published: 2026-08-01_"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, recognized := FormatWebSearchResult(tt.raw)
			if !recognized {
				t.Fatalf("provider payload was not recognized: %s", tt.raw)
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("formatted result missing %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "{") || strings.Contains(got, `\"results\"`) || strings.Contains(got, `\"references\"`) {
				t.Fatalf("formatted provider result exposed JSON:\n%s", got)
			}
		})
	}
}

func TestFormatToolStepResultWebSearchMalformedPayloadFailsReadably(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "web_search",
		Output:   map[string]any{"output": `{"status":200,"body":"not-json"}`},
	}
	body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if body != "No search results found." {
		t.Fatalf("unexpected safe fallback: %q", body)
	}
	if strings.Contains(body, "not-json") || strings.Contains(body, "{") {
		t.Fatalf("fallback exposed transport payload: %q", body)
	}
}

func TestRequestPermissionsCompletionShowsAutoApprovalReason(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "request_permissions",
		Input:    map[string]any{"reason": "inspect external files"},
		Output: map[string]any{
			"scope":           "session",
			"permissions":     map[string]any{"file_system": map[string]any{"entries": []map[string]any{{"path": map[string]any{"type": "path", "path": "/tmp/external"}, "access": "read"}}}},
			"approval_status": "auto_approved",
			"approval_reason": "permission mode is auto-approve, which approves all tools without prompting",
		},
	}

	body, truncated := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	for _, want := range []string{
		"inspect external files",
		"read `/tmp/external`",
		"auto-approved without an approval prompt",
		"permission mode is auto-approve",
		"scope: session",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if summary := SummarizeToolStep(evt); !strings.Contains(summary, "auto-approved") || !strings.Contains(summary, "auto-approve") {
		t.Fatalf("summary=%q want auto-approval reason", summary)
	}
	if meta := BuildToolMeta(evt); meta.Invocation != "request permissions" {
		t.Fatalf("invocation=%q", meta.Invocation)
	}
}

func TestRequestPermissionsCompletionShowsDenialReason(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "request_permissions",
		Output: map[string]any{
			"approval_status": "denied",
			"approval_reason": "matched deny rule `request_permissions` (project settings)",
		},
	}
	body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if !strings.Contains(body, "denied — no permissions were granted") || !strings.Contains(body, "matched deny rule") {
		t.Fatalf("body=%q", body)
	}
	if summary := SummarizeToolStep(evt); !strings.Contains(summary, "denied") {
		t.Fatalf("summary=%q", summary)
	}
}

// TestRequestPermissionsPolicyDenialStatusIsDenied guards the end-to-end status
// mapping: a policy denial is delivered as a normal (non-error) completion
// carrying approval_status=denied, so stepStatusLabel must not report it as
// "completed". Otherwise the renderer paints the success header/color over a
// card body that says the request was denied.
func TestRequestPermissionsPolicyDenialStatusIsDenied(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "request_permissions",
		Output: map[string]any{
			"approval_status": "denied",
			"approval_reason": "matched deny rule `request_permissions` (project settings)",
		},
	}
	if got := BuildToolMeta(evt).Status; got != "denied" {
		t.Fatalf("status=%q want denied", got)
	}

	// Approved and auto-approved completions must keep the normal status.
	for _, status := range []string{"approved", "auto_approved"} {
		ok := StepEvent{
			Kind:     StepKindToolCompleted,
			ToolName: "request_permissions",
			Output:   map[string]any{"approval_status": status},
		}
		if got := BuildToolMeta(ok).Status; got != "completed" {
			t.Fatalf("approval_status=%q produced status=%q want completed", status, got)
		}
	}

	// approval_status must not leak into other tools' status mapping.
	other := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "read_file",
		Output:   map[string]any{"approval_status": "denied"},
	}
	if got := BuildToolMeta(other).Status; got != "completed" {
		t.Fatalf("read_file status=%q want completed", got)
	}
}

func TestFormatToolStepResult_readFileFallsBackToOutputPath(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "read_file",
		Output: map[string]any{
			"abs_path":     "/tmp/internal/foo.go",
			"preview_text": "1|package foo\n2|\n",
			"preview_kind": "file",
		},
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "path: `/tmp/internal/foo.go`") {
		t.Fatalf("expected fallback path from output, got %q", got)
	}
	if !strings.Contains(got, "```go") {
		t.Fatalf("expected go fence from fallback path, got %q", got)
	}
}

func TestFormatToolStepResult_readFile_error(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "read_file",
		Error:    "permission denied",
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "permission denied") {
		t.Fatalf("got %q", got)
	}
}

func TestFormatToolStepResult_shellGitDiffShowsDiffFence(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Input:    map[string]any{"command": "git diff"},
		Output: map[string]any{
			"stdout":               "diff --git a/a.go b/a.go\n+added\n",
			"stdout_bytes":         125132,
			"stdout_omitted_bytes": 121000,
		},
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "stdout omitted 121000 bytes") {
		t.Fatalf("expected omitted bytes hint, got %q", got)
	}
	if !strings.Contains(got, "```diff") || !strings.Contains(got, "+added") {
		t.Fatalf("expected diff fence, got %q", got)
	}
	meta := BuildToolMeta(evt)
	if meta.ToolName != "shell" || meta.Status != "completed" || meta.Invocation != "git diff" {
		t.Fatalf("unexpected tool meta: %+v", meta)
	}
}

func TestFormatToolStepResult_clamp(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 2000)
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output:   map[string]any{"stdout": long},
	}
	got, trunc := FormatToolStepResult(evt, 400)
	if !trunc {
		t.Fatal("expected truncation")
	}
	if len(got) > 500 {
		t.Fatalf("expected bounded output, got len %d", len(got))
	}
}

func TestChromaLangFromPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want string
	}{
		{"a.go", "go"},
		{"dir/x.rs", "rust"},
		{"", "text"},
		{"noext", "text"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if g := chromaLangFromPath(tc.path); g != tc.want {
				t.Fatalf("chromaLangFromPath(%q)=%q want %q", tc.path, g, tc.want)
			}
		})
	}
}

func TestSummarizeToolStepPrefersExplicitResultLines(t *testing.T) {
	t.Parallel()
	got := SummarizeToolStep(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "read_file",
		Input:    map[string]any{"file_path": "internal/foo.go"},
		Output: map[string]any{
			"preview_text": "1|package foo",
			"preview_kind": "file",
			"result_lines": 42,
		},
	})
	if want := "ran read internal/foo.go · lines 1-42"; got != want {
		t.Fatalf("summary=%q want %q", got, want)
	}
}

func TestFormatToolStepResult_shellShowsOmittedBytesHint(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output: map[string]any{
			"stdout":               "hello from stdout",
			"stderr":               "warning from stderr",
			"exit_code":            0,
			"stdout_bytes":         42000,
			"stdout_omitted_bytes": 21000,
			"full_path":            "/tmp/tool-output.txt",
		},
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "stdout omitted 21000 bytes") {
		t.Fatalf("expected omitted bytes hint, got %q", got)
	}
	if !strings.Contains(got, "hello from stdout") {
		t.Fatalf("expected stdout preview, got %q", got)
	}
	if !strings.Contains(got, "warning from stderr") {
		t.Fatalf("expected stderr preview, got %q", got)
	}
	if !strings.Contains(got, "/tmp/tool-output.txt") {
		t.Fatalf("expected full_path hint, got %q", got)
	}
}

func TestFormatToolStepResult_shellShowsEllipsisForTruncatedPreview(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 5000)
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output: map[string]any{
			"stdout":               long[:3997] + "...",
			"exit_code":            0,
			"stdout_bytes":         5000,
			"stdout_omitted_bytes": 1000,
		},
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("expected ellipsis in stdout preview, got %q", got)
	}
}

func TestFormatToolStepResult_editFileNoOpHasEmptyDisplayBody(t *testing.T) {
	t.Parallel()
	for _, turnDiff := range []any{
		map[string]any{
			"path":         "/tmp/demo.txt",
			"added":        0,
			"deleted":      0,
			"unified_diff": "",
		},
		event.Summary{Path: "/tmp/demo.txt"},
	} {
		evt := StepEvent{
			Kind:     event.RunEventToolCompleted,
			ToolName: "edit_file",
			Input:    map[string]any{"file_path": "/tmp/demo.txt"},
			Output: map[string]any{
				"status":    "ok",
				"abs_path":  "/tmp/demo.txt",
				"replaced":  1,
				"turn_diff": turnDiff,
			},
		}
		got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
		if trunc {
			t.Fatal("unexpected truncation")
		}
		if got != "" {
			t.Fatalf("expected empty no-op edit body, got %q", got)
		}
	}
}

func TestFormatToolStepResult_editFileRealDiffRemainsVisible(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "edit_file",
		Output: map[string]any{
			"status":   "ok",
			"abs_path": "/tmp/demo.txt",
			"replaced": 1,
			"turn_diff": map[string]any{
				"path":         "/tmp/demo.txt",
				"added":        1,
				"deleted":      1,
				"unified_diff": "--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n",
			},
		},
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "turn diff: `/tmp/demo.txt` (+1/-1)") || !strings.Contains(got, "+new") {
		t.Fatalf("expected real edit diff body, got %q", got)
	}
}

func TestFormatToolStepResult_editFileMalformedTurnDiffIsNotSuppressed(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "edit_file",
		Output: map[string]any{
			"status":   "ok",
			"abs_path": "/tmp/demo.txt",
			"replaced": 1,
			"turn_diff": map[string]any{
				"path":         "/tmp/demo.txt",
				"added":        "unknown",
				"deleted":      0,
				"unified_diff": "",
			},
		},
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if got == "" {
		t.Fatal("malformed turn diff must not be mistaken for a no-op edit")
	}
}

func TestFormatToolStepResult_writeFileShowsTurnDiff(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "write_file",
		Output: map[string]any{
			"status":   "ok",
			"abs_path": "/tmp/demo.txt",
			"turn_diff": map[string]any{
				"path":         "/tmp/demo.txt",
				"added":        1,
				"deleted":      1,
				"unified_diff": "--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n",
			},
		},
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "```diff") {
		t.Fatalf("expected diff fence, got %q", got)
	}
	if !strings.Contains(got, "+new") {
		t.Fatalf("expected diff body, got %q", got)
	}
	meta := BuildToolMeta(evt)
	if meta.ToolName != "write_file" || meta.Status != "completed" || meta.Invocation != "write" {
		t.Fatalf("unexpected tool meta: %+v", meta)
	}
}

func TestFormatToolStepResult_errorIncludesInput(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Input: map[string]any{
			"command": "echo $(whoami)",
		},
		Error: `shell command requires approval: "echo $(whoami)"`,
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "**error** (shell)") {
		t.Fatalf("missing error header: %q", got)
	}
	if !strings.Contains(got, "shell command requires approval") {
		t.Fatalf("missing error body: %q", got)
	}
	meta := BuildToolMeta(evt)
	if meta.ToolName != "shell" || meta.Status != "failed" || meta.Input["command"] != "echo $(whoami)" {
		t.Fatalf("unexpected tool meta: %+v", meta)
	}
}

func TestFormatToolStepResult_errorWithoutInput(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "read_file",
		Error:    "path not allowed: /etc/passwd",
	}
	got, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "**error** (read_file)") {
		t.Fatalf("missing error header: %q", got)
	}
	if !strings.Contains(got, "path not allowed: /etc/passwd") {
		t.Fatalf("missing error body: %q", got)
	}
}

func TestFormatToolStepResult_startedReturnsEmpty(t *testing.T) {
	t.Parallel()
	got, trunc := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolStarted,
		ToolName: "shell",
		Input:    map[string]any{"command": "git status --porcelain=v1 -b"},
	}, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if got != "" {
		t.Fatalf("expected empty started body, got %q", got)
	}
}

func TestFormatToolStepResult_requiresApprovalReturnsEmpty(t *testing.T) {
	t.Parallel()
	got, trunc := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "shell",
		Input:    map[string]any{"command": "git status --porcelain=v1 -b"},
		Output:   map[string]any{"requires_action": true, "command": "git status --porcelain=v1 -b"},
	}, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if got != "" {
		t.Fatalf("expected empty awaiting-approval body, got %q", got)
	}
}

func TestFormatToolStepResult_genericDisplayBodyOmitsToolMetadata(t *testing.T) {
	t.Parallel()
	got, trunc := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "example_tool",
		StepID:   "call-123",
		Input:    map[string]any{"pattern": "TODO"},
		Output: map[string]any{
			"summary":        "found matches",
			"stdout_preview": "a.go:10: TODO\n",
			"command":        "example TODO",
			"matches":        1,
		},
	}, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "stdout preview:") {
		t.Fatalf("expected real preview content, got %q", got)
	}
	for _, needle := range []string{"tool:", "status:", "purpose:", "invocation:", "input:"} {
		if strings.Contains(got, needle) {
			t.Fatalf("display body must omit metadata %q, got %q", needle, got)
		}
	}
	if strings.Contains(got, "tool use id:") {
		t.Fatalf("display body must not expose tool use id, got %q", got)
	}
}

func TestFormatToolStepResult_genericPlacesPreviewBeforeTruncationNote(t *testing.T) {
	t.Parallel()
	got, trunc := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "example_tool",
		Output: map[string]any{
			"stdout_preview": "a.go:10: TODO\nb.go:20: TODO\n",
			"omitted_bytes":  2281,
			"full_path":      "/tmp/tool-output.txt",
		},
	}, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	previewIdx := strings.Index(got, "stdout preview:")
	noteIdx := strings.Index(got, "preview truncated:")
	if previewIdx < 0 {
		t.Fatalf("expected stdout preview, got %q", got)
	}
	if noteIdx < 0 {
		t.Fatalf("expected truncation note, got %q", got)
	}
	if noteIdx < previewIdx {
		t.Fatalf("expected preview before truncation note, got %q", got)
	}
}

func TestFormatMemoryAddNoteRequiresValidEmptyResult(t *testing.T) {
	valid, _ := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_add_ad_hoc_note",
		Input:    map[string]any{"note": "## Remember\n\n- Keep this."},
		Output:   map[string]any{"output": `{}`},
	}, DefaultMaxFormattedBody)
	if valid != "## Remember\n\n- Keep this." {
		t.Fatalf("valid result=%q", valid)
	}
	invalid, _ := FormatToolStepResult(StepEvent{Kind: StepKindToolCompleted, ToolName: "memories_add_ad_hoc_note"}, DefaultMaxFormattedBody)
	if !strings.Contains(invalid, "unreadable") {
		t.Fatalf("invalid result=%q", invalid)
	}
	missingNote, _ := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_add_ad_hoc_note",
		Output:   map[string]any{"output": `{}`},
	}, DefaultMaxFormattedBody)
	if strings.Contains(strings.ToLower(missingNote), "saved") {
		t.Fatalf("missing note must not claim success: %q", missingNote)
	}
}

func TestFormatMemoryToolMalformedPayloadsAreNeutral(t *testing.T) {
	for _, test := range []struct {
		name   string
		tool   string
		output map[string]any
	}{
		{name: "nil", tool: "memories_list", output: nil},
		{name: "empty", tool: "memories_list", output: map[string]any{}},
		{name: "malformed", tool: "memories_read", output: map[string]any{"output": "not-json"}},
		{name: "wrong shape", tool: "memories_search", output: map[string]any{"output": `{}`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _ := FormatToolStepResult(StepEvent{Kind: StepKindToolCompleted, ToolName: test.tool, Output: test.output}, DefaultMaxFormattedBody)
			if !strings.Contains(got, "unreadable") {
				t.Fatalf("got=%q", got)
			}
			for _, forbidden := range []string{"No files", "This file is empty", "No matching memories", "output:", "```json"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("malformed result made false/raw claim %q: %q", forbidden, got)
				}
			}
		})
	}
}

func TestFormatMemoryReadHonorsDisplayByteLimit(t *testing.T) {
	got, truncated := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_read",
		Output:   map[string]any{"output": `{"path":"MEMORY.md","start_line_number":1,"content":"abcdefghijklmnopqrstuvwxyz","truncated":false}`},
	}, 20)
	if !truncated || !strings.Contains(got, "formatted body truncated") {
		t.Fatalf("truncated=%v body=%q", truncated, got)
	}
	if strings.Contains(got, "output:") {
		t.Fatalf("transport envelope leaked: %q", got)
	}
}

func TestFormatMemoryToolAcceptsDirectStructuredPayload(t *testing.T) {
	got, _ := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_read",
		Output: map[string]any{
			"path": "MEMORY.md", "start_line_number": 1, "content": "# Direct\n", "truncated": false,
		},
	}, DefaultMaxFormattedBody)
	if !strings.Contains(got, "# Direct") || strings.Contains(got, "output:") {
		t.Fatalf("got=%q", got)
	}
}

func TestFormatToolStepResult_memoryListIsReadable(t *testing.T) {
	got, truncated := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_list",
		Output:   map[string]any{"output": `{"path":null,"entries":[{"path":"MEMORY.md","entry_type":"file"},{"path":"rollout_summaries","entry_type":"directory"}],"next_cursor":"2","truncated":true}`},
	}, DefaultMaxFormattedBody)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	for _, forbidden := range []string{"output:", "```json", `"entries"`, `"next_cursor"`} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("memory list contains forbidden raw transport text %q: %q", forbidden, got)
		}
	}
	for _, want := range []string{"Memories in `your memory store`", "MEMORY.md", "rollout_summaries", "More results are available"} {
		if !strings.Contains(got, want) {
			t.Fatalf("memory list missing %q: %q", want, got)
		}
	}
}

func TestFormatToolStepResult_memoryReadIsReadable(t *testing.T) {
	got, _ := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_read",
		Output:   map[string]any{"output": `{"path":"MEMORY.md","start_line_number":4,"content":"# Preferences\n- concise answers\n","truncated":true}`},
	}, DefaultMaxFormattedBody)
	if strings.Contains(got, "output:") || strings.Contains(got, "```json") || strings.Contains(got, `"content"`) {
		t.Fatalf("memory read leaked transport envelope: %q", got)
	}
	if strings.Contains(got, "```markdown") {
		t.Fatalf("memory read should be rendered as native markdown, got %q", got)
	}
	for _, want := range []string{"MEMORY.md", "starting at line 4", "# Preferences", "Only part of this file is shown"} {
		if !strings.Contains(got, want) {
			t.Fatalf("memory read missing %q: %q", want, got)
		}
	}
}

func TestFormatToolStepResult_memorySearchIsReadable(t *testing.T) {
	got, _ := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_search",
		Output:   map[string]any{"output": `{"queries":["deploy"],"matches":[{"path":"MEMORY.md","match_line_number":8,"content":"- deploy with staging first"}],"truncated":false}`},
	}, DefaultMaxFormattedBody)
	if strings.Contains(got, "output:") || strings.Contains(got, "```json") || strings.Contains(got, `"matches"`) {
		t.Fatalf("memory search leaked transport envelope: %q", got)
	}
	if strings.Contains(got, "```text") {
		t.Fatalf("memory search should use markdown blockquotes, got %q", got)
	}
	// The query that recalled this memory is marked where it occurs, so the
	// reader can see what matched without re-reading the call.
	for _, want := range []string{"MEMORY.md", "line 8", "**deploy** with staging first"} {
		if !strings.Contains(got, want) {
			t.Fatalf("memory search missing %q: %q", want, got)
		}
	}
	// The header already carries the queries and the searched path, so the body
	// must not echo them back.
	if strings.Contains(got, "Memory matches") {
		t.Fatalf("memory search body repeated the header: %q", got)
	}
}

// A search that matched nothing has no body, so every surface falls back to the
// same empty-output placeholder it shows for any other tool. A sentence of its
// own here would be the one empty state in the transcript that looks different
// from all the others — and, when the search was scoped to a single file, it
// read as "memory holds nothing about this" rather than "this file does not".
func TestFormatToolStepResult_memorySearchWithoutMatchesHasNoBody(t *testing.T) {
	got, truncated := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_search",
		Input:    map[string]any{"queries": []any{"deploy"}, "path": "MEMORY.md"},
		Output:   map[string]any{"output": `{"queries":["deploy"],"path":"MEMORY.md","matches":null,"truncated":false}`},
	}, DefaultMaxFormattedBody)
	if got != "" {
		t.Fatalf("memory search without matches produced a body: %q", got)
	}
	if truncated {
		t.Fatal("an empty memory search body must not report truncation")
	}
}

// The mark follows the search's own rules. Index terms come back folded, and a
// line rarely spells them the way the index stored them, so case is folded here
// too — otherwise every memory the index recalled would be shown unmarked,
// which is the case where the reader most needs to be told what matched.
func TestMemorySearchHighlightMarksTheTermsThatRecalledTheMemory(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   map[string]any
		content string
		terms   any
		want    string
	}{
		{
			name:    "case is folded so an index term is never left unmarked",
			input:   map[string]any{"queries": []any{"deploy"}},
			content: "Deploy and deploy",
			want:    "**Deploy** and **deploy**",
		},
		{
			name:    "a segmented term the index recalled is marked too",
			input:   map[string]any{"queries": []any{"审批覆盖层"}},
			content: "审批浮层必须完整展示待批准内容",
			terms:   []any{"审批"},
			want:    "**审批**浮层",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			terms := test.terms
			if terms == nil {
				terms = test.input["queries"]
			}
			output := map[string]any{"queries": []any{"x"}, "matches": []any{map[string]any{
				"path": "MEMORY.md", "match_line_number": float64(1), "content": test.content,
				"matched_queries": test.input["queries"], "matched_terms": terms,
			}}, "truncated": false}
			got, _ := FormatToolStepResult(StepEvent{
				Kind: StepKindToolCompleted, ToolName: "memories_search",
				Input: test.input, Output: map[string]any{"output": output},
			}, DefaultMaxFormattedBody)
			if !strings.Contains(got, test.want) {
				t.Fatalf("highlight=%q want it to contain %q", got, test.want)
			}
		})
	}
}

// Header segments carry the call's own arguments. They wrap; they are never cut
// short, and a scoped search says which file it was scoped to.
func TestMemorySearchHeaderKeepsEveryQueryAndTheScope(t *testing.T) {
	queries := []any{"exit_plan_mode", "multiline editor", "shared layout", "hard wrapping", "soft wrapping", "wide runes", "click mapping"}
	input := map[string]any{"queries": queries, "path": "MEMORY.md", "match_mode": map[string]any{"type": "any"}}
	meta := BuildToolMeta(StepEvent{Kind: StepKindToolCompleted, ToolName: "memories_search", Input: input})
	summary := SummarizeToolStep(StepEvent{Kind: StepKindToolCompleted, ToolName: "memories_search", Input: input})
	for _, label := range []string{meta.Invocation, summary} {
		for _, query := range queries {
			if !strings.Contains(label, query.(string)) {
				t.Fatalf("header %q dropped query %q", label, query)
			}
		}
		if strings.ContainsAny(label, "…") || strings.Contains(label, "...") {
			t.Fatalf("header %q truncated its arguments", label)
		}
		if !strings.Contains(label, "in MEMORY.md") {
			t.Fatalf("header %q does not say the search was scoped to MEMORY.md", label)
		}
	}
	unscoped := BuildToolMeta(StepEvent{Kind: StepKindToolCompleted, ToolName: "memories_search",
		Input: map[string]any{"queries": []any{"deploy"}}})
	if strings.Contains(unscoped.Invocation, " in ") {
		t.Fatalf("a search of the whole store must not claim a scope: %q", unscoped.Invocation)
	}
}

func TestMemoryToolInvocationLabelsNeverExposeRawArguments(t *testing.T) {
	for _, test := range []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{
			name:  "search",
			tool:  "memories_search",
			input: map[string]any{"queries": []any{"commit all changes", "forebrain git commit"}, "case_sensitive": false, "cursor": "0", "match_mode": map[string]any{"type": "any"}},
			want:  `search memories "commit all changes", "forebrain git commit"`,
		},
		{name: "search without queries", tool: "memories_search", input: map[string]any{}, want: "search memories"},
		{name: "read", tool: "memories_read", input: map[string]any{"path": "MEMORY.md", "line_offset": float64(80), "max_lines": float64(30)}, want: "read memory MEMORY.md"},
		{name: "list root", tool: "memories_list", input: map[string]any{"cursor": "0"}, want: "list memories"},
		{name: "list path", tool: "memories_list", input: map[string]any{"path": "rollout_summaries"}, want: "list memories rollout_summaries"},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := BuildToolMeta(StepEvent{Kind: StepKindToolCompleted, ToolName: test.tool, Input: test.input})
			if meta.Invocation != test.want {
				t.Fatalf("invocation=%q want %q", meta.Invocation, test.want)
			}
			for _, forbidden := range []string{"{", "}", `":`} {
				if strings.Contains(meta.Invocation, forbidden) {
					t.Fatalf("invocation leaked raw JSON %q: %q", forbidden, meta.Invocation)
				}
			}
		})
	}
}

func TestSummarizeToolStepMemoryToolsDescribeOutcome(t *testing.T) {
	for _, test := range []struct {
		name   string
		tool   string
		input  map[string]any
		output map[string]any
		want   string
	}{
		{
			name:   "search counts matches",
			tool:   "memories_search",
			input:  map[string]any{"queries": []any{"deploy"}},
			output: map[string]any{"output": `{"queries":["deploy"],"matches":[{"path":"MEMORY.md","match_line_number":8,"content":"- deploy"}],"truncated":false}`},
			want:   `searched memories "deploy" · 1 match`,
		},
		{
			name:   "read reports line range",
			tool:   "memories_read",
			input:  map[string]any{"path": "MEMORY.md", "line_offset": float64(80), "max_lines": float64(30)},
			output: map[string]any{"output": `{"path":"MEMORY.md","start_line_number":80,"content":"x","truncated":false}`},
			want:   "read memory MEMORY.md · lines 80-109",
		},
		{
			name:   "list counts entries",
			tool:   "memories_list",
			output: map[string]any{"output": `{"path":null,"entries":[{"path":"MEMORY.md","entry_type":"file"}],"truncated":false}`},
			want:   "listed memories · 1 entry",
		},
		{
			// A replayed call carries the stored arguments but no result payload.
			// The summary must describe the call without claiming a count.
			name:  "unknown payload claims no count",
			tool:  "memories_search",
			input: map[string]any{"queries": []any{"deploy"}},
			want:  `searched memories "deploy"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := SummarizeToolStep(StepEvent{Kind: StepKindToolCompleted, ToolName: test.tool, Input: test.input, Output: test.output})
			if got != test.want {
				t.Fatalf("summary=%q want %q", got, test.want)
			}
		})
	}
}

func TestBuildToolMetaCarriesStructuredMetadata(t *testing.T) {
	t.Parallel()
	meta := BuildToolMeta(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "example_tool",
		Input:    map[string]any{"pattern": "TODO"},
		Output:   map[string]any{"command": "example TODO"},
	})
	if meta.ToolName != "example_tool" || meta.Status != "completed" || meta.Invocation != "example TODO" {
		t.Fatalf("unexpected tool meta: %+v", meta)
	}
	if meta.Input["pattern"] != "TODO" {
		t.Fatalf("expected structured input in tool meta, got %+v", meta)
	}
}

func TestFormatToolStepResult_exitPlanModeRendersMessage(t *testing.T) {
	t.Parallel()
	// The tool returns a JSON envelope wrapped under the "output" key (a
	// string), exactly as fallbackToolOutput produces for a string-typed tool
	// result. The formatter must surface the human-readable message rather
	// than the escaped JSON blob.
	payload := `{"ok":true,"mode":"agent","plan_file":"/tmp/plans/add-feature.md","message":"Exited plan mode. You can now make edits, run tools, and take actions. The plan file is available for reference if needed."}`
	got, trunc := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "exit_plan_mode",
		Output:   map[string]any{"output": payload},
	}, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(got, "Exited plan mode. You can now make edits") {
		t.Fatalf("expected the confirmation message in body, got %q", got)
	}
	// Must not leak the raw JSON envelope, the "output:" label, or escaped
	// quoting into the user-facing display.
	for _, needle := range []string{"output:", `"message"`, `"plan_file"`, `\"`, "```json"} {
		if strings.Contains(got, needle) {
			t.Fatalf("body must not contain %q, got %q", needle, got)
		}
	}
}

func TestFormatToolStepResult_exitPlanModeFallsBackOnBadPayload(t *testing.T) {
	t.Parallel()
	// When the inner payload is not the expected JSON envelope, fall back to
	// the generic renderer instead of dropping the result silently.
	got, _ := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "exit_plan_mode",
		Output:   map[string]any{"output": "not-json"},
	}, DefaultMaxFormattedBody)
	if !strings.Contains(got, "not-json") {
		t.Fatalf("fallback should surface raw payload, got %q", got)
	}
}

// A user_interaction card is named by what it asks — every question, in
// order — never by its argument JSON, and reads as a question being asked.
func TestUserInteractionCardNamesItsQuestions(t *testing.T) {
	t.Parallel()
	input := map[string]any{"questions": []any{
		map[string]any{"header": "Triggers", "question": "When should it fire?", "options": []any{map[string]any{"label": "Just hi"}}},
		map[string]any{"question": "  What should the\ngreeting look like?  "},
	}}
	if got := UserInteractionQuestionsLabel(input); got != "Triggers · What should the greeting look like?" {
		t.Fatalf("label = %q", got)
	}
	meta := BuildToolMeta(StepEvent{Kind: StepKindToolStarted, ToolName: "user_interaction", Input: input})
	if meta.Invocation != "ask user · Triggers · What should the greeting look like?" {
		t.Fatalf("invocation = %q", meta.Invocation)
	}
	for kind, want := range map[string]string{
		StepKindToolStarted:   "asking user · Triggers · What should the greeting look like?",
		StepKindToolCompleted: "asked user · Triggers · What should the greeting look like?",
	} {
		got := SummarizeToolStep(StepEvent{Kind: kind, ToolName: "user_interaction", Input: input, Output: map[string]any{"answers": map[string]any{}}})
		if got != want {
			t.Fatalf("%s summary = %q, want %q", kind, got, want)
		}
		if strings.Contains(got, "{") || strings.Contains(got, `"`) {
			t.Fatalf("summary exposes JSON: %q", got)
		}
	}
	if got := SummarizeToolStep(StepEvent{Kind: StepKindToolStarted, ToolName: "user_interaction"}); got != "asking user" {
		t.Fatalf("summary without questions = %q", got)
	}
}

func TestFormatToolStepResult_userInteractionRendersAnswersInQuestionOrder(t *testing.T) {
	t.Parallel()
	payload := `{"answers":{"补充说明":{"other":"保留旧会话展示能力\n并维持缩进"},"修改范围":{"selections":["TUI","回放兼容"]},"交付方式":{"selections":["设计方案并开发实施"]}}}`
	got, truncated := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "user_interaction",
		Input: map[string]any{"questions": []any{
			map[string]any{"header": "交付方式"},
			map[string]any{"header": "修改范围"},
			map[string]any{"header": "补充说明"},
		}},
		Output: map[string]any{"output": payload},
	}, DefaultMaxFormattedBody)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	want := "交付方式\n  → 设计方案并开发实施\n" +
		"修改范围\n  → TUI\n  → 回放兼容\n" +
		"补充说明\n  → 保留旧会话展示能力\n    并维持缩进"
	if got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	for _, needle := range []string{"output:", "```", `\"answers\"`, `\"selections\"`, `\"other\"`} {
		if strings.Contains(got, needle) {
			t.Fatalf("body must not contain transport detail %q: %q", needle, got)
		}
	}
}

func TestFormatToolStepResult_userInteractionDirectOutputAndOther(t *testing.T) {
	t.Parallel()
	got, truncated := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "user_interaction",
		Input: map[string]any{"questions": []any{
			map[string]any{"header": "选择"},
			map[string]any{"header": "说明"},
		}},
		Output: map[string]any{"answers": map[string]any{
			"选择": map[string]any{"selections": []string{"保留默认"}, "other": "并增加兼容层"},
			"说明": map[string]any{"other": "仅自定义回答"},
		}},
	}, DefaultMaxFormattedBody)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	want := "选择\n  → 保留默认\n  → 并增加兼容层\n说明\n  → 仅自定义回答"
	if got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestFormatUserInteractionResult_fencedEnvelopeAndStableFallbackOrder(t *testing.T) {
	t.Parallel()
	content := "output:\n\n```json\n" +
		`{"output":"{\"answers\":{\"Zulu\":{\"selections\":[\"last\"]},\"alpha\":{\"selections\":[\"first\"]}}}"}` +
		"\n```"
	got, ok := FormatUserInteractionResult(content, nil)
	if !ok {
		t.Fatal("expected historical fenced envelope to be recognized")
	}
	want := "alpha\n  → first\nZulu\n  → last"
	if got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestFormatUserInteractionResult_missingAnswerAndMalformedFallback(t *testing.T) {
	t.Parallel()
	got, ok := FormatUserInteractionResult(`{"answers":{"已回答":{"selections":["是"]}}}`, map[string]any{
		"questions": []any{
			map[string]any{"header": "已回答"},
			map[string]any{"header": "未回答"},
		},
	})
	if !ok {
		t.Fatal("expected valid answers to be recognized")
	}
	if want := "已回答\n  → 是\n未回答\n  → (no answer)"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	const malformed = "not-json"
	if got, ok := FormatUserInteractionResult(malformed, nil); ok || got != "" {
		t.Fatalf("malformed result = (%q, %v), want unrecognized", got, ok)
	}
	fallback, _ := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "user_interaction",
		Output:   map[string]any{"output": malformed},
	}, DefaultMaxFormattedBody)
	if !strings.Contains(fallback, malformed) {
		t.Fatalf("generic fallback should preserve malformed payload, got %q", fallback)
	}
}

func TestFormatToolStepResult_workingSetPinRendersReadableList(t *testing.T) {
	t.Parallel()
	payload := `{"count":3,"pins":["/repo/a.go","/repo/b.go","/repo/c.go"],"session_id":"cli-123"}`
	got, trunc := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "working_set_pin",
		Output:   map[string]any{"output": payload},
	}, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	for _, needle := range []string{
		"Pinned 3 entries in the working set:",
		"- `/repo/a.go`",
		"- `/repo/b.go`",
		"- `/repo/c.go`",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("expected body to contain %q, got %q", needle, got)
		}
	}
	for _, needle := range []string{"output:", `"pins"`, `"session_id"`, "cli-123", `\\"`, "```json"} {
		if strings.Contains(got, needle) {
			t.Fatalf("body must not contain %q, got %q", needle, got)
		}
	}
}

func TestFormatToolStepResult_workingSetPinFallsBackOnBadPayload(t *testing.T) {
	t.Parallel()
	got, _ := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "working_set_pin",
		Output:   map[string]any{"output": "not-json"},
	}, DefaultMaxFormattedBody)
	if !strings.Contains(got, "not-json") {
		t.Fatalf("fallback should surface raw payload, got %q", got)
	}
}

func TestEnrichToolMetaCarriesSubagentIdentityFromContext(t *testing.T) {
	t.Parallel()
	ctx := WithHookAgentID(
		WithSubagentType(context.Background(), "verification"),
		"agent-123",
	)
	meta := EnrichToolMeta(ctx, BuildToolMeta(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "shell",
		Input:    map[string]any{"command": "git diff"},
	}))
	if meta.AgentID != "agent-123" || meta.AgentType != "verification" || meta.AgentKind != "typed" {
		t.Fatalf("meta=%+v", meta)
	}
}

func TestSummarizeToolStepFormatsMCPInvocationWithServerAndTool(t *testing.T) {
	t.Parallel()
	got := SummarizeToolStep(StepEvent{
		Kind:     StepKindToolStarted,
		ToolName: "mcp__codegraph__codegraph_explore",
		Input: map[string]any{
			"query":       "tui tool render",
			"projectPath": "/repo",
			"maxFiles":    5,
		},
	})
	for _, want := range []string{
		"running codegraph.codegraph_explore",
		"max files: 5",
		"project path: /repo",
		"query: tui tool render",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary=%q missing %q", got, want)
		}
	}
	if strings.Contains(got, "{") || strings.Contains(got, `"query"`) {
		t.Fatalf("MCP summary must not expose raw JSON: %q", got)
	}
}

func TestMCPToolInputSummaryIsDeterministicAndSemantic(t *testing.T) {
	t.Parallel()
	input := map[string]any{
		"projectPath": "/repo",
		"maxFiles":    5,
		"filters":     []any{"go", "test"},
		"options":     map[string]any{"verbose": true},
	}
	want := `filters: ["go","test"] · max files: 5 · options: {"verbose":true} · project path: /repo`
	for range 10 {
		if got := MCPToolInputSummary(input); got != want {
			t.Fatalf("MCPToolInputSummary() = %q, want %q", got, want)
		}
	}
}

func TestMCPToolInputSummaryKeepsAllArgumentsUntruncated(t *testing.T) {
	t.Parallel()
	query := "EventResult WorkedStatus consumption apply reducer result append frames history transcript worked status RunStartedMsg multi-turn internal/tui run.go commands.go renderer view model"
	input := map[string]any{
		"maxFiles":    12,
		"projectPath": "/Users/doudou/workspace/unionj-cloud/forebrain-harness",
		"query":       query,
	}

	got := MCPToolInputSummary(input)
	for _, want := range []string{
		"max files: 12",
		"project path: /Users/doudou/workspace/unionj-cloud/forebrain-harness",
		"query: " + query,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MCPToolInputSummary() missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "...") || strings.Contains(got, "…") {
		t.Fatalf("MCPToolInputSummary() truncated arguments: %q", got)
	}
}

func TestAddMemoryNoteFormattingUsesSemanticMetadataAndMarkdownBody(t *testing.T) {
	t.Parallel()
	note := "## Preferences\n\n- Prefer **concise** status updates."
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_add_ad_hoc_note",
		Input: map[string]any{
			"filename": "user-profile.md",
			"note":     note,
		},
		Output: map[string]any{"output": map[string]any{}},
	}

	body, truncated := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if body != note {
		t.Fatalf("body=%q want=%q", body, note)
	}
	meta := BuildToolMeta(evt)
	if meta.Invocation != "save memory note user-profile.md" {
		t.Fatalf("invocation=%q", meta.Invocation)
	}
	if meta.Purpose != "Save a user-requested durable memory note." {
		t.Fatalf("purpose=%q", meta.Purpose)
	}
	if strings.Contains(meta.Invocation, note) || strings.Contains(meta.Invocation, `"note"`) {
		t.Fatalf("invocation leaks note payload: %q", meta.Invocation)
	}
}

func TestAddMemoryNoteFormattingRejectsMalformedResult(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_add_ad_hoc_note",
		Input:    map[string]any{"filename": "user-profile.md", "note": "secret note"},
		Output:   map[string]any{"output": map[string]any{"unexpected": true}},
	}

	body, truncated := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if body != "Memory tool returned an unreadable note result." {
		t.Fatalf("body=%q", body)
	}
	if strings.Contains(body, "secret note") {
		t.Fatalf("diagnostic leaks note: %q", body)
	}
}

// The note-write tool returns the stored path so the model can read the note
// back. The formatter must still render the note body for that shape, not the
// "unreadable" diagnostic.
func TestFormatMemoryAddNoteAcceptsStoredPathResult(t *testing.T) {
	t.Parallel()
	note := "## Remember\n\n- Keep this."
	for _, output := range []any{
		`{"path":"extensions/ad_hoc/notes/2026-08-16T19-12-00-keep.md"}`,
		map[string]any{"path": "extensions/ad_hoc/notes/2026-08-16T19-12-00-keep.md"},
	} {
		body, _ := FormatToolStepResult(StepEvent{
			Kind:     StepKindToolCompleted,
			ToolName: "memories_add_ad_hoc_note",
			Input:    map[string]any{"note": note},
			Output:   map[string]any{"output": output},
		}, DefaultMaxFormattedBody)
		if body != note {
			t.Fatalf("body=%q want=%q", body, note)
		}
	}
	for _, output := range []any{
		map[string]any{"path": ""},
		map[string]any{"path": 7},
		map[string]any{"path": "extensions/ad_hoc/notes/keep.md", "unexpected": true},
	} {
		body, _ := FormatToolStepResult(StepEvent{
			Kind:     StepKindToolCompleted,
			ToolName: "memories_add_ad_hoc_note",
			Input:    map[string]any{"note": note},
			Output:   map[string]any{"output": output},
		}, DefaultMaxFormattedBody)
		if body != "Memory tool returned an unreadable note result." {
			t.Fatalf("body=%q for output %#v", body, output)
		}
	}
}

func TestIntermediateToolSummaryAndBodyAppend(t *testing.T) {
	t.Parallel()
	note := "## Findings\n\n- Finding A\n- Finding B"
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "intermediate_tool",
		Input: map[string]any{
			"action":  "append",
			"content": note,
		},
		Output: map[string]any{"action": "append"},
	}
	if got := SummarizeToolStep(evt); got != "saved intermediate notes" {
		t.Fatalf("summary=%q", got)
	}
	body, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	// The Markdown notes are returned verbatim (no label prefix) so the TUI can
	// render them as Markdown.
	if body != note {
		t.Fatalf("body=%q want=%q", body, note)
	}
	meta := BuildToolMeta(evt)
	if meta.Invocation != "record intermediate note" {
		t.Fatalf("meta=%+v", meta)
	}
}

func TestIntermediateToolSummaryAndBodyRead(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "intermediate_tool",
		Input:    map[string]any{"action": "read"},
		Output: map[string]any{
			"action":  "read",
			"content": "Key takeaway 1\nKey takeaway 2",
		},
	}
	if got := SummarizeToolStep(evt); got != "reviewed intermediate notes" {
		t.Fatalf("summary=%q", got)
	}
	body, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if body != "Key takeaway 1\nKey takeaway 2" {
		t.Fatalf("body=%q", body)
	}
	meta := BuildToolMeta(evt)
	if meta.Invocation != "review intermediate notes" {
		t.Fatalf("meta=%+v", meta)
	}
}

func TestIntermediateToolSummaryAndBodyClear(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "intermediate_tool",
		Input:    map[string]any{"action": "clear"},
		Output:   map[string]any{"action": "clear"},
	}
	if got := SummarizeToolStep(evt); got != "cleared intermediate notes" {
		t.Fatalf("summary=%q", got)
	}
	body, trunc := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if body != "Cleared intermediate notes." {
		t.Fatalf("body=%q", body)
	}
}

func TestShellDisplayUsesRegisteredToolName(t *testing.T) {
	evt := StepEvent{
		Kind: StepKindToolCompleted, StepID: "step-1", ToolName: "shell",
		Input: map[string]any{"command": "git status --short"}, Output: map[string]any{"stdout": "ok\n", "exit_code": 0},
	}
	_, _ = FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if got := BuildToolMeta(evt).ToolName; got != "shell" {
		t.Fatalf("tool name = %q, want shell", got)
	}
}

func TestRetrieveOutputDisplayUsesSemanticInputAndMarkdownBody(t *testing.T) {
	t.Parallel()
	input := map[string]any{
		"category": "shell",
		"id":       4,
		"query":    "internal/clifacade/chat_session_permission_state.go",
		"reason":   "Recover the relevant diff.",
		"top":      5,
	}
	bodySource := "# output 4 (`git diff`)\n\n**2 matched groups**\n\n- `130|first`\n- `142|second`"
	evt := StepEvent{
		Kind: event.RunEventToolCompleted, ToolName: "retrieve_output",
		Input: input, Output: map[string]any{"output": bodySource},
	}

	body, truncated := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if truncated || body != bodySource {
		t.Fatalf("body=%q truncated=%v, want verbatim Markdown", body, truncated)
	}
	if strings.Contains(body, "output:") {
		t.Fatalf("retrieve_output body kept the transport label: %q", body)
	}
	meta := BuildToolMeta(evt)
	for _, value := range []string{meta.Invocation, SummarizeToolStep(evt)} {
		if strings.Contains(value, "{") || strings.Contains(value, `"reason"`) {
			t.Fatalf("retrieve_output display exposed raw input JSON: %q", value)
		}
		for _, want := range []string{"saved output #4", `matching "internal/clifacade/chat_session_permission_state.go"`} {
			if !strings.Contains(value, want) {
				t.Fatalf("display %q missing %q", value, want)
			}
		}
	}

	started := evt
	started.Kind = event.RunEventToolStarted
	started.Output = nil
	if summary := SummarizeToolStep(started); !strings.Contains(summary, "running retrieve saved output #4") || strings.Contains(summary, "{") {
		t.Fatalf("started summary is not semantic: %q", summary)
	}
}

// A quoted memory line is shown as the bytes the file holds.
//
// The body is Markdown, and a memory file is full of characters Markdown reads
// as markup. The path "/srv/chatibs_claw_client" is the case that gave it away:
// its two underscores were read as emphasis and removed, and the card quoted
// "/srv/chatibsclawclient" — a path that exists nowhere — as though the file
// said it. Everything but the mark is therefore escaped, so a renderer prints
// the line and not its own reading of it.
func TestMemorySearchQuotesTheLineVerbatim(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "an identifier keeps its underscores",
			content: "cwd: /srv/chatibs_claw_client",
			want:    `> cwd: /srv/chatibs\_claw\_client`,
		},
		{
			name:    "a heading line stays a line that begins with hashes",
			content: "## User preferences",
			want:    `> \## User preferences`,
		},
		{
			name:    "a list line stays a line that begins with a hyphen",
			content: "- always commit",
			want:    `> \- always commit`,
		},
		{
			name:    "a numbered line keeps its number",
			content: "1. first",
			want:    `> 1\. first`,
		},
		{
			name:    "emphasis, code and table characters are text",
			content: "a *b* `c` <d> |e| ~f~ [g]",
			want:    "> a \\*b\\* \\`c\\` \\<d> \\|e\\| \\~f\\~ \\[g\\]",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := map[string]any{"queries": []any{"nothing-matches-here"}, "matches": []any{map[string]any{
				"path": "MEMORY.md", "match_line_number": float64(1), "content": test.content,
				"matched_terms": []any{"nothing-matches-here"},
			}}}
			payload, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := FormatToolStepResult(StepEvent{
				Kind: StepKindToolCompleted, ToolName: "memories_search",
				Input:  map[string]any{"queries": []any{"nothing-matches-here"}},
				Output: map[string]any{"output": string(payload)},
			}, DefaultMaxFormattedBody)
			if !strings.Contains(got, test.want) {
				t.Fatalf("quoted line = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

// One line of the file is one line of the quote. Without an explicit break a
// renderer folds the quoted window into a single paragraph, and a window of
// several lines comes back run together with spaces where the file had
// newlines — which is not what the file says.
func TestMemorySearchKeepsEachQuotedLineOnItsOwnLine(t *testing.T) {
	got, _ := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "memories_search",
		Input:    map[string]any{"queries": []any{"deploy"}},
		Output:   map[string]any{"output": `{"queries":["deploy"],"matches":[{"path":"MEMORY.md","match_line_number":9,"content":"before\ndeploy here\nafter"}],"truncated":false}`},
	}, DefaultMaxFormattedBody)
	want := "> before\\\n> **deploy** here\\\n> after"
	if !strings.Contains(got, want) {
		t.Fatalf("quoted window = %q, want it to contain %q", got, want)
	}
}

// The header names the file a search was scoped to and nothing else: the
// window around each hit is the search's own, so no argument is left for the
// header to report.
func TestMemorySearchHeaderNamesTheScopeItSearched(t *testing.T) {
	for name, test := range map[string]struct {
		input map[string]any
		want  string
	}{
		"whole store": {input: map[string]any{"queries": []any{"deploy"}}, want: `searched memories "deploy"`},
		"scoped":      {input: map[string]any{"queries": []any{"deploy"}, "path": "MEMORY.md"}, want: `searched memories "deploy" in MEMORY.md`},
	} {
		t.Run(name, func(t *testing.T) {
			got := SummarizeToolStep(StepEvent{Kind: StepKindToolCompleted, ToolName: "memories_search", Input: test.input})
			if got != test.want {
				t.Fatalf("summary = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildToolMetaCarriesReadFileResultLines(t *testing.T) {
	t.Parallel()
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "read_file",
		Input: map[string]any{
			"file_path": "/repo/big.py",
			"offset":    float64(0),
			"limit":     float64(220),
		},
		Output: map[string]any{
			"preview_text": "1|a\n2|b",
			"result_lines": 200,
			"offset":       0,
			"total_lines":  232,
		},
	}
	meta := BuildToolMeta(evt)
	if meta.ResultLines != 200 || meta.ResultOffset != 0 {
		t.Fatalf("result facts = (%d, %d), want (200, 0)", meta.ResultLines, meta.ResultOffset)
	}

	paged := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "read_file",
		Output: map[string]any{
			"preview_text": "201|a",
			"result_lines": 200,
			"offset":       200,
		},
	}
	meta = BuildToolMeta(paged)
	if meta.ResultLines != 200 || meta.ResultOffset != 200 {
		t.Fatalf("paged result facts = (%d, %d), want (200, 200)", meta.ResultLines, meta.ResultOffset)
	}

	// A step without a paging result carries no line facts.
	plain := BuildToolMeta(StepEvent{Kind: event.RunEventToolCompleted, ToolName: "shell"})
	if plain.ResultLines != 0 || plain.ResultOffset != 0 {
		t.Fatalf("non-paging meta carries line facts: %+v", plain)
	}
}

func TestFormatLSPStep(t *testing.T) {
	t.Parallel()
	resultText := "definition of `Run`: 2 results in 2 files\nmain.go\n  12:6  func Run()"
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "lsp",
		Input:    map[string]any{"operation": "definition", "file_path": "main.go", "line": 12, "symbol": "Run"},
		Output: map[string]any{
			"output":       resultText,
			"operation":    "definition",
			"result_count": 2,
		},
	}
	body, truncated := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if truncated {
		t.Fatalf("body truncated: %q", body)
	}
	want := "```text\n" + resultText + "\n```"
	if body != want {
		t.Fatalf("body = %q, want fenced result text %q", body, want)
	}
	if summary := SummarizeToolStep(evt); summary != "looked up definition · 2 results" {
		t.Fatalf("summary = %q, want %q", summary, "looked up definition · 2 results")
	}

	single := evt
	single.Output = map[string]any{"output": "symbols in main.go: 1", "operation": "document_symbols", "result_count": 1}
	if summary := SummarizeToolStep(single); summary != "looked up document_symbols · 1 result" {
		t.Fatalf("singular summary = %q, want %q", summary, "looked up document_symbols · 1 result")
	}

	// A step with no captured text renders no body rather than an empty fence.
	empty := evt
	empty.Output = map[string]any{"operation": "hover", "result_count": 0}
	if body, _ := FormatToolStepResult(empty, DefaultMaxFormattedBody); body != "" {
		t.Fatalf("empty-output body = %q, want empty", body)
	}
	if summary := SummarizeToolStep(empty); summary != "looked up hover · 0 results" {
		t.Fatalf("empty summary = %q, want %q", summary, "looked up hover · 0 results")
	}
}

func TestLSPDiagnosticsSection(t *testing.T) {
	t.Parallel()
	live := event.LSPDiagnosticsSummary{
		New:   2,
		Files: 1,
		Items: []event.LSPDiagnostic{{
			Path: "internal/foo.go", Line: 12, Column: 6, Severity: "error",
			Source: "gopls", Code: "syntax", Message: "missing return",
		}, {
			Path: "internal/foo.go", Line: 30, Column: 1, Severity: "warning",
			Message: "unused variable",
		}},
	}
	liveOut := map[string]any{"status": "ok", "lsp_diagnostics": live}
	// A replayed step carries the same summary as its JSON equivalent.
	b, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(b, &asMap); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	replayedOut := map[string]any{"status": "ok", "lsp_diagnostics": asMap}

	liveEvt := StepEvent{Kind: event.RunEventToolCompleted, ToolName: "edit_file", Output: liveOut}
	replayedEvt := StepEvent{Kind: event.RunEventToolCompleted, ToolName: "edit_file", Output: replayedOut}
	liveBody, trunc := FormatToolStepResult(liveEvt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	replayedBody, trunc := FormatToolStepResult(replayedEvt, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if liveBody != replayedBody {
		t.Fatalf("live body %q must equal replayed body %q", liveBody, replayedBody)
	}
	for _, want := range []string{
		"lsp diagnostics: 2 new in 1 file",
		"internal/foo.go",
		"  error 12:6 missing return [gopls syntax]",
		"  warning 30:1 unused variable",
	} {
		if !strings.Contains(liveBody, want) {
			t.Fatalf("body missing %q: %q", want, liveBody)
		}
	}
	if strings.Contains(liveBody, "lsp_diagnostics") {
		t.Fatalf("body must not leak the lsp_diagnostics key into the JSON dump: %q", liveBody)
	}

	// Only pending: the header says so and every pending file gets its line.
	pending := event.LSPDiagnosticsSummary{PendingFiles: []string{"slow.go"}}
	pendingBody := lspDiagnosticsSection(map[string]any{"lsp_diagnostics": pending})
	want := "lsp diagnostics: pending\n\n```text\ndiagnostics for slow.go are still being computed and will follow\n```"
	if pendingBody != want {
		t.Fatalf("pending-only section = %q, want %q", pendingBody, want)
	}

	// Nothing to report renders no section at all.
	if got := lspDiagnosticsSection(map[string]any{"lsp_diagnostics": event.LSPDiagnosticsSummary{}}); got != "" {
		t.Fatalf("empty summary section = %q, want empty", got)
	}
	if got := lspDiagnosticsSection(map[string]any{}); got != "" {
		t.Fatalf("absent summary section = %q, want empty", got)
	}
}

// SubagentTaskTitle is the one derivation of a dispatched task's name; the
// table pins the rule every surface inherits by calling it.
func TestSubagentTaskTitle(t *testing.T) {
	long := strings.Repeat("界", 200) // 600 bytes of one rune
	truncated := SubagentTaskTitle("", long)
	for _, tc := range []struct {
		name   string
		title  string
		prompt string
		want   string
	}{
		{name: "title given", title: "Goal check", prompt: "check the goal", want: "Goal check"},
		{name: "title whitespace trimmed", title: "  Goal check \t", prompt: "check the goal", want: "Goal check"},
		{name: "no title takes prompt's first line", prompt: "first line of the job\nsecond line", want: "first line of the job"},
		{name: "no title and crlf prompt", prompt: "first line\r\nsecond line", want: "first line"},
		{name: "overlong prompt truncated on a rune boundary", prompt: long, want: truncated},
		{name: "both empty", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SubagentTaskTitle(tc.title, tc.prompt)
			if got != tc.want {
				t.Fatalf("SubagentTaskTitle(%q, %q) = %q, want %q", tc.title, tc.prompt, got, tc.want)
			}
		})
	}
	// The truncation itself: content within the byte budget plus the
	// ellipsis, never mid-rune.
	if n := len(strings.TrimSuffix(truncated, "…")); n > SubagentTaskTitleMaxBytes {
		t.Fatalf("truncated title keeps %d bytes, over the %d bound: %q", n, SubagentTaskTitleMaxBytes, truncated)
	}
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncated title split a rune: %q", truncated)
	}
	if !strings.HasSuffix(truncated, "…") {
		t.Fatalf("truncated title must end in the ellipsis: %q", truncated)
	}
	// A title is used as given even when the prompt is longer.
	if got := SubagentTaskTitle("short", long); got != "short" {
		t.Fatalf("title must win over the prompt, got %q", got)
	}
}

// SubagentCallFromStep is the one derivation of a subagent_* call's card
// facts; the table walks every tool at its two states — input only, and the
// settled result the model received — including the execution clock each
// result does and does not carry.
func TestSubagentCallFromStep(t *testing.T) {
	t.Parallel()
	const started = event.RunEventToolStarted
	const completed = event.RunEventToolCompleted
	mustJSON := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	// One HistoryEntry as the record-bearing tools return it.
	record := func(status string, finished int64) map[string]any {
		return map[string]any{
			"agent_id": "agent-7", "agent_kind": "typed", "task_id": "task-7",
			"run_id": "run-7", "parent_run_id": "parent-1", "session_id": "session-1",
			"worker_session_id": "worker-1", "task_index": 2, "execution_id": "exec-7",
			"title": "Extend the ledger", "task": "the whole brief",
			"status": status, "error": "", "started_at": 1700000000,
			"updated_at": finished, "finished_at": finished,
			"agent_type": "general-purpose", "runtime_kind": "typed_subagent",
		}
	}
	failedRecord := func() map[string]any {
		m := record("failed", 1700000090)
		m["error"] = "provider refused the request"
		return m
	}
	runInput := map[string]any{"title": "Fix the parser", "task": "the whole prompt", "subagent_type": "explore"}
	sendInput := map[string]any{"title": "Async fix", "task": "the whole prompt", "subagent_type": "general-purpose"}
	queryInput := func() map[string]any { return map[string]any{"task_id": "task-7"} }
	fanoutInput := map[string]any{
		"max_parallel": 2,
		"tasks": []any{
			map[string]any{"title": "First", "prompt": "brief one", "subagent_type": "explore"},
			map[string]any{"prompt": "untitled prompt first line\nsecond line", "subagent_type": "general-purpose"},
			map[string]any{"title": "Third", "prompt": "brief three"},
		},
	}
	toolCall := func(kind, tool string, input map[string]any, result string) StepEvent {
		evt := StepEvent{Kind: kind, ToolName: tool, Input: input}
		if result != "" {
			evt.Output = map[string]any{"output": result}
		}
		return evt
	}
	cases := []struct {
		name string
		evt  StepEvent
		want *event.SubagentCall
	}{
		{
			name: "run started",
			evt:  toolCall(started, "subagent_run", runInput, ""),
			want: &event.SubagentCall{Verb: "run", Tasks: []event.SubagentCallTask{{
				Index: 0, Title: "Fix the parser", AgentType: "explore", Status: "waiting",
			}}},
		},
		{
			name: "run ok",
			evt: toolCall(completed, "subagent_run", runInput, mustJSON(map[string]any{
				"agent_id": "agent-9", "task_id": "task-9", "run_id": "run-9", "parent_run_id": "parent-1",
				"session_id": "session-1", "query_source": "q", "status": "ok", "output": "answer",
				"finished_at": 1700000060, "agent_type": "explore", "agent_kind": "typed",
				"runtime_kind": "typed_subagent", "definition_source": "built-in",
				"one_shot": false, "continuable": true,
			})),
			want: &event.SubagentCall{Verb: "run", Tasks: []event.SubagentCallTask{{
				Index: 0, Key: "task-9", Title: "Fix the parser", AgentType: "explore",
				Status: "done", ExecutionID: "run-9", FinishedAt: 1700000060,
			}}},
		},
		{
			name: "run skipped",
			evt:  toolCall(completed, "subagent_run", runInput, mustJSON(map[string]any{"status": "skipped", "error": "skipped: empty prompt"})),
			want: &event.SubagentCall{Verb: "run", Tasks: []event.SubagentCallTask{{
				Index: 0, Title: "Fix the parser", AgentType: "explore",
				Status: "skipped", Error: "skipped: empty prompt",
			}}},
		},
		{
			name: "fanout started",
			evt:  toolCall(started, "subagent_fanout", fanoutInput, ""),
			want: &event.SubagentCall{Verb: "run", Tasks: []event.SubagentCallTask{
				{Index: 0, Title: "First", AgentType: "explore", Status: "waiting"},
				{Index: 1, Title: "untitled prompt first line", AgentType: "general-purpose", Status: "waiting"},
				{Index: 2, Title: "Third", AgentType: "fork", Status: "waiting"},
			}},
		},
		{
			name: "fanout settled",
			evt: toolCall(completed, "subagent_fanout", fanoutInput, mustJSON(map[string]any{
				"summary": map[string]any{"total": 3, "succeed": 1, "failed": 2, "finished": 1700000060},
				"results": []any{
					map[string]any{"index": 0, "task": "brief one", "subagent_type": "explore", "output": "done", "ok": true},
					map[string]any{"index": 1, "task": "untitled", "error": "skipped: empty prompt", "ok": false},
					map[string]any{"index": 2, "task": "brief three", "error": "provider refused", "ok": false},
				},
			})),
			want: &event.SubagentCall{Verb: "run", Tasks: []event.SubagentCallTask{
				{Index: 0, Title: "First", AgentType: "explore", Status: "done"},
				{Index: 1, Title: "untitled prompt first line", AgentType: "general-purpose", Status: "skipped", Error: "skipped: empty prompt"},
				{Index: 2, Title: "Third", AgentType: "fork", Status: "failed", Error: "provider refused"},
			}},
		},
		{
			name: "fanout skipped by fail_fast",
			evt: toolCall(completed, "subagent_fanout", fanoutInput, mustJSON(map[string]any{
				"summary": map[string]any{"total": 3, "succeed": 0, "failed": 3, "finished": 1700000060},
				"results": []any{
					map[string]any{"index": 0, "task": "brief one", "error": "skipped due to fail_fast", "ok": false},
				},
			})),
			want: &event.SubagentCall{Verb: "run", Tasks: []event.SubagentCallTask{
				{Index: 0, Title: "First", AgentType: "explore", Status: "skipped", Error: "skipped due to fail_fast"},
				{Index: 1, Title: "untitled prompt first line", AgentType: "general-purpose", Status: "waiting"},
				{Index: 2, Title: "Third", AgentType: "fork", Status: "waiting"},
			}},
		},
		{
			name: "send started",
			evt:  toolCall(started, "subagent_send", sendInput, ""),
			want: &event.SubagentCall{Verb: "send", Tasks: []event.SubagentCallTask{{
				Index: 0, Title: "Async fix", AgentType: "general-purpose", Status: "waiting",
			}}},
		},
		{
			name: "send accepted",
			evt: toolCall(completed, "subagent_send", sendInput, mustJSON(map[string]any{
				"agent_id": "agent-8", "task_id": "task-8", "run_id": "run-8", "parent_run_id": "parent-1",
				"session_id": "session-1", "worker_session_id": "worker-8", "query_source": "q",
				"status": "running", "started_at": 1700000000, "agent_kind": "typed",
				"agent_type": "general-purpose", "runtime_kind": "typed_subagent",
			})),
			want: &event.SubagentCall{Verb: "send", Tasks: []event.SubagentCallTask{{
				Index: 0, Key: "task-8", Title: "Async fix", AgentType: "general-purpose",
				Status: "running", ExecutionID: "run-8", StartedAt: 1700000000,
			}}},
		},
		{
			name: "send skipped",
			evt:  toolCall(completed, "subagent_send", sendInput, mustJSON(map[string]any{"status": "skipped", "error": "skipped: empty prompt"})),
			want: &event.SubagentCall{Verb: "send", Tasks: []event.SubagentCallTask{{
				Index: 0, Title: "Async fix", AgentType: "general-purpose",
				Status: "skipped", Error: "skipped: empty prompt",
			}}},
		},
		{
			name: "continue started",
			evt:  toolCall(started, "subagent_continue", map[string]any{"task_id": "task-7", "message": "go on"}, ""),
			want: &event.SubagentCall{Verb: "continue", Tasks: []event.SubagentCallTask{{Index: 0, Key: "task-7"}}},
		},
		{
			name: "continue settled",
			evt:  toolCall(completed, "subagent_continue", map[string]any{"task_id": "task-7", "message": "go on"}, mustJSON(map[string]any{"record": record("ok", 1700000060)})),
			want: &event.SubagentCall{Verb: "continue", Tasks: []event.SubagentCallTask{{
				Index: 0, Key: "task-7", Title: "Extend the ledger", AgentType: "general-purpose",
				Status: "done", ExecutionID: "exec-7", StartedAt: 1700000000, FinishedAt: 1700000060,
			}}},
		},
		{
			name: "status started",
			evt:  toolCall(started, "subagent_status", queryInput(), ""),
			want: &event.SubagentCall{Verb: "status", Tasks: []event.SubagentCallTask{{Index: 0, Key: "task-7"}}},
		},
		{
			name: "status settled is the record itself",
			evt:  toolCall(completed, "subagent_status", queryInput(), mustJSON(record("running", 0))),
			want: &event.SubagentCall{Verb: "status", Tasks: []event.SubagentCallTask{{
				Index: 0, Key: "task-7", Title: "Extend the ledger", AgentType: "general-purpose",
				Status: "running", ExecutionID: "exec-7", StartedAt: 1700000000,
			}}},
		},
		{
			name: "wait started",
			evt:  toolCall(started, "subagent_wait", queryInput(), ""),
			want: &event.SubagentCall{Verb: "wait", Tasks: []event.SubagentCallTask{{Index: 0, Key: "task-7"}}},
		},
		{
			name: "wait timed out still running",
			evt:  toolCall(completed, "subagent_wait", queryInput(), mustJSON(map[string]any{"record": record("running", 0), "timed_out": true})),
			want: &event.SubagentCall{Verb: "wait", Tasks: []event.SubagentCallTask{{
				Index: 0, Key: "task-7", Title: "Extend the ledger", AgentType: "general-purpose",
				Status: "running", TimedOut: true, ExecutionID: "exec-7", StartedAt: 1700000000,
			}}},
		},
		{
			name: "wait ended within the timeout",
			evt:  toolCall(completed, "subagent_wait", queryInput(), mustJSON(map[string]any{"record": failedRecord()})),
			want: &event.SubagentCall{Verb: "wait", Tasks: []event.SubagentCallTask{{
				Index: 0, Key: "task-7", Title: "Extend the ledger", AgentType: "general-purpose",
				Status: "failed", Error: "provider refused the request",
				ExecutionID: "exec-7", StartedAt: 1700000000, FinishedAt: 1700000090,
			}}},
		},
		{
			name: "close started",
			evt:  toolCall(started, "subagent_close", queryInput(), ""),
			want: &event.SubagentCall{Verb: "close", Tasks: []event.SubagentCallTask{{Index: 0, Key: "task-7"}}},
		},
		{
			name: "close with record",
			evt:  toolCall(completed, "subagent_close", queryInput(), mustJSON(map[string]any{"status": "cancel_requested", "record": record("running", 0)})),
			want: &event.SubagentCall{Verb: "close", Tasks: []event.SubagentCallTask{{
				Index: 0, Key: "task-7", Title: "Extend the ledger", AgentType: "general-purpose",
				Status: "running", StopRequested: true, ExecutionID: "exec-7", StartedAt: 1700000000,
			}}},
		},
		{
			name: "close without record keeps the input key",
			evt:  toolCall(completed, "subagent_close", queryInput(), mustJSON(map[string]any{"status": "cancel_requested"})),
			want: &event.SubagentCall{Verb: "close", Tasks: []event.SubagentCallTask{{Index: 0, Key: "task-7", StopRequested: true}}},
		},
		{
			name: "list started",
			evt:  toolCall(started, "subagent_list", map[string]any{"limit": 5}, ""),
			want: &event.SubagentCall{Verb: "list"},
		},
		{
			name: "list settled",
			evt: toolCall(completed, "subagent_list", map[string]any{"limit": 5}, mustJSON(map[string]any{
				"records": []any{record("ok", 1700000060), failedRecord()},
			})),
			want: &event.SubagentCall{Verb: "list", Tasks: []event.SubagentCallTask{
				{
					Index: 0, Key: "task-7", Title: "Extend the ledger", AgentType: "general-purpose",
					Status: "done", ExecutionID: "exec-7", StartedAt: 1700000000, FinishedAt: 1700000060,
				},
				{
					Index: 1, Key: "task-7", Title: "Extend the ledger", AgentType: "general-purpose",
					Status: "failed", Error: "provider refused the request",
					ExecutionID: "exec-7", StartedAt: 1700000000, FinishedAt: 1700000090,
				},
			}},
		},
		{
			name: "list empty",
			evt:  toolCall(completed, "subagent_list", map[string]any{"limit": 5}, mustJSON(map[string]any{"records": []any{}})),
			want: &event.SubagentCall{Verb: "list", Tasks: []event.SubagentCallTask{}},
		},
		{
			name: "fork dispatch types itself fork",
			evt:  toolCall(started, "subagent_run", map[string]any{"task": "fork line one\nmore"}, ""),
			want: &event.SubagentCall{Verb: "run", Tasks: []event.SubagentCallTask{{
				Index: 0, Title: "fork line one", AgentType: "fork", Status: "waiting",
			}}},
		},
		{
			name: "call error fails every task without its own reason",
			evt:  StepEvent{Kind: completed, ToolName: "subagent_fanout", Input: fanoutInput, Error: "boom"},
			want: &event.SubagentCall{Verb: "run", Tasks: []event.SubagentCallTask{
				{Index: 0, Title: "First", AgentType: "explore", Status: "failed"},
				{Index: 1, Title: "untitled prompt first line", AgentType: "general-purpose", Status: "failed"},
				{Index: 2, Title: "Third", AgentType: "fork", Status: "failed"},
			}},
		},
		{
			name: "result in an unknown shape leaves the status unsaid",
			evt:  toolCall(completed, "subagent_status", queryInput(), "not json at all"),
			want: &event.SubagentCall{Verb: "status", Tasks: []event.SubagentCallTask{{Index: 0, Key: "task-7"}}},
		},
		{
			name: "result object without the tool's shape leaves the status unsaid",
			evt:  toolCall(completed, "subagent_wait", queryInput(), mustJSON(map[string]any{"something": "else"})),
			want: &event.SubagentCall{Verb: "wait", Tasks: []event.SubagentCallTask{{Index: 0, Key: "task-7"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SubagentCallFromStep(tc.evt)
			if !ok {
				t.Fatalf("SubagentCallFromStep said no for %q", tc.evt.ToolName)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("facts =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
	if _, ok := SubagentCallFromStep(StepEvent{Kind: completed, ToolName: "read_file"}); ok {
		t.Fatal("a non-subagent tool must have no SubagentCall")
	}
}

// Every subagent_* tool result carries a dispatch prompt or a full record
// (task text, output) the model needs but a card must never show: the prompt
// and the subagent's answer belong to the subagent's own view, and no surface
// may render raw JSON. This pins the body, the summary and the invocation
// label of all eight tools to facts only.
func TestSubagentLifecycleToolsNeverShowJSON(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-PROMPT-TEXT"
	mustJSON := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		return string(b)
	}
	// One realistic HistoryEntry as the tools return it (subagent_status is
	// the bare record; wait/continue/close wrap it; list wraps several).
	record := map[string]any{
		"agent_id": "agent-1", "agent_kind": "typed", "task_id": "task-1",
		"run_id": "run-1", "parent_run_id": "parent-1", "session_id": "session-1",
		"worker_session_id": "worker-1", "task_index": 2, "execution_id": "exec-1",
		"title": "Check the exports", "task": secret + "\nsecond line of the brief",
		"status": "ok", "output": "found auth.go:12", "started_at": 1700000000,
		"updated_at": 1700000060, "finished_at": 1700000060,
		"agent_type": "general-purpose", "runtime_kind": "typed_subagent",
	}
	cases := []struct {
		tool  string
		input map[string]any
		// result is the JSON string the tool returned to the model.
		result string
	}{
		{
			tool: "subagent_send",
			input: map[string]any{
				"title": "Ship the card facts", "task": secret,
				"subagent_type": "general-purpose",
			},
			result: mustJSON(map[string]any{
				"agent_id": "agent-1", "task_id": "task-1", "run_id": "run-1",
				"parent_run_id": "parent-1", "session_id": "session-1",
				"worker_session_id": "worker-1", "query_source": "agent:builtin:typed",
				"status": "running", "started_at": 1700000000, "agent_kind": "typed",
				"agent_type": "general-purpose", "runtime_kind": "typed_subagent",
			}),
		},
		{
			tool:   "subagent_status",
			input:  map[string]any{"task_id": "task-1"},
			result: mustJSON(record),
		},
		{
			tool:   "subagent_wait",
			input:  map[string]any{"task_id": "task-1", "timeout_ms": 5000},
			result: mustJSON(map[string]any{"record": record, "timed_out": true}),
		},
		{
			tool:   "subagent_continue",
			input:  map[string]any{"task_id": "task-1", "message": "keep going"},
			result: mustJSON(map[string]any{"record": record}),
		},
		{
			tool:   "subagent_close",
			input:  map[string]any{"task_id": "task-1"},
			result: mustJSON(map[string]any{"status": "cancel_requested", "record": record}),
		},
		{
			tool:   "subagent_list",
			input:  map[string]any{"limit": 5},
			result: mustJSON(map[string]any{"records": []any{record}}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			evt := StepEvent{
				Kind:     event.RunEventToolCompleted,
				ToolName: tc.tool,
				Input:    tc.input,
				Output:   map[string]any{"output": tc.result},
			}
			body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
			if strings.Contains(body, "{") || strings.Contains(body, `"agent_id"`) || strings.Contains(body, secret) {
				t.Fatalf("%s body leaked raw facts: %q", tc.tool, body)
			}
			summary := SummarizeToolStep(evt)
			if strings.Contains(summary, "{") || strings.Contains(summary, secret) {
				t.Fatalf("%s summary leaked raw facts: %q", tc.tool, summary)
			}
			invocation := BuildToolMeta(evt).Invocation
			if strings.Contains(invocation, "{") || strings.Contains(invocation, secret) {
				t.Fatalf("%s invocation leaked raw facts: %q", tc.tool, invocation)
			}
		})
	}

	// The dispatching tools put the whole prompt in their result and their
	// input; the card body may name the task, never quote the prompt.
	dispatched := []struct {
		tool  string
		input map[string]any
	}{
		{
			tool: "subagent_run",
			input: map[string]any{
				"title": "One blocking task", "task": secret,
				"subagent_type": "general-purpose",
			},
		},
		{
			tool: "subagent_fanout",
			input: map[string]any{
				"max_parallel": 2,
				"tasks": []any{
					map[string]any{"title": "First", "prompt": secret, "subagent_type": "explore"},
					map[string]any{"title": "Second", "prompt": secret, "subagent_type": "explore"},
				},
			},
		},
	}
	for _, tc := range dispatched {
		t.Run(tc.tool, func(t *testing.T) {
			evt := StepEvent{
				Kind:     event.RunEventToolCompleted,
				ToolName: tc.tool,
				Input:    tc.input,
				Output: map[string]any{"output": mustJSON(map[string]any{
					"summary": map[string]any{"total": 1, "succeed": 1, "failed": 0, "finished": 1700000060},
					"results": []any{map[string]any{"index": 0, "task": secret, "subagent_type": "explore", "output": "done", "ok": true}},
				})},
			}
			body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
			if strings.Contains(body, secret) {
				t.Fatalf("%s body leaked the dispatch prompt: %q", tc.tool, body)
			}
		})
	}
}

// trimBlankEdgeLines is the boundary-trim used on every multi-line tool
// payload. It must drop only the blank lines at either edge plus the last
// content line's trailing whitespace, and leave every content line otherwise
// byte-for-byte intact — most importantly the FIRST line's own indent.
func TestTrimBlankEdgeLines(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"keeps first-line indent", "  GET x\n  POST y\n", "  GET x\n  POST y"},
		{"drops blank edge lines", "\n\n  GET x\n\n\n", "  GET x"},
		{"trims last-line trailing space", "  GET x   \n", "  GET x"},
		{"keeps interior blank line", "a\n\nb", "a\n\nb"},
		{"all blank becomes empty", "   \n\t\n", ""},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trimBlankEdgeLines(tc.in); got != tc.want {
				t.Fatalf("trimBlankEdgeLines(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The reported bug: the shell card's output body dropped the leading spaces of
// its first line, so a grep'd route table started two columns left of every
// later row. The payload here is byte-for-byte the gateway startup banner
// ("  %-8s%s" in pkg/gateway/serve_run.go).
func TestFormatShellStepKeepsFirstLineIndent(t *testing.T) {
	t.Parallel()
	routeTable := "  GET     /api/chat/sessions\n" +
		"  POST    /api/chat/sessions\n" +
		"  GET     /api/chat/sessions/:id\n"
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Input:    map[string]any{"command": "grep -E '/api/chat' gw.log"},
		Output:   map[string]any{"stdout": routeTable, "exit_code": 0},
	}
	body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	wantFence := "```text\n" +
		"  GET     /api/chat/sessions\n" +
		"  POST    /api/chat/sessions\n" +
		"  GET     /api/chat/sessions/:id\n```"
	if !strings.Contains(body, wantFence) {
		t.Fatalf("stdout fence lost the first line's indent:\n%q", body)
	}
	assertPayloadLinesShareColumn(t, body, "/api/chat/sessions", 3)

	// Same shape on stderr.
	stderrEvt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Input:    map[string]any{"command": "routing-probe"},
		Output:   map[string]any{"stderr": routeTable, "exit_code": 1},
	}
	stderrBody, _ := FormatToolStepResult(stderrEvt, DefaultMaxFormattedBody)
	if !strings.Contains(stderrBody, wantFence) {
		t.Fatalf("stderr fence lost the first line's indent:\n%q", stderrBody)
	}
	assertPayloadLinesShareColumn(t, stderrBody, "/api/chat/sessions", 3)
}

// Every multi-line payload that is drawn line by line hit the same TrimSpace
// defect: only the first line lost its indent. One case per surface.
func TestToolBodiesKeepFirstLineIndent(t *testing.T) {
	t.Parallel()
	const payload = "  MARKER_ONE\n  MARKER_TWO\n"
	const want = "  MARKER_ONE\n  MARKER_TWO"
	cases := []struct {
		name     string
		toolName string
		input    map[string]any
		output   map[string]any
	}{
		{"shell stdout", "shell", map[string]any{"command": "probe"}, map[string]any{"stdout": payload}},
		{"shell stderr", "shell", map[string]any{"command": "probe"}, map[string]any{"stderr": payload}},
		{"generic stdout_preview", "generic_tool", nil, map[string]any{"stdout_preview": payload}},
		{"generic preview_text", "generic_tool", nil, map[string]any{"preview_text": payload}},
		{"lsp output", "lsp", map[string]any{"operation": "references"}, map[string]any{"output": payload}},
		{"retrieve_output body", "retrieve_output", nil, map[string]any{"output": payload}},
		{"intermediate_tool content", "intermediate_tool", map[string]any{"content": payload}, map[string]any{"content": payload}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evt := StepEvent{
				Kind:     event.RunEventToolCompleted,
				ToolName: tc.toolName,
				Input:    tc.input,
				Output:   tc.output,
			}
			body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
			if !strings.Contains(body, want) {
				t.Fatalf("payload first line lost its indent:\n%q", body)
			}
		})
	}
}

// assertPayloadLinesShareColumn checks that every line containing marker puts
// it at the same column — the aligned-table property the fix restores.
func assertPayloadLinesShareColumn(t *testing.T, body, marker string, wantLines int) {
	t.Helper()
	cols := []int{}
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, marker); i >= 0 {
			cols = append(cols, i)
		}
	}
	if len(cols) != wantLines {
		t.Fatalf("expected %d marker lines, got %d in:\n%q", wantLines, len(cols), body)
	}
	for _, c := range cols[1:] {
		if c != cols[0] {
			t.Fatalf("marker %q not column-aligned: %v in:\n%q", marker, cols, body)
		}
	}
}

// The exit-plan gate's wait paints no card on any surface: its approval prompt
// is the whole wait, and a canceled gate was abandoned to that same wait. The
// settled states still paint.
func TestToolStepHoldsNoCard(t *testing.T) {
	for _, status := range []string{"running", "awaiting approval", "canceled"} {
		if !ToolStepHoldsNoCard("exit_plan_mode", status) {
			t.Fatalf("exit_plan_mode/%q must hold no card", status)
		}
	}
	for _, tc := range []struct{ tool, status string }{
		{"exit_plan_mode", "denied"},
		{"exit_plan_mode", "completed"},
		{"exit_plan_mode", "failed"},
		{"shell", "running"},
		{"shell", "awaiting approval"},
		{"", "running"},
	} {
		if ToolStepHoldsNoCard(tc.tool, tc.status) {
			t.Fatalf("%s/%q paints its card", tc.tool, tc.status)
		}
	}
}
