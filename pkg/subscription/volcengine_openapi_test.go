package subscription

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The documented example pair from Volcengine's signing guide
// (docs.volcengine.com/docs/api-signature-call-guide/signature-method). It is a
// published placeholder that authenticates nothing; every value below is the one
// that page prints, so a change in this file's algorithm shows up as a mismatch
// against the vendor's own worked example rather than against our own output.
const (
	docAccessKeyID = "AKLT" + "YWViMTVmZGYzM2E0NDI5Mzk2MDZjNjFmMjc2MjRjMzg"
	docSecretKey   = "WkRZeE1EQmxPVGhsWWpWak5HVmtNbUUxTXpZeU9UVXlOMlE1TmpZeVlqTQ=="
)

// The signature is the vendor's own worked example, byte for byte: the GET and
// POST canonical requests it publishes, their hashes, and the two signatures.
func TestVolcengineSignatureMatchesTheDocumentedExample(t *testing.T) {
	const xDate = "20250329T180937Z"
	cases := []struct {
		name        string
		method      string
		query       string
		body        string
		bodyHash    string
		canonical   string
		canonicalID string
		signature   string
	}{
		{
			name:        "GET",
			method:      http.MethodGet,
			query:       "Action=QueryBalanceAcct&Version=2022-01-01",
			body:        "",
			bodyHash:    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			canonicalID: "43171c1658c64b5db55c58d54988a4598d2d09a5613136beaa5eef40eae6e2c1",
			signature:   "1eda9e7e6b1728151a8e8791fdaf67cfbd28bd5c80d0fce2eb208746cf483105",
		},
		{
			name:        "POST",
			method:      http.MethodPost,
			query:       "Action=ListBill&Version=2022-01-01",
			body:        `{"Limit":10,"BillPeriod":"2023-08"}`,
			bodyHash:    "e8cc56e129d9759d56c936e679a345d001a4235b58bee8e935ccad97f23ed663",
			canonicalID: "27383e3b56d03850f5634483527fbddcbf06cf98de1bc8a6679ef2300bff3b15",
			signature:   "5e8480ceea12d0000a23c054151c50dd02c1a7dec835004057d19f13d53a7658",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.bodyHash, volcengineHexSHA256([]byte(tc.body)))
			canonical := volcengineCanonicalRequest(tc.method, "/", tc.query, [][2]string{
				{"host", "billing.volcengineapi.com"},
				{"x-date", xDate},
			}, tc.bodyHash)
			require.Equal(t, tc.canonicalID, volcengineHexSHA256([]byte(canonical)),
				"canonical request:\n%s", canonical)
			stringToSign := volcengineStringToSign(xDate, "cn-beijing", "billing", tc.canonicalID)
			key := volcengineSigningKey(docSecretKey, "20250329", "cn-beijing", "billing")
			require.Equal(t, "b491ed164936de3bb06c1eb23326aa9587b5aaa6a4e02144b9d523bbebb7ca9f",
				hexString(key), "the derived signing key the guide prints")
			require.Equal(t, tc.signature, volcengineSignature(key, stringToSign))
		})
	}
}

// The Authorization header the request itself carries, over the four headers
// Ark documents: content-type, host, x-content-sha256, x-date. The expected
// value was computed from the vendor's published algorithm by an independent
// implementation (Python's hmac/hashlib) for this exact request.
func TestVolcengineAuthorizationCoversTheFourHeaders(t *testing.T) {
	const (
		xDate = "20260101T000000Z"
		body  = `{"Plan":"CodingPlan"}`
		host  = "ark.cn-beijing.volcengineapi.com"
		query = "Action=GetPersonalPlan&Version=2024-01-01"
	)
	bodyHash := volcengineHexSHA256([]byte(body))
	require.Equal(t, "9562118cea674b7aa3155c6d0d765248691367e6878eb1a81db873a574c5dbcf", bodyHash)
	headers := [][2]string{
		{"content-type", "application/json"},
		{"host", host},
		{"x-content-sha256", bodyHash},
		{"x-date", xDate},
	}
	canonical := volcengineCanonicalRequest(http.MethodPost, "/", query, headers, bodyHash)
	require.Equal(t, "content-type;host;x-content-sha256;x-date", volcengineSignedHeaders(headers))
	canonicalID := volcengineHexSHA256([]byte(canonical))
	require.Equal(t, "3528fe8304f16b4545d6566ca22061637a8f22478f497369a293a5a1aad3a3f7", canonicalID)
	signature := volcengineSignature(
		volcengineSigningKey(docSecretKey, "20260101", volcengineRegion, volcengineService),
		volcengineStringToSign(xDate, volcengineRegion, volcengineService, canonicalID))
	require.Equal(t, "1856e99cd09c95994e2ebb18aa08c5605b90aa2967ec66255935f86f77e27355", signature)
}

// One action goes out signed: the query names it, the body is the JSON it takes,
// and the STS session token rides in the header Volcengine reads it from.
func TestVolcengineClientPostsASignedAction(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	stub.results[volcengineGetPersonalPlan] = `{"PlanType":"Lite","Status":"Running"}`
	client := newVolcengineClient()
	creds := volcengineCreds{AccessKeyID: "AKLTstub", SecretAccessKey: "stub-secret", SessionToken: "stub-session"}

	var plan volcenginePersonalPlan
	require.NoError(t, client.Do(context.Background(), volcengineGetPersonalPlan, creds,
		volcenginePlanRequest(volcengineCodingPlan), &plan))
	require.Equal(t, "Lite", plan.PlanType)
	require.Equal(t, "Running", plan.Status)
	require.Equal(t, []string{volcengineGetPersonalPlan}, stub.seenAnswers())
	require.Equal(t, map[string]any{"Plan": "CodingPlan"}, stub.lastBody(t, volcengineGetPersonalPlan))

	stub.mu.Lock()
	query, authorization := stub.queries[0], stub.auth[0]
	stub.mu.Unlock()
	require.Equal(t, "Action=GetPersonalPlan&Version="+volcengineAPIVersion, query)
	require.True(t, strings.HasPrefix(authorization,
		"HMAC-SHA256 Credential=AKLTstub/20260101/cn-beijing/ark/request, SignedHeaders=content-type;host;x-content-sha256;x-date, Signature="),
		authorization)
}

// A refusal is a short sentence naming the action and the code, and the vendor's
// own message is kept only when it cannot quote the credential it was sent with.
func TestVolcengineClientKeepsTheCredentialOutOfAnError(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	creds := volcengineCreds{AccessKeyID: "AKLTstub", SecretAccessKey: "stub-secret", SessionToken: "stub-session"}
	stub.failures[volcengineListSeatInfos] = stubFailure{status: http.StatusForbidden, code: "AccessDenied"}

	err := newVolcengineClient().Do(context.Background(), volcengineListSeatInfos, creds, nil, nil)
	var refusal *volcengineError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "ark ListSeatInfos: AccessDenied (refused)", err.Error())
	require.True(t, refusal.rejected())

	// A message that quotes the session token is dropped instead of shown.
	stub.mu.Lock()
	stub.failures[volcengineListSeatInfos] = stubFailure{status: http.StatusBadRequest, code: "InvalidParameter"}
	stub.mu.Unlock()
	server := &volcengineError{Action: "GetPersonalPlan", Status: http.StatusBadRequest, Code: "InvalidParameter",
		Message: volcengineSafeMessage("access key AKLTstub is not allowed", creds)}
	require.Empty(t, server.Message)
	require.Equal(t, "ark GetPersonalPlan: InvalidParameter", server.Error())
}

// The three classifications the probe reads: a refused credential, a vendor that
// could not be reached, and a plan the account does not hold.
func TestVolcengineErrorClassification(t *testing.T) {
	require.True(t, (&volcengineError{Status: http.StatusUnauthorized}).rejected())
	require.True(t, (&volcengineError{Status: http.StatusBadRequest, Code: "SignatureDoesNotMatch"}).rejected())
	require.True(t, (&volcengineError{Status: http.StatusBadRequest, Code: "InvalidAccessKey"}).rejected())
	require.False(t, (&volcengineError{Status: http.StatusBadRequest, Code: "InvalidParameter"}).rejected())
	require.True(t, (&volcengineError{Status: http.StatusInternalServerError}).unreachable())
	require.False(t, (&volcengineError{Status: http.StatusBadRequest}).unreachable())
	require.True(t, (&volcengineError{Status: http.StatusNotFound, Code: "ResourceNotFound.Plan"}).notFound())
	require.False(t, (&volcengineError{Status: http.StatusForbidden, Code: "AccessDenied"}).notFound())
}

// The query the signature covers is RFC3986-encoded and sorted, so the two
// parameters every action carries always spell the same string.
func TestVolcengineCanonicalQueryIsEscapedAndSorted(t *testing.T) {
	require.Equal(t, "Action=GetPersonalPlan&Version=2024-01-01",
		volcengineCanonicalQuery("GetPersonalPlan"))
	require.Equal(t, "%E4%B8%AD%E6%96%87%20a", volcengineEscape("中文 a"))
}

// hexString is the test's own hex of a key, so the derived key is compared as
// the guide prints it.
func hexString(key []byte) string {
	const digits = "0123456789abcdef"
	var builder strings.Builder
	for _, b := range key {
		builder.WriteByte(digits[b>>4])
		builder.WriteByte(digits[b&0x0f])
	}
	return builder.String()
}
