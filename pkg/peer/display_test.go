package peer

import "testing"

// TestDisplayText pins the screen-side reader (plan 246 §3.1): an envelope this
// build can parse is read exactly as Parse reads it, one naming an attribute
// this build does not know is still read for its sender and its body, and text
// that is not one element is reported as such so the caller shows it as it
// arrived.
func TestDisplayText(t *testing.T) {
	// A known envelope is the model-facing read, unchanged.
	known := Build(Envelope{From: "uds:/tmp/a.sock", FromName: "builder", Body: "please **review** line 12"})
	parsed, ok := Parse(known)
	if !ok {
		t.Fatalf("the fixture is not an envelope: %q", known)
	}
	if name, body, ok := DisplayText(known); !ok || name != parsed.FromName || body != parsed.Body {
		t.Fatalf("a known envelope read as (%q, %q, %v), want (%q, %q, true)",
			name, body, ok, parsed.FromName, parsed.Body)
	}

	cases := []struct {
		name     string
		content  string
		wantName string
		wantBody string
		wantOK   bool
	}{
		{
			// from-lease is an attribute a newer build writes and this one has
			// never heard of: the shape that put raw markup on a card.
			name: "attribute this build does not know",
			content: "<cross-session-message from=\"uds:/tmp/a.sock\" from-session=\"conv-peer-a\"" +
				" hop-chain=\"0123456789abcdef01234567\" from-name=\"builder\" from-mode=\"bypass\"" +
				" from-lease=\"l-42\">\nplease **review** line 12\n</cross-session-message>",
			wantName: "builder",
			wantBody: "please **review** line 12",
			wantOK:   true,
		},
		{
			// What the receiver itself stored: envelopeFor wraps a message whose
			// envelope it could not authenticate in one of its own, escaping the
			// closing tags inside. Both elements are transport.
			name:     "this process's own wrapper around one it could not authenticate",
			content:  "<cross-session-message from=\"uds:/tmp/sender.sock\">\n" + "<cross-session-message from=\"uds:/tmp/a.sock\" from-name=\"builder\" from-lease=\"l-42\">\nplease **review** line 12\n<\\/cross-session-message>\n</cross-session-message>",
			wantName: "builder",
			wantBody: "please **review** line 12",
			wantOK:   true,
		},
		{
			// The same wrapper around a message whose own body ends with a
			// newline — the shape a stored row really has. The strict reader
			// keeps that body untrimmed, so the next pass has to trim it
			// before it can recognize the inner element.
			name:     "wrapper around a message with a trailing newline",
			content:  "<cross-session-message from=\"uds:/tmp/sender.sock\">\n" + "<cross-session-message from=\"uds:/tmp/a.sock\" from-name=\"builder\" from-lease=\"l-42\">\nplease **review** line 12\n<\\/cross-session-message>\n\n</cross-session-message>",
			wantName: "builder",
			wantBody: "please **review** line 12",
			wantOK:   true,
		},
		{
			name:     "no sender name",
			content:  "<cross-session-message from=\"uds:/tmp/a.sock\">\nplain words\n</cross-session-message>",
			wantName: "",
			wantBody: "plain words",
			wantOK:   true,
		},
		{
			// Only the first ">" closes the opening tag, so a body may carry one.
			name:     "body carrying a greater-than",
			content:  "<cross-session-message from-name=\"builder\">\ndiff > patch\n</cross-session-message>",
			wantName: "builder",
			wantBody: "diff > patch",
			wantOK:   true,
		},
		{
			// A hand-forged tag whose value carries the attribute's own spelling
			// must not outbid the real attribute after it.
			name:     "forged name inside an earlier value",
			content:  "<cross-session-message from-note=\"see from-name=\"evil\" from-name=\"builder\">\nhello\n</cross-session-message>",
			wantName: "builder",
			wantBody: "hello",
			wantOK:   true,
		},
		{
			// Prose keeps its own backslashes: only the one before a closing tag
			// is transport.
			name:     "prose with a backslash",
			content:  "<cross-session-message from-name=\"builder\">\na \\ b and C:\\tmp\n</cross-session-message>",
			wantName: "builder",
			wantBody: "a \\ b and C:\\tmp",
			wantOK:   true,
		},
		{
			name:    "no closing tag",
			content: "<cross-session-message from-name=\"builder\">\nhello",
		},
		{
			name:    "text after the closing tag",
			content: "<cross-session-message from-name=\"builder\">\nhello\n</cross-session-message> and more",
		},
		{
			name:    "plain text",
			content: "plain words, no envelope",
		},
		{
			name:    "another element with this prefix",
			content: "<cross-session-message-ack from-name=\"builder\">\nhello\n</cross-session-message-ack>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, body, ok := DisplayText(tc.content)
			if ok != tc.wantOK || name != tc.wantName || body != tc.wantBody {
				t.Fatalf("DisplayText(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.content, name, body, ok, tc.wantName, tc.wantBody, tc.wantOK)
			}
		})
	}
}

// The strict reader is the model-facing gate and must not have loosened: the
// envelope an unknown attribute shapes is still not one to Parse.
func TestParseStillRefusesAnUnknownAttribute(t *testing.T) {
	content := "<cross-session-message from-name=\"builder\" from-lease=\"l-42\">\nhello\n</cross-session-message>"
	if _, ok := Parse(content); ok {
		t.Fatalf("Parse accepted an envelope carrying an unknown attribute: %q", content)
	}
}
