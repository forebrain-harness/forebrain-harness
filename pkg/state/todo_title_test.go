package state

import (
	"encoding/json"
	"testing"
)

func TestItemReadsLegacyActiveForm(t *testing.T) {
	var old, cur Item
	if err := json.Unmarshal([]byte(`{"id":"1","content":"c","status":"in_progress","active_form":"Writing"}`), &old); err != nil || old.Title != "Writing" {
		t.Fatalf("legacy name not read: %+v %v", old, err)
	}
	if err := json.Unmarshal([]byte(`{"id":"1","content":"c","status":"in_progress","title":"Writing","active_form":"stale"}`), &cur); err != nil || cur.Title != "Writing" {
		t.Fatalf("title must win over the legacy name: %+v %v", cur, err)
	}
	b, _ := json.Marshal(cur)
	if string(b) == "" || json.Valid(b) == false || !contains(string(b), `"title":"Writing"`) || contains(string(b), "active_form") {
		t.Fatalf("stored form is not title: %s", b)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
