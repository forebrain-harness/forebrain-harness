package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// stageOnePromptGoldenValues is the model-visible text of the first-stage
// extraction prompts, keyed exactly as the golden file is. Comparing the key
// set as well as the bytes means a prompt added without a golden entry, or a
// golden entry without a prompt, fails instead of passing by silence.
func stageOnePromptGoldenValues() map[string]any {
	return map[string]any{
		"stageOneSystem":            stageOneSystem,
		"stageOneSystemIncremental": stageOneSystemIncremental,
	}
}

// TestStageOnePromptsGolden pins both extraction system prompts byte for byte.
// They are model-visible text with no caller-supplied values in them: the
// full and the incremental prompt are separate constants precisely so each stays
// stable, and a change to either is a deliberate contract change — plan text
// first, golden in the same diff. The test never rewrites the golden.
func TestStageOnePromptsGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "stage_one_prompts.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]any
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	want := stageOnePromptGoldenValues()
	if len(golden) != len(want) {
		t.Errorf("golden has %d keys, want %d", len(golden), len(want))
	}
	for key := range want {
		if _, ok := golden[key]; !ok {
			t.Errorf("golden is missing key %q", key)
		}
	}
	for key := range golden {
		if _, ok := want[key]; !ok {
			t.Errorf("golden has unexpected key %q", key)
		}
	}
	if !reflect.DeepEqual(golden, want) {
		for key, wantValue := range want {
			gotValue, ok := golden[key]
			if !ok {
				continue
			}
			if gotValue != wantValue {
				t.Errorf("%s differs from the golden:\ngot (%d bytes): %q\nwant (%d bytes): %q",
					key, len(gotValue.(string)), gotValue, len(wantValue.(string)), wantValue)
			}
		}
	}
}
