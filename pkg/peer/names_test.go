package peer

import (
	"strconv"
	"strings"
	"testing"
)

func TestDeriveName(t *testing.T) {
	name := DeriveName("/Users/u/My Repo!", nil)
	parts := strings.Split(name, "-")
	if len(parts) != 3 || parts[0] != "My" || parts[1] != "Repo" {
		t.Fatalf("DeriveName = %q, want a <slug>-<hex> shape", name)
	}
	if len(parts[2]) != 2 || strings.Trim(parts[2], "0123456789abcdef") != "" {
		t.Fatalf("suffix = %q, want two hex digits", parts[2])
	}

	if got := DeriveName("/", nil); !strings.HasPrefix(got, "forebrain-") {
		t.Fatalf("DeriveName(/)= %q, want the forebrain fallback", got)
	}
	if got := DeriveName("/a/"+strings.Repeat("x", 40), nil); !strings.HasPrefix(got, strings.Repeat("x", 20)+"-") {
		t.Fatalf("DeriveName with a long base = %q, want 20 characters of base", got)
	}

	// A taken name is rolled again: the third candidate is the first free one.
	calls := 0
	renamed := DeriveName("/Users/u/repo", func(string) bool {
		calls++
		return calls < 3
	})
	if calls != 3 {
		t.Fatalf("taken was called %d times, want 3", calls)
	}
	if !strings.HasPrefix(renamed, "repo-") {
		t.Fatalf("DeriveName = %q", renamed)
	}
}

func TestValidAgentName(t *testing.T) {
	valid := []string{"repo-28", "repo-28.review-auth", "forebrain-harness-3f", "a"}
	for _, name := range valid {
		if !ValidAgentName(name) {
			t.Fatalf("%q was rejected", name)
		}
	}
	invalid := []string{"", "   ", "main", "MAIN", "team-lead", "user", "system", "a@b", "a*b", `a"b`, "uds:/tmp/x.sock", "did:abc", "bridge:abc"}
	for _, name := range invalid {
		if ValidAgentName(name) {
			t.Fatalf("%q was accepted", name)
		}
	}
}

func TestRef(t *testing.T) {
	ref := Ref("agent", "cli-9b62dc22", nil)
	if len(ref) != 6 || strings.Trim(ref, "0123456789abcdef") != "" {
		t.Fatalf("Ref = %q, want six hex digits", ref)
	}
	if again := Ref("agent", "cli-9b62dc22", []string{"cli-9b62dc22"}); again != ref {
		t.Fatalf("Ref changed with itself in the set: %q vs %q", again, ref)
	}

	// Find two ids whose six-digit prefixes collide: the shorter ref then has
	// to grow until it is unique.
	seen := map[string]string{}
	var firstID, secondID string
	for i := 0; i < 200000 && secondID == ""; i++ {
		id := "cli-" + strconv.Itoa(i)
		prefix := refHash("agent", id)[:6]
		if earlier, ok := seen[prefix]; ok {
			firstID, secondID = earlier, id
			break
		}
		seen[prefix] = id
	}
	if secondID == "" {
		t.Skip("no six-digit collision found")
	}
	longer := Ref("agent", firstID, []string{secondID})
	if len(longer) < 7 || !strings.HasPrefix(refHash("agent", firstID), longer) {
		t.Fatalf("Ref with a colliding id = %q, want a longer unique prefix", longer)
	}
	if strings.HasPrefix(refHash("agent", secondID), longer) {
		t.Fatalf("Ref %q is not unique", longer)
	}
}
