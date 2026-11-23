# Plan 239: the WhatsApp inbound webhook authenticates its caller

> **Executor instructions**: Follow the steps in order and confirm each
> verification. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and
> report it. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 69ab0706..HEAD -- pkg/channel/whatsapp.go pkg/channel/whatsapp_test.go pkg/channel/slack.go`
> Compare the "Current state" excerpts with the live code; content mismatch is a
> STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED — an enabled channel with no secret loses its webhook route
  (deliberately; the poll loop keeps working). Say so in the warning and in the
  maintenance note.
- **Depends on**: none (plan 234 already mounts this route on every replica)
- **Category**: security
- **Planned at**: commit `69ab0706`, 2026-11-05
- **Finding**: branch audit 2026-11-05 (`origin/dev..HEAD`), finding 2
  (`introduced`: plan 234 widened the exposure of a route that never authenticated)

## Why this matters

`POST /channels/whatsapp/inbound` (the default inbound path) is **exempt from
control-plane auth** — `pkg/config/gateway.go` exempts it because "the endpoints
authenticate their caller themselves (signature, channel token)". The Slack
adapter does exactly that. The WhatsApp adapter does not: `handleInbound` decodes
any JSON body and publishes it as an inbound channel message.

The body chooses the conversation: `chatId`/`from`/`sessionId` becomes the
session id, `text` becomes the user turn. So anyone who can reach the gateway
port can inject instructions into an existing conversation — and pick which one
— and spend the operator's model budget. No credential is needed.

Before plan 234 this route existed on the lease holder only. Plan 234 deliberately
mounted it on **every** replica (a standby must answer the callback), so the open
door is now on every replica, which is what makes this a P1 rather than a note.

`WhatsAppConfig.Secret` already exists and is already carried into the adapter;
nothing reads it.

## Current state

`pkg/channel/whatsapp.go:14-21` — the config, including the unused secret:

```go
type WhatsAppConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}
```

`pkg/channel/whatsapp.go:55-64` — the mount point (replica-local, plan 234):

```go
func (w *WhatsApp) MountRoutes(add RouteAdder, bus Bus) {
	w.inboundAdd = add
	w.bus = bus
	if !w.cfg.Enabled {
		return
	}
	if path := strings.TrimSpace(w.cfg.InboundPath); path != "" {
		add(http.MethodPost, path, w.handleInbound)
	}
}
```

`pkg/channel/whatsapp.go:174-197` — the handler, with no check of any kind:

```go
func (w *WhatsApp) handleInbound(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(rw, "invalid json", http.StatusBadRequest)
		return
	}
	sessionID, text, msgID := parseWhatsAppInbound(payload)
	…
	_ = w.bus.PublishInbound(r.Context(), Inbound{ChannelID: "whatsapp", SessionID: sessionID, Text: text, Raw: payload})
```

`pkg/config/gateway.go:9-11` — why this path is exempt at all:

```go
// WebhookInboundPaths lists the inbound endpoints one channel section mounts.
// The control-plane middleware exempts these from its auth because the
// endpoints authenticate their caller themselves (signature, channel token).
```

`pkg/config/gateway.go:28` — WhatsApp's path is on that list
(`add(ch.WhatsApp.Enabled, ch.WhatsApp.InboundPath)`), and
`pkg/gateway/controlplane.go:376` is the middleware that honours it.

The convention to copy — `pkg/channel/slack.go:115-118` and
`verifySlackSignature` at `pkg/channel/slack.go:154-172`: read the raw body first,
compare with `hmac.Equal`, refuse with an HTTP error and no detail. Match that
shape, including the constant-time compare. Do **not** copy Slack's gating:
Slack skips the check when no secret is configured (`slack.go:115` tests
`Secret != ""` first and answers an unsigned request otherwise); this plan does
the opposite — without a secret there is no route at all (step 3).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel -run 'WhatsApp' -count=1` | `ok` |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel ./pkg/process ./pkg/gateway -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/channel/whatsapp.go`
- `pkg/channel/whatsapp_test.go`

**Out of scope**:
- `pkg/channel/slack.go` — its signature check is the model, not the target.
- `pkg/config/gateway.go` — keep the path exempt. Once the route authenticates
  (step 2) the exemption is again honest; removing it would break the callback
  before the adapter has a chance to refuse it.
- The poll loop (`pollLoop`) and `DeliverOutbound`: the poll path talks to the
  operator's local bridge with `Token`, not to the internet, and the webhook
  secret is a different credential.
- `w.cfg`'s lock-free mutation in `Start` (a real, separate defect — see
  maintenance).

## Steps

### Step 1: verify the body's signature

Add to `pkg/channel/whatsapp.go`, next to the handler:

```go
// verifyWhatsAppSignature checks the bridge's HMAC of the raw body. The header
// is Meta's own (`sha256=<hex>` over the body as sent), so a bridge that relays
// a Meta callback can pass the header through unchanged.
func verifyWhatsAppSignature(r *http.Request, body []byte, secret string) bool {
	sig := strings.TrimSpace(r.Header.Get("X-Hub-Signature-256"))
	if sig == "" || !strings.HasPrefix(sig, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write(body)
	expect := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expect), []byte(sig))
}
```

Imports: `crypto/hmac`, `crypto/sha256`, `encoding/hex`.

**Verify**: `go build ./...` → exit 0

### Step 2: require it in the handler

Rewrite `handleInbound` to read the body first (the HMAC is over the bytes as
received), then authenticate, then decode:

```go
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(rw, "bad body", http.StatusBadRequest)
		return
	}
	if secret := strings.TrimSpace(w.cfg.Secret); secret != "" && !verifyWhatsAppSignature(r, body, secret) {
		slog.Debug("whatsapp inbound refused: signature", "remote", r.RemoteAddr)
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil { … }
```

Keep the existing 204/`invalid json` answers otherwise. Import `log/slog`.

**Verify**: `go build ./...` → exit 0

### Step 3: refuse to mount an unauthenticated route

In `MountRoutes`, after the `Enabled` check:

```go
	// The route is exempt from control-plane auth because a webhook
	// authenticates its caller itself (pkg/config/gateway.go). This adapter can
	// only do that with a secret, so without one the route is not mounted at
	// all: an unauthenticated endpoint here would let any caller name a
	// conversation and put words in its user's mouth. The poll loop, which is
	// this channel's real inbound path, is unaffected.
	if strings.TrimSpace(w.cfg.Secret) == "" {
		slog.Warn("whatsapp inbound webhook not mounted: no secret configured", "path", w.cfg.InboundPath)
		return
	}
```

**Verify**: `go build ./...` → exit 0

### Step 4: test plan (see below)

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel -run 'WhatsApp' -count=1` → `ok`

## Test plan

In `pkg/channel/whatsapp_test.go`, following the existing `whatsAppBus` fake and
`httptest` usage already in that file (and `slack_test.go`'s signature cases if it
has any):

1. `secret set + valid signature` → 204 and exactly one published inbound whose
   `SessionID`/`Text` come from the body.
2. `secret set + wrong signature` → 403, **nothing published**.
3. `secret set + header missing` → 403, nothing published.
4. `body tampered after signing` (sign the original bytes, send different ones) →
   403. This is the case a naive "hash the decoded JSON" implementation passes
   and a correct one fails.
5. `secret empty + Enabled` → `MountRoutes` registers no route for the path (the
   `add` callback is never called for it), and the adapter still starts and polls
   (existing `Start` assertions keep passing).
6. `secret empty + not Enabled` → unchanged no-op (existing case).

**Verify**: the focused command above → `ok`, with the five new cases visible in
`-v` output.

## Done criteria

ALL must hold:

- [ ] `go build ./...` and `go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel ./pkg/process ./pkg/gateway -count=1` passes
- [ ] `grep -n 'verifyWhatsAppSignature' pkg/channel/whatsapp.go` matches, and
      `grep -n 'X-Hub-Signature-256' pkg/channel/whatsapp.go` matches
- [ ] A request with a bad signature publishes nothing (asserted)
- [ ] An enabled channel without a secret mounts no route (asserted) and logs one
      warning at mount time
- [ ] Only in-scope files modified (`git status --porcelain`)
- [ ] `plans/README.md` row for 239 updated

## STOP conditions

- The bridge this adapter talks to cannot compute an HMAC over the raw body — i.e.
  the operator has no way to set the header. Report it: the fallback (a shared
  token in `Authorization`) is a different design and needs the owner's decision.
- `whatsapp_test.go`'s existing route test asserts the route mounts **without** a
  secret (it does: `NewWhatsApp(WhatsAppConfig{Enabled: true, InboundPath: " /wa "})`
  at `whatsapp_test.go:40`). Update that fixture to set a `Secret`, and say so in the report — it
  is a fixture change, not a behaviour regression.
- Refusing to mount turns out to break a documented operator flow
  (`docs/config/…`): report the document instead of special-casing.

## Maintenance notes

- The rule this plan installs: **an auth-exempt path must authenticate itself, or
  not be mounted.** Any new adapter that adds a path to
  `config.WebhookInboundPaths` owes the same check.
- Rotate any webhook secret that was distributed to the bridge before this change
  landed: the endpoint was open to anyone who could reach the port, so treat the
  old value as public. Do not write the value into the plan, the commit message or
  the index — reference the config key only.
- A reviewer should check the raw bytes are hashed (not the re-marshalled JSON),
  that the compare is `hmac.Equal`, and that the 403 body carries no reason.
- The handler's own guard reads `secret != "" && !verify(…)`, i.e. an empty
  secret lets the request through — the mount refusal above is the only thing
  making that state unreachable. Any future path that mounts this route outside
  `MountRoutes` must keep the invariant: no secret, no route. (Making the handler
  itself fail closed is a one-line change, deliberately not taken here so the
  fix stays the plan's.)
- Deliberately deferred: `Start` writes `w.cfg.OutboundURL` while `DeliverOutbound`
  reads it (a data race a replica that never wins the lease can hit). Fixing it
  means normalising the URL in `NewWhatsApp` — a separate change.
