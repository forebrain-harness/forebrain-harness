// qodercn_test.go pins the plugin against the golden vectors captured from the
// official client's qoder_auth_wasm (byte-for-byte WASM comparison; the
// vectors below carry no real credential material) and drives every network
// path against httptest servers with temp-dir homes — the real ~/.qoder-cn and
// any real token are never read here.
package qodercn

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
)

// withQoderHome points the read-only official-client probe at a scratch dir
// for one test.
func withQoderHome(t *testing.T, dir string) {
	t.Helper()
	previous := qoderHomeOverride
	qoderHomeOverride = dir
	t.Cleanup(func() { qoderHomeOverride = previous })
}

// withGateway points the plugin at the test server for one test.
func withGateway(t *testing.T, server *httptest.Server) {
	t.Helper()
	previousGateway, previousAuth := GatewayBaseURL, AuthPageBaseURL
	GatewayBaseURL = server.URL
	AuthPageBaseURL = server.URL
	t.Cleanup(func() {
		GatewayBaseURL, AuthPageBaseURL = previousGateway, previousAuth
	})
}

// testState opens a throwaway state database the credentials can live in.
func testState(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// --- crypto: the WASM golden vectors ---------------------------------------

func TestCryptoCredentialEncryptGoldenVector(t *testing.T) {
	got, err := CredentialEncrypt("hello world", "0123456789abcdef")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if want := "wem0Upqsl5MBD0Z39jWO/g=="; got != want {
		t.Fatalf("CredentialEncrypt = %q, want %q (WASM vector)", got, want)
	}
	back, err := CredentialDecrypt(got, "0123456789abcdef")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if back != "hello world" {
		t.Fatalf("CredentialDecrypt = %q", back)
	}
	if _, err := CredentialDecrypt("!!!not-base64!!!", "0123456789abcdef"); err == nil {
		t.Fatal("garbage base64 must fail")
	}
	if _, err := CredentialEncrypt("x", "short"); err == nil {
		t.Fatal("a key that is not 16 bytes must fail")
	}
}

func TestCryptoRuntimeAuthInputFieldOrderIsTheWasmOne(t *testing.T) {
	raw, err := json.Marshal(RuntimeAuthInput{
		UID:              "u1",
		SecurityOAuthTok: "tok",
		OrganizationID:   "org",
		OrganizationTags: []string{"a", "b"},
		DataPolicyAgreed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{`"uid"`, `"security_oauth_token"`, `"organization_id"`, `"organization_tags"`, `"data_policy_agreed"`}
	at := -1
	for _, key := range wantOrder {
		next := strings.Index(string(raw), key)
		if next <= at {
			t.Fatalf("field order %s is not the WASM order: %s", key, raw)
		}
		at = next
	}
	// omitempty: an empty org leaves the keys out entirely.
	lean, _ := json.Marshal(RuntimeAuthInput{UID: "u1", SecurityOAuthTok: "tok", DataPolicyAgreed: true})
	if strings.Contains(string(lean), "organization") {
		t.Fatalf("empty organization fields must be omitted: %s", lean)
	}
}

func TestCryptoGenerateRuntimeAuthFieldsShape(t *testing.T) {
	info, key, err := GenerateRuntimeAuthFields(RuntimeAuthInput{
		UID: "u1", SecurityOAuthTok: "tok", DataPolicyAgreed: true,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if info == "" {
		t.Fatal("encrypt_user_info is empty")
	}
	// RSA-1024 output is 128 bytes -> 172 base64 characters: the sealed key
	// has exactly that shape.
	if len(key) != 172 {
		t.Fatalf("sealed key length = %d, want 172 (RSA-1024 base64)", len(key))
	}
	if _, err := base64.StdEncoding.DecodeString(key); err != nil {
		t.Fatalf("sealed key is not base64: %v", err)
	}
	// Two calls seal with fresh one-time keys: the same input yields
	// different fields.
	info2, key2, _ := GenerateRuntimeAuthFields(RuntimeAuthInput{UID: "u1", SecurityOAuthTok: "tok", DataPolicyAgreed: true})
	if info == info2 || key == key2 {
		t.Fatal("runtime auth fields must be per-call random")
	}
}

const encodePayloadGoldenInput = `{"request_id":"req-1","session_id":"sess-1","messages":[{"role":"user","content":"hi"}],"model_config":{"key":"qmodel_38max","source":"system"}}`

const encodePayloadGoldenOutput = "u(QOPSN*lEr*P(FQBMn*NS.bDHFrx^VIuSgIB*f*N(W.NOjLBMn*NkL^JHFtBE.WN(gEDxV*lLtXBEKbuHa*l*K.N(FYB*f*#(WhJHFhJoByBOi)BE.JGoKtu(CLugWMmYKYDxg.Dxjwx(LzBMn*NOF%G@T*GoK^Dxj^PSWhx(LzBMn*N(F^NYw%B*f*uSF^"

func TestCryptoEncodePayloadGoldenVector(t *testing.T) {
	if got := EncodePayload([]byte(encodePayloadGoldenInput)); got != encodePayloadGoldenOutput {
		t.Fatalf("EncodePayload golden vector mismatch:\n got %s\nwant %s", got, encodePayloadGoldenOutput)
	}
}

func TestCryptoPayloadRoundTripAllLengths(t *testing.T) {
	// 0..100 arbitrary lengths, as the WASM comparison did.
	for n := 0; n <= 100; n++ {
		plain := make([]byte, n)
		for i := range plain {
			plain[i] = byte((i*31 + n) % 256)
		}
		encoded := EncodePayload(plain)
		back, err := DecodePayload(encoded)
		if err != nil {
			t.Fatalf("len %d decode: %v", n, err)
		}
		if string(back) != string(plain) {
			t.Fatalf("len %d round trip mismatch", n)
		}
	}
	// A plain-JSON body passes through untouched (the server sometimes
	// answers unencoded).
	plain := `{"a":1}`
	back, err := DecodePayload(plain)
	if err != nil || string(back) != plain {
		t.Fatalf("plain JSON passthrough = %q err=%v", back, err)
	}
}

func TestCryptoSignCosyBearerGoldenVectors(t *testing.T) {
	// POST vector: the preimage the WASM hashed, with its exact parts.
	requestID := "31313131-3131-4131-b131-313131313131"
	bearer := SignCosyBearer(requestID, "INFO_VAL", "KEY_VAL", "1700000000",
		encodePayloadGoldenOutput, "/api/v2/service/pro/sse/agent_chat_generation")

	goldenB64 := "eyJ2ZXJzaW9uIjoidjEiLCJyZXF1ZXN0SWQiOiIzMTMxMzEzMS0zMTMxLTQxMzEtYjEzMS0zMTMxMzEzMTMxMzEiLCJpbmZvIjoiSU5GT19WQUwiLCJjb3N5VmVyc2lvbiI6IjEuMS43OS1ob3RmaXguMTkiLCJpZGVWZXJzaW9uIjoiIn0="
	parts := strings.SplitN(strings.TrimPrefix(bearer, "Bearer COSY."), ".", 3)
	if len(parts) != 2 {
		t.Fatalf("bearer = %q, want two dot-separated parts", bearer)
	}
	if parts[0] != goldenB64 {
		t.Fatalf("identity part = %s, want the WASM golden %s", parts[0], goldenB64)
	}
	if parts[1] != "61e82033157daeaab04685f056f27eb1" {
		t.Fatalf("signature = %s, want the WASM golden", parts[1])
	}

	// GET vector: empty encoded body, model-list path.
	sum := md5.Sum([]byte(goldenB64 + "\nKEY_VAL\n1700000000\n" + "" + "\n/api/v2/model/list"))
	_ = sum // the GET preimage shape is covered by signPreimage below.
	bearerGet := SignCosyBearer(requestID, "INFO_VAL", "KEY_VAL", "1700000000", "", "/api/v2/model/list")
	if !strings.HasPrefix(bearerGet, "Bearer COSY."+goldenB64+".") {
		t.Fatalf("GET bearer = %q", bearerGet)
	}
	if got := strings.TrimPrefix(bearerGet, "Bearer COSY."+goldenB64+"."); got == "" || len(got) != 32 {
		t.Fatalf("GET signature = %q, want a 32-hex md5", got)
	}
}

// signPreimageMD5 recomputes the signature the way the WASM does, so a bearer
// built by PrepareRequestHeaders can be verified end to end.
func signPreimageMD5(b64Part, cosyKey, cosyDate, encodedBody, signPath string) string {
	preimage := b64Part + "\n" + cosyKey + "\n" + cosyDate + "\n" + encodedBody + "\n" + signPath
	sum := md5.Sum([]byte(preimage))
	return hex.EncodeToString(sum[:])
}

func TestCryptoModelCacheRoundTripAndFraming(t *testing.T) {
	catalog := []byte(`{"models":[{"key":"auto"}]}`)
	blob, err := EncryptModelCache(catalog, "uid-123")
	if err != nil {
		t.Fatalf("encrypt cache: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:4]) != "QMC\x01" {
		t.Fatalf("magic = %q", raw[:4])
	}
	nonce := raw[4:16]
	if len(nonce) != 12 {
		t.Fatalf("nonce length = %d, want 12", len(nonce))
	}
	back, err := DecryptModelCache(blob, "uid-123")
	if err != nil || string(back) != string(catalog) {
		t.Fatalf("round trip = %q err=%v", back, err)
	}
	// The key is derived from the uid: another uid cannot open it.
	if _, err := DecryptModelCache(blob, "uid-456"); err == nil {
		t.Fatal("a wrong uid must not open the cache")
	}
	if _, err := DecryptModelCache(base64.StdEncoding.EncodeToString([]byte("XXXX")), "uid-123"); err == nil {
		t.Fatal("a blob without the QMC magic must be refused")
	}
	// HKDF determinism: the same uid derives the same key, so a second seal
	// opened under the same uid is readable.
	again, _ := EncryptModelCache([]byte(`{}`), "uid-123")
	if _, err := DecryptModelCache(again, "uid-123"); err != nil {
		t.Fatalf("deterministic derivation broken: %v", err)
	}
}

// --- identity ---------------------------------------------------------------

func TestIdentityMachineOSUsesTheClientSpellings(t *testing.T) {
	os := MachineOS()
	arch, platform, ok := strings.Cut(os, "_")
	if !ok {
		t.Fatalf("MachineOS = %q, want arch_os", os)
	}
	if runtime.GOARCH == "amd64" && arch != "x86_64" {
		t.Fatalf("amd64 must report x86_64, got %q", arch)
	}
	if runtime.GOARCH == "arm64" && arch != "aarch64" {
		t.Fatalf("arm64 must report aarch64, got %q", arch)
	}
	if runtime.GOOS == "windows" && platform != "win32" {
		t.Fatalf("windows must report win32, got %q", platform)
	}
}

func TestIdentityReusesTheOfficialClientsMachineIDReadOnly(t *testing.T) {
	official := t.TempDir()
	if err := os.MkdirAll(filepath.Join(official, ".auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	machineID := "11111111-2222-4333-b333-444444444444" // a UUID-shaped probe value
	if err := os.WriteFile(filepath.Join(official, ".auth", "machine_id"), []byte(machineID), 0o644); err != nil {
		t.Fatal(err)
	}
	umid := `{"info":{"machineToken":"umid-token","machineType":"7"}}`
	if err := os.WriteFile(filepath.Join(official, "umid-cache.json"), []byte(umid), 0o644); err != nil {
		t.Fatal(err)
	}
	withQoderHome(t, official)

	home := t.TempDir()
	id := LoadOrCreateMachineIdentity(home)
	if id.MachineID != machineID || id.MachineToken != "umid-token" || id.MachineType != "7" {
		t.Fatalf("identity = %+v, want the official client's", id)
	}
	// Read-only: nothing may appear under the official root.
	entries, err := os.ReadDir(filepath.Join(official, ".auth"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("the official .auth dir grew: %v (%v)", entries, err)
	}
	// And no file is created in the Forebrain home when the official one
	// answered.
	if _, err := os.Stat(filepath.Join(home, "qodercn-machine.json")); !os.IsNotExist(err) {
		t.Fatalf("forebrain machine file must not exist when the official id was reused: %v", err)
	}
}

func TestIdentityGeneratesItsOwnWhenNoOfficialInstall(t *testing.T) {
	withQoderHome(t, t.TempDir()) // an empty root: no machine_id anywhere
	home := t.TempDir()
	id := LoadOrCreateMachineIdentity(home)
	if len(id.MachineID) != 36 || id.MachineType != ClientType {
		t.Fatalf("generated identity = %+v", id)
	}
	again := LoadOrCreateMachineIdentity(home)
	if again.MachineID != id.MachineID {
		t.Fatalf("machine id is not stable across calls: %q vs %q", again.MachineID, id.MachineID)
	}
	info, err := os.Stat(filepath.Join(home, "qodercn-machine.json"))
	if err != nil {
		t.Fatalf("machine file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("machine file mode = %o, want 0600", perm)
	}
}

func TestIdentityPrepareRequestHeadersDressesTheRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	withGateway(t, server)

	creds := Credentials{
		UID: "user-1", AccessToken: "tok", EncryptUserInfo: "INFO", CosyKey: "KEY",
		OrganizationID: "org-9", OrganizationTags: []string{"pro", "team"},
	}
	id := MachineIdentity{MachineID: "machine-id-1234567890", MachineToken: "umid", MachineType: "5"}

	url, headers, body, err := PrepareRequestHeaders(creds, id, PrepareSpec{
		Path:        "/api/v2/model/list?Encode=1",
		EncodedBody: "",
		SignPath:    "/api/v2/model/list",
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if want := server.URL + "/algo/api/v2/model/list?Encode=1"; url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}
	if body != "" {
		t.Fatalf("GET body = %q", body)
	}
	for header, want := range map[string]string{
		"User-Agent":             UserAgent,
		"Login-Version":          "v2",
		"Cosy-Version":           CosyVersion,
		"Cosy-Clienttype":        "5",
		"Cosy-Business-Product":  "cli",
		"Cosy-Business-Type":     "agent",
		"Cosy-Scene":             "app",
		"Cosy-Data-Policy":       "agree",
		"Cosy-Key":               "KEY",
		"Cosy-Machineid":         "machine-id-1234567890",
		"Cosy-Machinetoken":      "umid",
		"Cosy-Machinetype":       "5",
		"Cosy-Organization-Id":   "org-9",
		"Cosy-Organization-Tags": "pro,team",
	} {
		if got := headers.Get(header); got != want {
			t.Fatalf("header %s = %q, want %q", header, got, want)
		}
	}
	if headers.Get("Cosy-Machineos") == "" {
		t.Fatal("Cosy-MachineOS missing")
	}
	if headers.Get("Cosy-Machinehostname") == "" {
		t.Fatal("Cosy-MachineHostname missing")
	}
	if !strings.HasPrefix(headers.Get("Authorization"), "Bearer COSY.") {
		t.Fatalf("Authorization = %q", headers.Get("Authorization"))
	}
	// The signature verifies against the header's own parts.
	b64Part, sig, ok := strings.Cut(strings.TrimPrefix(headers.Get("Authorization"), "Bearer COSY."), ".")
	if !ok {
		t.Fatal("bearer does not carry two parts")
	}
	if want := signPreimageMD5(b64Part, "KEY", headers.Get("Cosy-Date"), "", "/api/v2/model/list"); sig != want {
		t.Fatalf("signature %s does not verify (%s)", sig, want)
	}
	if key := headers.Get("X-Model-Key"); key != "" {
		t.Fatalf("a call without a model must not send X-Model-Key, sent %q", key)
	}

	// An inference call adds the model headers and a body content type.
	_, headers2, body2, err := PrepareRequestHeaders(creds, id, PrepareSpec{
		Path:        "/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1",
		EncodedBody: "ENCODEDBODY",
		SignPath:    "/api/v2/service/pro/sse/agent_chat_generation",
		ModelKey:    "qmodel_38max",
	})
	if err != nil {
		t.Fatalf("prepare infer: %v", err)
	}
	if headers2.Get("X-Model-Key") != "qmodel_38max" || headers2.Get("X-Model-Source") != "system" {
		t.Fatalf("model headers = %q / %q", headers2.Get("X-Model-Key"), headers2.Get("X-Model-Source"))
	}
	if body2 != "ENCODEDBODY" || headers2.Get("Content-Type") != "application/json" {
		t.Fatalf("body = %q content-type = %q", body2, headers2.Get("Content-Type"))
	}
	b64Part2, sig2, _ := strings.Cut(strings.TrimPrefix(headers2.Get("Authorization"), "Bearer COSY."), ".")
	if want := signPreimageMD5(b64Part2, "KEY", headers2.Get("Cosy-Date"), "ENCODEDBODY", "/api/v2/service/pro/sse/agent_chat_generation"); sig2 != want {
		t.Fatalf("inference signature %s does not verify (%s)", sig2, want)
	}

	// Credentials without the runtime auth fields are refused, not sent bare.
	if _, _, _, err := PrepareRequestHeaders(Credentials{UID: "u", AccessToken: "t"}, id, PrepareSpec{}); err == nil {
		t.Fatal("missing runtime auth fields must be an error")
	}
}

// --- auth: the PKCE browser device flow -------------------------------------

// tokenAnswer is the JSON the poll and the exchange answer with; the values
// are throwaway test tokens, never real ones.
func tokenAnswer(userID string) map[string]any {
	return map[string]any{
		"token":                    "test-access-token",
		"refresh_token":            "test-refresh-token",
		"user_id":                  userID,
		"user_name":                "Test User",
		"expires_in":               3600,
		"refresh_token_expires_in": 2592000,
	}
}

func TestLoginPKCEDeviceFlowAndUserInfo(t *testing.T) {
	withQoderHome(t, t.TempDir()) // no official install: the identity is our own

	var pollCalls int32
	var seenQuery url.Values
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/deviceToken/poll":
			atomic.AddInt32(&pollCalls, 1)
			seenQuery, seenHeaders = r.URL.Query(), r.Header.Clone()
			// The first poll answers "not confirmed yet".
			if atomic.LoadInt32(&pollCalls) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tokenAnswer("uid-77"))
		case "/api/v1/userinfo":
			if r.Header.Get("Authorization") != "Bearer test-access-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"uid":"uid-77","name":"Test User","email":"t@example.test","orgId":"org-1"}`))
		case "/api/v1/organizations/org-1/tags":
			_, _ = w.Write([]byte(`{"tags":["pro"]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	withGateway(t, server)

	var authURL string
	home := t.TempDir()
	db := testState(t)
	creds, err := LoginPKCEDeviceFlow(context.Background(), db, home, func(u string) { authURL = u })
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if creds.UID != "uid-77" || creds.Name != "Test User" || creds.OrganizationID != "org-1" {
		t.Fatalf("credentials = %+v", creds)
	}
	if creds.OrganizationTags == nil || len(creds.OrganizationTags) != 1 || creds.OrganizationTags[0] != "pro" {
		t.Fatalf("organization tags = %v", creds.OrganizationTags)
	}
	if creds.EncryptUserInfo == "" || creds.CosyKey == "" {
		t.Fatal("the login must generate the runtime auth fields")
	}
	if creds.AccessToken != "test-access-token" || creds.RefreshToken == "" {
		t.Fatalf("tokens not stored: %+v", creds)
	}

	// The authorization URL the browser opens: PKCE challenge, machine id and
	// the production client id, exactly as the official client builds it.
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("auth url: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Path != "/device/selectAccounts" {
		t.Fatalf("auth url = %q", authURL)
	}
	q := parsed.Query()
	if q.Get("challenge_method") != "S256" || len(q.Get("challenge")) != 43 {
		t.Fatalf("challenge = %q (method %q)", q.Get("challenge"), q.Get("challenge_method"))
	}
	if q.Get("client_id") != OAuthClientID || q.Get("machine_id") == "" || q.Get("nonce") == "" {
		t.Fatalf("auth url params = %v", q)
	}
	// The poll carries the verifier that matches the challenge.
	pollQ := seenQuery
	if pollQ.Get("challenge_method") != "S256" || len(pollQ.Get("verifier")) != 64 {
		t.Fatalf("poll params = %v", pollQ)
	}
	sum := sha256.Sum256([]byte(pollQ.Get("verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("challenge") {
		t.Fatal("the poll verifier does not match the authorization challenge")
	}
	// The poll wears the client identity.
	for header, want := range map[string]string{
		"User-Agent":      UserAgent,
		"Cosy-Version":    CosyVersion,
		"Cosy-Clienttype": "5",
	} {
		if got := seenHeaders.Get(header); got != want {
			t.Fatalf("poll header %s = %q, want %q", header, got, want)
		}
	}
	if seenHeaders.Get("Cosy-Machineos") == "" {
		t.Fatal("poll header Cosy-MachineOS missing")
	}

	// The credential is stored: a second lookups finds it without a flow.
	stored, ok, err := loadCredentials(context.Background(), db, home)
	if err != nil || !ok || stored.UID != "uid-77" {
		t.Fatalf("stored = %+v ok=%t err=%v", stored, ok, err)
	}
}

// --- auth: the token refresh, and its concurrency ----------------------------

func TestTokenRefreshAndConcurrentLock(t *testing.T) {
	withQoderHome(t, t.TempDir())
	var refreshes int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/deviceToken/refresh" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		atomic.AddInt32(&refreshes, 1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["refresh_token"] != "old-refresh" {
			t.Errorf("refresh body = %v", body)
		}
		if body["machine_id"] == "" {
			t.Error("a browser refresh must send the machine identity")
		}
		_, _ = w.Write([]byte(`{"device_token":"fresh-access","refresh_token":"fresh-refresh","expires_in":3600}`))
	}))
	defer server.Close()
	withGateway(t, server)

	home := t.TempDir()
	db := testState(t)
	// A login expiring inside the refresh margin.
	stale := Credentials{
		UID: "uid-1", AccessToken: "old-access", RefreshToken: "old-refresh",
		LoginMethod: "browser", ExpiresAt: time.Now().Add(20 * time.Second),
		EncryptUserInfo: "old-info", CosyKey: "old-key",
	}
	if err := saveCredentials(context.Background(), db, home, stale); err != nil {
		t.Fatal(err)
	}

	// Ten concurrent turns: the credential lock serializes them, so the
	// gateway sees exactly one refresh.
	var wg sync.WaitGroup
	results := make([]Credentials, 10)
	errs := make([]error, 10)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			creds, err := EnsureFreshCredentials(context.Background(), db, home)
			results[i], errs[i] = creds, err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&refreshes); got != 1 {
		t.Fatalf("refresh calls = %d, want exactly 1", got)
	}
	for _, creds := range results {
		if creds.AccessToken != "fresh-access" || creds.RefreshToken != "fresh-refresh" {
			t.Fatalf("fresh credentials = %+v", creds)
		}
		// A new token means new runtime auth fields.
		if creds.EncryptUserInfo == "old-info" || creds.CosyKey == "old-key" {
			t.Fatalf("runtime auth fields were not regenerated: %+v", creds)
		}
	}

	// A still-fresh token is not refreshed again.
	before := atomic.LoadInt32(&refreshes)
	if _, err := EnsureFreshCredentials(context.Background(), db, home); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&refreshes) != before {
		t.Fatal("a fresh token must not be refreshed")
	}

	// A login without a refresh token answers 401 (the surfaces read that as
	// "sign in again").
	bare := Credentials{UID: "uid-2", AccessToken: "a", ExpiresAt: time.Now().Add(20 * time.Second), EncryptUserInfo: "i", CosyKey: "k"}
	if err := saveCredentials(context.Background(), db, home, bare); err != nil {
		t.Fatal(err)
	}
	_, refreshErr := EnsureFreshCredentials(context.Background(), db, home)
	var api *llm.APIError
	if !errors.As(refreshErr, &api) || api.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing refresh token = %v, want a 401 APIError", refreshErr)
	}
}

// --- auth: adopting the official client's own login --------------------------

func TestAdoptOfficialClientLoginReadOnly(t *testing.T) {
	official := t.TempDir()
	machineID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if err := os.MkdirAll(filepath.Join(official, ".auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(official, ".auth", "machine_id"), []byte(machineID), 0o644); err != nil {
		t.Fatal(err)
	}
	// The official user file: encrypted with the machine id prefix. The
	// tokens below are throwaway test values.
	userJSON := fmt.Sprintf(`{"token":"official-access","refresh_token":"official-refresh","uid":"uid-9","login_method":"browser","expire_time":%d}`,
		time.Now().Add(24*time.Hour).Unix())
	sealed, err := CredentialEncrypt(userJSON, machineID[:16])
	if err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(official, ".auth", "user")
	if err := os.WriteFile(userPath, []byte(sealed), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(userPath)
	if err != nil {
		t.Fatal(err)
	}
	withQoderHome(t, official)

	home := t.TempDir()
	db := testState(t)
	creds, ok, err := tryAdoptLocalQoderCNCredentials(context.Background(), db, home)
	if err != nil || !ok {
		t.Fatalf("adopt = %+v ok=%t err=%v", creds, ok, err)
	}
	if creds.AccessToken != "official-access" || creds.RefreshToken != "official-refresh" || creds.UID != "uid-9" {
		t.Fatalf("adopted = %+v", creds)
	}
	if creds.EncryptUserInfo == "" || creds.CosyKey == "" {
		t.Fatal("adoption must generate the runtime auth fields")
	}
	// The official file is untouched — byte for byte.
	after, err := os.Stat(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("the official client's credential file was modified")
	}
	// And the adoption is stored under the Forebrain home.
	stored, ok, _ := loadCredentials(context.Background(), db, home)
	if !ok || stored.AccessToken != "official-access" {
		t.Fatalf("stored after adoption = %+v", stored)
	}
}

func TestAdoptPersonalAccessTokenExchange(t *testing.T) {
	withQoderHome(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/jobToken/exchange":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["personal_token"] != "pat-test" || body["machine_id"] == "" {
				t.Errorf("exchange body = %v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tokenAnswer("uid-88"))
		case "/api/v1/userinfo":
			_, _ = w.Write([]byte(`{"id":"uid-88","username":"PAT User"}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	withGateway(t, server)
	t.Setenv(personalTokenEnv, "pat-test")

	home := t.TempDir()
	creds, ok, err := tryAdoptLocalQoderCNCredentials(context.Background(), testState(t), home)
	if err != nil || !ok {
		t.Fatalf("pat adopt = %+v ok=%t err=%v", creds, ok, err)
	}
	if creds.LoginMethod != "job_token" || creds.UID != "uid-88" || creds.Name != "PAT User" {
		t.Fatalf("pat credentials = %+v", creds)
	}
}

// --- models ------------------------------------------------------------------

// encodedModelList builds an Encode=1 listing body, as the gateway answers it:
// the scenes at the TOP LEVEL of the body, each one an array of model records.
// (The {"data":{"scenes":…}} envelope the decoder used to require is a shape
// this gateway never sends — see liveModelList.)
func encodedModelList(t *testing.T, records []qoderModelRecord) string {
	t.Helper()
	plain, err := json.Marshal(map[string]any{"chat": records, "app": records})
	if err != nil {
		t.Fatal(err)
	}
	return EncodePayload(plain)
}

// liveModelList is the account's own listing as the gateway answers it
// (2026-11-13, decoded from Encode=1): scenes at the top level, and every model
// carrying the context tiers it may serve and the thinking levels it offers.
// The tiers are what the vendor client's selectMain reads, and they are the
// same three on every production model.
const liveModelList = `{
  "chat": [{"key": "auto", "display_name": "Auto", "is_vl": true, "is_reasoning": true,
            "is_default": true, "max_input_tokens": 200000,
            "context_config": {"1M": {"token_count": 1000000}, "200K": {"token_count": 200000, "is_default": true}, "400K": {"token_count": 400000}}}],
  "app": [{"key": "qmodel_38max", "display_name": "Qwen3.8-Max", "is_vl": true, "is_reasoning": true,
           "max_input_tokens": 180000,
           "context_config": {"1M": {"token_count": 1000000}, "200K": {"token_count": 200000, "is_default": true}, "400K": {"token_count": 400000}},
           "thinking_config": {"disabled": {}, "enabled": {"efforts": {"xhigh": {}, "low": {}, "medium": {"is_default": true}}, "is_default": true}}}]
}`

// The scenes live at the top level of the body, and the app scene wins over the
// older chat spelling. This is the shape that never decoded: every process fell
// back to the static production table instead of the account's own listing.
func TestDecodeModelListTopLevelScenes(t *testing.T) {
	records, err := decodeModelList([]byte(EncodePayload([]byte(liveModelList))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 1 || records[0].Key != "qmodel_38max" {
		t.Fatalf("records = %+v, want the app scene's one model", records)
	}
	if records[0].DisplayName != "Qwen3.8-Max" {
		t.Fatalf("display name = %q", records[0].DisplayName)
	}
}

// The envelope an older gateway build answers, and the bare models array the
// official client's catalog cache holds, both still decode: the top-level scene
// is a new shape, not a replacement.
func TestDecodeModelListLegacyEnvelope(t *testing.T) {
	envelope := `{"data":{"scenes":{"app":[{"key":"envelope-model","display_name":"Envelope"}]}}}`
	records, err := decodeModelList([]byte(envelope))
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(records) != 1 || records[0].Key != "envelope-model" {
		t.Fatalf("envelope records = %+v", records)
	}
	bare := `{"models":[{"key":"cache-model","display_name":"Cache","max_input_tokens":123000,"efforts":["low","high"]}]}`
	records, err = decodeModelList([]byte(bare))
	if err != nil {
		t.Fatalf("decode models array: %v", err)
	}
	if len(records) != 1 || records[0].Key != "cache-model" {
		t.Fatalf("models records = %+v", records)
	}
}

// context_config is the model's tiers and thinking_config its levels: the window
// the model serves is the top tier (what the vendor client runs the main model
// at), and the levels are the ones the listing advertises — "none" first when
// thinking can be turned off, which is a mode the listing carries.
func TestDecodeModelListContextTiersAndEfforts(t *testing.T) {
	records, err := decodeModelList([]byte(liveModelList))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("no records")
	}
	record := records[0]
	if got := record.contextWindow(); got != 1_000_000 {
		t.Fatalf("context window = %d, want the top tier", got)
	}
	if got := strings.Join(record.effortLevels(), ","); got != "none,low,medium,xhigh" {
		t.Fatalf("effort levels = %q", got)
	}
	// The vendor's own default level is the one a row's default effort should
	// carry once a provision can write caps (see the plan's out-of-scope list).
	if !record.Thinking.Enabled.Efforts["medium"].IsDefault {
		t.Fatal("medium is the level the listing defaults to")
	}
}

// The three thinking facts a listing answers — the levels, the default level and
// whether thinking can be turned off — come from one place, so an offer and the
// builtin table can never disagree about them.
func TestThinkingLevelsOfTheStaticListing(t *testing.T) {
	levels := make(map[string]qoderModelRecord)
	for _, record := range staticQoderModels() {
		levels[record.Key] = record
	}
	cases := []struct {
		key  string
		want string // "" = the listing names no levels
		def  string
		off  bool
	}{
		{key: "auto"},
		{key: "qmodel_38max", want: "low,medium,xhigh", def: "medium", off: true},
		{key: "qmodel_latest", want: "low,high,xhigh", def: "xhigh", off: true},
		{key: "gmodel", want: "low,high,max", def: "high"},
		{key: "dfmodel", want: "low,high,max", def: "high", off: true},
	}
	for _, tc := range cases {
		record, ok := levels[tc.key]
		if !ok {
			t.Fatalf("%s is not in the static listing", tc.key)
		}
		gotLevels, gotDefault, gotOff := record.thinkingLevels()
		if got := strings.Join(gotLevels, ","); got != tc.want {
			t.Fatalf("%s levels = %q, want %q", tc.key, got, tc.want)
		}
		if gotDefault != tc.def {
			t.Fatalf("%s default = %q, want %q", tc.key, gotDefault, tc.def)
		}
		if gotOff != tc.off {
			t.Fatalf("%s off = %v, want %v", tc.key, gotOff, tc.off)
		}
		// "none" is the off switch, never a level: the builtin table puts it in
		// front only when the listing carries that switch.
		want := tc.want
		if tc.off {
			if want == "" {
				want = "none"
			} else {
				want = "none," + want
			}
		}
		if got := strings.Join(record.effortLevels(), ","); got != want {
			t.Fatalf("%s effortLevels = %q, want %q", tc.key, got, want)
		}
	}
}

// A model whose listing names no tiers serves its own max_input_tokens, and one
// with no thinking_config offers no levels at all.
func TestModelRecordWithoutTiersOrThinking(t *testing.T) {
	record := qoderModelRecord{Key: "cache-model", MaxInputTokens: 123000, Efforts: []string{"low", "high"}}
	if got := record.contextWindow(); got != 123000 {
		t.Fatalf("context window = %d, want max_input_tokens", got)
	}
	if got := strings.Join(record.effortLevels(), ","); got != "low,high" {
		t.Fatalf("flat efforts = %q", got)
	}
	if levels := (qoderModelRecord{Key: "auto", IsReasoning: true}).effortLevels(); len(levels) != 0 {
		t.Fatalf("auto offers no levels, got %v", levels)
	}
	// A listing that answers JSON null for the switch is a listing with no
	// switch: only an object offers "none".
	nullSwitch := `{"app":[{"key":"gmodel","is_reasoning":true,"thinking_config":{"disabled":null,"enabled":{"efforts":{"high":{}}}}}]}`
	records, err := decodeModelList([]byte(nullSwitch))
	if err != nil || len(records) != 1 {
		t.Fatalf("decode = %+v err=%v", records, err)
	}
	if got := strings.Join(records[0].effortLevels(), ","); got != "high" {
		t.Fatalf("levels with a null switch = %q, want just the offered level", got)
	}
}

func storedLogin(t *testing.T, db *sql.DB, home string) {
	t.Helper()
	creds := Credentials{
		UID: "uid-5", AccessToken: "tok", RefreshToken: "ref", LoginMethod: "browser",
		ExpiresAt: time.Now().Add(time.Hour), EncryptUserInfo: "INFO", CosyKey: "KEY",
	}
	if err := saveCredentials(context.Background(), db, home, creds); err != nil {
		t.Fatal(err)
	}
}

func TestFetchModelsDecodesPayloadAndRegistersCaps(t *testing.T) {
	withQoderHome(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/algo/api/v2/model/list" {
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("Encode") != "1" {
			t.Error("the listing must ask Encode=1")
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer COSY.") {
			t.Error("the listing must carry the COSY bearer")
		}
		_, _ = w.Write([]byte(encodedModelList(t, staticQoderModels())))
	}))
	defer server.Close()
	withGateway(t, server)
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })

	home := t.TempDir()
	db := testState(t)
	storedLogin(t, db, home)

	records, err := FetchModels(context.Background(), db, home)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(records) != 13 {
		t.Fatalf("records = %d, want 13", len(records))
	}
	installBuiltinModels(records)
	// The levels are the listing's own, and "none" leads them because this
	// model declares a mode that turns thinking off — the older flat table
	// could not say that.
	levels, known := llm.ReasoningEfforts("qodercn", "qmodel_38max")
	if !known || strings.Join(levels, ",") != "none,low,medium,xhigh" {
		t.Fatalf("qmodel_38max efforts = %v known=%t", levels, known)
	}
	if _, known := llm.ReasoningEfforts("qodercn", "auto"); known {
		t.Fatal("auto has no effort levels")
	}
	// The window is the top context tier the listing declares, and the input
	// budget it also declares stays out of the capability row: the host reads
	// InputTokenLimit as the compaction ceiling.
	for model, want := range map[string]int64{
		"qmodel_38max": 1_000_000, "qmodel_latest": 1_000_000, "dmodel": 1_000_000,
	} {
		hit, ok := llm.Lookup("qodercn", model)
		if !ok || hit.ContextWindow != want || hit.InputTokenLimit != 0 {
			t.Fatalf("%s caps = %+v ok=%t, want window %d and no input limit", model, hit, ok, want)
		}
	}
	if got := inputLimitOf("qmodel_38max"); got != 180000 {
		t.Fatalf("qmodel_38max input budget = %d, want the listing's max_input_tokens", got)
	}
	if !llm.KnownProvider("qodercn") {
		t.Fatal("the installed caps must make qodercn known")
	}
}

// One listing record becomes one capability row: the window is what the model
// serves, the input budget is not a window, and the levels are the ones the
// listing advertises. A model the listing calls non-reasoning says so.
func TestToLLMModelPublishesWindowAndEfforts(t *testing.T) {
	var byKey = map[string]qoderModelRecord{}
	for _, record := range staticQoderModels() {
		byKey[record.Key] = record
	}
	max := toLLMModel(byKey["qmodel_38max"])
	if max.ID != "qmodel_38max" || max.Name != "Qwen3.8-Max" {
		t.Fatalf("identity = %+v", max)
	}
	if max.ContextWindow != 1_000_000 || max.InputTokenLimit != 0 {
		t.Fatalf("window = %d input = %d, want the top tier and no input limit", max.ContextWindow, max.InputTokenLimit)
	}
	if !max.CanReason || max.DefaultMaxTokens != defaultMaxOutputTokens || !max.SupportsAttachments {
		t.Fatalf("caps = %+v", max)
	}
	levels, known := max.ReasoningEfforts()
	if !known || strings.Join(levels, ",") != "none,low,medium,xhigh" {
		t.Fatalf("efforts = %v known=%t", levels, known)
	}
	if reasoner := toLLMModel(byKey["dfmodel"]); reasoner.CanReason {
		t.Fatalf("DeepSeek-Flash is not a reasoning model in the listing: %+v", reasoner)
	}
	if plain := toLLMModel(byKey["auto"]); plain.ReasoningKnown {
		t.Fatalf("auto offers no levels: %+v", plain)
	}
}

func TestFetchModelsFallsBackToTheCatalogCacheWhenOffline(t *testing.T) {
	// A gateway that is already closed: the network is unreachable.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()
	previous := GatewayBaseURL
	GatewayBaseURL = url
	t.Cleanup(func() { GatewayBaseURL = previous })

	official := t.TempDir()
	uid := "uid-5"
	cacheJSON := `{"models":[{"key":"cache-model","display_name":"Cache Model","max_input_tokens":123000,"max_output_tokens":8000,"is_vl":true,"is_reasoning":true,"efforts":["low","high"]}]}`
	blob, err := EncryptModelCache([]byte(cacheJSON), uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(official, ".models", uid), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(official, ".models", uid, "catalog-v7"), []byte(blob), 0o644); err != nil {
		t.Fatal(err)
	}
	withQoderHome(t, official)
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })

	home := t.TempDir()
	db := testState(t)
	storedLogin(t, db, home)

	records, err := FetchModels(context.Background(), db, home)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(records) != 1 || records[0].Key != "cache-model" {
		t.Fatalf("records = %+v, want the cached model", records)
	}
	installBuiltinModels(records)
	levels, known := llm.ReasoningEfforts("qodercn", "cache-model")
	if !known || strings.Join(levels, ",") != "low,high" {
		t.Fatalf("cache model efforts = %v known=%t", levels, known)
	}
}

func TestFetchModelsStaticFallbackWhenNothingAnswers(t *testing.T) {
	withQoderHome(t, t.TempDir()) // no cache, and a dead gateway
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()
	previous := GatewayBaseURL
	GatewayBaseURL = url
	t.Cleanup(func() { GatewayBaseURL = previous })

	home := t.TempDir()
	db := testState(t)
	storedLogin(t, db, home)
	records, err := FetchModels(context.Background(), db, home)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(records) != 13 || records[0].Key != "auto" {
		t.Fatalf("static fallback = %+v", records[:2])
	}
}

// --- transport: the inference disguise ---------------------------------------

// sseFrame wraps one chunk the way the gateway does.
func sseFrame(t *testing.T, chunk string) string {
	t.Helper()
	envelope, err := json.Marshal(map[string]any{
		"statusCode": "OK", "statusCodeValue": 200, "body": chunk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(envelope) + "\n\n"
}

func TestTransportRewritesToRemoteChatAskAndUnwrapsSSE(t *testing.T) {
	withQoderHome(t, t.TempDir())
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })

	var gotAsk remoteChatAsk
	var gotQuery url.Values
	var gotHeaders http.Header
	var gotBodyRaw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/algo/api/v2/service/pro/sse/agent_chat_generation" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery, gotHeaders = r.URL.Query(), r.Header.Clone()
		gotBodyRaw, _ = io.ReadAll(r.Body)
		plain, err := DecodePayload(string(gotBodyRaw))
		if err != nil {
			t.Errorf("the request body is not an Encode=1 payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(plain, &gotAsk); err != nil {
			t.Errorf("the RemoteChatAsk does not parse: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		write := func(s string) { _, _ = w.Write([]byte(s)); flusher.Flush() }
		// A keep-alive the client must drop…
		write("data: [NOT_EXCEED_QUOTA]\n\n")
		// …reasoning…
		write(sseFrame(t, `{"id":"c1","model":"qmodel_38max","choices":[{"index":0,"delta":{"reasoning_content":"checking the forecast"}}]}`))
		// …content in two parts…
		write(sseFrame(t, `{"id":"c1","model":"qmodel_38max","choices":[{"index":0,"delta":{"content":"It is sunny"}}]}`))
		write(sseFrame(t, `{"id":"c1","model":"qmodel_38max","choices":[{"index":0,"delta":{"content":" in Paris."}}]}`))
		// …a tool call…
		write(sseFrame(t, `{"id":"c1","model":"qmodel_38max","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`))
		write(sseFrame(t, `{"id":"c1","model":"qmodel_38max","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}}`))
		write("data: [DONE]\n\n")
	}))
	defer server.Close()
	withGateway(t, server)

	// The LLM is built exactly the way a configured qodercn row builds it:
	// through the run configuration, whose factory installs the transport.
	scratchHome := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", scratchHome)
	db := testState(t)
	storedLogin(t, db, scratchHome)
	installBuiltinModels(staticQoderModels())

	client, err := run.NewLLMFromYAML(&run.LLMProviderYAML{
		Provider: "qodercn", Model: "qmodel_38max", BaseURL: "https://gateway.qoder.com.cn",
		Params: config.WithReasoningEffort(nil, "medium"),
	})
	if err != nil {
		t.Fatalf("build the client: %v", err)
	}

	ctx := llm.WithAgentSessionID(context.Background(), "sess-4321")
	tool, err := llm.NewTool("get_weather", "Get the weather of a city",
		func(context.Context, struct {
			City string `json:"city"`
		}) (string, error) {
			return "", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Execute(ctx, []llm.Message{
		llm.SystemMessage("You are precise."),
		llm.UserMessage(llm.Text("What is the weather in Paris?")),
	}, []*llm.Tool{tool})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// The request the gateway saw: the RemoteChatAsk, field for field.
	if gotAsk.ChatTask != "FREE_INPUT" || gotAsk.Version != "3" || gotAsk.AgentID != "agent_common" ||
		gotAsk.TaskID != "common" || gotAsk.SessionType != "qoderclicn" || !gotAsk.IsReply || gotAsk.IsRetry ||
		gotAsk.Source != 1 || !gotAsk.Stream {
		t.Fatalf("RemoteChatAsk header fields = %+v", gotAsk)
	}
	if gotAsk.RequestID == "" || gotAsk.RequestID != gotAsk.ChatRecordID || gotAsk.RequestID != gotAsk.RequestSetID {
		t.Fatalf("request ids = %+v", gotAsk)
	}
	if gotAsk.SessionID != "sess-4321" {
		t.Fatalf("session id = %q, want the agent session from the context", gotAsk.SessionID)
	}
	if gotAsk.System != "You are precise." {
		t.Fatalf("system = %q", gotAsk.System)
	}
	if !strings.Contains(string(gotAsk.Messages), `"system"`) || !strings.Contains(string(gotAsk.Messages), "What is the weather in Paris?") {
		t.Fatalf("messages = %s", gotAsk.Messages)
	}
	if gotAsk.ChatContext.Text != "What is the weather in Paris?" || gotAsk.ChatContext.Extra.ModelConfig.Key != "qmodel_38max" {
		t.Fatalf("chat_context = %+v", gotAsk.ChatContext)
	}
	if gotAsk.ModelConfig.Key != "qmodel_38max" || gotAsk.ModelConfig.Source != "system" ||
		gotAsk.ModelConfig.Format != "openai" || gotAsk.ModelConfig.MaxInputTokens != 180000 {
		t.Fatalf("model_config = %+v", gotAsk.ModelConfig)
	}
	if !gotAsk.ModelConfig.IsReasoning || !gotAsk.ModelConfig.IsVL {
		t.Fatalf("model_config capabilities = %+v", gotAsk.ModelConfig)
	}
	// The window the model serves and the input budget it declares are two
	// numbers, and the request carries both (the vendor client's VUa/ZUa).
	if gotAsk.Parameters.ContextLength != 1_000_000 {
		t.Fatalf("parameters.context_length = %d, want the window the model serves", gotAsk.Parameters.ContextLength)
	}
	if gotAsk.Parameters.ReasoningEffort != "medium" {
		t.Fatalf("parameters.reasoning_effort = %q", gotAsk.Parameters.ReasoningEffort)
	}
	if gotAsk.Parameters.EnableThinking == nil || !*gotAsk.Parameters.EnableThinking {
		t.Fatalf("parameters.enable_thinking = %v, want true alongside an effort", gotAsk.Parameters.EnableThinking)
	}
	if !strings.Contains(string(gotAsk.Tools), "get_weather") {
		t.Fatalf("tools = %s", gotAsk.Tools)
	}
	if gotAsk.Parameters.MaxTokens <= 0 {
		t.Fatalf("parameters = %+v", gotAsk.Parameters)
	}

	// The request the gateway saw: the headers and query of the disguise.
	if gotQuery.Get("FetchKeys") != "llm_model_result" || gotQuery.Get("AgentId") != "agent_common" || gotQuery.Get("Encode") != "1" {
		t.Fatalf("query = %v", gotQuery)
	}
	if !strings.HasPrefix(gotHeaders.Get("Authorization"), "Bearer COSY.") {
		t.Fatalf("Authorization = %q", gotHeaders.Get("Authorization"))
	}
	for header, want := range map[string]string{
		"X-Model-Key":     "qmodel_38max",
		"X-Model-Source":  "system",
		"User-Agent":      UserAgent,
		"Cosy-Version":    CosyVersion,
		"Cosy-Clienttype": "5",
		"Cosy-User":       "uid-5",
		"Cosy-Scene":      "app",
	} {
		if got := gotHeaders.Get(header); got != want {
			t.Fatalf("header %s = %q, want %q", header, got, want)
		}
	}

	// The answer the client got: unwrapped, heartbeats dropped.
	if result.Message == nil || result.Message.Parts == nil || len(result.Message.Parts) == 0 {
		t.Fatalf("result message = %+v", result.Message)
	}
	if text := result.Message.Parts[0].Text; text != "It is sunny in Paris." {
		t.Fatalf("content = %q", text)
	}
	if len(result.Message.ToolCalls) != 1 || result.Message.ToolCalls[0].Function.Name != "get_weather" ||
		!strings.Contains(result.Message.ToolCalls[0].Function.Arguments, "Paris") {
		t.Fatalf("tool calls = %+v", result.Message.ToolCalls)
	}
	if result.Usage == nil || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

// captureAsk runs one Execute against a fake gateway and returns the
// RemoteChatAsk it received, with the SSE answer the given write function
// produces.
func captureAsk(t *testing.T, ctx context.Context, write func(w http.ResponseWriter, flush func(string))) (remoteChatAsk, *llm.Result, error) {
	t.Helper()
	withQoderHome(t, t.TempDir())
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })
	var gotAsk remoteChatAsk
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plain, err := DecodePayload(string(mustReadAll(t, r.Body)))
		if err != nil {
			t.Errorf("the request body is not an Encode=1 payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(plain, &gotAsk); err != nil {
			t.Errorf("the RemoteChatAsk does not parse: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		write(w, func(s string) { _, _ = w.Write([]byte(s)); flusher.Flush() })
	}))
	t.Cleanup(server.Close)
	withGateway(t, server)

	scratchHome := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", scratchHome)
	storedLogin(t, testState(t), scratchHome)
	installBuiltinModels(staticQoderModels())
	client, err := run.NewLLMFromYAML(&run.LLMProviderYAML{
		Provider: "qodercn", Model: "qmodel_38max", BaseURL: "https://gateway.qoder.com.cn",
	})
	if err != nil {
		t.Fatalf("build the client: %v", err)
	}
	tool, err := llm.NewTool("get_weather", "Get the weather of a city",
		func(context.Context, struct {
			City string `json:"city"`
		}) (string, error) {
			return "", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	call := llm.ToolCall{
		ID: "call_1", Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: "get_weather", Arguments: `{"city":"Paris"}`},
	}
	result, err := client.Execute(ctx, []llm.Message{
		llm.SystemMessage("You are precise."),
		llm.UserMessage(llm.Text("What is the weather in Paris?")),
		// A tool-only assistant turn: no Parts, only the call. This is the
		// shape that put no content field on the wire.
		llm.AssistantMessage(nil, call),
		llm.ToolResultMessage("call_1", llm.Text("sunny, 21C")),
	}, []*llm.Tool{tool})
	return gotAsk, result, err
}

func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read the request body: %v", err)
	}
	return body
}

// A tool-only assistant turn must carry a content string on the wire. The
// upstream behind the gateway does not take an assistant message that holds
// tool_calls and no content field at all: the tool message after it reads as
// an orphan and the whole round is rejected with
//
//	Messages with role 'tool' must be a response to a preceding message with 'tool_calls'
//
// go-openai's content is omitempty, so the host's own wire form is exactly the
// one that is refused.
func TestToolCallTurnCarriesAContentString(t *testing.T) {
	ask, _, err := captureAsk(t, context.Background(), func(w http.ResponseWriter, flush func(string)) {
		flush(sseFrame(t, `{"id":"c1","choices":[{"index":0,"delta":{"content":"It is sunny."}}]}`))
		flush(sseFrame(t, `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
		flush("data: [DONE]\n\n")
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(ask.Messages, &messages); err != nil {
		t.Fatalf("messages = %s: %v", ask.Messages, err)
	}
	var assistant map[string]json.RawMessage
	for _, message := range messages {
		if string(message["role"]) == `"assistant"` {
			assistant = message
		}
	}
	if assistant == nil {
		t.Fatalf("no assistant message on the wire: %s", ask.Messages)
	}
	content, carried := assistant["content"]
	if !carried {
		t.Fatalf("the tool-call assistant message carries no content field on the wire: %s", ask.Messages)
	}
	if string(content) != `""` {
		t.Fatalf("assistant content = %s, want an empty string", content)
	}
}

// sseFailureFrame wraps one rejected request the way the gateway does: HTTP 200,
// a text/event-stream, and the real status with the provider's error object
// inside the frame's body.
func sseFailureFrame(t *testing.T, status int, providerMessage string) string {
	t.Helper()
	details, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": providerMessage, "type": "invalid_request_error",
			"param": nil, "code": "invalid_request_error",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	failure, err := json.Marshal(map[string]any{
		"code": "provider_error", "message": "Error in upstream response",
		"request_id": "718be7b7-0000", "type": "provider_error", "details": string(details),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(map[string]any{
		"headers":         map[string][]string{"Content-Type": {"application/json"}},
		"statusCode":      "BAD_REQUEST",
		"statusCodeValue": status,
		"body":            string(failure),
	})
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(envelope) + "\n\n"
}

// The gateway reports a rejected request as HTTP 200 plus a frame holding the
// provider's 400. That frame is a failure, not a chunk: the turn must fail with
// what the provider said, and never fall back to a request whose answer then
// fails to parse ("invalid character 'd' looking for beginning of value").
func TestStreamSurfacesTheEnvelopeProviderError(t *testing.T) {
	const providerMessage = "Messages with role 'tool' must be a response to a preceding message with 'tool_calls' (request_id: ac2e305a)"
	_, _, err := captureAsk(t, context.Background(), func(w http.ResponseWriter, flush func(string)) {
		flush(sseFailureFrame(t, http.StatusBadRequest, providerMessage))
		flush("event: error\n\n")
		flush(`{"success":false,"code":"provider_error"}` + "\n")
	})
	if err == nil {
		t.Fatal("the rejected turn reported no error")
	}
	if status := llm.HTTPStatusOf(err); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want the provider's 400 (err = %v)", status, err)
	}
	if !strings.Contains(err.Error(), providerMessage) {
		t.Fatalf("error = %q, want the provider's own message", err)
	}
	if strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("error = %q, want the failure itself rather than a JSON syntax error", err)
	}
}

// The gateway streams whatever the request asks for, so a non-streaming
// request (the fallback a stream failure triggers) gets the stream folded into
// one chat.completion instead of a body the client cannot parse. The answer
// fields come out as the streaming path renders them, and the disguise itself
// does not change: the RemoteChatAsk still says stream.
func TestNonStreamingRequestAssemblesTheStreamedAnswer(t *testing.T) {
	answer := func(withDone bool) func(w http.ResponseWriter, flush func(string)) {
		return func(w http.ResponseWriter, flush func(string)) {
			// A keep-alive the assembled answer must not swallow…
			flush("data: [NOT_EXCEED_QUOTA]\n\n")
			// …reasoning…
			flush(sseFrame(t, `{"id":"c1","created":1700000000,"model":"qmodel_38max","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"checking the forecast"}}]}`))
			// …content in two parts…
			flush(sseFrame(t, `{"id":"c1","choices":[{"index":0,"delta":{"content":"It is sunny"}}]}`))
			flush(sseFrame(t, `{"id":"c1","choices":[{"index":0,"delta":{"content":" in Paris."}}]}`))
			// …a tool call whose arguments arrive in two fragments…
			flush(sseFrame(t, `{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}`))
			flush(sseFrame(t, `{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]}}]}`))
			flush(sseFrame(t, `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}}`))
			// …and the terminator, or, in the second case, a body that just
			// ends.
			if withDone {
				flush("data: [DONE]\n\n")
			}
		}
	}
	for _, tc := range []struct {
		name string
		done bool
	}{{"with-DONE", true}, {"no-DONE", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ask, result, err := captureAsk(t, llm.WithNonStreamingRequest(context.Background()), answer(tc.done))
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !ask.Stream {
				t.Fatalf("RemoteChatAsk.stream = %v, want the streamed wire form", ask.Stream)
			}
			if result.Message == nil || len(result.Message.Parts) == 0 {
				t.Fatalf("result message = %+v", result.Message)
			}
			if text := result.Message.Parts[0].Text; text != "It is sunny in Paris." {
				t.Fatalf("content = %q, want the two chunks and no keep-alive", text)
			}
			if len(result.Message.ToolCalls) != 1 || result.Message.ToolCalls[0].Function.Name != "get_weather" ||
				result.Message.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
				t.Fatalf("tool calls = %+v, want the two fragments added up", result.Message.ToolCalls)
			}
			if result.Usage == nil || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 7 {
				t.Fatalf("usage = %+v", result.Usage)
			}
		})
	}
}

// Thinking is one switch and one effort, sent together: the vendor client's VUa
// never sends an effort without enable_thinking, and turning thinking off takes
// the model out of the reasoning shape too.
func TestTransportPairsTheThinkingSwitchWithTheEffort(t *testing.T) {
	for _, tc := range []struct {
		name     string
		effort   string
		thinking bool
		reason   bool
	}{
		{name: "an effort turns thinking on", effort: "medium", thinking: true, reason: true},
		{name: "none turns it off", effort: "none", thinking: false, reason: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withQoderHome(t, t.TempDir())
			var gotAsk remoteChatAsk
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				plain, err := DecodePayload(string(body))
				if err != nil {
					t.Errorf("payload: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err := json.Unmarshal(plain, &gotAsk); err != nil {
					t.Errorf("RemoteChatAsk: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(sseFrame(t, `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)))
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
			}))
			defer server.Close()
			withGateway(t, server)

			scratch := t.TempDir()
			t.Setenv("FOREBRAIN_HOME", scratch)
			db := testState(t)
			storedLogin(t, db, scratch)
			installBuiltinModels(staticQoderModels())
			t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })

			client, err := run.NewLLMFromYAML(&run.LLMProviderYAML{
				Provider: "qodercn", Model: "qmodel_38max",
				Params: config.WithReasoningEffort(nil, tc.effort),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Execute(context.Background(),
				[]llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if gotAsk.Parameters.ReasoningEffort != tc.effort {
				t.Fatalf("reasoning_effort = %q, want %q", gotAsk.Parameters.ReasoningEffort, tc.effort)
			}
			if gotAsk.Parameters.EnableThinking == nil || *gotAsk.Parameters.EnableThinking != tc.thinking {
				t.Fatalf("enable_thinking = %v, want %t", gotAsk.Parameters.EnableThinking, tc.thinking)
			}
			if gotAsk.ModelConfig.IsReasoning != tc.reason {
				t.Fatalf("model_config.is_reasoning = %t, want %t", gotAsk.ModelConfig.IsReasoning, tc.reason)
			}
			// The two numbers do not move with the switch.
			if gotAsk.Parameters.ContextLength != 1_000_000 || gotAsk.ModelConfig.MaxInputTokens != 180000 {
				t.Fatalf("context_length = %d max_input_tokens = %d",
					gotAsk.Parameters.ContextLength, gotAsk.ModelConfig.MaxInputTokens)
			}
		})
	}
}

// A 401 from the inference endpoint forces one refresh and one retry.
func TestTransportRefreshesOnceOn401(t *testing.T) {
	withQoderHome(t, t.TempDir())
	var inferCalls, refreshes int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/deviceToken/refresh":
			atomic.AddInt32(&refreshes, 1)
			_, _ = w.Write([]byte(`{"device_token":"tok2","refresh_token":"ref2","expires_in":3600}`))
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			if atomic.AddInt32(&inferCalls, 1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer COSY.") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(sseFrame(t, `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	withGateway(t, server)

	scratch := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", scratch)
	db := testState(t)
	storedLogin(t, db, scratch)
	installBuiltinModels(staticQoderModels())
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })

	client, err := run.NewLLMFromYAML(&run.LLMProviderYAML{Provider: "qodercn", Model: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Execute(context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Message == nil || result.Message.Parts[0].Text != "ok" {
		t.Fatalf("result = %+v", result.Message)
	}
	if atomic.LoadInt32(&inferCalls) != 2 || atomic.LoadInt32(&refreshes) != 1 {
		t.Fatalf("infer calls = %d refreshes = %d, want 2 and 1", inferCalls, refreshes)
	}
}

// The subscription surface entry: offers carry the endpoint the transport
// owns, provisioning writes login-backed rows, and the flows list both
// browser kinds.
func TestSubscriptionOffersAndProvision(t *testing.T) {
	withQoderHome(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/algo/api/v2/model/list" {
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(encodedModelList(t, staticQoderModels()[:3])))
	}))
	defer server.Close()
	withGateway(t, server)

	home := t.TempDir()
	db := testState(t)
	storedLogin(t, db, home)
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })

	sub := qoderCNSubscription{}
	en, zh := sub.Label()
	if en != "Qoder CN" || zh != "Qoder CN" || sub.ID() != "qodercn" {
		t.Fatalf("identity = %q/%q %q", en, zh, sub.ID())
	}
	flows := sub.Flows()
	if len(flows) != 2 || flows[0] != subscription.FlowBrowserCallback || flows[1] != subscription.FlowDeviceCode {
		t.Fatalf("flows = %v", flows)
	}

	offers, err := sub.Offers(context.Background(), subscription.Env{Home: home, DB: db})
	if err != nil {
		t.Fatalf("offers: %v", err)
	}
	if len(offers) != 3 {
		t.Fatalf("offers = %d", len(offers))
	}
	first := offers[0]
	if first.Subscription != "qodercn" || first.Provider != "qodercn" || first.BaseURL != "https://gateway.qoder.com.cn" ||
		first.APIPath != "/algo/api/v2/service/pro/sse/agent_chat_generation" {
		t.Fatalf("offer = %+v", first)
	}
	// The offer carries what the listing said about the model — the window it
	// serves, its name and its image switch — so the row it becomes is the
	// model the vendor described.
	if first.Name != "Auto" || first.ContextWindow != 1_000_000 || !first.SupportsImages {
		t.Fatalf("offer capabilities = %+v", first)
	}
	if !first.Default {
		t.Fatalf("auto is the default-checked offer: %+v", first)
	}
	// Auto's listing carries no thinking_config at all: no levels, no default,
	// and no way to turn thinking off — an answer, not a missing one.
	if first.Efforts != nil || first.DefaultEffort != "" {
		t.Fatalf("auto offers no levels and no default: %+v", first)
	}
	if first.ReasoningOff == nil || *first.ReasoningOff {
		t.Fatalf("auto cannot turn thinking off: %v", first.ReasoningOff)
	}

	config.ResetProviderPluginsForTest()
	t.Cleanup(config.ResetProviderPluginsForTest)
	config.RegisterProviderPlugin(config.BuiltinProvider{
		ID: "qodercn", Label: "Qoder CN", LabelZH: "Qoder CN", LoginBacked: true,
		Endpoints: []config.ProviderEndpoint{{BaseURL: "https://gateway.qoder.com.cn"}},
	})
	ids, err := sub.Provision(context.Background(), subscription.Env{Home: home, DB: db}, offers[:1], subscription.ProvisionOptions{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
	if err := sub.RefreshKey(context.Background(), subscription.Env{}, "m"); err != subscription.ErrNoKeyToRefresh {
		t.Fatalf("refresh key = %v", err)
	}
}

// Signing an inference request is pure in-memory work (Base64 + MD5 + header
// assembly): the plan's latency budget is 5ms a request, and this pins it.
func TestPrepareRequestHeadersStaysUnderTheLatencyBudget(t *testing.T) {
	creds := Credentials{UID: "u", AccessToken: "t", EncryptUserInfo: "INFO", CosyKey: "KEY"}
	id := MachineIdentity{MachineID: "machine-id-1234567890", MachineToken: "umid", MachineType: "5"}
	spec := PrepareSpec{Path: inferPath, EncodedBody: strings.Repeat("x", 2048), SignPath: inferSignPath, ModelKey: "qmodel_38max"}
	start := time.Now()
	for i := 0; i < 100; i++ {
		if _, _, _, err := PrepareRequestHeaders(creds, id, spec); err != nil {
			t.Fatal(err)
		}
	}
	perCall := time.Since(start) / 100
	if perCall >= 5*time.Millisecond {
		t.Fatalf("PrepareRequestHeaders averages %s a call, want < 5ms", perCall)
	}
}

// --- the account the stored login names, and the sign-out -------------------

// The account is the login's own identity, read locally from whichever home
// holds the fresher copy: the email first, then the display name, then the uid.
func TestQoderCNAccountNamesTheStoredLogin(t *testing.T) {
	ctx := context.Background()

	db := testState(t)
	home := t.TempDir()
	saveCreds := func(creds Credentials) {
		t.Helper()
		if err := saveCredentials(ctx, db, home, creds); err != nil {
			t.Fatalf("save credentials: %v", err)
		}
	}

	saveCreds(Credentials{UID: "uid-1", Name: "Wang Bin", Email: "dev@example.test", AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour)})
	account, found, err := (qoderCNSubscription{}).Account(ctx, subscription.Env{DB: db, Home: home})
	if err != nil || !found {
		t.Fatalf("account = (%v, %v, %v), want the stored login named", account, found, err)
	}
	if account.Name != "dev@example.test" {
		t.Fatalf("name = %q, want the email", account.Name)
	}

	// No email: the display name is the name.
	saveCreds(Credentials{UID: "uid-2", Name: "Wang Bin", AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour)})
	account, found, err = (qoderCNSubscription{}).Account(ctx, subscription.Env{DB: db, Home: home})
	if err != nil || !found {
		t.Fatalf("account = (%v, %v, %v), want the stored login named", account, found, err)
	}
	if account.Name != "Wang Bin" {
		t.Fatalf("name = %q, want the display name", account.Name)
	}

	// No email, no name: the uid is the name.
	saveCreds(Credentials{UID: "uid-3", AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour)})
	account, _, err = (qoderCNSubscription{}).Account(ctx, subscription.Env{DB: db, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if account.Name != "uid-3" {
		t.Fatalf("name = %q, want the uid", account.Name)
	}

	// No login stored: absent is an answer.
	account, found, err = (qoderCNSubscription{}).Account(ctx, subscription.Env{DB: testState(t), Home: t.TempDir()})
	if err != nil || found {
		t.Fatalf("account = (%v, %v, %v), want absent", account, found, err)
	}
}

// Logout removes the login from both homes it lives in; an external source
// answers ErrExternalLogin with the login already removed.
func TestQoderCNLogoutRemovesTheLoginAndAnswersExternalSources(t *testing.T) {
	ctx := context.Background()

	// A plain logout: both homes lose the login, and nothing external stands
	// in the way (the official client's probe points at a scratch dir).
	db := testState(t)
	home := t.TempDir()
	official := t.TempDir()
	withQoderHome(t, official)
	saveCreds := func() {
		t.Helper()
		if err := saveCredentials(ctx, db, home, Credentials{
			UID: "uid-1", Email: "dev@example.test", AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("save credentials: %v", err)
		}
	}
	saveCreds()
	if err := (qoderCNSubscription{}).Logout(ctx, subscription.Env{DB: db, Home: home}); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, found, err := loadCredentials(ctx, db, home); err != nil || found {
		t.Fatalf("after logout the login = (%v, %v), want it gone from both homes", found, err)
	}

	// A configured personal access token: the stored login is removed, and
	// the surface is told the environment signs the machine in again.
	saveCreds()
	t.Setenv(personalTokenEnv, "pat-from-env")
	err := (qoderCNSubscription{}).Logout(ctx, subscription.Env{DB: db, Home: home})
	if !errors.Is(err, subscription.ErrExternalLogin) {
		t.Fatalf("PAT logout = %v, want ErrExternalLogin", err)
	}
	if _, found, loadErr := loadCredentials(ctx, db, home); loadErr != nil || found {
		t.Fatalf("after an external logout the login = (%v, %v), want it removed anyway", found, loadErr)
	}

	// The official client's own login file: same answer, same honesty. The
	// file is only ever read, never decrypted or written here.
	t.Setenv(personalTokenEnv, "")
	saveCreds()
	if err := os.MkdirAll(filepath.Join(official, ".auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(official, ".auth", "user"), []byte("official-login"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = (qoderCNSubscription{}).Logout(ctx, subscription.Env{DB: db, Home: home})
	if !errors.Is(err, subscription.ErrExternalLogin) {
		t.Fatalf("official-client logout = %v, want ErrExternalLogin", err)
	}
	if _, found, loadErr := loadCredentials(ctx, db, home); loadErr != nil || found {
		t.Fatalf("after an external logout the login = (%v, %v), want it removed anyway", found, loadErr)
	}
}
