package subscription

import (
	"context"
	"errors"
)

// Account names who a stored sign-in belongs to, read locally from the
// credential — no vendor call.
type Account struct {
	Name   string // email, nickname, or masked id
	Detail string // plan, org, or credential kind; "" for none
	// EnvKey marks a sign-in the environment provides (OPENCODE_API_KEY):
	// it cannot be removed here, and a surface offers no logout for it.
	EnvKey bool
	// SharedPath is the credential file this login lives in when that file is
	// not this install's own — the Codex CLI's auth.json, which the same login
	// signs that program in to. A surface that removes the login names the file
	// in its confirmation, so deleting another program's login is never silent.
	// "" means the file is this install's own.
	SharedPath string
}

// AccountReader is the optional capability of naming the stored account.
// A subscription without it shows its state alone.
type AccountReader interface {
	Account(ctx context.Context, env Env) (Account, bool, error)
}

// LogoutRemover is the optional capability of removing the stored sign-in.
// A subscription without it offers no logout.
type LogoutRemover interface {
	Logout(ctx context.Context, env Env) error
}

// ErrExternalLogin is answered by Logout when removing the stored login is
// not enough: an external source (an environment token, the official client's
// own login) will sign this machine back in on the next check. The stored
// login is still removed; surfaces explain the source instead of promising
// a signed-out state.
var ErrExternalLogin = errors.New("an external source provides this sign-in")

// MaskID shortens one half of a credential pair (an AccessKeyID) for display,
// because even that half identifies the account: more than 12 characters keeps
// 4+4 at the edges, up to 12 keeps only the last 4, and anything down to 5
// characters hides the whole middle — a tail of 4 would leave 4 of 5 showing,
// which is no mask at all. IDs of 4 characters or fewer are too short to hide.
func MaskID(id string) string {
	switch {
	case len(id) > 12:
		return id[:4] + "…" + id[len(id)-4:]
	case len(id) > 8:
		return "…" + id[len(id)-4:]
	case len(id) > 4:
		return "…"
	default:
		return id
	}
}
