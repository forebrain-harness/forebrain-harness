// Runtime resolution of the codex client version declared to the subscription
// backend. The version is queried from the npm registry — the official
// distribution channel of the codex-cli whose protocol it names — on every
// model discovery and parsed from the live response; it is never baked into
// the source and never cached.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// npmRegistryURL is the registry the official @openai/codex packages are
// published to; its dist-tags are the authoritative "latest released client".
// It is a var so tests can point it at a stub server.
var npmRegistryURL = "https://registry.npmjs.org"

// codexNPMPackage is the official codex-cli package name.
const codexNPMPackage = "@openai/codex"

// npmFetchTimeout bounds one registry lookup so a slow registry cannot eat the
// caller's whole discovery budget.
const npmFetchTimeout = 5 * time.Second

// maxDistTagsBytes bounds the dist-tags response; the real body is a few
// hundred bytes.
const maxDistTagsBytes = 64 << 10

// versionPattern is the whole-version shape (major.minor.patch) the backend's
// client_version is known to carry — the codex-cli itself strips pre-release
// suffixes before sending it.
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// ResolveClientVersion returns the client version to declare to the
// subscription backend: the latest released official codex-cli version,
// queried live from the npm registry's dist-tags on every call. There is no
// cached fallback: if the registry cannot be reached, resolution fails and the
// caller reports it.
func ResolveClientVersion(ctx context.Context) (string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, npmFetchTimeout)
	defer cancel()
	url := strings.TrimRight(npmRegistryURL, "/") + "/-/package/" + codexNPMPackage + "/dist-tags"
	req, err := http.NewRequestWithContext(lookupCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("npm dist-tags for %s: http %d", codexNPMPackage, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDistTagsBytes+1))
	if err != nil {
		return "", fmt.Errorf("npm dist-tags for %s: read response: %w", codexNPMPackage, err)
	}
	if len(body) > maxDistTagsBytes {
		return "", fmt.Errorf("npm dist-tags for %s: response exceeds %d bytes", codexNPMPackage, maxDistTagsBytes)
	}
	var tags struct {
		Latest string `json:"latest"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return "", fmt.Errorf("npm dist-tags for %s: decode response: %w", codexNPMPackage, err)
	}
	version := wholeVersion(tags.Latest)
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("npm dist-tags for %s: latest %q is not a whole version", codexNPMPackage, tags.Latest)
	}
	return version, nil
}

// wholeVersion strips a pre-release suffix the way codex-cli does before
// sending its own client_version ("1.2.3-rc.4" -> "1.2.3").
func wholeVersion(v string) string {
	return strings.TrimSpace(strings.SplitN(v, "-", 2)[0])
}
