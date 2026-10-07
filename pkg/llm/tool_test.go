package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestSanitizeToolCallArguments(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{
			name: "empty string",
			args: "",
			want: "{}",
		},
		{
			name: "whitespace only",
			args: "   \n\t  ",
			want: "{}",
		},
		{
			name: "valid object",
			args: `{"action":"append","content":"hello"}`,
			want: `{"action":"append","content":"hello"}`,
		},
		{
			name: "valid array",
			args: `[1,2,3]`,
			want: `[1,2,3]`,
		},
		{
			name: "valid primitive",
			args: `"hello"`,
			want: `"hello"`,
		},
		{
			name: "malformed premature close - reproduces glm-5.2 bug",
			args: `{"action": "append", "items": ["a", "b"]}, "content": ""}`,
			want: "{}",
		},
		{
			name: "trailing garbage after valid object",
			args: `{"a":1}garbage`,
			want: "{}",
		},
		{
			name: "trailing comma after valid object",
			args: `{"a":1},`,
			want: "{}",
		},
		{
			name: "unclosed object",
			args: `{"a":1`,
			want: "{}",
		},
		{
			name: "valid object with nested JSON string",
			args: `{"action":"append","content":"{\"finding\":\"test\"}"}`,
			want: `{"action":"append","content":"{\"finding\":\"test\"}"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeToolCallArguments(tt.args)
			if got != tt.want {
				t.Errorf("SanitizeToolCallArguments(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestSanitizeToolCallArguments_ReproducesExactBug verifies the exact malformed
// arguments that caused the original BadRequest error from the glm-5.2 model.
func TestSanitizeToolCallArguments_ReproducesExactBug(t *testing.T) {
	// This is the exact malformed JSON from debug.log line 8439.
	// The LLM closed the main object after the array, then placed a second
	// key outside the object:
	//   {"action":"append","content":"...","items":[...]}
	//   , "content": ""}
	malformed := `{"action": "append", "content": "{\"finding\":\"WRAP ALGO MISMATCH\"}", "items": ["ROOT CAUSE: test", "EXAMPLE: test"]}, "content": ""}`

	got := SanitizeToolCallArguments(malformed)
	if got != "{}" {
		t.Errorf("expected malformed args to be sanitized to {}, got %q", got)
	}
}

type fanoutLikeTask struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
}

type fanoutLikeInput struct {
	Tasks       []fanoutLikeTask `json:"tasks"`
	MaxParallel int              `json:"max_parallel"`
	FailFast    bool             `json:"fail_fast,omitempty"`
}

func newFanoutLikeTool(t *testing.T, ran *bool) *Tool {
	t.Helper()
	tl, err := NewTool("subagent_fanout", "fan out", func(ctx context.Context, in *fanoutLikeInput) (string, error) {
		*ran = true
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// The shape glm-5.3 produced for subagent_fanout: partway into the JSON of
// the tasks array it fell back to its own <arg_key>/<arg_value> call markup,
// and the provider passed that text through as the arguments. The key string
// opened at "prompt then runs to the next bare quote, so decoding fails far
// from the real break.
const glmMarkupLeakArguments = `{"fail_fast":false,"max_parallel":2,"tasks":[{"prompt</arg_key><ac7a3bd7><arg_value><b88a6f17>You are the executor for plan 003.\n\nREAD FIRST: docs/plan/README.md — \"## rules\".\n\nNOTES: deviations, surprises,"title\": \"Execute plan 003\"}, {\"prompt\": \"You are the executor for plan 004.\"}]`

func TestNewToolMalformedArgumentsErrorLocatesTheBreak(t *testing.T) {
	ran := false
	tl := newFanoutLikeTool(t, &ran)

	_, err := tl.Execute(context.Background(), glmMarkupLeakArguments)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if ran {
		t.Fatal("handler ran on arguments that are not valid JSON")
	}
	var parseErr *ToolArgumentParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error %T is not a ToolArgumentParseError", err)
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("error does not unwrap to the decoder's SyntaxError: %v", err)
	}
	markupAt := strings.Index(glmMarkupLeakArguments, "</arg_key>") + 1
	msg := err.Error()
	for _, want := range []string{
		"not valid JSON — invalid character 't' after object key at byte " + strconv.FormatInt(syntaxErr.Offset, 10),
		`surprises,"title`,
		"tool-call markup </arg_key> was written into the JSON at byte " + strconv.Itoa(markupAt),
		`"tasks":[{"prompt</arg_key>`,
		"did not run and the call shows as {} in the conversation",
		"not a placeholder call",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message lacks %q:\n%s", want, msg)
		}
	}
	if strings.ContainsAny(msg, "\n\t") {
		t.Errorf("error message is not a single line:\n%s", msg)
	}
}

func TestNewToolTruncatedArgumentsErrorHasNoMarkupClaim(t *testing.T) {
	ran := false
	tl := newFanoutLikeTool(t, &ran)

	_, err := tl.Execute(context.Background(), `{"tasks":[{"title":"a","prompt":"line one\nline two"}],"max_parallel":2`)
	if err == nil || ran {
		t.Fatalf("err = %v, ran = %v; want a parse error before the handler", err, ran)
	}
	msg := err.Error()
	if !strings.Contains(msg, "not valid JSON — unexpected end of JSON input") {
		t.Errorf("error message does not name the truncation:\n%s", msg)
	}
	if strings.Contains(msg, "tool-call markup") {
		t.Errorf("error message claims markup that is not there:\n%s", msg)
	}
}

func TestNewToolTypeMismatchKeepsDecoderMessage(t *testing.T) {
	ran := false
	tl := newFanoutLikeTool(t, &ran)

	// Valid JSON is replayed to the model as sent, so the decoder's own
	// message is enough there; only unreadable JSON needs the location.
	_, err := tl.Execute(context.Background(), `{"tasks":[{"title":"a","prompt":"b"}],"max_parallel":"two"}`)
	if err == nil || ran {
		t.Fatalf("err = %v, ran = %v; want a parse error before the handler", err, ran)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "failed to parse arguments: json: cannot unmarshal string") || strings.Contains(msg, "not valid JSON") {
		t.Errorf("unexpected type-mismatch message:\n%s", msg)
	}
}
