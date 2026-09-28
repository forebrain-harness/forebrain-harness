//go:build windows

package safety

import "testing"

func TestWindowsRequestHasFullDiskRead(t *testing.T) {
	t.Parallel()

	workDir := `C:\workspace\project`
	if !windowsRequestHasFullDiskRead([]string{`C:\`}, nil, workDir) {
		t.Fatal("volume root should represent unrestricted reads")
	}
	if windowsRequestHasFullDiskRead([]string{workDir}, nil, workDir) {
		t.Fatal("workspace-only reads must remain restricted")
	}
	if windowsRequestHasFullDiskRead([]string{`C:\`}, []string{`C:\secret`}, workDir) {
		t.Fatal("a deny-read carveout requires restricted-read enforcement")
	}
}

func TestWindowsCapabilityPolicyKeyIncludesRelevantDenials(t *testing.T) {
	t.Parallel()

	root := `C:\workspace`
	plain := windowsCapabilityPolicyKey(root, nil)
	unrelated := windowsCapabilityPolicyKey(root, []string{`D:\secret`})
	if unrelated != plain {
		t.Fatalf("unrelated denial changed capability key: %q != %q", unrelated, plain)
	}

	first := windowsCapabilityPolicyKey(root, []string{`C:\workspace\a`, `C:\workspace\b`})
	reordered := windowsCapabilityPolicyKey(root, []string{`C:\workspace\b`, `C:\workspace\a`})
	if first != reordered {
		t.Fatalf("denial order changed capability key: %q != %q", first, reordered)
	}
	if first == plain {
		t.Fatal("relevant denial did not isolate the capability SID")
	}
}
