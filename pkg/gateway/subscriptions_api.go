// The subscriptions page's API (plan 265): the one list of sign-ins, what a
// finished sign-in offers, and the write that turns chosen offers into model
// rows.
//
// Nothing here knows a subscription by name. Every entry comes from
// pkg/subscription's registry, so a new sign-in is one line in that registry
// and no change here (plan 264). The gateway is a surface like the terminal:
// it lists, probes, lists offers and provisions, and its own wording is all it
// adds.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// The words a sign-in's state is shown as. They are pkg/turn's four states, and
// the page words each one itself.
const (
	signInStateUsable      = "usable"
	signInStateMissing     = "missing"
	signInStateDead        = "dead"
	signInStateUnreachable = "unreachable"
)

// signInStateName is the word one probe answer is shown as.
func signInStateName(state turn.SignInState) string {
	switch state {
	case turn.SignInUsable:
		return signInStateUsable
	case turn.SignInDead:
		return signInStateDead
	case turn.SignInUnreachable:
		return signInStateUnreachable
	default:
		return signInStateMissing
	}
}

// subscriptionRow is one entry of the subscriptions page: what it signs in to,
// the flow kinds it can drive on a gateway, how that sign-in stands today, and
// who it is signed in as.
type subscriptionRow struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Flows []string `json:"flows"`
	State string   `json:"state"`
	// Account is who the stored sign-in belongs to, read locally from the
	// credential — no vendor call. Nil when the subscription has no login
	// stored, no capability to name one, or a stored login that reads no
	// name; the page shows the state alone then.
	Account *subscriptionAccount `json:"account,omitempty"`
	// CanLogout says the page may offer a sign-out: the subscription can
	// remove its stored login, one is stored (any state but missing), and the
	// environment is not the one holding it.
	CanLogout bool `json:"can_logout"`
}

// subscriptionAccount is who a stored sign-in belongs to, as the page shows
// it: the identity the person recognizes, the plan or organization beside it,
// and the two honesty flags — EnvKey (the environment provides this sign-in,
// so it cannot be removed here) and SharedPath (the login file belongs to
// another program, which the same sign-in also logs in).
type subscriptionAccount struct {
	Name       string `json:"name"`
	Detail     string `json:"detail,omitempty"`
	EnvKey     bool   `json:"env_key,omitempty"`
	SharedPath string `json:"shared_path,omitempty"`
}

// subscriptionOffer is one model a finished sign-in unlocked, as the picker
// lists it.
type subscriptionOffer struct {
	Plan     string `json:"plan"`
	PlanName string `json:"plan_name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Name is the vendor's own name for Model; "" when the listing named none,
	// in which case the page shows Model.
	Name           string `json:"name,omitempty"`
	BaseURL        string `json:"base_url"`
	APIPath        string `json:"api_path"`
	Endpoint       string `json:"endpoint"`
	ContextWindow  int64  `json:"context_window,omitempty"`
	SupportsImages bool   `json:"supports_images"`
	// PaidModel is the paid twin of a model the product sells at two rates
	// (workbuddy's Hy4/Hy3): the page shows the pair as one row, so this is
	// what tells it the row has a second rate behind the same name.
	PaidModel string `json:"paid_model,omitempty"`
	// AllowPaidSwitch is the product's own flag for that pair: requests may
	// fall back to PaidModel once the free tier's allowance is spent, and the
	// page may offer the switch.
	AllowPaidSwitch bool `json:"allow_paid_switch,omitempty"`
	Default         bool `json:"default"`
	// Held marks a model this install already holds as a row, so the page opens
	// on the truth instead of on the vendor's own first row. The terminal's
	// models page asks the same question through the same rule
	// (process.LLMModelHeld).
	Held bool `json:"held"`
	// NeedsKey marks a plan whose row carries an API key the user pastes from the
	// vendor's console (plan 281): the sign-in proved the plan, and no API hands
	// the key over, so the page asks for it before the write.
	NeedsKey bool `json:"needs_key"`
	// FromStaticList marks an offer a subscription knows from a shipped table
	// rather than from the vendor's own list, so the page can say the console is
	// the authority.
	FromStaticList bool `json:"from_static_list"`
}

// subscriptionStates answers each subscription's sign-in state once per
// request. A probe asks the vendor, and the models table marks every row of one
// subscription by the same answer, so a page with ten rows of one plan still
// costs one call.
type subscriptionStates struct {
	s        *Server
	ctx      context.Context
	state    map[string]string
	accounts map[string]*subscriptionAccount
}

// newSubscriptionStates starts one request's answers. ctx bounds them: a page
// that is already canceled must not keep probing.
func newSubscriptionStates(s *Server, ctx context.Context) *subscriptionStates {
	return &subscriptionStates{s: s, ctx: ctx, state: map[string]string{}, accounts: map[string]*subscriptionAccount{}}
}

// of answers how one subscription's sign-in stands: usable, missing, dead, or
// unreachable (the vendor could not be asked). An empty source — a row added by
// hand — and an id no subscription carries answer "".
func (st *subscriptionStates) of(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return ""
	}
	if answer, ok := st.state[id]; ok {
		return answer
	}
	sub, found := gatewaySubscriptionByID(id)
	if !found {
		st.state[id] = ""
		return ""
	}
	ctx := st.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, modelsDiscoveryTimeout)
	check := sub.Probe(probeCtx, st.s.subscriptionEnv())
	cancel()
	st.state[id] = signInStateName(check.State)
	return st.state[id]
}

// accountOf answers one subscription's stored account once per request, cached
// the way of() caches its state: an id no subscription carries answers nil
// without a read, a read that yields nothing is cached as nil, and the read
// itself is local (a DB or file read the request's ctx already bounds).
func (st *subscriptionStates) accountOf(id string) *subscriptionAccount {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return nil
	}
	if acct, ok := st.accounts[id]; ok {
		return acct
	}
	sub, found := gatewaySubscriptionByID(id)
	if !found {
		st.accounts[id] = nil
		return nil
	}
	ctx := st.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	acct := readSubscriptionAccount(ctx, sub, st.s.subscriptionEnv())
	st.accounts[id] = acct
	return acct
}

// readSubscriptionAccount reads the stored account locally; a subscription
// without the capability, without a login, or with an unreadable one answers
// nil — the surface shows its state alone.
func readSubscriptionAccount(ctx context.Context, sub subscription.Subscription, env subscription.Env) *subscriptionAccount {
	reader, ok := sub.(subscription.AccountReader)
	if !ok {
		return nil
	}
	acct, ok, err := reader.Account(ctx, env)
	if err != nil {
		slog.Debug("subscription account read failed", "id", sub.ID(), "err", err)
		return nil
	}
	if !ok || acct.Name == "" {
		return nil
	}
	return &subscriptionAccount{Name: acct.Name, Detail: acct.Detail, EnvKey: acct.EnvKey, SharedPath: acct.SharedPath}
}

// subscriptionCanLogout says whether a surface may offer the sign-out: the
// subscription can remove its login, one is stored (any state but missing —
// dead is included, because a dead login is exactly the one to remove), and
// the environment is not the one providing the sign-in.
func subscriptionCanLogout(sub subscription.Subscription, state string, acct *subscriptionAccount) bool {
	if _, ok := sub.(subscription.LogoutRemover); !ok {
		return false
	}
	if state == "" || state == signInStateMissing {
		return false
	}
	return acct == nil || !acct.EnvKey
}

// handleSubscriptions serves the registry: every subscription a surface offers,
// in display order, with its state.
func (s *Server) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), modelsDiscoveryTimeout)
	defer cancel()
	states := newSubscriptionStates(s, ctx)
	registry := gatewaySubscriptions()
	out := make([]subscriptionRow, 0, len(registry))
	for _, sub := range registry {
		label, labelZH := sub.Label()
		state := states.of(sub.ID())
		out = append(out, subscriptionRow{
			ID:        sub.ID(),
			Name:      llmText(r, labelZH, label),
			Flows:     sub.Flows(),
			State:     state,
			Account:   states.accountOf(sub.ID()),
			CanLogout: subscriptionCanLogout(sub, state, states.accountOf(sub.ID())),
		})
	}
	writeJSON(w, out)
}

// handleSubscriptionOffers serves what a finished sign-in unlocked: one entry
// per model per plan, each carrying the endpoint its requests go to.
func (s *Server) handleSubscriptionOffers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sub, ok := s.subscriptionRoute(w, r)
	if !ok {
		return
	}
	env := s.subscriptionEnv()
	offers, err := sub.Offers(r.Context(), env)
	if err != nil {
		// The vendor's own refusal (an expired token, an unreachable console) is
		// not a sentence for the page: it says "sign in again" and the probe
		// above already told it what is wrong.
		slog.Debug("subscription offers unavailable", "subscription", sub.ID(), "err", err)
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": llmText(r, "读取订阅的模型列表失败，请重新登录", "could not read this subscription's models; sign in again"),
		})
		return
	}
	out := make([]subscriptionOffer, 0, len(offers))
	for _, offer := range offers {
		held := false
		if env.DB != nil {
			if _, found, herr := process.LLMModelHeld(r.Context(), env.DB, offer.Provider, offer.Model, offer.BaseURL, offer.APIPath); herr == nil {
				held = found
			}
		}
		out = append(out, subscriptionOffer{
			Plan:            offer.Plan,
			PlanName:        offer.PlanLabel,
			Provider:        offer.Provider,
			Model:           offer.Model,
			Name:            offer.Name,
			BaseURL:         offer.BaseURL,
			APIPath:         offer.APIPath,
			Endpoint:        turn.ModelEndpointLabel(offer.BaseURL, offer.APIPath, false),
			ContextWindow:   offer.ContextWindow,
			SupportsImages:  offer.SupportsImages,
			PaidModel:       offer.PaidModel,
			AllowPaidSwitch: offer.AllowPaidSwitch,
			Default:         offer.Default,
			Held:            held,
			NeedsKey:        offer.KeyRequired,
			FromStaticList:  offer.FromStaticList,
		})
	}
	writeJSON(w, out)
}

// handleSubscriptionProvision writes the chosen offers as model rows. Only what
// the subscription itself lists can be written: the request names offers, and
// each one is matched against the listing first, so this route cannot be used
// to store a row the user never agreed to.
func (s *Server) handleSubscriptionProvision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sub, ok := s.subscriptionRoute(w, r)
	if !ok {
		return
	}
	var body struct {
		Offers []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			BaseURL  string `json:"base_url"`
			APIPath  string `json:"api_path"`
			// SubagentAllowed is the authorization switch for the row this
			// offer writes (owner 2026-11-10): may the agent pick it for a
			// subagent on its own? Closed unless the user ticks it.
			SubagentAllowed bool `json:"subagent_allowed"`
		} `json:"offers"`
		KeyID     string `json:"key_id"`
		CreateKey bool   `json:"create_key"`
		// Key is the API key a plan asks the user to paste (plan 281). It is
		// forwarded to the subscription and stored on the row it writes; it is
		// never logged and never echoed back.
		Key string `json:"key"`
	}
	if !decodeLLMModelBody(w, r, &body) {
		return
	}
	if len(body.Offers) == 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{
			"error": llmText(r, "请选择要添加的模型", "choose the models to add"),
		})
		return
	}
	env := s.subscriptionEnv()
	available, err := sub.Offers(r.Context(), env)
	if err != nil {
		slog.Debug("subscription offers unavailable", "subscription", sub.ID(), "err", err)
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": llmText(r, "读取订阅的模型列表失败，请重新登录", "could not read this subscription's models; sign in again"),
		})
		return
	}
	chosen := make([]subscription.Offer, 0, len(body.Offers))
	for _, want := range body.Offers {
		offer, found := matchSubscriptionOffer(available, want.Provider, want.Model, want.BaseURL, want.APIPath)
		if !found {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{
				"error": llmText(r, "这个订阅里没有你选择的那一行", "this subscription does not offer the model you picked"),
			})
			return
		}
		chosen = append(chosen, offer)
	}
	// The first model an agent gets is its default, and a later one never takes
	// the default from the model the agent already runs on: the condition is
	// read before the rows land, so a first kit of models leaves a default
	// behind (the rule plan 262's setup follows).
	agentID, hadDefault := s.subscriptionDefaultTarget(r.Context())
	opts := subscription.ProvisionOptions{
		Key:       strings.TrimSpace(body.Key),
		KeyID:     strings.TrimSpace(body.KeyID),
		CreateKey: body.CreateKey,
	}
	ids, err := sub.Provision(r.Context(), env, chosen, opts)
	if err != nil {
		s.failSubscriptionWrite(w, r, err)
		return
	}
	// The authorization each ticked offer carried is written on the row it
	// just wrote: provisionRows writes one row per offer, in input order, so
	// the ids line up with the offers and their answers (owner 2026-11-10).
	if len(ids) != len(chosen) {
		s.failSubscriptionWrite(w, r, fmt.Errorf("stored %d rows for %d models", len(ids), len(chosen)))
		return
	}
	models := &state.LLMModelStore{DB: env.DB}
	for i, id := range ids {
		if err := models.SetSubagentAllowed(r.Context(), id, body.Offers[i].SubagentAllowed); err != nil {
			s.failSubscriptionWrite(w, r, err)
			return
		}
	}
	if !hadDefault && len(ids) > 0 {
		if err := process.SetAgentDefaultModel(r.Context(), s.Env.SQL, agentID, ids[0]); err != nil {
			slog.Debug("subscription default model not stored", "agent", agentID, "err", err)
		}
	}
	// The rows just written are the model table's, so they are only usable once
	// the process re-composes: the same adoption every other model write makes
	// (models_api.go adoptLLMModelWrite). A failure is this write's failure.
	if err := s.adoptLLMModelWrite(r.Context()); err != nil {
		s.failSubscriptionWrite(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"ids": ids})
}

// handleSubscriptionLogout removes a stored sign-in. The write is deliberately
// narrow: model rows stay (their source_state flips on the next read, and
// calling fails until the user signs in again), nothing is re-probed for the
// response — the page refreshes itself — and an external source that will sign
// the machine back in is a 409, so the page can say so instead of promising a
// signed-out state.
func (s *Server) handleSubscriptionLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sub, ok := s.subscriptionRoute(w, r)
	if !ok {
		return
	}
	remover, ok := sub.(subscription.LogoutRemover)
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"error": llmText(r, "这个订阅没有可退出的登录", "this subscription has no login to remove"),
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), modelsDiscoveryTimeout)
	defer cancel()
	err := remover.Logout(ctx, s.subscriptionEnv())
	if err != nil {
		if errors.Is(err, subscription.ErrExternalLogin) {
			writeJSONStatus(w, http.StatusConflict, map[string]any{
				"error": llmText(r, "本机保存的登录已删除，但环境变量或官方客户端仍会让这台机器重新登录",
					"The stored login was removed, but an environment variable or the official client will sign this machine back in"),
				"external_login": true,
			})
			return
		}
		slog.Debug("subscription logout failed", "subscription", sub.ID(), "err", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{
			"error": llmText(r, "退出失败，请重试", "sign-out failed; try again"),
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// subscriptionDefaultTarget answers the agent whose default a new model may
// become, and whether that agent already resolves to one.
func (s *Server) subscriptionDefaultTarget(ctx context.Context) (string, bool) {
	if s == nil || s.Env == nil || s.Env.SQL == nil {
		return "", true
	}
	active, err := s.activePrimarySummary()
	if err != nil {
		return "", true
	}
	cfg, err := s.persistedConfig(ctx)
	if err != nil {
		return active.ID, true
	}
	return active.ID, config.EffectiveDefaultLLMFor(&cfg, active.ID) != nil
}

// matchSubscriptionOffer finds the listed offer a request names, comparing the
// four identity fields the way the table compares them: a trailing slash or an
// empty api_path is the same row.
func matchSubscriptionOffer(offers []subscription.Offer, provider, model, baseURL, apiPath string) (subscription.Offer, bool) {
	provider, model, baseURL, apiPath = config.NormalizeLLMModelIdentity(provider, model, baseURL, apiPath)
	for _, offer := range offers {
		wantProvider, wantModel, wantBaseURL, wantAPIPath := config.NormalizeLLMModelIdentity(
			offer.Provider, offer.Model, offer.BaseURL, offer.APIPath)
		if wantProvider == provider && wantModel == model && wantBaseURL == baseURL && wantAPIPath == apiPath {
			return offer, true
		}
	}
	return subscription.Offer{}, false
}

// subscriptionRoute resolves the subscription the path names. An id no
// subscription carries is a 404 like an unknown row.
func (s *Server) subscriptionRoute(w http.ResponseWriter, r *http.Request) (subscription.Subscription, bool) {
	sub, ok := gatewaySubscriptionByID(pathParam(r, "id"))
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"error": llmText(r, "没有这个订阅", "no such subscription"),
		})
		return nil, false
	}
	return sub, true
}

// failSubscriptionWrite answers a failed sign-in write the way its cause reads:
// a plan whose key pool holds several keys asks which one, an empty pool asks
// for consent to create one, and a sign-in that stores no key answers that the
// login is the credential. Anything else is this server's own failure, which
// the page shows as a retry rather than as a vendor message.
func (s *Server) failSubscriptionWrite(w http.ResponseWriter, r *http.Request, err error) {
	var choice subscription.ErrKeyChoice
	if errors.As(err, &choice) {
		keys := make([]map[string]string, 0, len(choice.Keys))
		for _, key := range choice.Keys {
			keys = append(keys, map[string]string{"id": key.ID, "name": key.Name, "tail": key.Tail})
		}
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":      llmText(r, "这个套餐里有多把 Key，请选择一把", "this plan holds several keys; choose one"),
			"key_choice": keys,
		})
		return
	}
	if errors.Is(err, subscription.ErrNoKey) {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": llmText(r, "账号里还没有 Key，需要你确认创建一把",
				"the account has no key yet; creating one needs your consent"),
			"needs_key_consent": true,
		})
		return
	}
	if errors.Is(err, subscription.ErrNoKeyToRefresh) {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{
			"error": llmText(r, "这个订阅不存 Key，登录就是凭据", "this subscription stores no key: the sign-in is the credential"),
		})
		return
	}
	var needsKey subscription.ErrPlanKeyRequired
	if errors.As(err, &needsKey) {
		// The plan is named in the offers' own vocabulary, so the page can point
		// at the group that asks; the console is where the key is copied from.
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": llmText(r, "这个套餐的 Key 需要你从控制台复制后填进来",
				"this plan's key is copied from the vendor's console and pasted here"),
			"needs_key":   true,
			"plan":        needsKey.Plan,
			"console_url": needsKey.ConsoleURL,
		})
		return
	}
	if errors.Is(err, subscription.ErrKeyPasted) {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{
			"error": llmText(r, "这个套餐的 Key 是手填的，重新粘一次即可",
				"this plan's key is pasted by hand; paste a new one"),
		})
		return
	}
	slog.Debug("subscription write failed", "err", err)
	writeJSONStatus(w, http.StatusInternalServerError, map[string]any{
		"error": llmText(r, "写入失败，请重试", "the write failed; try again"),
	})
}
