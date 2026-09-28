package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
