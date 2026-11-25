// OpenCode Go: the subscription reached by a device-code login against the
// OpenCode Console. What it unlocks is the Go subscription's catalog, which
// lives on its own inference root — so the rows it writes are "opencode-go"
// rows, never Console ones, and carry no key: the login is the credential.
package subscription

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// openCodeGoID is the provider id a Go subscription row carries, and the source
// recorded on every row this subscription writes.
const openCodeGoID = "opencode-go"

// The flow, the probe and the listing, as variables so a test drives them
// without a browser and without talking to opencode.ai. Production runs the
// same opencode functions the terminal's /connect and the web login run, so one
// sign-in serves every surface.
var (
	openCodeLogin = opencode.LoginDeviceFlow
	openCodeProbe = func(ctx context.Context, home string) turn.SignInCheck {
		return turn.ProbeOpenCodeGoModels(ctx, process.NewOpenCodeModelsSource(home))
	}
	openCodeList = func(ctx context.Context, home string) ([]turn.ModelRecord, error) {
		// The only surface this subscription offers is the Go plan, whose
		// catalog is the one that needs the subscription's token.
		return turn.DiscoverOpenCodeModels(ctx, process.NewOpenCodeModelsSource(home), true)
	}
)

// openCodeGo is the OpenCode Go subscription.
type openCodeGo struct{}

func (openCodeGo) ID() string { return openCodeGoID }

func (openCodeGo) Label() (en, zh string) { return "OpenCode", "OpenCode" }

// Flows is the device code alone: the user confirms a short code, and the flow
// polls for the token. There is no redirect, so nothing has to reach this
// machine and no surface has to be the one with the browser.
func (openCodeGo) Flows() []string { return []string{FlowDeviceCode} }

// Probe answers whether the login this machine holds can list the Go catalog,
// counting OPENCODE_API_KEY mode: a key authenticates the same catalog.
func (openCodeGo) Probe(ctx context.Context, env Env) turn.SignInCheck {
	return openCodeProbe(ctx, env.Home)
}

// Start begins the device-code sign-in. The verification URL and the user code
// reach the surface through the handle as soon as the Console issues them, and
// the browser is opened here: this flow runs on the machine the user is sitting
// in front of.
func (openCodeGo) Start(ctx context.Context, env Env, flow string) (Login, error) {
	if flow != FlowDeviceCode {
		return nil, fmt.Errorf("OpenCode has no %q sign-in", flow)
	}
	return startLogin(ctx, nil, func(flowCtx context.Context, l *login) error {
		// out is discarded: the surface is the progress display. The opener is
		// an explicit no-op rather than nil — nil means "open the system
		// browser", and only the surface knows whether this machine is the one
		// with the user's browser. A surface that can opens AuthorizeURL itself.
		_, err := openCodeLogin(flowCtx, opencode.DefaultPath(env.Home), io.Discard,
			func(string) error { return nil },
			func(info opencode.LoginInfo) {
				// Published as one snapshot: the Console names the address
				// and the code in one answer, so a page reading them cannot
				// catch half of it. There is no separate expiry to show — the
				// Console publishes none for this code — and the login's own
				// token lifetime is not it.
				l.setDeviceCode(strings.TrimSpace(info.VerificationURL), strings.TrimSpace(info.UserCode), time.Time{})
			},
			// The web has its own progress UI, so the flow reports no stages.
			nil,
		)
		return err
	}), nil
}

// Offers lists the Go subscription's models, each under the plan's own
// inference root.
func (openCodeGo) Offers(ctx context.Context, env Env) ([]Offer, error) {
	records, err := openCodeList(ctx, env.Home)
	if err != nil {
		return nil, err
	}
	return offersFromRecords(openCodeGoID, records, opencode.ProviderBaseURL(openCodeGoID)), nil
}

// Provision writes the chosen models as rows. There is no key to store: the
// login is the credential, and the row names it.
func (openCodeGo) Provision(ctx context.Context, env Env, offers []Offer, opts ProvisionOptions) ([]string, error) {
	return provisionRows(ctx, env, offers, openCodeGoID)
}

// RefreshKey has nothing to fetch: a Go subscription row holds no key.
func (openCodeGo) RefreshKey(ctx context.Context, env Env, modelID string) error {
	return ErrNoKeyToRefresh
}

// Account names who the stored sign-in belongs to, read locally from the
// credential — no vendor call. The email is the identity the person
// recognizes; the account id stands in when the login carried no email, and
// the organization rides along as the detail. With no stored login but
// OPENCODE_API_KEY set, the account is the variable itself and EnvKey says a
// surface must not offer a logout for it — the environment, not this store,
// is holding the sign-in.
func (openCodeGo) Account(ctx context.Context, env Env) (Account, bool, error) {
	raw, err := opencode.LoadStoredCredentials(ctx, env.Home)
	if err != nil {
		return Account{}, false, err
	}
	if raw == nil {
		if strings.TrimSpace(os.Getenv(opencode.APIKeyEnv)) != "" {
			return Account{Name: opencode.APIKeyEnv, EnvKey: true}, true, nil
		}
		return Account{}, false, nil
	}
	c, err := opencode.LoadProcessCredentials(ctx, opencode.DefaultPath(env.Home))
	if err != nil {
		return Account{}, false, nil // stored but unreadable: no name to show
	}
	name := c.Email
	if name == "" {
		name = c.AccountID
	}
	if name == "" {
		return Account{}, false, nil
	}
	return Account{Name: name, Detail: c.OrgName}, true, nil
}

// Logout removes the stored sign-in from the store's row and the file. When
// the environment supplies the sign-in instead, there is nothing to remove
// here: ErrExternalLogin tells the surface the machine will be signed in
// again until the variable is unset where Forebrain Harness starts.
func (openCodeGo) Logout(ctx context.Context, env Env) error {
	raw, err := opencode.LoadStoredCredentials(ctx, env.Home)
	if err != nil {
		return err
	}
	envKey := strings.TrimSpace(os.Getenv(opencode.APIKeyEnv)) != ""
	if raw == nil {
		if envKey {
			return fmt.Errorf("%w: unset %s in the shell that starts Forebrain Harness", ErrExternalLogin, opencode.APIKeyEnv)
		}
		return nil
	}
	if err := opencode.ClearStoredCredentials(ctx, env.Home); err != nil {
		return err
	}
	if envKey {
		return fmt.Errorf("%w: the stored login was removed; %s still signs this machine in", ErrExternalLogin, opencode.APIKeyEnv)
	}
	return nil
}
