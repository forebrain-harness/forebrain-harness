// identity.go carries the client identity the plugin must present to look
// exactly like the official Qoder CN CLI: its version strings, its machine
// fingerprint, and the Cosy-* header suite every gateway request wears. The
// machine identity is probed read-only from the real ~/.qoder-cn when present
// (never written there) and otherwise lives under the Forebrain home.
package qodercn

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client identity constants, verbatim from the official client
// (qoder-worker-runtime.obf.mjs and runtime-info.json).
const (
	CosyVersion     = "1.1.79-hotfix.19"
	ClientType      = "5"   // 5 = CLI
	BusinessProduct = "cli" //
	BusinessType    = "agent"
	DefaultScene    = "app"
	OAuthClientID   = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"
)

// UserAgent is the HTTP User-Agent the official CLI sends.
var UserAgent = "qoder/" + CosyVersion

// The endpoints, overridable so every test drives an httptest.Server instead
// of the production gateway.
var (
	// GatewayBaseURL is the API gateway host.
	GatewayBaseURL = "https://gateway.qoder.com.cn"
	// AuthPageBaseURL is the web authorization host.
	AuthPageBaseURL = "https://qoder.cn"
)

// qoderHomeOverride points the read-only ~/.qoder-cn probe at a test
// directory. Empty means the real one.
var qoderHomeOverride string

// QoderHomeDir is the on-disk Qoder CN root this plugin probes (read-only).
func QoderHomeDir() string {
	if qoderHomeOverride != "" {
		return qoderHomeOverride
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".qoder-cn")
}

// MachineIdentity is the machine fingerprint the gateway ties a device token
// to: the machine id, the UMID machine token, and the machine type the UMID
// cache reports.
type MachineIdentity struct {
	MachineID    string `json:"machine_id"`
	MachineToken string `json:"machine_token,omitempty"`
	MachineType  string `json:"machine_type,omitempty"`
}

// umidCache is the subset of ~/.qoder-cn/umid-cache.json the identity reads.
type umidCache struct {
	Info struct {
		MachineToken string `json:"machineToken"`
		MachineType  string `json:"machineType"`
	} `json:"info"`
}

// LoadOrCreateMachineIdentity resolves the machine identity: an official
// client on this machine already published one, so its values are reused
// (read-only — nothing is ever written under ~/.qoder-cn); otherwise the
// plugin keeps its own UUID under the Forebrain home.
func LoadOrCreateMachineIdentity(forebrainHome string) MachineIdentity {
	if id, ok := probeLocalMachineIdentity(); ok {
		return id
	}
	path := filepath.Join(forebrainHome, "qodercn-machine.json")
	if raw, err := os.ReadFile(path); err == nil {
		var id MachineIdentity
		if json.Unmarshal(raw, &id) == nil && strings.TrimSpace(id.MachineID) != "" {
			return id
		}
	}
	id := MachineIdentity{MachineID: mustUUIDv4(), MachineType: ClientType}
	_ = os.MkdirAll(forebrainHome, 0o700)
	if raw, err := json.Marshal(id); err == nil {
		_ = os.WriteFile(path, raw, 0o600)
	}
	return id
}

// probeLocalMachineIdentity reads the official client's machine_id and UMID
// cache. It answers ok only when a machine id exists; missing pieces fall back
// to their documented defaults.
func probeLocalMachineIdentity() (MachineIdentity, bool) {
	root := QoderHomeDir()
	if root == "" {
		return MachineIdentity{}, false
	}
	raw, err := os.ReadFile(filepath.Join(root, ".auth", "machine_id"))
	id := strings.TrimSpace(string(raw))
	if err != nil || id == "" {
		return MachineIdentity{}, false
	}
	out := MachineIdentity{MachineID: id, MachineType: ClientType}
	if raw, err := os.ReadFile(filepath.Join(root, "umid-cache.json")); err == nil {
		var cache umidCache
		if json.Unmarshal(raw, &cache) == nil {
			if cache.Info.MachineToken != "" {
				out.MachineToken = cache.Info.MachineToken
			}
			if cache.Info.MachineType != "" {
				out.MachineType = cache.Info.MachineType
			}
		}
	}
	return out, true
}

// MachineOS is the arch_os token the client reports (aarch64_darwin,
// x86_64_linux, x86_64_win32).
func MachineOS() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	osName := runtime.GOOS
	if osName == "windows" {
		osName = "win32"
	}
	return arch + "_" + osName
}

// machineHostname is cached once: every request sends it, and os.Hostname is a
// syscall.
var machineHostname = syncOnceString(func() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "localhost"
	}
	return strings.TrimSpace(host)
})

// syncOnceString memoizes a value computed at most once.
func syncOnceString(fn func() string) func() string {
	var once sync.Once
	var value string
	return func() string {
		once.Do(func() { value = fn() })
		return value
	}
}

// PrepareSpec names one gateway call: the API path (without the /algo prefix
// the gateway mounts the algo service under), the already-encoded request body
// ("" for GET), and the path the COSY signature covers.
type PrepareSpec struct {
	Path        string // e.g. /api/v2/model/list?Encode=1
	EncodedBody string
	SignPath    string // e.g. /api/v2/model/list
	ModelKey    string // X-Model-Key for inference calls ("" otherwise)
	ModelSource string
}

// PrepareRequestHeaders builds the full request the official client would
// send: the absolute URL (with the /algo prefix) and the complete Cosy-* header
// suite plus the signed Authorization.
func PrepareRequestHeaders(creds Credentials, id MachineIdentity, spec PrepareSpec) (url string, headers http.Header, body string, err error) {
	if strings.TrimSpace(creds.UID) == "" || creds.EncryptUserInfo == "" || creds.CosyKey == "" {
		return "", nil, "", fmt.Errorf("qodercn: credentials carry no runtime auth fields; sign in again")
	}
	url = strings.TrimRight(GatewayBaseURL, "/") + "/algo" + spec.Path
	headers = http.Header{}
	headers.Set("Accept", "application/json, text/plain, */*")
	headers.Set("User-Agent", UserAgent)
	headers.Set("Login-Version", "v2")
	headers.Set("Cosy-Version", CosyVersion)
	headers.Set("Cosy-ClientType", ClientType)
	headers.Set("Cosy-Business-Product", BusinessProduct)
	headers.Set("Cosy-Business-Type", BusinessType)
	headers.Set("Cosy-Scene", DefaultScene)
	headers.Set("Cosy-Data-Policy", "agree")
	headers.Set("Cosy-Date", strconv.FormatInt(time.Now().Unix(), 10))
	headers.Set("Cosy-Key", creds.CosyKey)
	headers.Set("Cosy-MachineId", id.MachineID)
	headers.Set("Cosy-MachineToken", id.MachineToken)
	headers.Set("Cosy-MachineType", id.MachineType)
	headers.Set("Cosy-MachineOS", MachineOS())
	headers.Set("Cosy-MachineHostname", machineHostname())
	headers.Set("Cosy-User", creds.UID)
	if creds.OrganizationID != "" {
		headers.Set("Cosy-Organization-Id", creds.OrganizationID)
	}
	if len(creds.OrganizationTags) > 0 {
		headers.Set("Cosy-Organization-Tags", strings.Join(creds.OrganizationTags, ","))
	}
	if spec.ModelKey != "" {
		headers.Set("X-Model-Key", spec.ModelKey)
		source := spec.ModelSource
		if source == "" {
			source = "system"
		}
		headers.Set("X-Model-Source", source)
	}
	requestID, err := newUUIDv4()
	if err != nil {
		return "", nil, "", err
	}
	headers.Set("Authorization", SignCosyBearer(requestID, creds.EncryptUserInfo, creds.CosyKey,
		headers.Get("Cosy-Date"), spec.EncodedBody, spec.SignPath))
	body = spec.EncodedBody
	if body != "" {
		headers.Set("Content-Type", "application/json")
	}
	return url, headers, body, nil
}

// newUUIDv4 is the random UUID every protocol step names a request with.
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("qodercn: uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(b[:])
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:], nil
}

func mustUUIDv4() string {
	id, err := newUUIDv4()
	if err != nil {
		// A crypto/rand failure means the process cannot run any flow anyway.
		return "00000000-0000-4000-8000-000000000000"
	}
	return id
}
