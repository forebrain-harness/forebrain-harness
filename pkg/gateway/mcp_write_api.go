package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// Writing MCP configuration from the web surface.
//
// Three layers hold MCP entries — the shared list every primary agent
// inherits, one primary agent's own list, and a project's file — and the modal
// edits all three. The three write routes and the three test routes therefore
// share one payload shape and one pair of translate functions
// (decodeMCPServerWrite / applyMCPServerWrite / readMCPServerWrite), so what
// the modal sent, what the test tried and what was saved are the same entry.
//
// Two invariants run through the whole file:
//
//   - A field the client did not send is left as it is. The modal may only
//     touch the enable switch; everything it does not render — the per-tool
//     approval table above all — survives the round trip untouched.
//   - A secret the surface is not allowed to read comes back as
//     [REDACTED], and sending that placeholder back means "leave it alone".
//     The literal that replaces it on disk is never a value the client saw.

// mcpAPIError is a refused request with the status the client should read.
// Everything else reaching a handler is a server-side failure and answers 500.
type mcpAPIError struct {
	Status int
	Msg    string
}

func (e *mcpAPIError) Error() string { return e.Msg }

func mcpBadRequest(format string, a ...any) error {
	return &mcpAPIError{Status: http.StatusBadRequest, Msg: fmt.Sprintf(format, a...)}
}

func mcpNotFound(format string, a ...any) error {
	return &mcpAPIError{Status: http.StatusNotFound, Msg: fmt.Sprintf(format, a...)}
}

func mcpConflict(format string, a ...any) error {
	return &mcpAPIError{Status: http.StatusConflict, Msg: fmt.Sprintf(format, a...)}
}

func mcpUnavailable(format string, a ...any) error {
	return &mcpAPIError{Status: http.StatusServiceUnavailable, Msg: fmt.Sprintf(format, a...)}
}

// failMCPWrite answers with the error's own status and text. The text is the
// service's own sentence — a refused write, a server that would not connect —
// and goes to the client verbatim, because it is the only explanation there is.
func failMCPWrite(w http.ResponseWriter, err error) {
	var api *mcpAPIError
	if errors.As(err, &api) {
		http.Error(w, api.Msg, api.Status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// mcpOAuthWrite is an entry's OAuth block as a client sends it. Each field is
// the value the client means the configuration to hold: empty clears it, and
// [REDACTED] in the three credential fields keeps what is stored.
type mcpOAuthWrite struct {
	Mode             string   `json:"mode"`
	AccessToken      string   `json:"access_token"`
	TokenType        string   `json:"token_type"`
	ClientID         string   `json:"client_id"`
	ClientSecret     string   `json:"client_secret"`
	TokenURL         string   `json:"token_url"`
	AuthorizationURL string   `json:"authorization_url"`
	RedirectURL      string   `json:"redirect_url"`
	RefreshToken     string   `json:"refresh_token"`
	Scopes           []string `json:"scopes"`
}

// mcpServerWrite is one MCP entry as a client sends it: every field of the
// configured entry, with the stdio command and its arguments carried as the
// single target line a form shows them in.
//
// Absent means unchanged, which is why every scalar a client might not render
// is a pointer: a form that only flips the enable switch sends one field and
// leaves the rest of the entry — including the per-tool approval table, which
// has no field here at all — exactly as it was. The exceptions are documented
// per field:
//
//   - name is the entry's identity on create; an update takes the name from the
//     route and never renames (renaming would orphan the stored credential, the
//     disable mark and the project consent that are all keyed by name).
//   - transport is required on create and unchanged when absent on update, and
//     only "stdio" and "streamable_http" are accepted.
//   - target is the command line for a stdio entry and the URL for an http one.
//     Sending back the string the server rendered for it means "unchanged", so
//     the masked form a list shows can never be written over the real value; a
//     different string replaces it.
//   - headers and env are merged key by key: [REDACTED] keeps one key's stored
//     value, an empty value removes that key, and a key the client did not send
//     is left alone.
//   - oauth, when present, is the whole new block.
type mcpServerWrite struct {
	Name                     string            `json:"name"`
	Transport                string            `json:"transport"`
	Target                   *string           `json:"target"`
	Required                 *bool             `json:"required"`
	Enabled                  *bool             `json:"enabled"`
	StartupTimeout           *float64          `json:"startup_timeout"`
	RequestTimeout           *float64          `json:"request_timeout"`
	Headers                  map[string]string `json:"headers"`
	Env                      map[string]string `json:"env"`
	InheritParentEnv         *bool             `json:"inherit_parent_env"`
	DisableStandaloneSSE     *bool             `json:"disable_standalone_sse"`
	ExpectedCommandSHA256    *string           `json:"expected_command_sha256"`
	DefaultToolsApprovalMode *string           `json:"default_tools_approval_mode"`
	OAuth                    *mcpOAuthWrite    `json:"oauth"`
}

// decodeMCPServerWrite reads one entry from a request body.
func decodeMCPServerWrite(r *http.Request) (mcpServerWrite, error) {
	var in mcpServerWrite
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		return mcpServerWrite{}, mcpBadRequest("the request body is not an MCP server entry: %v", err)
	}
	return in, nil
}

// readMCPServerWrite renders one configured entry the way a client edits it:
// every field of mcpServerWrite (so what a list returns can be sent straight
// back), with the credentials the surface may not read replaced by the
// placeholder and the stdio command rendered as the one line a form shows.
func readMCPServerWrite(stored appcfg.MCPServerConfig) map[string]any {
	return map[string]any{
		"name":                        strings.TrimSpace(stored.Name),
		"transport":                   writeTransport(stored),
		"target":                      mcpWriteTarget(stored),
		"required":                    stored.Required,
		"enabled":                     appcfg.ServerEnabled(stored),
		"startup_timeout":             stored.StartupTimeout,
		"request_timeout":             stored.RequestTimeout,
		"headers":                     redactedMCPSecretMap(stored.Headers),
		"env":                         redactedMCPSecretMap(stored.Env),
		"inherit_parent_env":          stored.InheritParentEnv != nil && *stored.InheritParentEnv,
		"disable_standalone_sse":      stored.DisableStandaloneSSE != nil && *stored.DisableStandaloneSSE,
		"expected_command_sha256":     strings.TrimSpace(stored.ExpectedCommandSHA256),
		"default_tools_approval_mode": string(stored.DefaultToolsApprovalMode),
		"oauth":                       readMCPOAuthWrite(stored.OAuth),
	}
}

// readMCPOAuthWrite renders an OAuth block with its credentials masked. An
// unset value stays empty: "never configured" and "configured but hidden" are
// different facts a settings screen has to be able to tell apart.
func readMCPOAuthWrite(o appcfg.MCPOAuthConfig) map[string]any {
	return map[string]any{
		"mode":              strings.TrimSpace(o.Mode),
		"access_token":      redactMCPSecret(o.AccessToken),
		"token_type":        strings.TrimSpace(o.TokenType),
		"client_id":         strings.TrimSpace(o.ClientID),
		"client_secret":     redactMCPSecret(o.ClientSecret),
		"token_url":         strings.TrimSpace(o.TokenURL),
		"authorization_url": strings.TrimSpace(o.AuthorizationURL),
		"redirect_url":      strings.TrimSpace(o.RedirectURL),
		"refresh_token":     redactMCPSecret(o.RefreshToken),
		"scopes":            append([]string(nil), o.Scopes...),
	}
}

// redactedMCPSecretMap renders an env or header map for a surface: key names
// travel, values do not. A single ${ENV_NAME} reference is passed through —
// that is a pointer, not a secret — and an empty value stays empty.
func redactedMCPSecretMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = redactMCPSecret(v)
	}
	return out
}

// redactMCPSecret is one value's read form: a single ${ENV_NAME} reference is
// the value itself, everything else set is masked, and empty stays empty.
func redactMCPSecret(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if isMCPEnvReference(trimmed) {
		return trimmed
	}
	return appcfg.RedactedSecretPlaceholder
}

// isMCPEnvReference reports whether value is exactly one ${ENV_NAME}
// reference, the one form of a credential a configuration file may carry.
func isMCPEnvReference(value string) bool {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	if name == "" {
		return false
	}
	for i, r := range name {
		isAlpha := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
		isDigitAfterFirst := i > 0 && r >= '0' && r <= '9'
		if r == '_' || isAlpha || isDigitAfterFirst {
			continue
		}
		return false
	}
	return true
}

// writeTransport is the transport an entry is written and shown as: the
// configured one, or the one its shape implies when the file omits it.
func writeTransport(srv appcfg.MCPServerConfig) string {
	if t := strings.ToLower(strings.TrimSpace(srv.Transport)); t != "" {
		return t
	}
	if strings.TrimSpace(srv.URL) != "" {
		return "streamable_http"
	}
	return "stdio"
}

// mcpWriteTarget is the one-line target a client shows for an entry: the
// command with its arguments for a stdio entry, the sanitized URL for an http
// one — the same string readMCPServerWrite hands out, so a client sending it
// back is recognized as "unchanged" rather than parsed as a new value.
func mcpWriteTarget(srv appcfg.MCPServerConfig) string {
	if writeTransport(srv) == "stdio" {
		return mcp.FormatStdioTarget(strings.TrimSpace(srv.Command), srv.Args)
	}
	return turn.SanitizeEndpoint(srv.URL)
}

// applyMCPServerWrite applies one submitted entry onto stored, in place. It is
// the single translation the three layers share: the test endpoint builds the
// entry it will try with this function too, so what a user tests is what a save
// would write.
func applyMCPServerWrite(stored *appcfg.MCPServerConfig, in mcpServerWrite) error {
	if stored == nil {
		return fmt.Errorf("no entry to apply a write to")
	}
	if transport := strings.TrimSpace(in.Transport); transport != "" {
		switch strings.ToLower(transport) {
		case "stdio", "streamable_http":
			stored.Transport = strings.ToLower(transport)
		default:
			return mcpBadRequest("transport must be stdio or streamable_http, not %q", transport)
		}
	}
	if err := applyMCPTarget(stored, in); err != nil {
		return err
	}
	if in.Required != nil {
		stored.Required = *in.Required
	}
	if in.Enabled != nil {
		stored.Enabled = appcfg.BoolPtr(*in.Enabled)
	}
	if in.StartupTimeout != nil {
		if err := appcfg.ValidateMCPTimeout(*in.StartupTimeout); err != nil {
			return mcpBadRequest("startup_timeout %v", err)
		}
		stored.StartupTimeout = *in.StartupTimeout
	}
	if in.RequestTimeout != nil {
		if err := appcfg.ValidateMCPTimeout(*in.RequestTimeout); err != nil {
			return mcpBadRequest("request_timeout %v", err)
		}
		stored.RequestTimeout = *in.RequestTimeout
	}
	if in.Headers != nil {
		stored.Headers = mergeMCPSecretMap(stored.Headers, in.Headers)
	}
	if in.Env != nil {
		stored.Env = mergeMCPSecretMap(stored.Env, in.Env)
	}
	if in.InheritParentEnv != nil {
		stored.InheritParentEnv = appcfg.BoolPtr(*in.InheritParentEnv)
	}
	if in.DisableStandaloneSSE != nil {
		stored.DisableStandaloneSSE = appcfg.BoolPtr(*in.DisableStandaloneSSE)
	}
	if in.ExpectedCommandSHA256 != nil {
		stored.ExpectedCommandSHA256 = strings.TrimSpace(*in.ExpectedCommandSHA256)
	}
	if in.DefaultToolsApprovalMode != nil {
		mode := appcfg.MCPToolApprovalMode(strings.ToLower(strings.TrimSpace(*in.DefaultToolsApprovalMode)))
		if !mode.Valid() {
			return mcpBadRequest("default_tools_approval_mode must be auto, prompt, writes, or approve")
		}
		stored.DefaultToolsApprovalMode = mode
	}
	if in.OAuth != nil {
		stored.OAuth = applyMCPOAuthWrite(stored.OAuth, *in.OAuth)
	}
	return validateMCPServerWrite(*stored)
}

// applyMCPTarget replaces the command and arguments, or the URL, from the
// submitted target line — unless the client sent back exactly what the entry
// renders as, which is how "I did not touch this field" travels through a form
// that always submits every field. Without that rule the sanitized URL a list
// shows (userinfo and query stripped) would be written over the real one.
func applyMCPTarget(stored *appcfg.MCPServerConfig, in mcpServerWrite) error {
	if in.Target == nil {
		return nil
	}
	target := *in.Target
	if target == mcpWriteTarget(*stored) {
		return nil
	}
	switch writeTransport(*stored) {
	case "stdio":
		command, args, err := mcp.SplitStdioTarget(target)
		if err != nil {
			return mcpBadRequest("%v", err)
		}
		stored.Command = strings.TrimSpace(command)
		stored.Args = args
		stored.URL = ""
	case "streamable_http":
		u := strings.TrimSpace(target)
		if err := validateMCPHTTPURL(u); err != nil {
			return err
		}
		stored.URL = u
		stored.Command = ""
		stored.Args = nil
	}
	return nil
}

// validateMCPHTTPURL accepts only an absolute http(s) URL with a host: an
// entry the runtime cannot dial is a configuration mistake worth catching while
// the user is looking at it.
func validateMCPHTTPURL(raw string) error {
	if raw == "" {
		return mcpBadRequest("a streamable_http entry needs a URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return mcpBadRequest("url %v", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return mcpBadRequest("a streamable_http target must be an http(s) URL")
	}
	if strings.TrimSpace(u.Host) == "" {
		return mcpBadRequest("a streamable_http target must name a host")
	}
	return nil
}

// mergeMCPSecretMap builds the new value of an env or header map. One key at a
// time, the same rule the scalar fields follow: a key the client did not send is
// left as it is, the placeholder keeps the stored value, an empty value removes
// the key, and anything else is the new value. Omission therefore never drops a
// credential — the one thing a form that only renders some of the keys could do
// by accident.
func mergeMCPSecretMap(stored, in map[string]string) map[string]string {
	out := make(map[string]string, len(stored)+len(in))
	for rawKey, value := range stored {
		if key := strings.TrimSpace(rawKey); key != "" {
			out[key] = value
		}
	}
	for rawKey, value := range in {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		switch {
		case value == appcfg.RedactedSecretPlaceholder:
			// Keep what is stored: a surface sends the placeholder back for
			// every secret it was not allowed to read.
		case strings.TrimSpace(value) == "":
			delete(out, key)
		default:
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyMCPOAuthWrite builds the new OAuth block. The three credential fields
// answer to the placeholder; the rest are what the client sent.
func applyMCPOAuthWrite(stored appcfg.MCPOAuthConfig, in mcpOAuthWrite) appcfg.MCPOAuthConfig {
	return appcfg.MCPOAuthConfig{
		Mode:             strings.TrimSpace(in.Mode),
		AccessToken:      applyMCPSecretValue(stored.AccessToken, in.AccessToken),
		TokenType:        strings.TrimSpace(in.TokenType),
		ClientID:         strings.TrimSpace(in.ClientID),
		ClientSecret:     applyMCPSecretValue(stored.ClientSecret, in.ClientSecret),
		TokenURL:         strings.TrimSpace(in.TokenURL),
		AuthorizationURL: strings.TrimSpace(in.AuthorizationURL),
		RedirectURL:      strings.TrimSpace(in.RedirectURL),
		RefreshToken:     applyMCPSecretValue(stored.RefreshToken, in.RefreshToken),
		Scopes:           append([]string(nil), in.Scopes...),
	}
}

func applyMCPSecretValue(stored, in string) string {
	if in == appcfg.RedactedSecretPlaceholder {
		return stored
	}
	return strings.TrimSpace(in)
}

// validateMCPServerWrite checks an entry once the client's fields are applied:
// a name a reader can find, a transport the runtime can dial, and a target for
// it. What the *list* must satisfy — uniqueness, the config-file rules — is
// checked by the caller, which is the only place that knows the whole list.
func validateMCPServerWrite(srv appcfg.MCPServerConfig) error {
	name := strings.TrimSpace(srv.Name)
	if name == "" {
		return mcpBadRequest("an MCP server needs a name")
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return mcpBadRequest("an MCP server name must not contain whitespace")
	}
	switch writeTransport(srv) {
	case "stdio":
		if strings.TrimSpace(srv.Command) == "" {
			return mcpBadRequest("a stdio entry needs a command")
		}
	case "streamable_http":
		if err := validateMCPHTTPURL(strings.TrimSpace(srv.URL)); err != nil {
			return err
		}
	default:
		return mcpBadRequest("transport must be stdio or streamable_http, not %q", strings.TrimSpace(srv.Transport))
	}
	if !appcfg.ServerEnabled(srv) && srv.Required {
		return mcpBadRequest("a required server must be enabled; enabled is false")
	}
	if err := appcfg.ValidateMCPTimeout(srv.StartupTimeout); err != nil {
		return mcpBadRequest("startup_timeout %v", err)
	}
	if err := appcfg.ValidateMCPTimeout(srv.RequestTimeout); err != nil {
		return mcpBadRequest("request_timeout %v", err)
	}
	return nil
}

// findMCPServerIndex finds one entry by name the way every other reader does.
func findMCPServerIndex(servers []appcfg.MCPServerConfig, name string) int {
	for i, srv := range servers {
		if mcp.SameServerName(srv.Name, name) {
			return i
		}
	}
	return -1
}

// --- the three layers -------------------------------------------------------

// mcpWriteLayer is the layer one write addresses: the entries it holds now, and
// what it means to save a new list of them.
type mcpWriteLayer struct {
	scope  string // appcfg.MCPServerScope{Global,Agent,Project}
	label  string // how messages name the layer
	source string // the file the layer lives in
	// agentID is the primary agent an agent-layer write belongs to.
	agentID string
	// projectRoot is the root whose file a project-layer write edits.
	projectRoot string
	entries     []appcfg.MCPServerConfig
}

// mcpGlobalWriteLayer resolves the shared list in forebrain.yaml.
func (s *Server) mcpGlobalWriteLayer() (mcpWriteLayer, error) {
	cfg, path, err := s.mcpConfigForWrite()
	if err != nil {
		return mcpWriteLayer{}, err
	}
	return mcpWriteLayer{
		scope:   appcfg.MCPServerScopeGlobal,
		label:   "the shared list",
		source:  path,
		entries: cfg.Agents.Defaults.MCPServers,
	}, nil
}

// mcpAgentWriteLayer resolves one primary agent's own list. The agent must
// already exist: this route edits an agent the user configured, it does not
// invent one, and "main" always exists.
func (s *Server) mcpAgentWriteLayer(agentID string) (mcpWriteLayer, error) {
	cfg, path, err := s.mcpConfigForWrite()
	if err != nil {
		return mcpWriteLayer{}, err
	}
	id, err := s.resolveMCPAgentID(agentID)
	if err != nil {
		return mcpWriteLayer{}, err
	}
	def := cfg.Agents.Definitions[id]
	return mcpWriteLayer{
		scope:   appcfg.MCPServerScopeAgent,
		label:   fmt.Sprintf("primary agent %q", id),
		source:  path,
		agentID: id,
		entries: def.MCPServers,
	}, nil
}

// mcpProjectWriteLayer resolves a project's own file. A project that is not
// trusted has no MCP servers at all — the trust decision is what lets a
// repository's file mean anything — so its file is not editable either.
func (s *Server) mcpProjectWriteLayer(ctx context.Context, projectID string) (mcpWriteLayer, error) {
	store := s.projectStore()
	if store == nil {
		return mcpWriteLayer{}, mcpUnavailable("projects unavailable")
	}
	p, err := store.Get(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return mcpWriteLayer{}, mcpNotFound("project not found")
	}
	launch, err := safety.ResolveRegisteredContext(s.Home, p.Root)
	if err != nil {
		return mcpWriteLayer{}, mcpBadRequest("project could not be resolved")
	}
	if safety.TrustedRoot(launch) == "" {
		return mcpWriteLayer{}, mcpConflict("project %s is not trusted; trust it before editing its MCP servers", p.Root)
	}
	entries, err := mcp.LoadProjectMCPServersForWrite(p.Root)
	if err != nil {
		// The file is replaced wholesale by a write, so anything this process
		// cannot read would be deleted by it. Refusing leaves the file to the
		// user's editor instead of quietly dropping half of it.
		return mcpWriteLayer{}, mcpConflict("the project MCP file cannot be edited from here: %v", err)
	}
	paths := mcp.ProjectMCPPaths(p.Root)
	source := ""
	if len(paths) > 0 {
		source = paths[0]
	}
	return mcpWriteLayer{
		scope:       appcfg.MCPServerScopeProject,
		label:       "this project",
		source:      source,
		projectRoot: p.Root,
		entries:     entries,
	}, nil
}

// mcpConfigForWrite reads forebrain.yaml as written, ready to be edited in
// memory and saved back.
func (s *Server) mcpConfigForWrite() (*appcfg.Root, string, error) {
	path := strings.TrimSpace(s.configPath())
	if path == "" {
		return nil, "", mcpUnavailable("no config path is bound")
	}
	cfg, err := s.persistedConfig()
	if err != nil {
		return nil, "", err
	}
	return &cfg, path, nil
}

// resolveMCPAgentID resolves the route's agent to an id that is in effect.
func (s *Server) resolveMCPAgentID(agentID string) (string, error) {
	if strings.TrimSpace(agentID) == "" {
		return "", mcpNotFound("no primary agent named in the route")
	}
	resolver, err := s.primaryAgentResolver()
	if err != nil {
		return "", err
	}
	sum, err := resolver.Resolve(agentID)
	if err != nil {
		return "", mcpNotFound("%v", err)
	}
	return strings.TrimSpace(sum.ID), nil
}

// saveMCPLayer validates the layer's new list the way the loader would, writes
// it, and brings the running process onto the change.
//
// The rebind is not optional: an MCP list is frozen when a session starts, so a
// write that only reached the file would look like it did nothing. Rebuilding
// the pool is what "the next session uses it" means for a process that already
// has runners.
func (s *Server) saveMCPLayer(layer mcpWriteLayer, servers []appcfg.MCPServerConfig) error {
	appcfg.NormalizeMCPServers(servers)
	if err := appcfg.ValidateMCPServers(layer.source, servers); err != nil {
		return mcpBadRequest("%v", err)
	}
	// The loader refuses to read a file with a plaintext secret in it, so
	// writing one would leave a configuration nothing can start from.
	if err := appcfg.RejectPlaintextMCPSecrets(layer.source, servers); err != nil {
		return mcpBadRequest("%v", err)
	}
	if layer.scope == appcfg.MCPServerScopeProject {
		if err := mcp.SaveProjectMCPServers(layer.projectRoot, servers); err != nil {
			return err
		}
	} else {
		cfg, _, err := s.mcpConfigForWrite()
		if err != nil {
			return err
		}
		if layer.scope == appcfg.MCPServerScopeAgent {
			def := cfg.Agents.Definitions[layer.agentID]
			def.MCPServers = servers
			cfg.Agents.Definitions[layer.agentID] = def
		} else {
			cfg.Agents.Defaults.MCPServers = servers
		}
		if err := s.saveAndReload(*cfg); err != nil {
			return err
		}
	}
	s.rebindAgentRunners()
	return nil
}

// rebindAgentRunners rebuilds the pooled runners of the agent serving this
// request, so a configuration change lands in the next session rather than
// waiting for the process to restart.
func (s *Server) rebindAgentRunners() {
	if s == nil || s.Env == nil || s.Runner == nil {
		return
	}
	if pool := s.Env.RunnerPool(); pool != nil {
		pool.RebindAgent(s.Runner.AgentName)
	}
}

// --- write endpoints --------------------------------------------------------

// mcpLayerResolver resolves the layer one request addresses.
type mcpLayerResolver func(r *http.Request) (mcpWriteLayer, error)

// handleMCPGlobalServersWrite serves POST /v1/mcp/servers, PUT and
// DELETE /v1/mcp/servers/:name — the shared list in forebrain.yaml.
func (s *Server) handleMCPGlobalServersWrite(w http.ResponseWriter, r *http.Request) {
	s.writeMCPServer(w, r, func(*http.Request) (mcpWriteLayer, error) {
		return s.mcpGlobalWriteLayer()
	})
}

// handleMCPAgentServersWrite serves the primary agent's own list.
func (s *Server) handleMCPAgentServersWrite(w http.ResponseWriter, r *http.Request) {
	s.writeMCPServer(w, r, func(r *http.Request) (mcpWriteLayer, error) {
		return s.mcpAgentWriteLayer(ParamsFromContext(r.Context()).ByName("id"))
	})
}

// handleProjectMCPServersWrite serves a project's own file.
func (s *Server) handleProjectMCPServersWrite(w http.ResponseWriter, r *http.Request) {
	s.writeMCPServer(w, r, func(r *http.Request) (mcpWriteLayer, error) {
		return s.mcpProjectWriteLayer(r.Context(), ParamsFromContext(r.Context()).ByName("id"))
	})
}

// writeMCPServer is the one write path behind all three layers: POST creates
// an entry, PUT replaces the one the route names, DELETE removes it.
//
// The whole body runs under configWriteMu: resolve snapshots layer.entries and
// saveMCPLayer later replaces that entire layer, so any config write landing
// in between would be silently overwritten. saveMCPLayer's rebind (which can
// wait up to runnerPoolCloseDeadline on a stuck MCP subprocess) runs inside
// the lock by the same token — a rebind of stale runners would race the layer
// replacement it is part of.
func (s *Server) writeMCPServer(w http.ResponseWriter, r *http.Request, resolve mcpLayerResolver) {
	s.configWriteMu.Lock()
	defer s.configWriteMu.Unlock()
	layer, err := resolve(r)
	if err != nil {
		failMCPWrite(w, err)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	switch r.Method {
	case http.MethodPost:
		s.createMCPServer(w, r, layer)
	case http.MethodPut:
		s.updateMCPServer(w, r, layer, name)
	case http.MethodDelete:
		s.deleteMCPServer(w, layer, name)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (s *Server) createMCPServer(w http.ResponseWriter, r *http.Request, layer mcpWriteLayer) {
	in, err := decodeMCPServerWrite(r)
	if err != nil {
		failMCPWrite(w, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		failMCPWrite(w, mcpBadRequest("an MCP server needs a name"))
		return
	}
	if i := findMCPServerIndex(layer.entries, name); i >= 0 {
		failMCPWrite(w, mcpConflict("an MCP server named %q is already configured in %s", name, layer.label))
		return
	}
	if strings.TrimSpace(in.Transport) == "" {
		failMCPWrite(w, mcpBadRequest("transport is required; use stdio or streamable_http"))
		return
	}
	srv := appcfg.MCPServerConfig{Name: name}
	if err := applyMCPServerWrite(&srv, in); err != nil {
		failMCPWrite(w, err)
		return
	}
	next := append(append([]appcfg.MCPServerConfig(nil), layer.entries...), srv)
	if err := s.saveMCPLayer(layer, next); err != nil {
		failMCPWrite(w, err)
		return
	}
	writeMCPServerWriteResult(w, layer, srv, false)
}

func (s *Server) updateMCPServer(w http.ResponseWriter, r *http.Request, layer mcpWriteLayer, name string) {
	if name == "" {
		failMCPWrite(w, mcpBadRequest("an MCP server name is required"))
		return
	}
	in, err := decodeMCPServerWrite(r)
	if err != nil {
		failMCPWrite(w, err)
		return
	}
	idx := findMCPServerIndex(layer.entries, name)
	if idx < 0 {
		failMCPWrite(w, mcpNotFound("no MCP server named %q in %s", name, layer.label))
		return
	}
	srv := layer.entries[idx]
	if err := applyMCPServerWrite(&srv, in); err != nil {
		failMCPWrite(w, err)
		return
	}
	next := append([]appcfg.MCPServerConfig(nil), layer.entries...)
	next[idx] = srv
	if err := s.saveMCPLayer(layer, next); err != nil {
		failMCPWrite(w, err)
		return
	}
	writeMCPServerWriteResult(w, layer, srv, false)
}

func (s *Server) deleteMCPServer(w http.ResponseWriter, layer mcpWriteLayer, name string) {
	if name == "" {
		failMCPWrite(w, mcpBadRequest("an MCP server name is required"))
		return
	}
	idx := findMCPServerIndex(layer.entries, name)
	if idx < 0 {
		failMCPWrite(w, mcpNotFound("no MCP server named %q in %s", name, layer.label))
		return
	}
	removed := layer.entries[idx]
	next := make([]appcfg.MCPServerConfig, 0, len(layer.entries)-1)
	next = append(next, layer.entries[:idx]...)
	next = append(next, layer.entries[idx+1:]...)
	if err := s.saveMCPLayer(layer, next); err != nil {
		failMCPWrite(w, err)
		return
	}
	writeMCPServerWriteResult(w, layer, removed, true)
}

// writeMCPServerWriteResult answers a successful write with the entry as it now
// stands, so the caller does not have to read the list back to know what
// landed.
func writeMCPServerWriteResult(w http.ResponseWriter, layer mcpWriteLayer, srv appcfg.MCPServerConfig, removed bool) {
	writeAgentsJSON(w, map[string]any{
		"ok":      true,
		"path":    layer.source,
		"scope":   layer.scope,
		"applied": true,
		"removed": removed,
		"server":  readMCPServerWrite(srv),
	})
}

// --- test endpoint ----------------------------------------------------------

// handleMCPServersTest serves POST /v1/mcp/servers/test: try one submitted
// entry without saving it. Which stored entry the placeholders in the payload
// stand for depends on the layer, so the route is what says which layer is
// being tested.
func (s *Server) handleMCPServersTest(w http.ResponseWriter, r *http.Request) {
	s.testMCPServer(w, r, func(*http.Request) (mcpWriteLayer, error) {
		return s.mcpGlobalWriteLayer()
	})
}

func (s *Server) handleMCPAgentServersTest(w http.ResponseWriter, r *http.Request) {
	s.testMCPServer(w, r, func(r *http.Request) (mcpWriteLayer, error) {
		return s.mcpAgentWriteLayer(ParamsFromContext(r.Context()).ByName("id"))
	})
}

func (s *Server) handleProjectMCPServersTest(w http.ResponseWriter, r *http.Request) {
	s.testMCPServer(w, r, func(r *http.Request) (mcpWriteLayer, error) {
		return s.mcpProjectWriteLayer(r.Context(), ParamsFromContext(r.Context()).ByName("id"))
	})
}

// testMCPServer tries one entry and reports what happened. It writes nothing:
// the only record of the attempt is the answer. Testing a project entry does
// not require the entry to have been confirmed — pressing the button is a
// deliberate act about that one entry, and a test that could not run until the
// entry was allowed would be useless precisely when it is wanted.
func (s *Server) testMCPServer(w http.ResponseWriter, r *http.Request, resolve mcpLayerResolver) {
	layer, err := resolve(r)
	if err != nil {
		failMCPWrite(w, err)
		return
	}
	in, err := decodeMCPServerWrite(r)
	if err != nil {
		failMCPWrite(w, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	srv := appcfg.MCPServerConfig{Name: name}
	if i := findMCPServerIndex(layer.entries, name); i >= 0 {
		// The stored entry is where a placeholder's real value comes from.
		srv = layer.entries[i]
	}
	if err := applyMCPServerWrite(&srv, in); err != nil {
		failMCPWrite(w, err)
		return
	}
	dir := layer.projectRoot
	if dir == "" && s.Runner != nil {
		// The same working directory the session's own startup would use.
		dir = s.Runner.ProjectRoot
	}
	ctx, cancel := context.WithTimeout(r.Context(), mcp.StartupTimeoutFor(srv)+5*time.Second)
	defer cancel()
	res, err := mcp.TestConnection(ctx, s.Home, dir, srv)
	if err != nil {
		// The entry never became a connection attempt: that is the caller's
		// input to fix, not a server's answer.
		failMCPWrite(w, mcpBadRequest("%v", err))
		return
	}
	writeAgentsJSON(w, res)
}

// --- listing configuration layers -------------------------------------------

// handleMCPLayerList serves GET /v1/mcp/servers?scope=global|agent: one
// configuration layer as a client edits it, whether or not its entries are in
// the session's effective list. An entry a higher layer shadows is still
// configured and still editable, so it is listed and reported as not running
// rather than hidden — which is what "why is my server not in the list" needs
// to find. A shared entry the primary agent's layer replaces carries
// shadowed_by/shadowed_by_name, so the row can say what keeps it from running.
func (s *Server) handleMCPLayerList(w http.ResponseWriter, r *http.Request, lc *appcfg.Root, rc mcpRowContext, scope string) {
	runtimeRunner := s.Runner
	var rows []appcfg.MCPServerConfig
	source := ""
	if scope == appcfg.MCPServerScopeAgent {
		if runtimeRunner == nil {
			writeAgentsJSON(w, map[string]any{"servers": []any{}, "scope": scope})
			return
		}
		rows = lc.Agents.MCPServersFor(runtimeRunner.AgentName)
	} else {
		rows = lc.Agents.Defaults.MCPServers
	}
	source = turn.GlobalMCPSource(lc)
	// A shared entry a same-named entry of the primary agent's own list
	// replaces never runs. It is still listed here — this is the page it is
	// edited on — and the row says which entry covers it, so "why is my
	// server not running" has an answer other than the bare not-running
	// state.
	shadowed := make(map[string]bool)
	shadowedBy := ""
	if scope == appcfg.MCPServerScopeGlobal && runtimeRunner != nil {
		for _, srv := range lc.Agents.MCPServersFor(runtimeRunner.AgentName) {
			shadowed[strings.ToLower(strings.TrimSpace(srv.Name))] = true
		}
		shadowedBy = runtimeRunner.AgentName
	}
	out := make([]map[string]any, 0, len(rows))
	for _, srv := range rows {
		entry := mcpLayerEntry(rc, srv, scope)
		row := s.mcpServerRow(rc, srv, scope, source, entry != nil, entry)
		if shadowed[strings.ToLower(strings.TrimSpace(srv.Name))] {
			row["shadowed_by"] = appcfg.MCPServerScopeAgent
			row["shadowed_by_name"] = shadowedBy
		}
		out = append(out, row)
	}
	// The order is the configuration file's own: a layer is listed the way it
	// is written, so a row does not move between two reads of the same file.
	writeMCPRows(w, readListQuery(r), mcpLayerFilter(r.URL.Query().Get("source")), out,
		map[string]any{"scope": scope, "source": source})
}

// handleMCPAgentView serves GET /v1/mcp/servers?view=agent: the menu bar's MCP
// page, which shows the primary agent's own layer and the shared list it
// inherits.
//
// A name the agent layer defines hides the shared entry of the same name — the
// agent's entry replaces it whole — so a page that listed both would show a
// server twice, only one of which runs. The settings page keeps listing the
// shared layer including its overridden entries (?scope=global), because that
// is where they are edited; this page shows what one agent loads, its own rows
// first.
func (s *Server) handleMCPAgentView(w http.ResponseWriter, r *http.Request, lc *appcfg.Root, rc mcpRowContext) {
	var own []appcfg.MCPServerConfig
	if runtimeRunner := s.Runner; runtimeRunner != nil {
		own = lc.Agents.MCPServersFor(runtimeRunner.AgentName)
	}
	// Without a Runner the agent layer cannot be named at all, so only the
	// shared layer is listed rather than inventing an empty agent layer.
	source := turn.GlobalMCPSource(lc)
	out := make([]map[string]any, 0, len(own)+len(lc.Agents.Defaults.MCPServers))
	shadowed := make(map[string]bool, len(own))
	for _, srv := range own {
		shadowed[strings.ToLower(strings.TrimSpace(srv.Name))] = true
		entry := mcpLayerEntry(rc, srv, appcfg.MCPServerScopeAgent)
		out = append(out, s.mcpServerRow(rc, srv, appcfg.MCPServerScopeAgent, source, entry != nil, entry))
	}
	for _, srv := range lc.Agents.Defaults.MCPServers {
		if shadowed[strings.ToLower(strings.TrimSpace(srv.Name))] {
			continue
		}
		entry := mcpLayerEntry(rc, srv, appcfg.MCPServerScopeGlobal)
		out = append(out, s.mcpServerRow(rc, srv, appcfg.MCPServerScopeGlobal, source, entry != nil, entry))
	}
	writeMCPRows(w, readListQuery(r), mcpLayerFilter(r.URL.Query().Get("source")), out,
		map[string]any{"view": "agent"})
}

// mcpLayerEntry is the runtime record a configured entry of one layer can
// claim: the session's entry for the name only when it is this same layer's and
// actually running. A higher layer's same-named entry (or the disable store)
// means the configured entry is not the one that started, and borrowing that
// state would describe a different server.
func mcpLayerEntry(rc mcpRowContext, srv appcfg.MCPServerConfig, scope string) *turn.MCPServerEntry {
	if eff := rc.inv.Entry(srv.Name); eff != nil && eff.Scope == scope && eff.Running {
		return eff
	}
	return nil
}

// writeMCPRows answers one MCP read view under the paging contract every list
// endpoint follows: without "page" the view keeps the answer it always gave
// (rows under "servers" plus the caller's own top-level fields); with it, the
// search and the layer filter are applied, then the order the caller built, then
// the slice — and the answer keeps the same "servers" key, the caller's fields,
// and total/page/page_size beside them.
func writeMCPRows(w http.ResponseWriter, q listQuery, layers map[string]bool, rows []map[string]any, extra map[string]any) {
	if !q.Paged {
		payload := make(map[string]any, len(extra)+1)
		for field, value := range extra {
			payload[field] = value
		}
		payload["servers"] = rows
		writeAgentsJSON(w, payload)
		return
	}
	matched := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if mcpRowMatches(row, q.Q, layers) {
			matched = append(matched, row)
		}
	}
	page, served := pageOf(matched, q)
	writeListPage(w, "servers", page, len(matched), served, q, extra)
}

// mcpRowMatches reports whether one rendered row survives a read view's search
// and layer filter. The search reads the two things a person looks a server up
// by — its name and its command or address; the layer filter reads the row's
// scope, which every view stamps.
func mcpRowMatches(row map[string]any, q string, layers map[string]bool) bool {
	if len(layers) > 0 {
		scope, _ := row["scope"].(string)
		if !layers[strings.ToLower(strings.TrimSpace(scope))] {
			return false
		}
	}
	if q == "" {
		return true
	}
	name, _ := row["name"].(string)
	target, _ := row["target"].(string)
	needle := strings.ToLower(q)
	return strings.Contains(strings.ToLower(name), needle) || strings.Contains(strings.ToLower(target), needle)
}

// mcpLayerFilter reads a view's "source" filter: the layers to keep, as a
// comma-separated list of project/agent/global. An empty or unrecognized value
// filters nothing, which is what the table's "all sources" option means.
func mcpLayerFilter(raw string) map[string]bool {
	var layers map[string]bool
	for _, part := range strings.Split(raw, ",") {
		switch layer := strings.ToLower(strings.TrimSpace(part)); layer {
		case appcfg.MCPServerScopeProject, appcfg.MCPServerScopeAgent, appcfg.MCPServerScopeGlobal:
			if layers == nil {
				layers = make(map[string]bool, 3)
			}
			layers[layer] = true
		}
	}
	return layers
}

// mcpServerScopeOf is the layer an entry came from: the merge in
// process.ResolveSessionMCP stamps it, and an unstamped entry is the shared one.
func mcpServerScopeOf(srv appcfg.MCPServerConfig) string {
	switch {
	case mcp.IsProjectScope(srv):
		return appcfg.MCPServerScopeProject
	case mcp.IsAgentScope(srv):
		return appcfg.MCPServerScopeAgent
	}
	return appcfg.MCPServerScopeGlobal
}

// mcpRowContext is what one row's rendering reads: the session's effective list
// and its runtime, and the stores a row's state comes from.
type mcpRowContext struct {
	inv           turn.MCPInventory
	runtime       *turn.MCPRuntimeView
	runtimeByName map[string]mcp.ServerRecord
	official      *mcp.OfficialRegistry
	officialSet   bool
	disabledNext  func(appcfg.MCPServerConfig) bool
}

func (s *Server) mcpRowsContext(lc *appcfg.Root, runtimeRunner *run.Runner) mcpRowContext {
	rc := mcpRowContext{}
	if runtimeRunner != nil {
		rc.inv = turn.BuildMCPInventory(s.mcpInventorySource(runtimeRunner, lc))
		snapshot := runtimeRunner.MCPStartup().Snapshot()
		if len(snapshot.Servers) > 0 {
			rc.runtime = &turn.MCPRuntimeView{
				Servers:    snapshot.Servers,
				Generation: snapshot.Generation,
				Pending:    snapshot.Pending,
			}
		}
		if next, err := mcp.DisabledForNextSession(runtimeRunner.WorkspaceRoot); err == nil {
			rc.disabledNext = next
		}
	} else if lc != nil {
		// No runtime to quote — the base runner hosts no servers — but "what
		// a session here would load" is still a real question: the inventory
		// comes from resolving the shared list plus this agent's own, exactly
		// the layers a runner freeze used to hold, only the live state is
		// absent. A Server assembled without a Runner at all has only the
		// configuration to show: no session, no runtime, no store.
		if s.Runner != nil {
			res := process.ResolveSessionMCP(s.Home, s.Runner.WorkspaceRoot, lc.Agents, s.Runner.AgentName, safety.ProjectContext{})
			src := turn.MCPInventorySource{
				Servers:      res.Servers,
				Disabled:     res.Disabled,
				GlobalSource: turn.GlobalMCPSource(lc),
			}
			if next, err := mcp.DisabledForNextSession(s.Runner.WorkspaceRoot); err == nil {
				src.DisabledNext = next
				rc.disabledNext = next
			}
			rc.inv = turn.BuildMCPInventory(src)
		} else {
			rc.inv = turn.BuildMCPInventory(turn.MCPInventorySource{Servers: lc.Agents.Defaults.MCPServers})
		}
	}
	rc.runtimeByName = map[string]mcp.ServerRecord{}
	if rc.runtime != nil {
		for _, rec := range rc.runtime.Servers {
			rc.runtimeByName[strings.ToLower(strings.TrimSpace(rec.Name))] = rec
		}
	}
	rc.official, rc.officialSet, _ = mcp.LoadOfficialRegistryFromEnv(s.Home)
	return rc
}

// mcpServerRow renders one entry plus what it is doing right now. running says
// whether this entry is in the session's effective list — an entry the disable
// store kept out is listed too, and is not running — and entry is that list's
// own record for the name, nil when it has none.
func (s *Server) mcpServerRow(rc mcpRowContext, srv appcfg.MCPServerConfig, scope, source string, running bool, entry *turn.MCPServerEntry) map[string]any {
	row := readMCPServerWrite(srv)
	row["scope"] = scope
	row["source"] = source
	row["url_set"] = strings.TrimSpace(srv.URL) != ""
	row["oauth_configured"] = mcp.OAuthConfiguredPublic(srv.OAuth)
	row["running"] = running
	row["disabled_by_config"] = !appcfg.ServerEnabled(srv)
	next := false
	if rc.disabledNext != nil {
		next = rc.disabledNext(srv)
	}
	if entry != nil {
		next = entry.DisabledNext
	}
	row["disabled_next_session"] = next
	if _, ok, err := mcp.LoadOAuthOverlay(s.Home, mcp.OverlayProjectKey(srv), srv.Name); err == nil {
		row["oauth_overlay"] = ok
	}
	if entry == nil {
		return row
	}
	row["auth_status"] = entry.Auth
	row["required"] = entry.Required
	if len(entry.Tools) > 0 {
		row["tools"] = entry.Tools
	}
	if rc.officialSet {
		row["official_url"] = rc.official != nil && rc.official.IsOfficialURL(strings.TrimSpace(srv.URL))
	}
	if rc.runtime == nil {
		// No generation to report: the entry is in the session's list but
		// nothing has published what it is doing.
		return row
	}
	rec, ok := rc.runtimeByName[strings.ToLower(entry.Name)]
	connStatus := "unknown"
	if ok {
		connStatus = string(rec.ConnStatus)
	}
	row["conn_status"] = connStatus
	row["tool_count"] = entry.ToolCount
	row["generation"] = rc.runtime.Generation
	row["pending"] = rc.runtime.Pending
	if entry.Error != "" {
		row["error"] = entry.Error
	}
	return row
}
