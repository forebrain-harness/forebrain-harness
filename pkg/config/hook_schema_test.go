package config

import "testing"

func TestValidateHookCommandTypes(t *testing.T) {
	tests := []struct {
		name    string
		in      HookCommand
		wantErr bool
	}{
		{name: "command", in: HookCommand{Type: HookTypeCommand, Command: "echo ok"}, wantErr: false},
		{name: "prompt", in: HookCommand{Type: HookTypePrompt, Prompt: "check"}, wantErr: false},
		{name: "agent", in: HookCommand{Type: HookTypeAgent, Prompt: "verify"}, wantErr: false},
		{name: "http", in: HookCommand{Type: HookTypeHTTP, URL: "https://example.com/hook"}, wantErr: false},
		{name: "function not persisted", in: HookCommand{Type: "function"}, wantErr: true},
		{name: "empty command", in: HookCommand{Type: HookTypeCommand}, wantErr: true},
		{name: "bad condition", in: HookCommand{Type: HookTypeCommand, Command: "echo ok", If: "shell *"}, wantErr: true},
		{name: "condition", in: HookCommand{Type: HookTypeCommand, Command: "echo ok", If: "shell(git *)"}, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHookCommand(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateHookCommand() err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestLooksLikePermissionRule(t *testing.T) {
	if !LooksLikePermissionRule("tool(pattern *)") {
		t.Fatalf("expected permission rule")
	}
	if LooksLikePermissionRule("tool pattern *") {
		t.Fatalf("unexpected permission rule")
	}
}

func TestHookConditionMatchesToolAndInput(t *testing.T) {
	if !HookConditionMatches("shell(git *)", "shell", "git status") {
		t.Fatalf("expected shell condition to match command")
	}
	if HookConditionMatches("shell(git *)", "web_fetch", "git status") {
		t.Fatalf("condition must not match a different tool")
	}
	if HookConditionMatches("shell(git *)", "shell", "npm test") {
		t.Fatalf("condition must not match a different input")
	}
	if !HookConditionMatches("", "shell", "npm test") {
		t.Fatalf("empty condition should match")
	}
}

func TestValidateHookMatcherAndSettings(t *testing.T) {
	valid := HooksSettings{
		"PreToolUse": {{
			Matcher: "shell|write_file",
			Hooks: []HookCommand{
				{Type: HookTypeCommand, Command: "echo ok"},
			},
		}},
		"UserPromptSubmit": {{
			Hooks: []HookCommand{
				{Type: HookTypePrompt, Prompt: "check $ARGUMENTS"},
			},
		}},
		"PermissionRequest": {{
			Matcher: "shell",
			Hooks:   []HookCommand{{Type: HookTypeCommand, Command: "echo ok"}},
		}},
	}
	if err := ValidateHooksSettings(valid); err != nil {
		t.Fatalf("ValidateHooksSettings(valid) err=%v", err)
	}

	if err := ValidateHookMatcher(HookMatcher{}); err == nil {
		t.Fatal("ValidateHookMatcher empty error = nil")
	}
	if err := ValidateHooksSettings(HooksSettings{
		"UnknownEvent": {{
			Hooks: []HookCommand{{Type: HookTypeCommand, Command: "echo ok"}},
		}},
	}); err == nil {
		t.Fatal("ValidateHooksSettings unknown event error = nil")
	}
}
