// The gateway's token when nobody configured one.
package process

import (
	"context"
	"fmt"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// EnsureGatewayToken gives a gateway that is about to start a token when its
// configuration has none in effect, and returns the one it generated — "" when
// a token was already configured (owner 2026-11-11, 2026-11-12).
//
// gateway.auth.token in the configuration file is the operator's to write, in
// plaintext or as a ${NAME} reference the variables or the process environment
// supply, and a token in effect is never replaced. What counts as none is the
// composed answer, not the file's text: an absent or empty key, and equally a
// reference nothing resolves, which is how a home reads after its token's
// source went away. Either way every request would be refused.
//
// The generated token belongs to this process alone: the caller prints it
// where the gateway was started, and nothing writes it to the configuration
// file. It lasts until the process stops, and the next start generates
// another. Gateways that have to agree on one — replicas sharing a home — need
// a configured token.
func EnsureGatewayToken(ctx context.Context, home string) (string, error) {
	cfg, err := StartupConfig(ctx, home)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.Gateway.Auth.Token) != "" {
		return "", nil
	}
	token, err := homepkg.GenerateGatewayToken()
	if err != nil {
		return "", fmt.Errorf("generate a gateway token: %w", err)
	}
	appcfg.SetGeneratedGatewayToken(token)
	// The cached startup resolve was read before the token existed.
	ResetResolve()
	return token, nil
}
