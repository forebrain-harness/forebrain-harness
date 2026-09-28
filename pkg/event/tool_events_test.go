package event

import "testing"

func TestClassifyToolError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "empty", msg: "", want: ToolErrorNone},
		{name: "permission", msg: "tool request previously denied", want: ToolErrorPermissionDenied},
		{name: "policy", msg: "classifier blocked: risky command", want: ToolErrorPolicyBlocked},
		{name: "auth", msg: "mcp server returned insufficient_scope", want: ToolErrorMCPAuth},
		{name: "session", msg: `{"error":{"code": -32001}} session expired`, want: ToolErrorMCPSessionExpired},
		{name: "validation", msg: "invalid tool arguments: json unmarshal failed", want: ToolErrorValidation},
		{name: "execution", msg: "open file: no such file", want: ToolErrorExecution},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyToolError(tt.msg); got != tt.want {
				t.Fatalf("ClassifyToolError(%q)=%q want %q", tt.msg, got, tt.want)
			}
		})
	}
}
