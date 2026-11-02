package peer

import (
	"encoding/json"
	"strings"
	"testing"
)

// decodeGolden parses one frame fixture.
func decodeGolden(t *testing.T, name string) Frame {
	t.Helper()
	frame, err := DecodeFrame(readGolden(t, name))
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return frame
}

func TestDecodeGoldenUserFrame(t *testing.T) {
	frame := decodeGolden(t, "user-frame.json")
	if frame.Type != FrameTypeUser {
		t.Fatalf("type = %q", frame.Type)
	}
	if frame.MsgV != msgProtocolVersion {
		t.Fatalf("msgV = %d", frame.MsgV)
	}
	if frame.MsgID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("msg_id = %q", frame.MsgID)
	}
	if frame.Message.Role != "user" {
		t.Fatalf("role = %q", frame.Message.Role)
	}
	if frame.From != "uds:/tmp/cc-socks/49280.sock" || frame.Priority != PriorityNext {
		t.Fatalf("addressing = %#v", frame)
	}
	if frame.Forebrain != nil {
		t.Fatalf("Claude Code's frame carries a forebrain block: %#v", frame.Forebrain)
	}
	if !strings.Contains(frame.Message.Content, `hello <\/cross-session-message> world`) {
		t.Fatalf("content is not exactly as written: %q", frame.Message.Content)
	}
	envelope, ok := Parse(frame.Message.Content)
	if !ok {
		t.Fatal("the golden content is not an envelope")
	}
	if envelope.From != "uds:/tmp/cc-socks/49280.sock" || envelope.FromName != "repo-28" || envelope.FromMode != "prompting" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if len(envelope.HopChain) != 1 || envelope.HopChain[0] != "0123456789abcdef01234567" {
		t.Fatalf("hop chain = %#v", envelope.HopChain)
	}
}

func TestGoldenControlFrames(t *testing.T) {
	held := decodeGolden(t, "status-held.json")
	if held.Type != FrameTypeControl || held.Action != ActionPeerMessageStatus {
		t.Fatalf("frame = %#v", held)
	}
	if held.Status != StatusHeld || held.Reason != "held for approval" {
		t.Fatalf("status = %#v", held)
	}
	if held.From != "uds:/tmp/cc-socks/50000.sock" || held.OrigMsgID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("receipt addressing = %#v", held)
	}
	if held.StatusDetail != "" || held.DropReason != "" || len(held.DroppedMsgIDs) != 0 {
		t.Fatalf("a held receipt carries drop fields: %#v", held)
	}

	refused := decodeGolden(t, "status-refused.json")
	if refused.Status != StatusExpired || refused.StatusDetail != StatusDetailRefused {
		t.Fatalf("a refusal is expired+refused on the wire, got %#v", refused)
	}
	built := RefusedStatus(refused.Reason, refused.From, refused.OrigMsgID)
	if built.Status != refused.Status || built.StatusDetail != refused.StatusDetail || built.Reason != refused.Reason {
		t.Fatalf("RefusedStatus = %#v, fixture says %#v", built, refused)
	}

	dropped := decodeGolden(t, "status-dropped.json")
	if dropped.Status != StatusDropped || dropped.DropReason != ReasonRateLimited {
		t.Fatalf("dropped receipt = %#v", dropped)
	}

	notify := decodeGolden(t, "notify-when-idle.json")
	if notify.Action != ActionNotifyWhenIdle || notify.From != "uds:/tmp/cc-socks/49280.sock" {
		t.Fatalf("subscription = %#v", notify)
	}
	if notify.MsgID != "88888888-9999-4aaa-8bbb-cccccccccccc" {
		t.Fatalf("subscription msg_id = %q", notify.MsgID)
	}

	notice := decodeGolden(t, "idle-notice.json")
	if notice.Action != ActionPeerIdleNotice || notice.State != "idle" {
		t.Fatalf("notice = %#v", notice)
	}
	if notice.FinishedAt == nil || *notice.FinishedAt != 1793344027704 {
		t.Fatalf("finished_at = %v", notice.FinishedAt)
	}
	if notice.OrigMsgID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("notice orig_msg_id = %q", notice.OrigMsgID)
	}
}

func TestEncodeKeepsUnknownKeys(t *testing.T) {
	line := `{"msgV":1,"msg_id":"11111111-2222-4333-8444-555555555555","type":"user","message":{"role":"user","content":"hi"},"priority":"now","from":"uds:/tmp/cc-socks/1.sock","session_id":"elsewhere","file_attachments":[{"path":"/tmp/a"}]}`
	frame, err := DecodeFrame([]byte(line))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"session_id", "file_attachments"} {
		if _, ok := frame.Raw(key); !ok {
			t.Fatalf("%s was dropped on decode", key)
		}
	}
	if frame.Priority != PriorityNow {
		t.Fatalf("priority = %q", frame.Priority)
	}
	if frame.immediate() {
		t.Fatal("a now-priority frame carrying attachments waits its turn")
	}

	// The same frame without attachments is handled on arrival.
	withoutAttachments := frame
	withoutAttachments.extra = map[string]json.RawMessage{}
	for key, value := range frame.extra {
		if key != "file_attachments" {
			withoutAttachments.extra[key] = value
		}
	}
	if !withoutAttachments.immediate() {
		t.Fatal("a now-priority frame without attachments should be immediate")
	}

	encoded, err := frame.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("re-encoded frame is not JSON: %v", err)
	}
	for _, key := range []string{"session_id", "file_attachments", "message", "priority"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("%s was dropped on encode: %s", key, encoded)
		}
	}
}

func TestEncodeConstructsTheWireShape(t *testing.T) {
	frame := NewUserFrame(UserFrameOptions{
		From:      "uds:/tmp/fb-socks-501/4242.sock",
		Content:   "<cross-session-message>\nhi\n</cross-session-message>",
		FromAgent: "session-1",
		ToAgent:   "session-2",
	})
	if frame.MsgID == "" || frame.MsgV != msgProtocolVersion || frame.Priority != PriorityNext {
		t.Fatalf("frame = %#v", frame)
	}
	encoded, err := frame.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"msgV", "msg_id", "type", "message", "priority", "from", "forebrain"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("%s missing from %s", key, encoded)
		}
	}
	if frame.Forebrain == nil || frame.Forebrain.To != "session-2" || frame.Forebrain.V != 1 {
		t.Fatalf("extension = %#v", frame.Forebrain)
	}

	auth, err := NewAuthFrame("0123456789abcdef0123456789abcdef").Encode()
	if err != nil {
		t.Fatalf("encode auth: %v", err)
	}
	if want := `{"token":"0123456789abcdef0123456789abcdef","type":"auth"}`; string(auth) != want {
		t.Fatalf("auth line = %s, want %s", auth, want)
	}
}

func TestFrameImmediacy(t *testing.T) {
	cases := []struct {
		name  string
		frame Frame
		want  bool
	}{
		{"receipt", PeerMessageStatus{Status: StatusDelivered}.Frame(), true},
		{"idle notice", PeerIdleNotice{OrigMsgID: "x", State: "idle"}.Frame(), true},
		{"idle subscription", NotifyWhenIdle{From: "uds:/tmp/x.sock"}.Frame(), false},
		{"user next", NewUserFrame(UserFrameOptions{From: "uds:/tmp/x.sock", Content: "hi"}), false},
		{"user now", NewUserFrame(UserFrameOptions{From: "uds:/tmp/x.sock", Content: "hi", Priority: PriorityNow}), true},
		{"auth", NewAuthFrame("t"), false},
	}
	for _, testCase := range cases {
		if got := testCase.frame.immediate(); got != testCase.want {
			t.Fatalf("%s: immediate = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestControlFrameRoundTrip(t *testing.T) {
	original := decodeGolden(t, "status-dropped.json")
	encoded, err := original.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Status != original.Status || decoded.DropReason != original.DropReason || decoded.Reason != original.Reason {
		t.Fatalf("receipt fields changed: %#v", decoded)
	}
	if decoded.OrigMsgID != original.OrigMsgID || decoded.From != original.From || decoded.MsgID != original.MsgID {
		t.Fatalf("receipt identity changed: %#v", decoded)
	}

	notice := PeerIdleNotice{OrigMsgID: "x", State: "exited", Detail: "done", From: "uds:/tmp/x.sock", FromMode: "prompting"}
	finished := int64(1793344027704)
	notice.FinishedAt = &finished
	encoded, err = notice.Frame().Encode()
	if err != nil {
		t.Fatalf("encode notice: %v", err)
	}
	decoded, err = DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("decode notice: %v", err)
	}
	if decoded.FinishedAt == nil || *decoded.FinishedAt != finished || decoded.State != "exited" || decoded.Detail != "done" {
		t.Fatalf("notice fields changed: %#v", decoded)
	}
}
