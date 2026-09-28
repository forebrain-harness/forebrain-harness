package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func PKCEVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func PKCEChallengeS256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func OAuthAuthorizeURL(o appcfg.MCPOAuthConfig, state, codeChallenge string) (string, error) {
	au := strings.TrimSpace(o.AuthorizationURL)
	if au == "" {
		return "", fmt.Errorf("authorization_url required")
	}
	if strings.TrimSpace(o.ClientID) == "" {
		return "", fmt.Errorf("client_id required")
	}
	u, err := url.Parse(au)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", strings.TrimSpace(o.ClientID))
	if redir := strings.TrimSpace(o.RedirectURL); redir != "" {
		q.Set("redirect_uri", redir)
	}
	if len(o.Scopes) > 0 {
		q.Set("scope", strings.Join(o.Scopes, " "))
	}
	if strings.TrimSpace(state) != "" {
		q.Set("state", state)
	}
	if strings.TrimSpace(codeChallenge) != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
