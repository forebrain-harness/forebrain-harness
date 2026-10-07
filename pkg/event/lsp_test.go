package event

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLSPRecommendationChoiceValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		c    LSPRecommendationChoice
		want bool
	}{
		{name: "enable", c: LSPChoiceEnable, want: true},
		{name: "install", c: LSPChoiceInstall, want: true},
		{name: "not now", c: LSPChoiceNotNow, want: true},
		{name: "never", c: LSPChoiceNever, want: true},
		{name: "disable all", c: LSPChoiceDisableAll, want: true},
		{name: "empty", c: "", want: false},
		{name: "yes", c: "yes", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.Valid(); got != tt.want {
				t.Fatalf("LSPRecommendationChoice(%q).Valid()=%v want %v", tt.c, got, tt.want)
			}
		})
	}
}

func TestLSPSnapshotJSONShape(t *testing.T) {
	t.Parallel()
	snap := LSPSnapshot{
		ProjectRoot:    "/tmp/demo",
		Trusted:        true,
		FeatureEnabled: true,
		Servers: []LSPServerStatus{{
			ID:          "gopls",
			DisplayName: "gopls (Go)",
			Role:        "primary",
			Scope:       "catalog",
			Enabled:     true,
			State:       LSPStateReady,
		}},
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"feature_enabled":`, `"servers":[`, `"state":"ready"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("snapshot JSON missing %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"binary_path"`) {
		t.Fatalf("snapshot JSON contains the empty optional field binary_path:\n%s", got)
	}
}
