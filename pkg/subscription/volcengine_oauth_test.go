package subscription

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The stub's own made-up values: every one of them is a placeholder for a field
// the vendor would fill, and none of them can authenticate anywhere.
const (
	stubAccessKeyID = "AKLTfromconsoleplaceholder"
	stubSecretKey   = "secret-from-console-placeholder"
	stubSession     = "session-from-console-placeholder"
	stubRefresh     = "refresh-from-console-placeholder"
)

// stubAccessToken is the console's access_token: a JSON *string* that holds the
// STS triple.
func stubAccessToken(keyID, secret, session string) string {
	inner, err := json.Marshal(map[string]string{
		"access_key_id":     keyID,
		"secret_access_key": secret,
		"session_token":     session,
	})
	if err != nil {
		panic(err)
	}
	return string(inner)
}

// stubTokenBody is one token endpoint answer.
func stubTokenBody(accessToken, refresh string, expiresIn int) string {
	quoted, err := json.Marshal(accessToken)
	if err != nil {
		panic(err)
	}
	body := `{"access_token":` + string(quoted) + `,"token_type":"urn:ietf:params:oauth:token-type:access_token_sts"` +
		`,"refresh_token":` + string(mustJSON(refresh)) + `,"id_token":"jwt-placeholder"` +
		`,"scope":"Console:All:All"`
	if expiresIn > 0 {
		body += `,"expires_in":` + strconv.Itoa(expiresIn)
	}
	return body + "}"
}

func mustJSON(value string) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

// A device authorization asks for the public cross-device client the official
// CLI uses, with the console-wide scope and a name the console can list.
func TestVolcengineDeviceAuthorizationAsksForTheConsoleClient(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	stub.device = volcengineDeviceAuth{
		DeviceCode: "device-code-1", UserCode: "ABCD-1234",
		VerificationURI:         "https://signin.volcengine.com/device",
		VerificationURIComplete: "https://signin.volcengine.com/device?user_code=ABCD-1234",
		ExpiresIn:               600, Interval: 5,
	}
	device, err := newVolcengineSignIn().startDevice(context.Background())
	require.NoError(t, err)
	require.Equal(t, "ABCD-1234", device.UserCode)
	require.Equal(t, "https://signin.volcengine.com/device?user_code=ABCD-1234", device.browserURL())

	forms := stub.formsFor(volcengineDeviceAuthPath)
	require.Len(t, forms, 1)
	require.Equal(t, volcengineClientID, forms[0].Get("client_id"))
	require.Equal(t, "Console:All:All", forms[0].Get("scope"))
	require.Equal(t, volcengineDeviceInfo, forms[0].Get("device_info"))
	require.Equal(t, "trn:signin:::devtools/cross-device", volcengineClientID)

	// Without the prefilled page the plain one is what a surface opens.
	device.VerificationURIComplete = ""
	require.Equal(t, "https://signin.volcengine.com/device", device.browserURL())
}

// The whole sign-in: the console's code reaches the surface, the poll rides out
// a pending round and a slow_down, and the credential it takes is stored where
// every later action reads it.
func TestVolcengineDevicePollWaitsOutTheIntervalAndTakesTheCredential(t *testing.T) {
	stub, waits := newVolcengineStub(t)
	env := testEnv(t)
	stub.device = volcengineDeviceAuth{
		DeviceCode: "device-code-1", UserCode: "ABCD-1234",
		VerificationURI:         "https://signin.volcengine.com/device",
		VerificationURIComplete: "https://signin.volcengine.com/device?user_code=ABCD-1234",
		ExpiresIn:               600, Interval: 5,
	}
	stub.tokens = []stubAnswer{
		{status: http.StatusBadRequest, body: `{"error":"authorization_pending"}`},
		{status: http.StatusBadRequest, body: `{"error":"slow_down"}`},
		{status: http.StatusOK, body: stubTokenBody(stubAccessToken(stubAccessKeyID, stubSecretKey, stubSession), stubRefresh, 900)},
	}

	login, err := (volcengine{}).Start(context.Background(), env, FlowDeviceCode)
	require.NoError(t, err)
	require.NoError(t, login.Wait(context.Background()))
	require.Equal(t, "https://signin.volcengine.com/device?user_code=ABCD-1234", login.AuthorizeURL())
	require.Equal(t, "ABCD-1234", login.UserCode())

	// One HTTP request per round, and slow_down adds its step.
	require.Equal(t, []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}, *waits)
	tokenForms := stub.formsFor(volcengineTokenPath)
	require.Len(t, tokenForms, 3)
	for _, form := range tokenForms {
		require.Equal(t, "device_code", form.Get("grant_type"))
		require.Equal(t, "device-code-1", form.Get("device_code"))
		require.Equal(t, volcengineClientID, form.Get("client_id"))
	}

	credential, found, err := loadVolcengineCredential(context.Background(), env)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, stubAccessKeyID, credential.AccessKeyID)
	require.Equal(t, stubSecretKey, credential.SecretAccessKey)
	require.Equal(t, stubSession, credential.SessionToken)
	require.Equal(t, stubRefresh, credential.RefreshToken)
	require.Equal(t, volcengineClientID, credential.ClientID)
	require.Equal(t, int64(900), credential.ExpiresAt-credential.IssuedAt)
}

// A denial and an expired code are the two answers that end the flow instead of
// waiting: the wording carries the word the surfaces map.
func TestVolcengineDevicePollEndsOnTheConsolesRefusals(t *testing.T) {
	cases := []struct {
		name string
		code string
		want string
	}{
		{"denied", "access_denied", "denied"},
		{"expired", "expired_token", "expired"},
		{"unknown device", "invalid_device_code", "expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub, _ := newVolcengineStub(t)
			stub.device = volcengineDeviceAuth{
				DeviceCode: "d", UserCode: "u", VerificationURI: "https://signin.volcengine.com/device",
				ExpiresIn: 600, Interval: 1,
			}
			stub.tokens = []stubAnswer{{status: http.StatusBadRequest, body: `{"error":"` + tc.code + `"}`}}
			_, err := newVolcengineSignIn().poll(context.Background(), stub.device)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// The transient budget: a handful of blips is ridden out, one more ends the
// sign-in rather than polling a dead device code forever.
func TestVolcengineDevicePollToleratesThenGivesUpOnBlips(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	blip := stubAnswer{status: http.StatusInternalServerError, body: `{"error":"server_error"}`}
	stub.device = volcengineDeviceAuth{
		DeviceCode: "d", UserCode: "u", VerificationURI: "https://signin.volcengine.com/device",
		ExpiresIn: 600, Interval: 1,
	}
	stub.tokens = []stubAnswer{blip, blip, blip, blip, blip,
		{status: http.StatusOK, body: stubTokenBody(stubAccessToken(stubAccessKeyID, stubSecretKey, stubSession), stubRefresh, 900)}}
	credential, err := newVolcengineSignIn().poll(context.Background(), stub.device)
	require.NoError(t, err, "five blips are inside the budget")
	require.Equal(t, stubAccessKeyID, credential.AccessKeyID)

	stub.tokens = []stubAnswer{blip, blip, blip, blip, blip, blip}
	_, err = newVolcengineSignIn().poll(context.Background(), stub.device)
	require.Error(t, err, "the sixth consecutive blip ends the poll")
}

// A refresh replays the client id and scope the credential was issued to, keeps
// a rotated refresh token, and stores the renewed credential.
func TestVolcengineRefreshReplaysTheClientID(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	env := testEnv(t)
	seedVolcengineCredential(t, env, time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC))
	stub.device = deviceAuthForTests()
	stub.tokens = []stubAnswer{{status: http.StatusOK, body: stubTokenBody(
		stubAccessToken("AKLTrenewedplaceholder", "renewed-secret-placeholder", "renewed-session-placeholder"),
		"rotated-refresh-placeholder", 900)}}

	credential, found, err := volcengineSessionCredential(context.Background(), env)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "AKLTrenewedplaceholder", credential.AccessKeyID)
	require.Equal(t, "rotated-refresh-placeholder", credential.RefreshToken)

	forms := stub.formsFor(volcengineTokenPath)
	require.Len(t, forms, 1)
	require.Equal(t, "refresh_token", forms[0].Get("grant_type"))
	require.Equal(t, "stub-refresh-token", forms[0].Get("refresh_token"))
	require.Equal(t, volcengineClientID, forms[0].Get("client_id"))
	require.Equal(t, "Console:All:All", forms[0].Get("scope"))

	stored, found, err := loadVolcengineCredential(context.Background(), env)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "AKLTrenewedplaceholder", stored.AccessKeyID)
}

// A token answer that is not a credential is refused with a sentence that
// repeats none of it.
func TestVolcengineAccessTokenIsParsedAsAJSONString(t *testing.T) {
	creds, err := parseVolcengineSTS(stubAccessToken(stubAccessKeyID, stubSecretKey, stubSession))
	require.NoError(t, err)
	require.Equal(t, stubAccessKeyID, creds.AccessKeyID)
	require.Equal(t, stubSession, creds.SessionToken)

	// A vendor that hands the object over unquoted still parses.
	creds, err = parseVolcengineSTS(stubAccessToken("AKLTbareplaceholder", "bare-secret-placeholder", "bare-session-placeholder"))
	require.NoError(t, err)
	require.Equal(t, "AKLTbareplaceholder", creds.AccessKeyID)

	_, err = parseVolcengineSTS("not-a-credential-at-all")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "not-a-credential-at-all")
	require.Equal(t, "the sign-in answer's access token is not a credential", err.Error())
}

// Submitting a device flow has nowhere to paste an address.
func TestVolcengineLoginTakesNoCallback(t *testing.T) {
	env := testEnv(t)
	_, err := (volcengine{}).Start(context.Background(), env, FlowPasteCallback)
	require.EqualError(t, err, `Volcengine has no "paste_callback" sign-in`)
}

// deviceAuthForTests is the console's answer a test needs when it only drives
// the token endpoint.
func deviceAuthForTests() volcengineDeviceAuth {
	return volcengineDeviceAuth{
		DeviceCode: "d", UserCode: "u", VerificationURI: "https://signin.volcengine.com/device",
		ExpiresIn: 600, Interval: 1,
	}
}
