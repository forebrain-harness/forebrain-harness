package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v2"
)

var envReferencePattern = regexp.MustCompile(`\$\{([^}]+)\}`)
var primaryAgentIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

var builtInSubagentDefinitionNames = map[string]struct{}{
	"general-purpose":       {},
	"explore":               {},
	"plan":                  {},
	"verification":          {},
	"cavecrew-investigator": {},
	"cavecrew-builder":      {},
	"cavecrew-reviewer":     {},
	"guardian":              {},
}

// builtInPublicSubagentNames and builtInSystemSubagentNames are this package's
// copy of agent.PublicSubagentNames / agent.SystemSubagentNames, which decide
// whether a subagent entry overrides a built-in or declares a custom one. They
// are copies rather than a read of the agent package so the configuration layer
// stays independent of the runtime it configures;
// TestBuiltInSubagentNameCopiesMatchAgentPackage fails when the two disagree,
// so add a new built-in to both places.
var builtInPublicSubagentNames = []string{"general-purpose", "explore", "plan", "verification"}

var builtInSystemSubagentNames = []string{"plan-reviewer", "goal-evaluator", "guardian", "cavecrew-investigator", "cavecrew-builder", "cavecrew-reviewer"}

func Load(path string) (Root, error) {
	r, err := loadPersisted(path)
	if err != nil {
		return Root{}, err
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	r.SourceFiles = []string{path}
	if err := applyDotEnvAfterNormalize(path, &r); err != nil {
		return Root{}, err
	}
	expandRootEnvReferences(&r)
	if err := validateLoadedRoot(&r); err != nil {
		return Root{}, err
	}
	return r, nil
}

// LoadPersisted reads the on-disk configuration without merging runtime
// environment secrets into the returned value. Use it for read-modify-write
// operations so a UI setting change cannot persist API keys sourced from
// ~/.forebrain/.env or process environment variables back into forebrain.yaml.
func LoadPersisted(path string) (Root, error) {
	r, err := loadPersisted(path)
	if err != nil {
		return Root{}, err
	}
	if err := validateLoadedRoot(&r); err != nil {
		return Root{}, err
	}
	return r, nil
}

// FinishLoad runs the tail of Load on a root the caller assembled by hand:
// the home .env merge, ${ENV} expansion, and validation, in Load's order. The
// composed runtime configuration — the YAML's startup sections plus the
// database's runtime sections (plan 187) — runs through the same steps a root
// read from one file did, so both behave identically.
func FinishLoad(r *Root) error {
	if r == nil {
		return fmt.Errorf("finish load: nil root")
	}
	if err := applyDotEnvAfterNormalize("", r); err != nil {
		return err
	}
	expandRootEnvReferences(r)
	return validateLoadedRoot(r)
}

func loadPersisted(path string) (Root, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Root{}, err
	}
	if err := rejectPlaintextConfigSecrets(path, b); err != nil {
		return Root{}, err
	}
	// approval_policy is deliberately left at its zero value: an empty mode is
	// how "not configured" reaches sandboxrt.EffectiveConfig, which derives the
	// policy from the launch project's trust decision. Seeding a concrete mode
	// here would make every configuration look explicitly configured.
	r := Root{ApprovalsReviewer: "user"}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(b, &r); err != nil {
			return Root{}, err
		}
	case ".json":
		if err := json.Unmarshal(b, &r); err != nil {
			return Root{}, err
		}
	default:
		if err := json.Unmarshal(b, &r); err != nil {
			if err2 := yaml.Unmarshal(b, &r); err2 != nil {
				return Root{}, err
			}
		}
	}
	normalize(&r)
	return r, nil
}

func validateLoadedRoot(r *Root) error {
	switch r.ApprovalPolicy.Mode {
	case "", ApprovalPolicyUntrusted, ApprovalPolicyOnRequest, ApprovalPolicyNever, ApprovalPolicyGranular:
	default:
		return fmt.Errorf("invalid approval_policy")
	}
	switch strings.ToLower(strings.TrimSpace(r.ApprovalsReviewer)) {
	case "", "user", "auto_review":
	default:
		return fmt.Errorf("approvals_reviewer must be user or auto_review")
	}
	switch r.SandboxMode {
	case "", SandboxModeReadOnly, SandboxModeWorkspaceWrite, SandboxModeDangerFullAccess:
	default:
		return fmt.Errorf("sandbox_mode must be read-only, workspace-write, or danger-full-access")
	}
	switch r.Windows.Sandbox {
	case "", WindowsSandboxUnelevated, WindowsSandboxElevated:
	default:
		return fmt.Errorf("windows.sandbox must be unelevated or elevated")
	}
	if r.Compact.ModelAutoCompactTokenLimit < 0 {
		return fmt.Errorf("assembly.model_auto_compact_token_limit must be non-negative")
	}
	if v := r.Agents.Defaults.NetworkRetryMax; v != nil && *v < 0 {
		return fmt.Errorf("agents.defaults.network_retry_max must be non-negative")
	}
	if v := r.Agents.Defaults.StreamIdleTimeoutSeconds; v != nil && *v < 0 {
		return fmt.Errorf("agents.defaults.stream_idle_timeout_seconds must be non-negative")
	}
	scope := strings.TrimSpace(r.Compact.ModelAutoCompactTokenLimitScope)
	if scope != "" && scope != "total" && scope != "body_after_prefix" {
		return fmt.Errorf("assembly.model_auto_compact_token_limit_scope must be total or body_after_prefix")
	}
	if err := validateSandboxConfig(r); err != nil {
		return err
	}
	if err := validatePermissionProfiles(r); err != nil {
		return err
	}
	if err := validateNetworkProxyFeature(r.Features.NetworkProxy); err != nil {
		return err
	}
	if err := validateMCPToolApprovalModes(r); err != nil {
		return err
	}
	if err := validateTokenEstimate(r.Agents.Defaults.TokenEstimate); err != nil {
		return err
	}
	if err := validateLSPSection(r); err != nil {
		return err
	}
	if err := ValidateHooksSettings(r.Hooks); err != nil {
		return err
	}
	if err := ValidateCronSection(r.Cron); err != nil {
		return err
	}
	if err := validatePrimaryAgents(r); err != nil {
		return err
	}
	return nil
}

// ValidateCronSection checks the install's scheduled-task settings. It is the
// one verdict behind every write path — startup, hot reload, the YAML editor,
// and the structured settings endpoint — so an out-of-range retention is
// rejected wherever it comes from.
func ValidateCronSection(c CronSection) error {
	if c.RetentionDays == nil {
		return nil
	}
	if *c.RetentionDays < MinCronRetentionDays || *c.RetentionDays > MaxCronRetentionDays {
		return fmt.Errorf("cron.retention_days must be between %d and %d", MinCronRetentionDays, MaxCronRetentionDays)
	}
	return nil
}

func expandRootEnvReferences(r *Root) {
	if r == nil {
		return
	}
	expandStringEnvReferences(reflect.ValueOf(r))
}

func expandStringEnvReferences(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		expandStringEnvReferences(v.Elem())
		return
	}
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(expandBracedEnvReferences(v.String()))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			expandStringEnvReferences(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			expandStringEnvReferences(v.Index(i))
		}
	case reflect.Map:
		expandMapStringEnvReferences(v)
	}
}

func expandMapStringEnvReferences(v reflect.Value) {
	if v.IsNil() {
		return
	}
	for _, key := range v.MapKeys() {
		value := v.MapIndex(key)
		if value.Kind() == reflect.String {
			v.SetMapIndex(key, reflect.ValueOf(expandBracedEnvReferences(value.String())))
			continue
		}
		if value.Kind() == reflect.Pointer && value.IsNil() {
			continue
		}
		if value.Kind() == reflect.Pointer || value.Kind() == reflect.Struct ||
			value.Kind() == reflect.Slice || value.Kind() == reflect.Array || value.Kind() == reflect.Map {
			next := reflect.New(value.Type()).Elem()
			next.Set(value)
			expandStringEnvReferences(next)
			v.SetMapIndex(key, next)
		}
	}
}

func expandBracedEnvReferences(s string) string {
	return envReferencePattern.ReplaceAllStringFunc(s, func(match string) string {
		if !isSingleEnvReference(match) {
			return match
		}
		name := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		return os.Getenv(name)
	})
}

func Save(path string, r Root) error {
	normalize(&r)
	applyPersistedDefaults(&r)
	if err := validatePrimaryAgents(&r); err != nil {
		return err
	}

	if persisted, err := LoadPersisted(path); err == nil {
		restorePersistedSecrets(&r, persisted)
	}
	dropPlaceholderLLMProviders(&r)

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".json" {
		b, _ := json.MarshalIndent(r, "", "  ")
		return writeFileAtomic(path, b, 0o600)
	}

	b, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}

// dropPlaceholderLLMProviders removes provider entries that carry no settings.
// normalize keeps an empty entry in memory so the primary provider always has a
// slot to merge .env values into, but persisting it writes a bare `- {}` that
// documents nothing. Load recreates the slot, so removing it is round-trip
// safe. It runs last, after validation has seen the in-memory shape.
func dropPlaceholderLLMProviders(r *Root) {
	if r == nil {
		return
	}
	for name, def := range r.Agents.Definitions {
		kept := make([]AgentLLMProviderConfig, 0, len(def.LLMProviders))
		for _, provider := range def.LLMProviders {
			if !rendersEmpty(provider) {
				kept = append(kept, provider)
			}
		}
		if len(kept) == len(def.LLMProviders) {
			continue
		}
		def.LLMProviders = kept
		r.Agents.Definitions[name] = def
	}
}

// restorePersistedSecrets removes runtime-only environment values from a
// configuration about to be written. Load merges .env values for runtime use;
// a following Save must retain only the corresponding persisted secret value
// (usually a ${ENV_NAME} reference) or clear the field when none was stored.
func restorePersistedSecrets(dst *Root, src Root) {
	if dst == nil {
		return
	}
	restoreSecretValues(reflect.ValueOf(dst).Elem(), reflect.ValueOf(src))
}

func restoreSecretValues(dst, src reflect.Value) {
	if !dst.IsValid() || !src.IsValid() || dst.Type() != src.Type() {
		return
	}
	switch dst.Kind() {
	case reflect.Struct:
		for i := 0; i < dst.NumField(); i++ {
			field := dst.Type().Field(i)
			if field.PkgPath != "" {
				continue
			}
			dstField, srcField := dst.Field(i), src.Field(i)
			name := strings.Split(field.Tag.Get("yaml"), ",")[0]
			if isSecretLikeConfigKey(name) && dstField.Kind() == reflect.String {
				// Skip restore if dst is already a safe-to-persist env reference.
				// This preserves the authoritative value after reorders/mutations.
				dstStr := dstField.String()
				if dstStr != "" && isSingleEnvReference(dstStr) {
					continue
				}
				dstField.Set(srcField)
				continue
			}
			restoreSecretValues(dstField, srcField)
		}
	case reflect.Ptr:
		if src.IsNil() {
			if containsSecretConfigField(dst.Type().Elem()) {
				dst.SetZero()
			}
			return
		}
		if !dst.IsNil() {
			restoreSecretValues(dst.Elem(), src.Elem())
		}
	case reflect.Slice:
		// For slices with secret-bearing structs, match by identity when possible
		// so reorders preserve the correct secret for each logical entry.
		if dst.Len() > 0 && src.Len() > 0 && containsSecretConfigField(dst.Type().Elem()) {
			restoreSliceSecretsWithIdentity(dst, src)
		} else {
			for i := 0; i < dst.Len(); i++ {
				var srcItem reflect.Value
				if i < src.Len() {
					srcItem = src.Index(i)
				} else {
					srcItem = reflect.Zero(dst.Index(i).Type())
				}
				restoreSecretValues(dst.Index(i), srcItem)
			}
		}
	case reflect.Map:
		for _, key := range dst.MapKeys() {
			dstItem := dst.MapIndex(key)
			srcItem := src.MapIndex(key)
			if !srcItem.IsValid() {
				srcItem = reflect.Zero(dstItem.Type())
			}
			copy := reflect.New(dstItem.Type()).Elem()
			copy.Set(dstItem)
			restoreSecretValues(copy, srcItem)
			dst.SetMapIndex(key, copy)
		}
	}
}

// restoreSliceSecretsWithIdentity matches dst slice elements to src by
// structural identity (equal non-secret fields), falling back to the element in
// the same position only when the two name the same secret owner. This prevents
// secret swaps when llm_providers are reordered, stops an entry that landed in
// another provider's slot from being handed that provider's key, keeps a
// login-backed entry keyless once a write cleared it, and drops a reference the
// configuration itself attributes to another provider.
func restoreSliceSecretsWithIdentity(dst, src reflect.Value) {
	for i := 0; i < dst.Len(); i++ {
		dstItem := dst.Index(i)
		// Try to find a src element with matching non-secret identity
		srcItem := findMatchingSourceItem(dstItem, src)
		if !srcItem.IsValid() && i < src.Len() && sameSecretOwner(dstItem, src.Index(i)) {
			// Fall back to positional only for the same credential owner
			srcItem = src.Index(i)
		}
		if keyClearedByWrite(dstItem) || foreignProviderKey(dstItem, srcItem, src) {
			// The key must go: either the write path removed it on purpose, or
			// it names another provider's key. restoreSecretValues leaves a
			// value that is already a ${ENV} reference in place, which is
			// right for a key the user keeps and wrong for one that must go,
			// so the field is emptied here rather than left to the restore.
			clearProviderAPIKey(dstItem)
			srcItem = reflect.Zero(dstItem.Type())
		}
		if !srcItem.IsValid() {
			srcItem = reflect.Zero(dstItem.Type())
		}
		restoreSecretValues(dstItem, srcItem)
	}
}

// sameSecretOwner reports whether the positional pair names the same credential
// owner, so restoring a secret between them cannot hand one provider's key to
// another. The provider name is the owner: an entry whose model or base_url
// changed in place still owns the key it had, while a provider inserted at (or
// shifted into) a slot that used to hold a different provider owns nothing there
// and must start without a key. Elements of any other type have no owner this
// function can name, so they are never paired positionally.
func sameSecretOwner(dst, src reflect.Value) bool {
	d, ok := providerEntryOf(dst)
	if !ok {
		return false
	}
	s, ok := providerEntryOf(src)
	if !ok {
		return false
	}
	dp, sp := strings.TrimSpace(d.Provider), strings.TrimSpace(s.Provider)
	return dp != "" && sp != "" && strings.EqualFold(dp, sp)
}

// providerEntryOf reads a slice element as an AgentLLMProviderConfig and
// reports false for every other element type: the secret-bearing slices this
// package restores are not all provider lists, and only a provider entry has an
// owner or a key that can be attributed.
func providerEntryOf(v reflect.Value) (AgentLLMProviderConfig, bool) {
	if !v.IsValid() || !v.CanInterface() {
		return AgentLLMProviderConfig{}, false
	}
	c, ok := v.Interface().(AgentLLMProviderConfig)
	return c, ok
}

// clearProviderAPIKey empties the entry's api_key so a key the write path
// dropped stays dropped.
func clearProviderAPIKey(v reflect.Value) {
	f := v.FieldByName("APIKey")
	if f.IsValid() && f.CanSet() && f.Kind() == reflect.String {
		f.SetString("")
	}
}

// keyClearedByWrite reports whether the entry's key was removed on purpose by
// the write that produced this configuration — /connect configuring the
// provider from a login. An empty api_key alone cannot say that: it is also
// what an entry looks like when its ${ENV} reference on disk resolved to
// nothing, and that reference must survive the save.
func keyClearedByWrite(v reflect.Value) bool {
	c, ok := providerEntryOf(v)
	return ok && c.APIKeyCleared
}

// providerKeyEnvName is the variable name the write paths give a provider's
// key: UPPERCASE(provider) with dashes as underscores, then _API_KEY. It
// mirrors pkg/process.ProviderKeyEnvMap, which is what actually names the
// variable when a key is stored; pkg/config cannot import it (pkg/process
// imports this package), so the two spellings have to be kept in step here.
func providerKeyEnvName(provider string) string {
	p := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(provider)), "-", "_")
	return p + "_API_KEY"
}

// foreignProviderKey reports whether the key this save would write for a
// login-backed entry is a ${ENV} reference another provider in the same list
// declares as its own: that provider's row carries the same reference, and the
// name is the variable the write path gives that provider. Such a reference is
// a secret lent across providers — how /connect used to hand a displaced
// provider's key to a new entry — and a login-backed entry must never keep one,
// because for OpenCode a configured key outranks the login file in the client's
// resolution order, which is what produced the 401. Nothing else is touched: a
// custom name, a token two providers share under a name neither owns, and a
// reference nobody claims all stay, so the check removes only what the
// configuration itself attributes to another provider.
func foreignProviderKey(dstItem, srcItem, src reflect.Value) bool {
	dst, ok := providerEntryOf(dstItem)
	if !ok || !CredentialsBackedProvider(dst.Provider) {
		return false
	}
	srcEntry, ok := providerEntryOf(srcItem)
	if !ok {
		return false
	}
	ref := strings.TrimSpace(srcEntry.APIKey)
	name := envNameOfReference(ref)
	if name == "" || name == providerKeyEnvName(dst.Provider) {
		return false
	}
	for j := 0; j < src.Len(); j++ {
		other, ok := providerEntryOf(src.Index(j))
		if !ok || strings.EqualFold(strings.TrimSpace(other.Provider), strings.TrimSpace(dst.Provider)) {
			continue
		}
		if strings.TrimSpace(other.APIKey) == ref && providerKeyEnvName(other.Provider) == name {
			return true
		}
	}
	return false
}

// envNameOfReference returns the variable a ${NAME} reference names, or "" for
// anything else.
func envNameOfReference(ref string) string {
	if !isSingleEnvReference(ref) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(ref), "${"), "}")
}

// findMatchingSourceItem searches src slice for an element whose non-secret
// fields match dstItem. Returns invalid reflect.Value if no match.
func findMatchingSourceItem(dstItem, src reflect.Value) reflect.Value {
	if dstItem.Kind() != reflect.Struct && (dstItem.Kind() != reflect.Ptr || dstItem.Elem().Kind() != reflect.Struct) {
		return reflect.Value{}
	}
	dstElem := dstItem
	if dstItem.Kind() == reflect.Ptr {
		if dstItem.IsNil() {
			return reflect.Value{}
		}
		dstElem = dstItem.Elem()
	}
	for i := 0; i < src.Len(); i++ {
		srcItem := src.Index(i)
		srcElem := srcItem
		if srcItem.Kind() == reflect.Ptr {
			if srcItem.IsNil() {
				continue
			}
			srcElem = srcItem.Elem()
		}
		if structIdentityMatches(dstElem, srcElem) {
			return srcItem
		}
	}
	return reflect.Value{}
}

// structIdentityMatches returns true if dst and src have equal non-secret
// string fields. Used to match reordered AgentLLMProviderConfig entries.
func structIdentityMatches(dst, src reflect.Value) bool {
	if dst.Kind() != reflect.Struct || src.Kind() != reflect.Struct {
		return false
	}
	if dst.Type() != src.Type() {
		return false
	}
	matchCount := 0
	for i := 0; i < dst.NumField(); i++ {
		field := dst.Type().Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("yaml"), ",")[0]
		// Skip secret fields and non-string fields
		if isSecretLikeConfigKey(name) || field.Type.Kind() != reflect.String {
			continue
		}
		dstVal := dst.Field(i).String()
		srcVal := src.Field(i).String()
		dstTrim := strings.TrimSpace(dstVal)
		srcTrim := strings.TrimSpace(srcVal)
		// Both empty: no signal
		if dstTrim == "" && srcTrim == "" {
			continue
		}
		// Mismatch: not the same entry
		if !strings.EqualFold(dstTrim, srcTrim) {
			return false
		}
		matchCount++
	}
	// Require at least one non-secret field match to avoid false positives
	return matchCount > 0
}

func containsSecretConfigField(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if isSecretLikeConfigKey(name) || containsSecretConfigField(field.Type) {
			return true
		}
	}
	return false
}

func normalize(r *Root) {
	r.ApprovalsReviewer = strings.ToLower(strings.TrimSpace(r.ApprovalsReviewer))
	r.AutoReview.Policy = strings.TrimSpace(r.AutoReview.Policy)
	r.Compact.Prompt = strings.TrimSpace(r.Compact.Prompt)
	r.Compact.ModelAutoCompactTokenLimitScope = strings.TrimSpace(r.Compact.ModelAutoCompactTokenLimitScope)
	if r.Gateway.HTTPAddr == "" {
		r.Gateway.HTTPAddr = "127.0.0.1:6060"
	}
	if strings.TrimSpace(r.Gateway.Auth.Mode) == "" {
		r.Gateway.Auth.Mode = "token"
	}
	if r.Agents.Definitions == nil {
		r.Agents.Definitions = map[string]AgentDefinition{}
	}
	if _, ok := r.Agents.Definitions["main"]; !ok {
		r.Agents.Definitions["main"] = AgentDefinition{
			LLMProviders: []AgentLLMProviderConfig{{}},
		}
	}
	mainDef := r.Agents.Definitions["main"]
	if len(mainDef.LLMProviders) == 0 {
		mainDef.LLMProviders = []AgentLLMProviderConfig{{}}
	}
	NormalizeLLMProviderEntries(&mainDef)
	r.Agents.Definitions["main"] = mainDef
	for k := range r.Agents.Definitions {
		if k == "main" {
			continue
		}
		d := r.Agents.Definitions[k]
		NormalizeLLMProviderEntries(&d)
		r.Agents.Definitions[k] = d
	}
	NormalizeMCPServers(r.Agents.Defaults.MCPServers)
	for k := range r.Agents.Definitions {
		d := r.Agents.Definitions[k]
		NormalizeMCPServers(d.MCPServers)
		r.Agents.Definitions[k] = d
	}
	normalizeSandboxConfig(r)
	normalizeLSPSection(&r.LSP)
}

// validateMCPToolApprovalModes validates every MCP server list the
// configuration holds: the shared one and each primary agent's own. The source
// named in the message is the section a reader can find in forebrain.yaml, so
// an entry that is wrong in an agent's list does not send them to the shared
// one.
func validateMCPToolApprovalModes(r *Root) error {
	if r == nil {
		return nil
	}
	if err := ValidateMCPServers("agents.defaults.mcp_servers", r.Agents.Defaults.MCPServers); err != nil {
		return err
	}
	for _, id := range sortedDefinitionIDs(r.Agents.Definitions) {
		def := r.Agents.Definitions[id]
		if err := ValidateMCPServers("agents.definitions."+id+".mcp_servers", def.MCPServers); err != nil {
			return err
		}
	}
	return nil
}

// sortedDefinitionIDs lists the agent definition keys in ascending order, so a
// validation error names the same agent on every load rather than whichever
// one a map iteration happened to reach first.
func sortedDefinitionIDs(defs map[string]AgentDefinition) []string {
	ids := make([]string, 0, len(defs))
	for id := range defs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// validateTokenEstimate checks the agents.defaults.token_estimate section: the
// hubs are known, and every override names a model and exactly one of a
// built-in tokenizer id or a local path.
func validateTokenEstimate(c TokenEstimateConfig) error {
	for _, hub := range c.Hubs {
		switch strings.ToLower(strings.TrimSpace(hub)) {
		case "huggingface", "modelscope":
		default:
			return fmt.Errorf("agents.defaults.token_estimate.hubs: unknown hub %q (want huggingface or modelscope)", hub)
		}
	}
	for i, override := range c.Tokenizers {
		if strings.TrimSpace(override.Model) == "" {
			return fmt.Errorf("agents.defaults.token_estimate.tokenizers[%d]: model is required", i)
		}
		hasTokenizer := strings.TrimSpace(override.Tokenizer) != ""
		hasPath := strings.TrimSpace(override.Path) != ""
		if hasTokenizer == hasPath {
			return fmt.Errorf("agents.defaults.token_estimate.tokenizers[%d] (%s): exactly one of tokenizer or path must be set", i, override.Model)
		}
		// One process serves several projects (the gateway), so a relative
		// path would resolve against whatever its working directory is.
		if hasPath && !filepath.IsAbs(strings.TrimSpace(override.Path)) {
			return fmt.Errorf("agents.defaults.token_estimate.tokenizers[%d] (%s): path must be absolute", i, override.Model)
		}
	}
	return nil
}

func intPtr(v int) *int { return &v }

// NormalizeMCPServers canonicalizes approval-mode strings in place. It runs on
// every MCP server list regardless of source — the global file and project
// files share one shape, so they share one normalization.
func NormalizeMCPServers(servers []MCPServerConfig) {
	for i := range servers {
		server := &servers[i]
		server.DefaultToolsApprovalMode = MCPToolApprovalMode(strings.ToLower(strings.TrimSpace(string(server.DefaultToolsApprovalMode))))
		for name, tool := range server.Tools {
			tool.ApprovalMode = MCPToolApprovalMode(strings.ToLower(strings.TrimSpace(string(tool.ApprovalMode))))
			server.Tools[name] = tool
		}
	}
}

// ValidateMCPServers checks one MCP server list and reports the first problem
// with source naming the list it came from ("agents.defaults.mcp_servers" or
// "agents.definitions.<id>.mcp_servers" for the shared and per-agent lists in
// forebrain.yaml, the project file path for a project one) so the message
// points the user at the section they need to fix.
func ValidateMCPServers(source string, servers []MCPServerConfig) error {
	for i, server := range servers {
		if !server.DefaultToolsApprovalMode.Valid() {
			return fmt.Errorf("%s[%d].default_tools_approval_mode must be auto, prompt, writes, or approve", source, i)
		}
		if err := ValidateMCPTimeout(server.StartupTimeout); err != nil {
			return fmt.Errorf("%s[%d].startup_timeout %w", source, i, err)
		}
		if err := ValidateMCPTimeout(server.RequestTimeout); err != nil {
			return fmt.Errorf("%s[%d].request_timeout %w", source, i, err)
		}
		// A server that is both switched off and required is a contradiction:
		// nothing can start it, so any run that cannot do without it can never
		// succeed. Rejecting it here says so once, at the file, instead of
		// failing the first unattended run that needed it.
		if !ServerEnabled(server) && server.Required {
			return fmt.Errorf("%s[%d].required is true while enabled is false; a required server must be enabled", source, i)
		}
		for name, tool := range server.Tools {
			if !tool.ApprovalMode.Valid() {
				return fmt.Errorf("%s[%d].tools.%s.approval_mode must be auto, prompt, writes, or approve", source, i, name)
			}
		}
	}
	return nil
}

func applyPersistedDefaults(r *Root) {
	if r == nil {
		return
	}
	f := false

	r.Gateway.HTTPAddr = strings.TrimSpace(r.Gateway.HTTPAddr)
	r.Gateway.Auth.Mode = strings.TrimSpace(r.Gateway.Auth.Mode)

	if r.Agents.Defaults.ContextInject.WarnRemainingTokens <= 0 {
		r.Agents.Defaults.ContextInject.WarnRemainingTokens = 25000
	}
	if r.Agents.Defaults.ContextInject.BlockRemainingTokens <= 0 {
		r.Agents.Defaults.ContextInject.BlockRemainingTokens = 13000
	}

	if r.Agents.Defaults.Guardrails.Input.MaxRunes <= 0 {
		r.Agents.Defaults.Guardrails.Input.MaxRunes = 200000
	}
	if r.Agents.Defaults.EnableSubagent == nil {
		// Subagent tools (subagent_run, subagent_fanout, etc.) are opt-in:
		// they must be enabled explicitly via agents.defaults.enable_subagent.
		r.Agents.Defaults.EnableSubagent = &f
	}
	if r.Agents.Defaults.Guardrails.Retrieval.MaxChunkRunes <= 0 {
		r.Agents.Defaults.Guardrails.Retrieval.MaxChunkRunes = 120000
	}

	materializeOptionalDefaults(r)
}

// materializeOptionalDefaults writes the resolved value of every optional
// setting back into the configuration about to be persisted, so forebrain.yaml
// documents the settings that exist and the values actually in force instead of
// an opaque `features: {}`.
//
// Every value is read back through the same accessor the runtime consults, so
// the persisted default cannot drift from the running default: changing one
// changes both. Save is the only caller — a loaded configuration keeps nil
// meaning "unset" in memory.
func materializeOptionalDefaults(r *Root) {
	features := r.EffectiveFeatures()
	r.Features.ExecPermissionApprovals = BoolPtr(features.ExecPermissionApprovals)
	r.Features.RequestPermissionsTool = BoolPtr(features.RequestPermissionsTool)
	r.Features.Memories = BoolPtr(features.Memories)
	r.Features.LSP = BoolPtr(features.LSP)

	lsp := r.EffectiveLSP()
	r.LSP.Recommendations = BoolPtr(lsp.Recommendations)
	r.LSP.Diagnostics.AfterEdit = BoolPtr(lsp.AfterEdit)
	r.LSP.Diagnostics.LateDelivery = BoolPtr(lsp.LateDelivery)
	waitMS := int(lsp.Wait / time.Millisecond)
	r.LSP.Diagnostics.WaitMS = &waitMS

	memories := r.EffectiveMemories()
	r.Memories.DisableOnExternalContext = BoolPtr(memories.DisableOnExternalContext)
	r.Memories.GenerateMemories = BoolPtr(memories.GenerateMemories)
	r.Memories.UseMemories = BoolPtr(memories.UseMemories)
	r.Memories.DedicatedTools = BoolPtr(memories.DedicatedTools)
	r.Memories.MaxRawMemoriesForConsolidation = intPtr(memories.MaxRawMemoriesForConsolidation)
	r.Memories.MaxUnusedDays = intPtr(memories.MaxUnusedDays)
	r.Memories.MaxRolloutAgeDays = intPtr(memories.MaxRolloutAgeDays)
	r.Memories.MaxRolloutsPerStartup = intPtr(memories.MaxRolloutsPerStartup)
	r.Memories.MinRolloutIdleHours = intPtr(memories.MinRolloutIdleHours)
	r.Memories.MinRateLimitRemainingPercent = intPtr(memories.MinRateLimitRemainingPercent)

	r.Compact.RemoteCompaction = BoolPtr(r.Compact.UseRemoteCompaction())
	r.Compact.RemoteCompactionV2 = BoolPtr(r.Compact.UseRemoteV2())
	r.Tools.CommandRewrite.Enabled = BoolPtr(r.Tools.CommandRewrite.UseRewrite())

	r.SandboxWorkspaceWrite.NetworkAccess = BoolPtr(r.SandboxWorkspaceWrite.EffectiveNetworkAccess())
	r.Windows.SandboxPrivateDesktop = BoolPtr(r.Windows.UseSandboxPrivateDesktop())
}

func IsEffectivePrimaryAgent(id string, def AgentDefinition) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "main" {
		return true
	}
	if IsBuiltInSubagentDefinition(id) {
		return false
	}
	return def.Primary
}

func IsBuiltInSubagentDefinition(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	_, ok := builtInSubagentDefinitionNames[id]
	return ok
}

func validatePrimaryAgents(r *Root) error {
	if r == nil || r.Agents.Definitions == nil {
		return nil
	}
	for id, def := range r.Agents.Definitions {
		if !primaryAgentIDPattern.MatchString(id) {
			return fmt.Errorf("agents.definitions.%s: invalid agent id", id)
		}
		// A dangling default_model must never reach disk: the runtime treats
		// an unresolvable default as an error (no fall-back to the first
		// entry), so load, hot reload and every structured write path share
		// this one verdict via validateLoadedRoot/Save.
		if strings.TrimSpace(def.DefaultModel) != "" && EffectiveDefaultLLM(def) == nil {
			return fmt.Errorf(
				"agents.definitions.%s.default_model: %q does not match any llm_providers entry on this agent's list",
				id, def.DefaultModel)
		}
		if err := ValidateAgentSubagents(id, def, builtInPublicSubagentNames, builtInSystemSubagentNames); err != nil {
			return err
		}
	}
	return nil
}

// ParseRootYAML reads a configuration from YAML text the way a file is read:
// same unmarshal, same normalisation, same validation. An editor that submits
// text therefore gets the same verdict the loader would give the file, before
// anything is written to disk.
func ParseRootYAML(b []byte) (Root, error) {
	r := Root{ApprovalsReviewer: "user"}
	if err := yaml.Unmarshal(b, &r); err != nil {
		return Root{}, err
	}
	normalize(&r)
	if err := validateLoadedRoot(&r); err != nil {
		return Root{}, err
	}
	return r, nil
}

// ValidPrimaryAgentID reports whether id is a legal tenant key: the same
// pattern the loader enforces on agents.definitions keys.
func ValidPrimaryAgentID(id string) bool {
	return primaryAgentIDPattern.MatchString(id)
}

// ValidateAgentRoot runs the loader's validation on a root that is about to
// be written, so a configuration the CLI would reject never reaches disk.
func ValidateAgentRoot(r *Root) error {
	return validateLoadedRoot(r)
}
