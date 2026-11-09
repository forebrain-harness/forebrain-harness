package home

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

func generateGatewayToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func seedWorkspace(root string) error {
	ws := filepath.Join(root, WorkspaceDirName)
	lock := filepath.Join(ws, ".forebrainhub", "lock.json")
	if _, err := os.Stat(lock); os.IsNotExist(err) {
		if err := writeSeedFile(lock, []byte("{}\n"), 0o644); err != nil {
			return err
		}
	}
	cfgPath := ConfigPath(root)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		tok, err := generateGatewayToken()
		if err != nil {
			return err
		}
		// The token is the file's own secret: the gateway's only authentication
		// lives in gateway.auth.token, inside a file only its owner can read
		// (owner 2026-11-07, plan 268). A container supplies the same value
		// through the process environment instead, and a seeded
		// `${FOREBRAIN_GATEWAY_TOKEN}` then resolves from there.
		seed := strings.ReplaceAll(defaultForebrainYAML, gatewayTokenPlaceholder, tok)
		if err := writeSeedFile(cfgPath, []byte(seed), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// writeSeedFile writes a seed file whole and atomically (plan 213 §3.6): the
// content appears only once it is complete, so a replica starting against a
// shared home never reads a half-written config, and two replicas seeding one
// home leave the same bytes behind instead of interleaving writes over a file
// one of them is already reading.
func writeSeedFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".seed-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// gatewayTokenPlaceholder is where the seeded configuration carries the token
// it was born with; the seed substitutes the generated value for it before the
// file is written.
const gatewayTokenPlaceholder = "__FOREBRAIN_GATEWAY_TOKEN__"

var defaultForebrainYAML = `gateway:
  http_addr: "127.0.0.1:6060"
  auth:
    token: "` + gatewayTokenPlaceholder + `"
# Channels (telegram, slack, feishu, wecom, ...) are not seeded. Add one during
# first-run setup or by writing its section here by hand; only enabled channels
# are persisted back to this file.
approval_policy: "on-request"
approvals_reviewer: "user"
features:
  exec_permission_approvals: false
  request_permissions_tool: false
  memories: true
agents:
  defaults:
    context_inject:
      warn_remaining_percent: 12
    guardrails:
      input:
        max_runes: 200000
`
