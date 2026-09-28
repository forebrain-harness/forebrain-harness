package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type OfficialRegistry struct {
	urls map[string]struct{}
}

func NewOfficialRegistryFromJSON(raw []byte) (*OfficialRegistry, error) {
	var doc struct {
		Servers []struct {
			Server struct {
				Remotes []struct {
					URL string `json:"url"`
				} `json:"remotes"`
			} `json:"server"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	reg := &OfficialRegistry{urls: map[string]struct{}{}}
	for _, entry := range doc.Servers {
		for _, remote := range entry.Server.Remotes {
			if normalized := NormalizeRegistryURL(remote.URL); normalized != "" {
				reg.urls[normalized] = struct{}{}
			}
		}
	}
	return reg, nil
}

func NormalizeRegistryURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/")
}

func (r *OfficialRegistry) IsOfficialURL(raw string) bool {
	if r == nil {
		return false
	}
	normalized := NormalizeRegistryURL(raw)
	if normalized == "" {
		return false
	}
	_, ok := r.urls[normalized]
	return ok
}

type OfficialRegistryOptions struct {
	Home                       string
	URL                        string
	DisableNonessentialTraffic bool
	HTTPClient                 *http.Client
	Timeout                    time.Duration
}

func LoadOfficialRegistryFromEnv(home string) (*OfficialRegistry, bool, error) {
	raw := strings.TrimSpace(os.Getenv("FOREBRAIN_MCP_OFFICIAL_REGISTRY_JSON"))
	if raw != "" {
		reg, err := NewOfficialRegistryFromJSON([]byte(raw))
		return reg, true, err
	}
	path := strings.TrimSpace(os.Getenv("FOREBRAIN_MCP_OFFICIAL_REGISTRY_FILE"))
	if path == "" {
		url := strings.TrimSpace(os.Getenv("FOREBRAIN_MCP_OFFICIAL_REGISTRY_URL"))
		return LoadOfficialRegistry(context.Background(), OfficialRegistryOptions{
			Home:                       strings.TrimSpace(home),
			URL:                        url,
			DisableNonessentialTraffic: truthyEnv("FOREBRAIN_DISABLE_NONESSENTIAL_TRAFFIC"),
		})
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, true, err
	}
	reg, err := NewOfficialRegistryFromJSON(b)
	return reg, true, err
}

func LoadOfficialRegistry(ctx context.Context, opts OfficialRegistryOptions) (*OfficialRegistry, bool, error) {
	if opts.DisableNonessentialTraffic {
		return nil, false, nil
	}
	regURL := strings.TrimSpace(opts.URL)
	if regURL == "" {
		return nil, false, nil
	}
	body, err := fetchOfficialRegistry(ctx, opts)
	if err == nil {
		if home := strings.TrimSpace(opts.Home); home != "" {
			_ = saveOfficialRegistryCache(home, body)
		}
		reg, parseErr := NewOfficialRegistryFromJSON(body)
		return reg, true, parseErr
	}
	if cached, ok := loadOfficialRegistryCache(strings.TrimSpace(opts.Home)); ok {
		reg, parseErr := NewOfficialRegistryFromJSON(cached)
		return reg, true, parseErr
	}
	return nil, true, nil
}

func fetchOfficialRegistry(ctx context.Context, opts OfficialRegistryOptions) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, strings.TrimSpace(opts.URL), nil)
	if err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("registry status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func officialRegistryCachePath(home string) string {
	return filepath.Join(strings.TrimSpace(home), "state", "mcp-registry", "official.json")
}

func saveOfficialRegistryCache(home string, body []byte) error {
	p := officialRegistryCachePath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, body, 0o600)
}

func loadOfficialRegistryCache(home string) ([]byte, bool) {
	if strings.TrimSpace(home) == "" {
		return nil, false
	}
	b, err := os.ReadFile(officialRegistryCachePath(home))
	if err != nil {
		return nil, false
	}
	return b, true
}

func truthyEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
