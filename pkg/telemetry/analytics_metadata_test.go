package telemetry

import (
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

type counterSink struct {
	events []string
	meta   []Metadata
}

func (s *counterSink) LogEvent(name string, metadata Metadata) {
	s.events = append(s.events, name)
	s.meta = append(s.meta, metadata)
}

func TestCounterIncrementLogsAnalyticsMetadata(t *testing.T) {
	ResetAnalyticsForTesting()
	t.Cleanup(ResetAnalyticsForTesting)
	sink := &counterSink{}
	AttachSink(sink)
	var counter atomic.Uint64
	incCounter(&counter, "job_started")
	if len(sink.events) != 1 || sink.events[0] != "inproc.counter" {
		t.Fatalf("analytics events mismatch: %+v", sink.events)
	}
	if sink.meta[0]["counter"] != "job_started" {
		t.Fatalf("counter metadata mismatch: %+v", sink.meta[0])
	}
}

// ---------------------------------------------------------------------------
// 1) Bearer token formats — various cases, multiple tokens, edge spacing
// ---------------------------------------------------------------------------

func TestRedactLogLine_BearerTokenFormats(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool     // true if at least one redaction happened
		wantSub string   // expected substring in output (usually "Bearer [REDACTED]")
		forbid  []string // secrets that must NOT appear
	}{
		{
			name:    "standard lowercase bearer",
			input:   `Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9`,
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		},
		{
			name:    "uppercase BEARER",
			input:   `Authorization: BEARER mysecrettoken`,
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"mysecrettoken"},
		},
		{
			name:    "mixed case BeArEr",
			input:   `auth: BeArEr s3cr3t_v4lu3`,
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"s3cr3t_v4lu3"},
		},
		{
			name:    "multiple bearer tokens on same line",
			input:   `Bearer tok_one and also Bearer tok_two end`,
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"tok_one", "tok_two"},
		},
		{
			name:    "three bearer tokens",
			input:   `Bearer tokenA1 Bearer tokenB2 Bearer tokenC3`,
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"tokenA1", "tokenB2", "tokenC3"},
		},
		{
			name:   "bearer with no space after keyword (should NOT match)",
			input:  `Bearerabc`,
			wantOK: false,
			forbid: nil,
		},
		{
			name:   "bearer followed only by whitespace (no value — should NOT match)",
			input:  `Bearer `,
			wantOK: false,
			forbid: nil,
		},
		{
			name:   "just the word Bearer alone (should NOT match)",
			input:  `Bearer`,
			wantOK: false,
			forbid: nil,
		},
		{
			name:   "bearer with empty value via tab",
			input:  "Bearer\t",
			wantOK: false,
			forbid: nil,
		},
		{
			name:    "bearer with very long token",
			input:   `Authorization: Bearer ` + strings.Repeat("x", 1000),
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{strings.Repeat("x", 10)},
		},
		{
			name:    "bearer inside JSON log",
			input:   `{"level":"info","msg":"request","headers":{"authorization":"Bearer secret123"}}`,
			wantOK:  true,
			wantSub: `"Bearer [REDACTED]`,
			forbid:  []string{"secret123"},
		},
		{
			name:    "bearer at start of line",
			input:   `Bearer alpha-beta-gamma-delta`,
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"alpha-beta-gamma-delta"},
		},
		{
			name:    "bearer embedded in URL-like context",
			input:   `GET /api?token=Bearer hidden_value HTTP/1.1`,
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"hidden_value"},
		},
		{
			name:    "bearer with special characters in token",
			input:   `Bearer abc!@#$%^&*()_+-=[]{}|;':",./<>?`,
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"abc!@#$%^&*()_+-="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactLogLine(tt.input)

			if tt.wantOK {
				if !strings.Contains(got, tt.wantSub) {
					t.Errorf("expected %q to be present in output, got %q", tt.wantSub, got)
				}
			} else {
				if strings.Contains(got, "[REDACTED]") {
					t.Errorf("unexpected redaction for input %q", tt.input)
				}
			}

			// Verify original secrets never appear in output
			for _, forbidden := range tt.forbid {
				if strings.Contains(got, forbidden) {
					t.Errorf("forbidden secret %q leaked into output %q", forbidden, got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2) Non-bearer authorization headers — Basic, Token, custom schemes
// ---------------------------------------------------------------------------

func TestRedactLogLine_NonBearerAuthHeaders(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool // true if we expect some redaction (currently only Bearer is handled)
		wantSub string
		forbid  []string
	}{
		{
			name:   "Basic auth header — not redacted by current impl",
			input:  `Authorization: Basic dXNlcjpwYXNz`,
			wantOK: false, // Basic is NOT matched by \bBearer\s+\S+
			forbid: nil,
		},
		{
			name:   "Basic auth with credentials — not redacted",
			input:  `Authorization: Basic YWRtaW46c2VjcmV0MTIz`,
			wantOK: false,
			forbid: nil,
		},
		{
			name:   "Token auth scheme — not redacted",
			input:  `Authorization: Token my-api-key-12345`,
			wantOK: false,
			forbid: nil,
		},
		{
			name:   "Custom auth scheme — not redacted",
			input:  `Authorization: Digest realm="api", qop="auth"`,
			wantOK: false,
			forbid: nil,
		},
		{
			name:   "mac auth — not redacted",
			input:  `Authorization: MAC id="h480djs9378hsdf8", nonce="xyz"`,
			wantOK: false,
			forbid: nil,
		},
		{
			name:    "bearer mixed with basic on same line",
			input:   `Basic dXNlcjogcGFzcw then Bearer secret_tok`,
			wantOK:  true,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"secret_tok"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactLogLine(tt.input)

			if tt.wantOK {
				if !strings.Contains(got, tt.wantSub) {
					t.Errorf("expected %q in output, got %q", tt.wantSub, got)
				}
			}

			for _, forbidden := range tt.forbid {
				if strings.Contains(got, forbidden) {
					t.Errorf("forbidden secret %q leaked into output %q", forbidden, got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3) Edge cases — empty, single char, unicode, long lines, newlines, binary
// ---------------------------------------------------------------------------

func TestRedactLogLine_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // exact expected output
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "single character",
			input: "A",
			want:  "A",
		},
		{
			name:  "single space",
			input: " ",
			want:  " ",
		},
		{
			name:  "single newline",
			input: "\n",
			want:  "\n",
		},
		{
			name:  "single tab",
			input: "\t",
			want:  "\t",
		},
		{
			name:  "whitespace only",
			input: "   \t\n  ",
			want:  "   \t\n  ",
		},
		{
			name:  "unicode emoji",
			input: "🔑🔐🗝️",
			want:  "🔑🔐🗝️",
		},
		{
			name:  "plain unicode text",
			input: "plain unicode log line",
			want:  "plain unicode log line",
		},
		{
			name:  "unicode mixed with bearer",
			input: "ログ: Bearer secret_token 🚀",
			want:  "ログ: Bearer [REDACTED] 🚀",
		},
		{
			name:  "Cyrillic bearer",
			input: "Авторизация: Bearer секретный_токен",
			want:  "Авторизация: Bearer [REDACTED]",
		},
		{
			name:  "very long line without token (100KB)",
			input: strings.Repeat("a", 100_000),
			want:  strings.Repeat("a", 100_000),
		},
		{
			name:  "very long line with bearer near end",
			input: strings.Repeat("x", 50_000) + ` Bearer long_token_at_end ` + strings.Repeat("y", 50_000),
			want:  strings.Repeat("x", 50_000) + ` Bearer [REDACTED] ` + strings.Repeat("y", 50_000),
		},
		{
			name:  "newlines within string",
			input: "line1\nBearer tok1\nline3\nBearer tok2\nline5",
			want:  "line1\nBearer [REDACTED]\nline3\nBearer [REDACTED]\nline5",
		},
		{
			name:  "carriage return + newline",
			input: "Bearer crlf\r\ntoken",
			want:  "Bearer [REDACTED]\r\ntoken",
		},
		{
			name:  "binary-like content (null bytes)",
			input: "Bearer \x00\x01\x02\xff\xfe",
			want:  "Bearer [REDACTED]",
		},
		{
			name:  "binary null byte only",
			input: "\x00\x00\x00",
			want:  "\x00\x00\x00",
		},
		{
			name:  "zero-width joiner sequence",
			input: "👨‍👩‍👧‍👦 Bearer family_token 👨‍👩‍👧‍👦",
			want:  "👨‍👩‍👧‍👦 Bearer [REDACTED] 👨‍👩‍👧‍👦",
		},
		{
			name:  "right-to-left text",
			input: "مرحبا Bearer سر_الرمز أهلا",
			want:  "مرحبا Bearer [REDACTED] أهلا",
		},
		{
			name:  "surrogate pair emoji",
			input: "🧑‍💻 Bearer dev_token 🧑‍💻",
			want:  "🧑‍💻 Bearer [REDACTED] 🧑‍💻",
		},
		{
			name:  "bearer at very end of string",
			input: `some log Bearer`,
			want:  `some log Bearer`, // no value → no match
		},
		{
			name:  "bearer at very end with value",
			input: `some log Bearer final`,
			want:  `some log Bearer [REDACTED]`,
		},
		{
			name:  "consecutive bearer tokens with minimal separator",
			input: `Bearer aBearer b`,
			want:  `Bearer [REDACTED] b`, // \S+ greedily consumes "aBearer" as token value
		},
		{
			name:  "word boundary test — 'bear' prefix",
			input: `bearBering is not a token`,
			want:  `bearBering is not a token`,
		},
		{
			name:  "word boundary test — 'bearer' as part of larger word",
			input: `Unbearerized`,
			want:  `Unbearerized`,
		},
		{
			name:  "only digits as token",
			input: `Bearer 1234567890`,
			want:  `Bearer [REDACTED]`,
		},
		{
			name:  "underscore-only token",
			input: `Bearer _______`,
			want:  `Bearer [REDACTED]`,
		},
		{
			name:  "dot-separated token",
			input: `Bearer section1.section2.section3`,
			want:  `Bearer [REDACTED]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactLogLine(tt.input)

			if got != tt.want {
				t.Errorf("input=%q\n  want: %q\n  got:  %q", tt.input, tt.want, got)
			}

			// Sanity: output must be valid UTF-8 if input was
			if utf8.ValidString(tt.input) && !utf8.ValidString(got) {
				t.Errorf("output lost UTF-8 validity: %q", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4) Original secrets NEVER appear in output — property-based checks
// ---------------------------------------------------------------------------

func TestRedactLogLine_NoSecretLeakage(t *testing.T) {
	secrets := []string{
		"sk-live-abcdef1234567890",
		"ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"AKIAIOSFODNN7EXAMPLE",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"mongodb+srv://user:pass@cluster.example.com/db",
		"postgresql://admin:s3cret@localhost:5432/mydb",
		"smtp://user:password@smtp.gmail.com",
		"Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0",
		"xoxb-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx",
		"slackx-1234567890-abcdefghijklmnopqrstuvwx",
	}

	for _, secret := range secrets {
		t.Run(secret[:min(30, len(secret))]+"...", func(t *testing.T) {
			// Only check Bearer-prefixed contexts since RedactLogLine only handles Bearer tokens.
			bearerContexts := []string{
				`Authorization: Bearer ` + secret,
				`header: Authorization: Bearer ` + secret,
				`Bearer ` + secret + ` and more text`,
				strings.Repeat(`Bearer `+secret+` `, 5),
			}

			for _, ctx := range bearerContexts {
				got := RedactLogLine(ctx)
				if strings.Contains(got, secret) {
					t.Errorf("secret leaked in context %q:\n  got: %q", ctx, got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 5) Idempotency — running RedactLogLine twice should produce the same result
// ---------------------------------------------------------------------------

func TestRedactLogLine_Idempotent(t *testing.T) {
	inputs := []string{
		`Bearer tok1`,
		`Bearer tok1 and Bearer tok2`,
		`Authorization: Bearer secret123`,
		`{"auth":"Bearer key"}`,
		"",
		"no tokens here",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			once := RedactLogLine(input)
			twice := RedactLogLine(once)
			if once != twice {
				t.Errorf("not idempotent:\n  once: %q\n  twice: %q", once, twice)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 6) Regex word-boundary behavior — ensure \b works correctly around Bearer
// ---------------------------------------------------------------------------

func TestRedactLogLine_WordBoundaryBehavior(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantSub string
		forbid  []string
	}{
		{
			name:    "Bearer preceded by colon and space",
			input:   `Authorization: Bearer abc`,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"abc"},
		},
		{
			name:    "Bearer preceded by quote",
			input:   `"Authorization": "Bearer xyz"`,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"xyz"},
		},
		{
			name:    "Bearer preceded by equals",
			input:   `auth=Bearer eqval`,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"eqval"},
		},
		{
			name:    "bearer lowercase preceded by non-word char",
			input:   `(bearer lowercase_val)`,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"lowercase_val"},
		},
		{
			name:    "BEARER all caps preceded by dash",
			input:   `-BEARER CAPS_VAL`,
			wantSub: "Bearer [REDACTED]",
			forbid:  []string{"CAPS_VAL"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactLogLine(tt.input)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("expected %q in output, got %q", tt.wantSub, got)
			}
			for _, f := range tt.forbid {
				if strings.Contains(got, f) {
					t.Errorf("secret %q leaked in %q", f, got)
				}
			}
		})
	}
}

func TestRedactLogLine_BearerToken(t *testing.T) {
	input := "Authorization: Bearer abc123xyz789"
	got := RedactLogLine(input)
	if got == input {
		t.Error("expected bearer token to be redacted")
	}
	if !strings.Contains(got, "Bearer [REDACTED]") {
		t.Errorf("expected redacted output, got %q", got)
	}
	if strings.Contains(got, "abc123") {
		t.Error("original token should not appear in output")
	}
}

func TestRedactLogLine_BearerUppercase(t *testing.T) {
	input := "authorization: BEARER mysecrettoken"
	got := RedactLogLine(input)
	if !strings.Contains(got, "Bearer [REDACTED]") {
		t.Errorf("expected case-insensitive match, got %q", got)
	}
}

func TestRedactLogLine_NoBearer(t *testing.T) {
	input := "normal log line without tokens"
	got := RedactLogLine(input)
	if got != input {
		t.Errorf("expected no change, got %q", got)
	}
}

func TestRedactLogLine_EmptyString(t *testing.T) {
	got := RedactLogLine("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestRedactLogLine_MultipleBearers(t *testing.T) {
	input := "Bearer tok1 and also Bearer tok2"
	got := RedactLogLine(input)
	count := strings.Count(got, "[REDACTED]")
	if count != 2 {
		t.Errorf("expected 2 redactions, got %d in %q", count, got)
	}
}

func TestRedactLogLine_BearerNoSpace(t *testing.T) {
	// No space after Bearer — should NOT match the pattern
	input := "Bearerabc"
	got := RedactLogLine(input)
	if got != input {
		t.Errorf("expected no match for Bearer without space, got %q", got)
	}
}

func TestRedactLogLine_BearerEmptyValue(t *testing.T) {
	// Bearer followed by nothing meaningful — \S+ requires at least one non-whitespace
	input := "Bearer "
	got := RedactLogLine(input)
	if got != input {
		t.Errorf("expected no match for Bearer with trailing space only, got %q", got)
	}
}

func TestRedactLogLine_JustBearer(t *testing.T) {
	input := "Bearer"
	got := RedactLogLine(input)
	if got != input {
		t.Errorf("expected just 'Bearer' to not match, got %q", got)
	}
}
