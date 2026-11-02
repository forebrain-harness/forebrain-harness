package config

// ApprovalPresetID names the approval preset: the one setting a user picks to
// decide what Forebrain Harness may do without asking. It is the only approval
// setting there is (plan 203): the approval policy and the sandbox mode are
// derived from it at runtime, so the two halves of one decision — how much the
// sandbox permits and when forebrain stops to ask — can no longer be chosen
// apart and drift into a state nobody wants.
type ApprovalPresetID string

const (
	// ApprovalPresetWorkspaceWrite is the default: work happens inside the
	// workspace, and anything past it is asked about. An untrusted project
	// narrows its sandbox further, which is a trust decision rather than a
	// preset of its own.
	ApprovalPresetWorkspaceWrite ApprovalPresetID = "workspace_write"
	// ApprovalPresetFullAccess lets everything through except an explicit
	// deny rule — the same semantics the YOLO environment flag has, which is
	// why YOLO is now just a way of forcing this preset.
	ApprovalPresetFullAccess ApprovalPresetID = "full_access"
)

// Effective is the preset actually in force. An unset value means the default:
// empty reaches here only from a test, or from a state database whose value
// predates the migration that writes the default in (plan 207).
func (p ApprovalPresetID) Effective() ApprovalPresetID {
	switch p {
	case ApprovalPresetFullAccess:
		return ApprovalPresetFullAccess
	default:
		return ApprovalPresetWorkspaceWrite
	}
}

// Valid reports whether p is one of the two presets, or unset. It is the one
// verdict behind every write path, so a typo cannot reach the store.
func (p ApprovalPresetID) Valid() bool {
	switch p {
	case "", ApprovalPresetWorkspaceWrite, ApprovalPresetFullAccess:
		return true
	default:
		return false
	}
}
