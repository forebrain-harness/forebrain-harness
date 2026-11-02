package peer

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// The frame types and control actions of peerProtocol 1.
const (
	FrameTypeAuth    = "auth"
	FrameTypeUser    = "user"
	FrameTypeControl = "control"

	ActionRename            = "rename"
	ActionPeerMessageStatus = "peer_message_status"
	ActionNotifyWhenIdle    = "notify_when_idle"
	ActionPeerIdleNotice    = "peer_idle_notice"

	// msgProtocolVersion is the only frame version on the wire.
	msgProtocolVersion = 1

	// PriorityNow is the priority of a message the receiver handles at once;
	// PriorityNext waits its turn in the serial queue.
	PriorityNow  = "now"
	PriorityNext = "next"

	// The status a receipt carries. A refusal is the wire's "expired" with
	// status_detail "refused" — Claude Code's own encoding, not a mistake to
	// correct.
	StatusHeld      = "held"
	StatusDelivered = "delivered"
	StatusDenied    = "denied"
	StatusExpired   = "expired"
	StatusDropped   = "dropped"

	StatusDetailRefused = "refused"
)

// Message is the user-message body of a frame: what travels is a peer envelope
// as the receiving model's user turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// FrameExtension is forebrain's block inside a user frame. Claude Code ignores
// it; it is how a forebrain sender addresses an agent rather than a process.
type FrameExtension struct {
	V    int    `json:"v"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Frame is one NDJSON line. A frame keeps the keys it did not recognise, so a
// later stage can read fields this package does not model (a session id, file
// attachments) without the frame having been rewritten on the way.
type Frame struct {
	Type      string
	Action    string
	MsgID     string
	MsgV      int
	Message   Message
	Priority  string
	From      string
	Forebrain *FrameExtension
	Token     string

	// Receipt and idle-notice payloads, all at the top level of a control
	// frame.
	Status        string
	Reason        string
	OrigMsgID     string
	StatusDetail  string
	DropReason    string
	DroppedMsgIDs []string
	FromMode      string
	State         string
	FinishedAt    *int64
	Detail        string

	// extra holds every key as it arrived.
	extra map[string]json.RawMessage
}

// Raw returns a key of the frame exactly as it arrived, whether or not this
// package models it.
func (f Frame) Raw(key string) (json.RawMessage, bool) {
	value, ok := f.extra[key]
	return value, ok
}

// DecodeFrame parses one line. Unknown keys are kept; known ones are typed.
func DecodeFrame(line []byte) (Frame, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return Frame{}, err
	}
	if raw == nil {
		return Frame{}, errors.New("peer: frame is not a JSON object")
	}
	frame := Frame{
		Type:   rawString(raw, "type"),
		Action: rawString(raw, "action"),
		MsgID:  rawString(raw, "msg_id"),
		MsgV:   rawInt(raw, "msgV"),
		Token:  rawString(raw, "token"),

		Priority:     rawString(raw, "priority"),
		From:         rawString(raw, "from"),
		Status:       rawString(raw, "status"),
		Reason:       rawString(raw, "reason"),
		OrigMsgID:    rawString(raw, "orig_msg_id"),
		StatusDetail: rawString(raw, "status_detail"),
		DropReason:   rawString(raw, "drop_reason"),
		FromMode:     rawString(raw, "from_mode"),
		State:        rawString(raw, "state"),
		Detail:       rawString(raw, "detail"),
		extra:        raw,
	}
	if value, ok := raw["message"]; ok {
		_ = json.Unmarshal(value, &frame.Message)
	}
	if value, ok := raw["forebrain"]; ok {
		var extension FrameExtension
		if err := json.Unmarshal(value, &extension); err == nil {
			frame.Forebrain = &extension
		}
	}
	if value, ok := raw["dropped_msg_ids"]; ok {
		_ = json.Unmarshal(value, &frame.DroppedMsgIDs)
	}
	if value, ok := raw["finished_at"]; ok {
		var finished int64
		if err := json.Unmarshal(value, &finished); err == nil {
			frame.FinishedAt = &finished
		}
	}
	if frame.Priority == "" {
		frame.Priority = PriorityNext
	}
	return frame, nil
}

// Encode renders the frame as one line, without its newline. Only the keys the
// frame's type defines are written, plus any key that arrived with it and this
// package does not model: a frame can be relayed without losing what it
// carried.
func (f Frame) Encode() ([]byte, error) {
	body := map[string]json.RawMessage{}
	switch f.Type {
	case FrameTypeAuth:
		put(body, "type", FrameTypeAuth)
		put(body, "token", f.Token)
	case FrameTypeUser:
		put(body, "type", FrameTypeUser)
		put(body, "msgV", msgVersion(f.MsgV))
		put(body, "msg_id", f.MsgID)
		message := f.Message
		if message.Role == "" {
			message.Role = "user"
		}
		put(body, "message", message)
		put(body, "priority", priorityOrNext(f.Priority))
		put(body, "from", f.From)
		if f.Forebrain != nil {
			put(body, "forebrain", f.Forebrain)
		}
	case FrameTypeControl:
		put(body, "type", FrameTypeControl)
		put(body, "action", f.Action)
		put(body, "msgV", msgVersion(f.MsgV))
		put(body, "msg_id", f.MsgID)
		mergeControlPayload(body, f)
	default:
		return nil, errors.New("peer: unknown frame type " + f.Type)
	}
	for key, value := range f.extra {
		if _, known := body[key]; !known {
			body[key] = value
		}
	}
	return json.Marshal(body)
}

// mergeControlPayload adds the keys a control action carries.
func mergeControlPayload(body map[string]json.RawMessage, f Frame) {
	switch f.Action {
	case ActionPeerMessageStatus:
		mergeJSON(body, PeerMessageStatus{
			Status:        f.Status,
			Reason:        f.Reason,
			From:          f.From,
			OrigMsgID:     f.OrigMsgID,
			StatusDetail:  f.StatusDetail,
			DropReason:    f.DropReason,
			DroppedMsgIDs: f.DroppedMsgIDs,
		})
	case ActionNotifyWhenIdle:
		mergeJSON(body, NotifyWhenIdle{From: f.From, MsgID: f.MsgID, FromMode: f.FromMode})
	case ActionPeerIdleNotice:
		mergeJSON(body, PeerIdleNotice{
			OrigMsgID:  f.OrigMsgID,
			State:      f.State,
			FinishedAt: f.FinishedAt,
			Detail:     f.Detail,
			From:       f.From,
			FromMode:   f.FromMode,
		})
	case ActionRename:
		// Rename is accepted on the wire and ignored by forebrain (the
		// surfaces own naming), so nothing is added.
	}
}

// isAuth reports whether the frame is the authentication line.
func (f Frame) isAuth() bool { return f.Type == FrameTypeAuth }

// immediate reports whether the frame is handled on arrival rather than in the
// serial queue: every control frame except the idle subscription (whose point
// is to be answered later), and a user frame that insists on now and carries no
// attachments.
func (f Frame) immediate() bool {
	switch f.Type {
	case FrameTypeControl:
		return f.Action != ActionNotifyWhenIdle
	case FrameTypeUser:
		return f.Priority == PriorityNow && !f.hasAttachments()
	}
	return false
}

// hasAttachments reports whether the frame carries file attachments, which
// make a "now" message wait its turn with everything else.
func (f Frame) hasAttachments() bool {
	value, ok := f.extra["file_attachments"]
	return ok && len(value) > 2
}

// UserFrameOptions is what a sender fills in to build a user frame.
type UserFrameOptions struct {
	From      string
	Content   string
	Priority  string
	FromAgent string
	ToAgent   string
}

// NewUserFrame is a user frame carrying a peer envelope, with a fresh message
// id.
func NewUserFrame(o UserFrameOptions) Frame {
	frame := Frame{
		Type:     FrameTypeUser,
		MsgID:    uuid.NewString(),
		MsgV:     msgProtocolVersion,
		Message:  Message{Role: "user", Content: o.Content},
		Priority: priorityOrNext(o.Priority),
		From:     o.From,
	}
	if o.FromAgent != "" || o.ToAgent != "" {
		frame.Forebrain = &FrameExtension{V: 1, From: o.FromAgent, To: o.ToAgent}
	}
	return frame
}

// NewAuthFrame is the authentication line a sender puts first.
func NewAuthFrame(token string) Frame {
	return Frame{Type: FrameTypeAuth, Token: token, MsgV: msgProtocolVersion}
}

// PeerMessageStatus is a receipt for a peer message.
type PeerMessageStatus struct {
	Status        string   `json:"status"`
	Reason        string   `json:"reason,omitempty"`
	From          string   `json:"from,omitempty"`
	OrigMsgID     string   `json:"orig_msg_id,omitempty"`
	StatusDetail  string   `json:"status_detail,omitempty"`
	DropReason    string   `json:"drop_reason,omitempty"`
	DroppedMsgIDs []string `json:"dropped_msg_ids,omitempty"`
}

// RefusedStatus is the receipt for a message this process will not take: the
// wire value is "expired" with the detail "refused".
func RefusedStatus(reason, from, origMsgID string) PeerMessageStatus {
	return PeerMessageStatus{
		Status:       StatusExpired,
		Reason:       reason,
		From:         from,
		OrigMsgID:    origMsgID,
		StatusDetail: StatusDetailRefused,
	}
}

// Frame builds the control frame that carries the receipt.
func (s PeerMessageStatus) Frame() Frame {
	return Frame{
		Type:          FrameTypeControl,
		Action:        ActionPeerMessageStatus,
		MsgID:         uuid.NewString(),
		MsgV:          msgProtocolVersion,
		Status:        s.Status,
		Reason:        s.Reason,
		From:          s.From,
		OrigMsgID:     s.OrigMsgID,
		StatusDetail:  s.StatusDetail,
		DropReason:    s.DropReason,
		DroppedMsgIDs: s.DroppedMsgIDs,
	}
}

// NotifyWhenIdle asks to be told when the receiver goes idle. MsgID is the
// subscription's own id, which the notice quotes back.
type NotifyWhenIdle struct {
	From     string `json:"from,omitempty"`
	MsgID    string `json:"msg_id,omitempty"`
	FromMode string `json:"from_mode,omitempty"`
}

// Frame builds the control frame that carries the subscription.
func (n NotifyWhenIdle) Frame() Frame {
	msgID := n.MsgID
	if msgID == "" {
		msgID = uuid.NewString()
	}
	return Frame{
		Type:     FrameTypeControl,
		Action:   ActionNotifyWhenIdle,
		MsgID:    msgID,
		MsgV:     msgProtocolVersion,
		From:     n.From,
		FromMode: n.FromMode,
	}
}

// PeerIdleNotice tells a subscriber that the receiver is idle, exited or
// unavailable.
type PeerIdleNotice struct {
	OrigMsgID  string `json:"orig_msg_id"`
	State      string `json:"state"`
	FinishedAt *int64 `json:"finished_at,omitempty"`
	Detail     string `json:"detail,omitempty"`
	From       string `json:"from,omitempty"`
	FromMode   string `json:"from_mode,omitempty"`
}

// Frame builds the control frame that carries the notice.
func (n PeerIdleNotice) Frame() Frame {
	return Frame{
		Type:       FrameTypeControl,
		Action:     ActionPeerIdleNotice,
		MsgID:      uuid.NewString(),
		MsgV:       msgProtocolVersion,
		OrigMsgID:  n.OrigMsgID,
		State:      n.State,
		FinishedAt: n.FinishedAt,
		Detail:     n.Detail,
		From:       n.From,
		FromMode:   n.FromMode,
	}
}

// put stores a rendered value under key, ignoring a value that cannot render
// (which none of this package's types can produce).
func put(body map[string]json.RawMessage, key string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	body[key] = data
}

// mergeJSON folds a struct's JSON object into body.
func mergeJSON(body map[string]json.RawMessage, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return
	}
	for key, field := range fields {
		body[key] = field
	}
}

// rawString reads a string field.
func rawString(raw map[string]json.RawMessage, key string) string {
	value, ok := raw[key]
	if !ok {
		return ""
	}
	var out string
	_ = json.Unmarshal(value, &out)
	return out
}

// rawInt reads an int field.
func rawInt(raw map[string]json.RawMessage, key string) int {
	value, ok := raw[key]
	if !ok {
		return 0
	}
	var out int
	_ = json.Unmarshal(value, &out)
	return out
}

// msgVersion keeps a frame at the protocol version this package speaks.
func msgVersion(v int) int {
	if v <= 0 {
		return msgProtocolVersion
	}
	return v
}

// priorityOrNext defaults an unset priority the way the receiver reads it.
func priorityOrNext(priority string) string {
	if priority == "" {
		return PriorityNext
	}
	return priority
}
