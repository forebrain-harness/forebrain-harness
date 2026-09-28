package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovalPolicyGranularYAMLAndJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		ext  string
		body string
	}{
		{
			name: "yaml",
			ext:  ".yaml",
			body: "approval_policy:\n  granular:\n    sandbox_approval: true\n    rules: false\n    mcp_elicitations: true\n",
		},
		{
			name: "json",
			ext:  ".json",
			body: `{"approval_policy":{"granular":{"sandbox_approval":true,"rules":false,"mcp_elicitations":true}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "forebrain"+tc.ext)
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			got := cfg.ApprovalPolicy
			if got.Mode != ApprovalPolicyGranular || !got.Granular.SandboxApproval || got.Granular.Rules || !got.Granular.MCPElicitations {
				t.Fatalf("approval_policy=%+v", got)
			}
			if got.Granular.SkillApproval || got.Granular.RequestPermissions {
				t.Fatalf("optional granular fields must default to false: %+v", got.Granular)
			}
		})
	}
}

func TestApprovalPolicyGranularRequiresMandatoryFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, []byte("approval_policy:\n  granular:\n    sandbox_approval: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "sandbox_approval, rules, and mcp_elicitations") {
		t.Fatalf("err=%v", err)
	}
}

func TestApprovalPolicyGranularRejectsStringForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, []byte("approval_policy: granular\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "requires a granular configuration object") {
		t.Fatalf("err=%v", err)
	}
}

func TestApprovalPolicyOnFailureUsesOnRequestSemantics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, []byte("approval_policy: on-failure\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ApprovalPolicy.Mode != ApprovalPolicyOnRequest {
		t.Fatalf("approval_policy=%+v", cfg.ApprovalPolicy)
	}
}

func TestApprovalReviewerAndPolicyConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := "approvals_reviewer: auto_review\nauto_review:\n  policy: '  deny production changes  '\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ApprovalsReviewer != "auto_review" || cfg.AutoReview.Policy != "deny production changes" {
		t.Fatalf("review configuration=%+v/%+v", cfg.ApprovalsReviewer, cfg.AutoReview)
	}
}

func TestSandboxWorkspaceWriteDefaultsAndOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, []byte("sandbox_mode: workspace-write\nsandbox_workspace_write:\n  writable_roots: [./cache]\n  network_access: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace := cfg.SandboxWorkspaceWrite
	if len(workspace.WritableRoots) != 1 || workspace.WritableRoots[0] != "./cache" || !workspace.EffectiveNetworkAccess() {
		t.Fatalf("sandbox_workspace_write=%+v", workspace)
	}
	if workspace.ExcludeTmpdirEnvVar || workspace.ExcludeSlashTmp {
		t.Fatalf("temporary roots must be included by default: %+v", workspace)
	}
}
