package llm

import (
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
