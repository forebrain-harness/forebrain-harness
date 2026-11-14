// Volcengine Ark: the Ark plans an account holds, reached by signing in to the
// Volcengine console. The sign-in proves the account and says which plans (and
// which team seats) it holds; it does not hand over the API key a plan's rows
// carry, because the vendor publishes no way to read one back. So a personal
// plan's key is pasted once from the console, and a team seat's key is fetched.
//
// That split is why this subscription is the first one whose rows hold a key:
// Offer.KeyRequired says which offers ask for one, ErrPlanKeyRequired is what a
// write without one answers, and ErrKeyPasted is what "refresh" answers for the
// rows only a person can re-key.
package subscription

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

const (
	// volcengineID is the provider id a Volcengine row carries, and the source
	// recorded on every row this subscription writes.
	volcengineID = "volcengine"

	// The plans the console names, as the plan a row records. The team plans are
	// Agent Plan seats: the vendor sells no Coding Plan seats.
	volcengineCodingPlan    = "coding-plan"
	volcengineAgentPlan     = "agent-plan"
	volcengineAgentPlanTeam = "agent-plan-team"

	// The two data-plane roots, with the Anthropic Messages protocol the owner
	// made the default (2026-11-07). They are the provider registry's own Coding
	// Plan and Agent Plan endpoints; a test pins the two together, so a row the
	// login writes and a row a hand-entered key writes land on the same host.
	volcengineCodingBaseURL = "https://ark.cn-beijing.volces.com/api/coding"
	volcengineAgentBaseURL  = "https://ark.cn-beijing.volces.com/api/plan"
	// volcengineKeyConsoleURL is where the user copies a personal plan's key
	// from: the console's API key page for this region.
	volcengineKeyConsoleURL = "https://console.volcengine.com/ark/region:ark+cn-beijing/apiKey"

	// volcengineMaxSeatPages bounds the seat listing's paging: a team large
	// enough to exceed it still yields the first pages' seats, and no loop can
	// spin on a vendor that keeps answering full pages.
	volcengineMaxSeatPages = 20
	volcengineSeatPageSize = 100
)

// The two calls this subscription makes, as variables so a test drives the whole
// subscription against an httptest.Server instead of a vendor.
var (
	newVolcengineSignIn = func() *volcengineSignIn { return &volcengineSignIn{} }
	newVolcengineClient = func() *volcengineClient { return &volcengineClient{} }
)

// volcengine is the Volcengine Ark subscription. Its one flow is the console's
// device code: no redirect has to reach this machine, so a gateway drives the
// same sign-in a terminal does.
type volcengine struct{}

func (volcengine) ID() string { return volcengineID }

func (volcengine) Label() (en, zh string) { return "Volcengine Ark", "火山方舟" }

func (volcengine) Flows() []string { return []string{FlowDeviceCode} }

// Probe answers whether this machine's console sign-in still works: the STS it
// holds signs an authenticated action, and a refusal means only a new sign-in
// restores it.
func (volcengine) Probe(ctx context.Context, env Env) turn.SignInCheck {
	credential, found, err := volcengineSessionCredential(ctx, env)
	if err != nil {
		return turn.SignInCheck{State: volcengineSignInState(err), Err: err}
	}
	if !found {
		return turn.SignInCheck{State: turn.SignInMissing}
	}
	client := newVolcengineClient()
	var plan volcenginePersonalPlan
	err = client.Do(ctx, volcengineGetPersonalPlan, credential.creds(),
		volcenginePlanRequest(volcengineCodingPlan), &plan)
	switch {
	case err == nil, volcengineIsNotFound(err):
		// Answered, or answered "no such plan": either way the credential
		// authenticated, and the plan list is Offers' to read.
		return turn.SignInCheck{State: turn.SignInUsable}
	}
	var api *volcengineError
	if errors.As(err, &api) {
		if api.rejected() {
			return turn.SignInCheck{State: turn.SignInDead, Err: err}
		}
		if !api.unreachable() {
			// A refusal of this request rather than of the sign-in: the login
			// stands and Offers is where a retry belongs.
			return turn.SignInCheck{State: turn.SignInUsable}
		}
	}
	return turn.SignInCheck{State: turn.SignInUnreachable, Err: err}
}

// volcengineSignInState maps a sign-in failure to the probe's state: a refusal
// of the credential, and a login that ran out with nothing to renew it, are dead;
// anything else leaves the login standing.
func volcengineSignInState(err error) turn.SignInState {
	var refusal *volcengineOAuthRefusal
	if errors.As(err, &refusal) {
		return turn.SignInDead
	}
	if errors.Is(err, errVolcengineExpired) {
		return turn.SignInDead
	}
	var api *volcengineError
	if errors.As(err, &api) && api.rejected() {
		return turn.SignInDead
	}
	return turn.SignInUnreachable
}

// Start begins the console's device-code sign-in. The page the console issues is
// the prefilled one when it has one, so a surface that opens a browser needs the
// user to do nothing else; the code reaches the surface either way, because a
// gateway shows it instead of opening anything.
func (volcengine) Start(ctx context.Context, env Env, flow string) (Login, error) {
	if flow != FlowDeviceCode {
		return nil, fmt.Errorf("Volcengine has no %q sign-in", flow)
	}
	signIn := newVolcengineSignIn()
	return startLogin(ctx, nil, func(flowCtx context.Context, l *login) error {
		device, err := signIn.startDevice(flowCtx)
		if err != nil {
			return err
		}
		l.setURL(device.browserURL())
		l.setUserCode(strings.TrimSpace(device.UserCode))
		credential, err := signIn.poll(flowCtx, device)
		if err != nil {
			return err
		}
		// The credential is the sign-in: stored before the flow reports
		// success, so a surface that reads "signed in" finds it there.
		return saveVolcengineCredential(flowCtx, env, credential)
	}), nil
}

// Offers lists the models each plan this account holds unlocks, one offer per
// model per plan. A listing that fails falls back to the shipped table for that
// plan, marked as static, because an answer with the console as the authority is
// still better than refusing to show a plan the account paid for.
func (volcengine) Offers(ctx context.Context, env Env) ([]Offer, error) {
	credential, found, err := volcengineSessionCredential(ctx, env)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("this machine holds no Volcengine sign-in; sign in first")
	}
	return volcengineOffers(ctx, credential, newVolcengineClient())
}

// Provision writes the chosen offers as rows, each carrying the key its plan
// uses: the one the user pasted for a personal plan, the seat's own for a team
// plan. A key never appears in an error, and a plan that needs one that was not
// given writes nothing.
func (volcengine) Provision(ctx context.Context, env Env, offers []Offer, opts ProvisionOptions) ([]string, error) {
	if len(offers) == 0 {
		return nil, errors.New("no models chosen")
	}
	pasted := strings.TrimSpace(opts.Key)
	needSeat := false
	for _, offer := range offers {
		if !offer.KeyRequired {
			needSeat = true
			break
		}
	}
	seatKey := ""
	if needSeat {
		credential, found, err := volcengineSessionCredential(ctx, env)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.New("this machine holds no Volcengine sign-in; sign in first")
		}
		seatKey, _, err = volcengineSeatKey(ctx, newVolcengineClient(), credential)
		if err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(offers))
	for _, offer := range offers {
		rowKey := pasted
		if !offer.KeyRequired {
			rowKey = seatKey
		}
		var key *string
		switch {
		case rowKey != "":
			key = &rowKey
		case offer.KeyRequired:
			// Only a personal plan can arrive without a key: a team seat's is
			// fetched above, and an empty answer there is already an error. A
			// row that already holds its plan's key is not asked for it again
			// — the write keeps what the row has.
			held, err := volcengineRowKeyHeld(ctx, env, offer)
			if err != nil {
				return nil, err
			}
			if !held {
				return nil, ErrPlanKeyRequired{
					Plan: offer.Plan, PlanLabel: offer.PlanLabel, ConsoleURL: volcengineKeyConsoleURL,
				}
			}
		}
		id, err := process.UpsertLLMModel(ctx, env.DB, offerRowInput(offer, volcengineID, key))
		if err != nil {
			return nil, fmt.Errorf("store %s/%s: %w", offer.Provider, offer.Model, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// volcengineRowKeyHeld reports whether the row this offer would write already
// holds a key. The plan asks for a pasted key only for a row that would end up
// keyless — the same sentence UpsertLLMModel answers with, and the same rule the
// save follows (owner 2026-11-13): a key the row already has is never asked for
// again.
func volcengineRowKeyHeld(ctx context.Context, env Env, offer Offer) (bool, error) {
	if env.DB == nil {
		return false, nil
	}
	row, found, err := process.LLMModelHeld(ctx, env.DB, offer.Provider, offer.Model, offer.BaseURL, offer.APIPath)
	if err != nil || !found {
		return false, err
	}
	return strings.TrimSpace(row.APIKey) != "", nil
}

// RefreshKey fetches a team seat's key again for one row this subscription
// wrote. A personal plan's key is the user's to paste: no API reads it back, so
// the answer is ErrKeyPasted and the page asks for a new one.
func (volcengine) RefreshKey(ctx context.Context, env Env, modelID string) error {
	if env.DB == nil {
		return errors.New("this install has no state database")
	}
	row, err := (&state.LLMModelStore{DB: env.DB}).Get(ctx, modelID)
	if err != nil {
		return err
	}
	if row.SourcePlan != volcengineAgentPlanTeam {
		return ErrKeyPasted
	}
	credential, found, err := volcengineSessionCredential(ctx, env)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("this machine holds no Volcengine sign-in; sign in again to fetch the seat's key")
	}
	fresh, _, err := volcengineSeatKey(ctx, newVolcengineClient(), credential)
	if err != nil {
		return err
	}
	key := fresh
	_, err = process.UpsertLLMModel(ctx, env.DB, process.LLMModelInput{
		Provider:    row.Provider,
		Model:       row.Model,
		BaseURL:     row.BaseURL,
		APIPath:     row.APIPath,
		APIKey:      &key,
		Source:      volcengineID,
		SourcePlan:  row.SourcePlan,
		URLVerbatim: row.URLVerbatim,
	})
	return err
}

// volcengineSessionCredential is this machine's usable sign-in: the stored
// credential, renewed when its STS is close to expiring, so an expired-but-
// refreshable login never reads as a missing one.
func volcengineSessionCredential(ctx context.Context, env Env) (volcengineCredential, bool, error) {
	credential, found, err := loadVolcengineCredential(ctx, env)
	if err != nil || !found {
		return volcengineCredential{}, found, err
	}
	signIn := newVolcengineSignIn()
	if credential.valid(signIn.now()) {
		return credential, true, nil
	}
	renewed, err := signIn.refresh(ctx, credential)
	if err != nil {
		return volcengineCredential{}, false, err
	}
	if err := saveVolcengineCredential(ctx, env, renewed); err != nil {
		return volcengineCredential{}, false, err
	}
	return renewed, true, nil
}

// volcengineOffers assembles what the account's plans unlock: each personal plan
// it holds, and the team seats it runs.
func volcengineOffers(ctx context.Context, credential volcengineCredential, client *volcengineClient) ([]Offer, error) {
	creds := credential.creds()
	offers := make([]Offer, 0, 32)
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, plan := range []struct{ id, label string }{
		{volcengineCodingPlan, "Coding Plan"},
		{volcengineAgentPlan, "Agent Plan"},
	} {
		held, err := volcengineHoldsPlan(ctx, client, creds, plan.id)
		if err != nil {
			note(err)
			offers = append(offers, volcengineStaticOffers(plan.id, plan.label)...)
			continue
		}
		if !held {
			continue
		}
		models, err := volcenginePlanModels(ctx, client, creds, plan.id)
		if err != nil {
			note(err)
			offers = append(offers, volcengineStaticOffers(plan.id, plan.label)...)
			continue
		}
		offers = append(offers, volcengineModelOffers(plan.id, plan.label, models, true)...)
	}
	// One team kit, from the seat whose id sorts first: a model row is one row
	// per provider, model and endpoint, so two seats serving one model would
	// otherwise write the same row twice with two different keys.
	if seat, seatErr := volcengineFirstActiveSeat(ctx, client, creds); seatErr != nil {
		note(seatErr)
	} else if seat.SeatID != "" {
		models, err := volcenginePlanModels(ctx, client, creds, volcengineAgentPlanTeam)
		if err != nil {
			note(err)
		} else {
			label := "Agent Plan · Team"
			if biz := strings.TrimSpace(seat.BizInfo); biz != "" {
				label = "Agent Plan · " + biz
			}
			offers = append(offers, volcengineModelOffers(volcengineAgentPlanTeam, label, models, false)...)
		}
	}
	if len(offers) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, errors.New("this Volcengine account holds no Ark plan")
	}
	// A first sign-in ticks one model, and the account's own first row is the
	// one it would pick.
	offers[0].Default = true
	return offers, nil
}

// volcengineHoldsPlan asks whether the account holds one personal plan. A plan
// the console does not name is not held; a call that failed outright is an
// error the caller falls back from.
func volcengineHoldsPlan(ctx context.Context, client *volcengineClient, creds volcengineCreds, plan string) (bool, error) {
	var answer volcenginePersonalPlan
	if err := client.Do(ctx, volcengineGetPersonalPlan, creds, volcenginePlanRequest(plan), &answer); err != nil {
		if volcengineIsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if strings.TrimSpace(answer.PlanType) == "" || strings.EqualFold(strings.TrimSpace(answer.Status), "Expired") {
		return false, nil
	}
	return true, nil
}

// volcenginePlanModels lists the models one plan runs.
func volcenginePlanModels(ctx context.Context, client *volcengineClient, creds volcengineCreds, plan string) ([]string, error) {
	action, params := volcengineModelAction(plan)
	var answer struct {
		Datas []struct {
			ModelID string `json:"ModelID"`
		} `json:"Datas"`
	}
	if err := client.Do(ctx, action, creds, params, &answer); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(answer.Datas))
	seen := map[string]bool{}
	for _, row := range answer.Datas {
		id := strings.TrimSpace(row.ModelID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	return models, nil
}

// volcengineFirstActiveSeat is the seat a team kit's key comes from: the running
// seat whose id sorts first, so the same account always keys the same rows.
func volcengineFirstActiveSeat(ctx context.Context, client *volcengineClient, creds volcengineCreds) (volcengineSeat, error) {
	seats, err := volcengineSeats(ctx, client, creds)
	if err != nil {
		return volcengineSeat{}, err
	}
	if len(seats) == 0 {
		return volcengineSeat{}, nil
	}
	return seats[0], nil
}

// volcengineSeats lists the account's running team seats, following the pages
// the API answers until one comes up short.
func volcengineSeats(ctx context.Context, client *volcengineClient, creds volcengineCreds) ([]volcengineSeat, error) {
	seats := make([]volcengineSeat, 0, 8)
	for page := 1; page <= volcengineMaxSeatPages; page++ {
		var answer volcengineSeatList
		err := client.Do(ctx, volcengineListSeatInfos, creds, map[string]any{
			// Running and active: a pending, expired or reclaimed seat has no
			// key to serve.
			"Filter":     map[string]any{"SeatStatus": 2, "BillingStatus": 2},
			"PageNumber": page,
			"PageSize":   volcengineSeatPageSize,
		}, &answer)
		if err != nil {
			return nil, err
		}
		for _, seat := range answer.Data {
			if strings.TrimSpace(seat.SeatID) != "" {
				seats = append(seats, seat)
			}
		}
		if len(answer.Data) < volcengineSeatPageSize {
			break
		}
	}
	sort.SliceStable(seats, func(i, j int) bool { return seats[i].SeatID < seats[j].SeatID })
	return seats, nil
}

// volcengineSeatKey fetches one seat's key, with the plan label the seat runs.
func volcengineSeatKey(ctx context.Context, client *volcengineClient, credential volcengineCredential) (key, bizInfo string, err error) {
	seat, err := volcengineFirstActiveSeat(ctx, client, credential.creds())
	if err != nil {
		return "", "", err
	}
	if seat.SeatID == "" {
		return "", "", errors.New("this Volcengine account runs no active team seat")
	}
	var answer struct {
		APIKey string `json:"APIKey"`
	}
	if err := client.Do(ctx, volcengineGetTeamSeatAPIKey, credential.creds(), map[string]any{"SeatId": seat.SeatID}, &answer); err != nil {
		return "", "", err
	}
	key = strings.TrimSpace(answer.APIKey)
	if key == "" {
		return "", "", errors.New("ark " + volcengineGetTeamSeatAPIKey + ": the seat answered no key")
	}
	return key, seat.BizInfo, nil
}

// volcengineModelOffers projects a plan's model ids into offers. Capability
// data comes from the shipped table when it names the model — the vendor's plan
// listing carries ids only — and a model it does not name is offered with the
// image switch undeclared rather than off: the listing never said the model
// rejects pictures, so the row must not answer for it. keyRequired says whether
// a row asks for a pasted key.
func volcengineModelOffers(plan, label string, models []string, keyRequired bool) []Offer {
	baseURL, apiPath := volcenginePlanEndpoint(plan)
	offers := make([]Offer, 0, len(models))
	for _, model := range models {
		images, declared := volcengineModelImages(model)
		offers = append(offers, Offer{
			Subscription:     volcengineID,
			Plan:             plan,
			PlanLabel:        label,
			Provider:         volcengineID,
			Model:            model,
			BaseURL:          baseURL,
			APIPath:          apiPath,
			ContextWindow:    volcengineModelWindow(model),
			SupportsImages:   images,
			ImagesUndeclared: !declared,
			KeyRequired:      keyRequired,
		})
	}
	return offers
}

// volcengineStaticOffers is the shipped table's answer for a plan whose listing
// failed: the models plan 263 already ships for a hand-entered key, marked as a
// static list so a surface says the console is the authority.
func volcengineStaticOffers(plan, label string) []Offer {
	models := make([]string, 0, 16)
	if provider, ok := appcfg.BuiltinProviderByID(volcengineID); ok {
		for _, model := range provider.Models {
			if id := strings.TrimSpace(model.ID); id != "" {
				models = append(models, id)
			}
		}
	}
	offers := volcengineModelOffers(plan, label, models, plan != volcengineAgentPlanTeam)
	for i := range offers {
		offers[i].FromStaticList = true
	}
	return offers
}

// volcenginePlanEndpoint is the data-plane root one plan's requests go to.
func volcenginePlanEndpoint(plan string) (baseURL, apiPath string) {
	if plan == volcengineAgentPlan || plan == volcengineAgentPlanTeam {
		return volcengineAgentBaseURL, appcfg.APIPathAnthropicMessages
	}
	return volcengineCodingBaseURL, appcfg.APIPathAnthropicMessages
}

// volcengineModelWindow and volcengineModelImages read the capability data the
// shipped table holds for one model. A model it does not name is offered with no
// window — the picker simply does not show one — and with its image switch
// undeclared: the table not naming a model says nothing about its input
// modalities, and guessing "no images" for it is what refused valid pictures.
func volcengineModelWindow(model string) int64 {
	provider, ok := appcfg.BuiltinProviderByID(volcengineID)
	if !ok {
		return 0
	}
	for _, entry := range provider.Models {
		if entry.ID == model {
			return entry.ContextWindow
		}
	}
	return 0
}

func volcengineModelImages(model string) (images, declared bool) {
	provider, ok := appcfg.BuiltinProviderByID(volcengineID)
	if !ok {
		return false, false
	}
	for _, entry := range provider.Models {
		if entry.ID == model {
			return entry.Images, true
		}
	}
	return false, false
}

// volcengineIsNotFound reports the console's answer for a plan the account does
// not hold: an answer, not a failure.
func volcengineIsNotFound(err error) bool {
	var api *volcengineError
	return errors.As(err, &api) && api.notFound()
}
