// Context compaction: orchestration, compression, and the compose step.
package turn

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

type Compactor interface {
	ManualCompactSession(context.Context, string, string) (assembly.Result, error)
}

type CompactResult struct {
	Reply  string
	Result assembly.Result
	Err    error
}

// ExecuteCompact runs the manual /compact. The compaction publishes its own
// lifecycle to the surface, so the reply is only for a surface that draws
// none of it: one sentence saying what happened.
func ExecuteCompact(ctx context.Context, sessionID string, svc Compactor) CompactResult {
	if svc == nil {
		return CompactResult{Reply: "compact: unavailable"}
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	res, err := svc.ManualCompactSession(ctx, sid, "manual")
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return CompactResult{Reply: "Compaction cancelled.", Err: err}
		}
		return CompactResult{Reply: "Compaction failed: " + err.Error(), Err: err}
	}
	reply := "Context compacted"
	if banner := assembly.FormatCompactBanner(assembly.CompactResultPayload(res), res.Duration); banner != "" {
		reply += " · " + banner
	}
	if res.Strategy == "local" {
		reply += "\n\n" + assembly.WarningMessage
	}
	return CompactResult{Reply: reply, Result: res}
}

// CompactionPosition places a finished compaction of the conversation in its
// display transcript, as the index of the row it is drawn before. Every surface
// that rebuilds a conversation places it here, so a resumed terminal and a
// reloaded web page draw it exactly where the live conversation did.
//
// A checkpoint is its own row in the transcript, which is the precise anchor:
// the compaction is drawn right after it. A compaction that replaced nothing
// has no row and falls back to the clock. One that ran before a turn — an
// automatic one outside any run — is drawn after the message it made room
// for, because that message was on screen first.
//
// A subagent's compaction belongs to that subagent's view, not to this
// transcript, and is reported as not placed.
func CompactionPosition(turns []state.Message, evt event.RunEvent) (int, bool) {
	var trigger, boundaryID, agentID string
	switch evt.Type {
	case event.RunEventContextCompacted:
		var payload event.ContextCompactedPayload
		if json.Unmarshal(evt.Payload, &payload) != nil {
			return 0, false
		}
		trigger, boundaryID, agentID = payload.Trigger, payload.BoundaryID, payload.AgentID
	case event.RunEventContextCompactError:
		var payload event.ContextCompactFailedPayload
		if json.Unmarshal(evt.Payload, &payload) != nil {
			return 0, false
		}
		trigger, agentID = payload.Trigger, payload.AgentID
	default:
		return 0, false
	}
	if strings.TrimSpace(agentID) != "" {
		return 0, false
	}
	pos := -1
	if boundaryID = strings.TrimSpace(boundaryID); boundaryID != "" {
		for i, turn := range turns {
			if part, ok := state.ParseCompactBoundaryPart(turn.PartsJSON); ok && part.WindowID == boundaryID {
				pos = i + 1
				break
			}
		}
	}
	if pos < 0 {
		pos = len(turns)
		for i, turn := range turns {
			if turn.CreatedAt*1000 > evt.CreatedAt.UnixMilli() {
				pos = i
				break
			}
		}
	}
	beforeTurn := strings.TrimSpace(trigger) != "manual" && strings.TrimSpace(evt.RunID) == ""
	if beforeTurn && pos < len(turns) && strings.EqualFold(strings.TrimSpace(turns[pos].Role), "user") {
		pos++
	}
	return pos, true
}

// GoalPosition places one of a /goal's lines in the display transcript, as the
// index of the row it is drawn before; every surface that rebuilds a
// conversation places it here. The goal's opening and each of its rounds name
// the row they are drawn after — the message that asked for the goal, and the
// last row the rounds before had written — so they land exactly where they
// were drawn live. The goal closes after its last round, before the next
// message the user sent.
func GoalPosition(turns []state.Message, evt event.RunEvent) (int, bool) {
	var afterRowID int64
	started := evt.CreatedAt
	switch evt.Type {
	case event.RunEventGoalStarted:
		var payload event.GoalStartedPayload
		if json.Unmarshal(evt.Payload, &payload) != nil {
			return 0, false
		}
		afterRowID = payload.AfterRowID
	case event.RunEventGoalRoundStarted:
		var payload event.GoalRoundStartedPayload
		if json.Unmarshal(evt.Payload, &payload) != nil {
			return 0, false
		}
		afterRowID = payload.AfterRowID
	case event.RunEventGoalCompleted:
		var payload event.GoalCompletedPayload
		if json.Unmarshal(evt.Payload, &payload) != nil {
			return 0, false
		}
		started = started.Add(-time.Duration(payload.DurationMs) * time.Millisecond)
	default:
		return 0, false
	}
	if afterRowID > 0 {
		// Rows are in the order they were stored, and the anchor itself may
		// be one a surface does not show: the line goes before the first row
		// stored after it.
		for i, turn := range turns {
			if turn.RowID > afterRowID {
				return i, true
			}
		}
		return len(turns), true
	}
	pos := len(turns)
	for i, turn := range turns {
		if turn.CreatedAt*1000 > started.UnixMilli() {
			pos = i
			break
		}
	}
	if evt.Type != event.RunEventGoalCompleted {
		return pos, true
	}
	for i := pos; i < len(turns); i++ {
		if !strings.EqualFold(strings.TrimSpace(turns[i].Role), llm.RoleUser) {
			continue
		}
		if _, _, _, isMeta := state.ParseMessageParts(turns[i].PartsJSON, ""); !isMeta {
			return i, true
		}
	}
	return len(turns), true
}

// This file holds the composer-side half of the engine: locating the "@" token
// under a cursor and turning an accepted candidate back into draft text. Both
// are pure functions over (draft, cursor) so every surface — terminal composer,
// web textarea — resolves mentions identically instead of reimplementing the
// rules against its own editor.

func tokenRuneAllowed(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
		return true
	}
	switch r {
	case '_', '-', '.', '/', '\\', '(', ')', '[', ']', '~', ':':
		return true
	}
	return false
}

// TokenAtCursor reports the "@" token the cursor sits in, as a rune offset and
// the query text after the sigil. A mid-word "@" (user@host) is not a mention,
// and a closed quote ends the token.
//
// Offsets are in runes, not bytes, so callers index the draft the same way a
// text cursor does.
func TokenAtCursor(draft string, cursor int) (start int, query string, ok bool) {
	runes := []rune(draft)
	if cursor < 0 || cursor > len(runes) {
		return 0, "", false
	}
	at := -1
	for i := cursor - 1; i >= 0; i-- {
		if runes[i] == '@' {
			at = i
			break
		}
	}
	if at == -1 {
		return 0, "", false
	}
	if at > 0 && !unicode.IsSpace(runes[at-1]) {
		return 0, "", false
	}
	token := runes[at+1 : cursor]
	if len(token) > 0 && token[0] == '"' {
		inner := token[1:]
		for _, r := range inner {
			if r == '"' {
				return 0, "", false
			}
		}
		return at, string(inner), true
	}
	for _, r := range token {
		if !tokenRuneAllowed(r) {
			return 0, "", false
		}
	}
	return at, string(token), true
}

// Acceptance is the outcome of accepting a candidate into a draft.
//
// The "@" is an affordance for locating a file, not prompt syntax, so
// accepting a file resolves it away: the draft keeps a bare path, exactly what
// the user would have typed by hand. The model is never told that "@" means
// anything, and nothing about the mention reaches the prompt beyond the path.
type Acceptance struct {
	Draft  string // draft with the token replaced by the selection
	Cursor int    // rune offset just past the replacement

	// ImagePath is the absolute path of an accepted image, whose token is
	// already gone from Draft. No tool can hand the model pixels, so an image
	// is attached to the turn instead of named in it.
	ImagePath string

	// KeepOpen reports that the token is still being built — a directory was
	// accepted and the picker should stay open to drill into it.
	KeepOpen bool
}

// Accept replaces the token at [tokenStart, tokenEnd) (rune offsets) with cand,
// resolving image candidates against root.
//
// An empty range is valid and inserts at that point, which is how a selection
// made outside the composer — a file tree, a drag — enters the draft without
// an "@" token to replace.
func Accept(draft string, tokenStart, tokenEnd int, cand Candidate, root string) (Acceptance, bool) {
	runes := []rune(draft)
	if tokenStart < 0 || tokenStart > len(runes) || tokenEnd > len(runes) || tokenEnd < tokenStart {
		return Acceptance{}, false
	}
	prefix := string(runes[:tokenStart])
	suffix := string(runes[tokenEnd:])

	if img, ok := imagePath(cand, root); ok {
		return Acceptance{
			Draft:     prefix + suffix,
			Cursor:    len([]rune(prefix)),
			ImagePath: img,
		}, true
	}

	replacement := Replacement(cand)
	return Acceptance{
		Draft:    prefix + replacement + suffix,
		Cursor:   len([]rune(prefix + replacement)),
		KeepOpen: cand.IsDir,
	}, true
}

// imagePath returns the absolute path when cand is an image the attachment
// pipeline can encode, reusing the one classification that decides which
// formats are attachable.
func imagePath(cand Candidate, root string) (string, bool) {
	if cand.IsDir {
		return "", false
	}
	abs, _, ok := CwdResolver{Dir: root}.Resolve(cand.Path)
	if !ok || Classify(abs) != KindImage {
		return "", false
	}
	return abs, true
}

// Replacement renders an accepted candidate into draft text. A file becomes a
// bare path, quoted when it contains whitespace so it survives as one
// argument. A directory keeps the "@" because the token must stay open for the
// next drill-down keystroke.
func Replacement(cand Candidate) string {
	p := cand.Path
	if cand.IsDir && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	if strings.ContainsAny(p, " \t") {
		if cand.IsDir {
			return `@"` + p
		}
		return `"` + p + `" `
	}
	if cand.IsDir {
		return "@" + p
	}
	return p + " "
}
