package home

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// GatewayTokenEnvVar is the environment variable that supplies
// gateway.auth.token. The seeded config references it as
// ${FOREBRAIN_GATEWAY_TOKEN} so the secret lives in ~/.forebrain/.env rather than in
// plaintext inside forebrain.yaml.
const GatewayTokenEnvVar = "FOREBRAIN_GATEWAY_TOKEN"

func generateGatewayToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// UpsertEnvVar writes key=value into ~/.forebrain/.env, replacing an existing
// assignment for key when present and otherwise appending it. Unrelated lines
// (comments and other keys) are preserved verbatim and the file is kept at
// 0600 because it holds secrets.
func UpsertEnvVar(root, key, value string) error {
	path := DefaultEnvPath(root)
	var lines []string
	if b, err := os.ReadFile(path); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else if content := strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"); content != "" {
		lines = strings.Split(content, "\n")
	}
	assignment := key + "=" + value
	prefix := key + "="
	replaced := false
	for i, ln := range lines {
		trimmed := strings.TrimPrefix(strings.TrimSpace(ln), "export ")
		if strings.HasPrefix(trimmed, prefix) {
			lines[i] = assignment
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, assignment)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func seedWorkspace(root string) error {
	ws := filepath.Join(root, WorkspaceDirName)
	lock := filepath.Join(ws, ".forebrainhub", "lock.json")
	if _, err := os.Stat(lock); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(lock, []byte("{}\n"), 0o644); err != nil {
			return err
		}
	}
	cfgPath := ConfigPath(root)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		tok, err := generateGatewayToken()
		if err != nil {
			return err
		}
		// Keep the secret out of forebrain.yaml: store it in ~/.forebrain/.env and
		// leave a ${FOREBRAIN_GATEWAY_TOKEN} reference in the config so the
		// plaintext-secret guard in config.Load accepts it.
		if err := UpsertEnvVar(root, GatewayTokenEnvVar, tok); err != nil {
			return err
		}
		if err := os.WriteFile(cfgPath, []byte(defaultForebrainYAML), 0o600); err != nil {
			return err
		}
	}
	return nil
}

const defaultForebrainYAML = `gateway:
  http_addr: "127.0.0.1:6060"
  auth:
    mode: "token"
    token: "${FOREBRAIN_GATEWAY_TOKEN}"
# Channels (telegram, slack, feishu, wecom, ...) are not seeded. Add one during
# first-run setup or by writing its section here by hand; only enabled channels
# are persisted back to this file.
approval_policy: "on-request"
approvals_reviewer: "user"
features:
  exec_permission_approvals: false
  request_permissions_tool: false
  network_proxy: false
  memories: true
agents:
  definitions:
    main:
      llm_providers:
        - provider: ""
          model: ""
  defaults:
    context_inject:
      warn_remaining_tokens: 8000
    guardrails:
      input:
        max_runes: 200000
      retrieval:
        max_chunk_runes: 120000
`
