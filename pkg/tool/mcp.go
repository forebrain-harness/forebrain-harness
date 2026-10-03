// MCP tools: bridging, approval, and sandbox metadata.
package tool

import (
	"context"
	"encoding/json"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// mcp.go — accounting for MCP tool responses.
//
// The MCP bridge already compresses tool results (JSON text blocks are
// re-encoded as TOON before the size check), but the effect was never recorded.
// That left two things broken: the original response was unrecoverable, so the
// compression was lossy in practice rather than in appearance only, and the
// savings never reached the stats that decide whether a compression mechanism
// is worth keeping.
//
// This mirrors CompressShellOutput's contract deliberately, including the
// significance gate and the fail-open behaviour, so MCP records and shell
// records aggregate on the same terms.

// MCPCapabilityTOON identifies the JSON→TOON re-encoding in savings stats.
// The prefix separates it from "toml:" pipelines and "go:" semantic parsers.
const MCPCapabilityTOON = "mcp:toon"

// RecordMCPResult accounts for an already-compressed MCP tool result and, when
// the saving is significant, stores the original so retrieve_output can recover
// it. It returns the response to hand back to the agent (with a retrieval
// marker injected as a content block when one was stored) plus the metadata.
//
// The marker is injected as a {"type":"text","text":"..."} block inside the
// envelope's content array rather than appended as raw bytes. This is
// necessary because the MCP result is a JSON envelope: appending plain text to
// the closing "}" would produce invalid JSON and break every downstream
// consumer (resultJSONContainsImage, the size check, client unmarshalling).
//
// It never fails: on any storage error the compressed response is returned
// unchanged, matching the fail-open rule the rest of the layer follows.
func (c *Compressor) RecordMCPResult(serverName, toolName, original, compressed, sessionID string) (string, Meta) {
	if c == nil || original == "" || compressed == "" {
		return compressed, Meta{}
	}
	if envTruthy(DisableEnv) {
		return compressed, Meta{}
	}
	beforeBytes := len(original)
	afterBytes := len(compressed)
	if beforeBytes <= afterBytes {
		return compressed, Meta{Applied: false}
	}
	savedBytes := beforeBytes - afterBytes
	meta := Meta{
		Applied:       true,
		Filter:        MCPCapabilityTOON,
		BeforeBytes:   beforeBytes,
		AfterBytes:    afterBytes,
		SavedTokens:   EstimateTokens(savedBytes),
		CompressedPct: 100 * savedBytes / beforeBytes,
	}
	if meta.CompressedPct < significantPct || savedBytes < significantBytes {
		return compressed, meta
	}
	if c.store == nil {
		return compressed, meta
	}
	stored := original
	if RedactEnabled() {
		stored = Redact(stored)
	}
	id, err := c.store.Save(Entry{
		Kind:           KindMCP,
		Command:        MCPTarget(serverName, toolName),
		OriginalOutput: stored,
		FilteredOutput: compressed,
		OriginalBytes:  beforeBytes,
		FilteredBytes:  afterBytes,
		SavedTokens:    meta.SavedTokens,
		CapabilityID:   MCPCapabilityTOON,
		SessionID:      sessionID,
	})
	if err != nil || id <= 0 {
		return compressed, meta
	}
	meta.RetrieveID = id
	out, injected := injectMCPMarker(compressed, MarkerText(meta.CompressedPct, meta.SavedTokens, id))
	if !injected {
		// The envelope shape was unrecognised; return the compressed form
		// without a marker. The record is still stored — the agent can use
		// stats / the id from a concurrent retrieve_output call.
		meta.AfterBytes = len(compressed)
		return compressed, meta
	}
	meta.AfterBytes = len(out)
	return out, meta
}

// injectMCPMarker adds a {"type":"text","text":marker} content block to the
// envelope's content array. The boolean reports whether the injection succeeded;
// on false the caller must use compressed unchanged.
func injectMCPMarker(compressed, marker string) (string, bool) {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(compressed), &envelope); err != nil {
		return compressed, false
	}
	content, ok := envelope["content"].([]any)
	if !ok {
		return compressed, false
	}
	markerBlock := map[string]any{"type": "text", "text": marker}
	envelope["content"] = append(content, markerBlock)
	out, err := json.Marshal(envelope)
	if err != nil {
		return compressed, false
	}
	return string(out), true
}

// MCPTarget renders the "server/tool" label stored as the record's command.
func MCPTarget(serverName, toolName string) string {
	server := strings.TrimSpace(serverName)
	tool := strings.TrimSpace(toolName)
	switch {
	case server == "" && tool == "":
		return "mcp"
	case server == "":
		return tool
	case tool == "":
		return server
	default:
		return server + "/" + tool
	}
}

func ActionAllowsApprovalUpdate(kind, payloadJSON string) bool {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(kind)), "mcp__") {
		return true
	}
	var payload struct {
		Mode string `json:"mcp_approval_mode"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(payloadJSON)), &payload) != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(payload.Mode)) {
	case "", "auto":
		return true
	default:
		return false
	}
}

func toolExecutionMetadata(ctx context.Context, st *State, rt *AgentToolRuntime, kind safety.ToolKind) map[string]any {
	decision := safety.NewManager().Decide(sessionConfig(ctx, st, rt), kind)
	out := map[string]any{
		"sandbox_mode":               decision.ModeString(),
		"sandbox_backend":            string(decision.Backend),
		"sandbox_reason":             decision.Reason,
		"sandbox_unavailable_reason": decision.UnavailableReason,
	}
	if rt != nil && rt.YOLO {
		out["yolo"] = true
		out["approval_bypassed_by_yolo"] = true
	}
	return out
}

// appendToolExecutionMetadata fills in the sandbox posture a tool call ran
// under. The values it derives describe the configured default, so a key the
// caller already set from the decision the command actually ran with wins: the
// generic default reports "sandboxed" even for a call that was explicitly
// escalated, which makes the audit trail disagree with what happened.
func appendToolExecutionMetadata(ctx context.Context, st *State, dst map[string]any, rt *AgentToolRuntime, kind safety.ToolKind) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range toolExecutionMetadata(ctx, st, rt, kind) {
		if _, ok := dst[k]; ok {
			continue
		}
		dst[k] = v
	}
	return dst
}

// sessionConfig is the configuration the calling conversation's sandbox
// decisions are made under: the runtime's, with the conversation's own
// sandbox mode in force when it picked one for itself.
func sessionConfig(ctx context.Context, st *State, rt *AgentToolRuntime) *appcfg.Root {
	if rt == nil {
		return nil
	}
	return safety.ConfigForSnapshot(rt.Cfg, permissionSnapshotForContext(ctx, st, rt))
}
