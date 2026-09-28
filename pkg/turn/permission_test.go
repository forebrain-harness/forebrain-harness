package turn

import (
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestPermissionRuleCount(t *testing.T) {
	snapshot := safety.Snapshot{Rules: map[safety.PermissionSource]map[safety.PermissionBehavior][]safety.PermissionRuleValue{
		safety.SourceSession: {
			safety.BehaviorAllow: {{RuleContent: "git status"}},
			safety.BehaviorAsk:   {{RuleContent: "go test"}, {RuleContent: "go vet"}},
		},
	}}
	if got := PermissionRuleCount(snapshot); got != 3 {
		t.Fatalf("count = %d", got)
	}
}

func TestExecutePermissionExplain(t *testing.T) {
	usage := "usage"
	if got := ExecutePermissionExplain(nil, "s1", []string{"Bash"}, usage); got != "permissions: unavailable" {
		t.Fatalf("unavailable = %q", got)
	}
	facade := &stubPermissionFacade{explain: safety.ExplainResult{Decision: safety.Decision{Behavior: safety.BehaviorAllow}}}
	if got := ExecutePermissionExplain(facade, "s1", nil, usage); got != usage {
		t.Fatalf("usage = %q", got)
	}
	got := ExecutePermissionExplain(facade, "s1", []string{"Bash", "git", "status"}, usage)
	if !strings.Contains(got, "Permissions: Bash") {
		t.Fatalf("reply = %q", got)
	}
}
