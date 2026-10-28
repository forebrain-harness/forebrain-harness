// Batch turns: the admission table, the execution lifecycle, and the task
// context a batch conversation carries.
//
// A batch project is the engine an integrating business system runs its work
// on: nobody is watching the conversation, nobody may join it, and every
// execution of a task is its own conversation in its own workspace. This file
// is the one place that decides whether a turn may start an execution and how
// that execution ends, so the websocket, the terminal, a detached turn and a
// channel all answer with the same stable codes.
package turn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// BatchContinuePrompt is the turn a stopped execution continues with. It is a
// constant rather than the surface's own wording because the engine owns how a
// batch task resumes: the same words reach the model whether the person
// pressed continue in the web UI, typed /continue in the terminal, or the
// business system sent an empty start_run.
const BatchContinuePrompt = "Continue this task from where it stopped. Inspect existing outputs/, .work/ and prior tool results before redoing any step. Preserve finished work, resume the first unfinished checklist item, and do not ask for new input. Before finishing, verify every deliverable and present any final files not already presented; then give the overall conclusion and coverage counts."

// BatchTurnInput is the original task text before attachment/enrichment
// blocks. Every surface starting a batch turn sets TurnRequest.BatchInput, so
// the engine can tell "the client sent nothing" from "the client sent only an
// attachment block it prepared for us".
type BatchTurnInput struct {
	Text           string
	HasAttachments bool
}

// BatchTaskStore is the project-store slice used by batch turns. It is an
// interface here so the engine depends on the three calls it makes, not on the
// whole store.
type BatchTaskStore interface {
	ExecutionForSession(ctx context.Context, sessionID string) (state.TaskExecution, state.Task, bool, error)
	StartTaskExecution(ctx context.Context, sessionID, expectedStatus, inputText string, recordInput bool) (state.TaskExecution, error)
	MarkExecutionEnded(ctx context.Context, sessionID, status, code, text string) error
}

// BatchArtifactStore is the session-store slice the batch epilogue needs to
// collect a task's deliverables. *state.SessionStore satisfies it.
type BatchArtifactStore interface {
	ListArtifactsPage(ctx context.Context, sessionID string, limit, offset int) ([]state.Artifact, int, error)
	AddArtifact(ctx context.Context, a state.Artifact) (state.Artifact, error)
}

// BatchExecutionEnd reports how one execution ended, with the final row and
// every artifact the execution delivered. A surface that has to tell an
// integrating system its task finished reads it.
type BatchExecutionEnd struct {
	SessionID string
	RunID     string
	Task      state.Task
	Execution state.TaskExecution
	Status    string
	ErrorCode string
	ErrorText string
	Artifacts []state.Artifact
}

// BatchRefusal is a batch turn the engine refused, with a stable code every
// surface (and every integrating system) can switch on. The message is an
// English sentence; the surfaces localize by Code.
type BatchRefusal struct{ Code, Message string }

func (e *BatchRefusal) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) == "" {
		return e.Code
	}
	return e.Message
}

// The refusals a batch turn can meet. They are part of the protocol the
// integration documentation publishes: a client switches on the code, never on
// the sentence.
const (
	BatchCodeNoTask            = "batch_session_has_no_task"
	BatchCodeRunning           = "batch_execution_running"
	BatchCodeFinished          = "batch_execution_finished"
	BatchCodeTakesNoInput      = "batch_takes_no_input"
	BatchCodeInputFixed        = "batch_task_input_fixed"
	BatchCodeInputMissing      = "batch_task_input_missing"
	BatchCodeCommandRefused    = "batch_command_not_allowed"
	BatchCodeModeFixed         = "batch_mode_fixed"
	BatchCodeAdmissionRequired = "batch_admission_required"
)

// BatchTurnKind says which of a task's turns this one is.
type BatchTurnKind string

const (
	// BatchTurnFirst starts an execution: it records the task's input when the
	// task has none yet, or runs on the input an earlier execution fixed.
	BatchTurnFirst BatchTurnKind = "first"
	// BatchTurnRerun is the first turn of a new execution of a task that
	// already has its input; the turn runs on that input.
	BatchTurnRerun BatchTurnKind = "rerun"
	// BatchTurnContinue resumes a stopped execution in its own conversation.
	BatchTurnContinue BatchTurnKind = "continue"
)

// BatchAdmission is what the engine decided about a batch turn.
type BatchAdmission struct {
	Kind  BatchTurnKind
	Input string // the turn's model input
	Task  state.Task
	// Execution is the execution the admission read; admitBatchTurn replaces
	// it with the running row its transition committed, so the turn announces
	// the execution's own start time and number.
	Execution state.TaskExecution
}

// AdmitBatchTurn is a READ-ONLY preview of the admission table. It never
// records task input and never changes execution status: Submit repeats it
// under the session lock before StartTaskExecution commits the transition, and
// a surface uses it to refuse a turn before it prepares attachments or creates
// a run.
func AdmitBatchTurn(ctx context.Context, store BatchTaskStore, sessionID, content string, hasAttachments bool) (BatchAdmission, error) {
	if store == nil {
		return BatchAdmission{}, &BatchRefusal{Code: BatchCodeNoTask, Message: "this batch conversation has no task"}
	}
	exec, task, ok, err := store.ExecutionForSession(ctx, sessionID)
	if err != nil {
		return BatchAdmission{}, err
	}
	if !ok {
		return BatchAdmission{}, &BatchRefusal{Code: BatchCodeNoTask, Message: "this batch conversation has no task"}
	}
	content = strings.TrimSpace(content)
	hasInput := content != "" || hasAttachments
	admission := BatchAdmission{Task: task, Execution: exec}
	switch exec.Status {
	case state.TaskStatusRunning:
		return BatchAdmission{}, &BatchRefusal{Code: BatchCodeRunning, Message: "another turn of this task execution is still running"}
	case state.TaskStatusCompleted, state.TaskStatusFailed:
		return BatchAdmission{}, &BatchRefusal{Code: BatchCodeFinished, Message: "this task execution has already finished; start a new execution of the task"}
	case state.TaskStatusStopped:
		// A stopped execution continues where it stopped. Its input was fixed
		// by the turn that started it, so new input has nowhere to go.
		if hasInput {
			return BatchAdmission{}, &BatchRefusal{Code: BatchCodeTakesNoInput, Message: "a stopped task execution continues without new input; start a new execution to run the task again"}
		}
		admission.Kind, admission.Input = BatchTurnContinue, BatchContinuePrompt
		return admission, nil
	case state.TaskStatusPending:
		if task.InputSet {
			// The task's input is fixed: this execution runs on it, and a
			// message carrying its own text is refused rather than merged.
			if hasInput {
				return BatchAdmission{}, &BatchRefusal{Code: BatchCodeInputFixed, Message: "this task already has its input; start a new execution instead of sending more"}
			}
			admission.Kind, admission.Input = BatchTurnRerun, task.InputText
			return admission, nil
		}
		if !hasInput {
			// A task's first turn has to carry the input it records.
			return BatchAdmission{}, &BatchRefusal{Code: BatchCodeInputMissing, Message: "this task has no input yet; its first message carries the task's instructions"}
		}
		admission.Kind, admission.Input = BatchTurnFirst, content
		return admission, nil
	default:
		return BatchAdmission{}, &BatchRefusal{Code: BatchCodeFinished, Message: "this task execution is not open for a turn"}
	}
}

// batchStartRefusal maps a store transition failure onto the refusal the
// admission table would have produced had it seen the winning turn's commit.
func batchStartRefusal(err error) error {
	var stateErr *state.TaskExecutionStateError
	if errors.As(err, &stateErr) {
		switch stateErr.Status {
		case state.TaskStatusRunning:
			return &BatchRefusal{Code: BatchCodeRunning, Message: "another turn of this task execution is still running"}
		case state.TaskStatusPending:
			return &BatchRefusal{Code: BatchCodeInputMissing, Message: "this task execution is not open for a turn yet"}
		default:
			return &BatchRefusal{Code: BatchCodeFinished, Message: "this task execution has already finished; start a new execution of the task"}
		}
	}
	if errors.Is(err, state.ErrTaskInputFixed) {
		return &BatchRefusal{Code: BatchCodeInputFixed, Message: "this task already has its input; start a new execution instead of sending more"}
	}
	if errors.Is(err, state.ErrTaskNotFound) {
		return &BatchRefusal{Code: BatchCodeNoTask, Message: "this batch conversation has no task"}
	}
	return err
}

// WithBatchTasks installs the batch store the turn service admits and settles
// executions through. artifacts collects a task's deliverables from its
// outputs/ folder when the model presented none; onEnd is told how every
// execution ended (a surface that notifies an integrating system passes it).
// A service built without this option never treats a turn as a batch turn,
// which is what every test that does not care about tasks wants.
func WithBatchTasks(store BatchTaskStore, artifacts BatchArtifactStore, onEnd func(context.Context, BatchExecutionEnd)) Option {
	return func(s *Service) {
		if s == nil {
			return
		}
		s.batchTasks = store
		s.batchArtifacts = artifacts
		s.batchOnEnd = onEnd
	}
}

// admitBatchTurn runs the admission check for one turn of an execution: the
// read-only preview, then the transition that starts it. It returns the
// admission the caller carries on with, or the refusal that ended the turn.
//
// It is called with the session lock held, so the preview and the transition
// see the same execution; the store re-checks under its own transaction, so a
// competing process still loses cleanly.
func (s *Service) admitBatchTurn(ctx context.Context, req TurnRequest) (BatchAdmission, error) {
	if s == nil || s.batchTasks == nil {
		return BatchAdmission{}, nil
	}
	_, _, ok, err := s.batchTasks.ExecutionForSession(ctx, req.SessionID)
	if err != nil {
		return BatchAdmission{}, err
	}
	if !ok {
		// A conversation that is not an execution never becomes one here: the
		// surfaces refuse a batch conversation without an execution row before
		// they get this far, and a non-batch conversation takes the old path.
		return BatchAdmission{}, nil
	}
	if req.BatchInput == nil {
		return BatchAdmission{}, &BatchRefusal{Code: BatchCodeAdmissionRequired, Message: "this batch turn did not pass the engine's admission check"}
	}
	admission, err := AdmitBatchTurn(ctx, s.batchTasks, req.SessionID, req.BatchInput.Text, req.BatchInput.HasAttachments)
	if err != nil {
		return BatchAdmission{}, err
	}
	recordInput := admission.Kind == BatchTurnFirst && !admission.Task.InputSet
	// The transition returns the row it committed — running, with the start
	// time it wrote — so the frame this turn announces carries the execution's
	// own facts rather than the pending snapshot the preview read.
	started, err := s.batchTasks.StartTaskExecution(ctx, req.SessionID, admission.Execution.Status, req.BatchInput.Text, recordInput)
	if err != nil {
		return BatchAdmission{}, batchStartRefusal(err)
	}
	admission.Execution = started
	return admission, nil
}

// endBatchExecution settles the execution a turn ran: the status it ended in,
// the code when it failed, the fallback deliverables when the turn delivered
// nothing, and the report a surface passes on.
//
// ctx is expected to be a context.WithoutCancel of the turn's: a cancelled
// execution still has to be recorded as stopped, still has its outputs
// collected and still reaches whoever waits for the end, and a canceled
// context would swallow all three.
func (s *Service) endBatchExecution(ctx context.Context, req TurnRequest, runID, status, code string, cause error) {
	if s == nil || s.batchTasks == nil {
		return
	}
	sid := strings.TrimSpace(req.SessionID)
	if sid == "" {
		return
	}
	text := ""
	if cause != nil {
		text = truncateRunes(cause.Error(), batchErrorTextLimit)
		if code == "" {
			if explained, ok := llm.Explain(cause); ok {
				code = string(explained.Code)
			}
		}
	}
	if code == "" && status == state.TaskStatusFailed {
		code = "error"
	}
	if err := s.batchTasks.MarkExecutionEnded(ctx, sid, status, code, text); err != nil {
		slog.Error("batch execution end not recorded", "session", sid, "run_id", runID, "status", status, "err", err)
	}
	// Read the row back: the caller needs finished_at and the final status, not
	// the running snapshot this turn started from.
	exec, task, ok, err := s.batchTasks.ExecutionForSession(ctx, sid)
	if err != nil || !ok {
		if err != nil {
			slog.Error("batch execution read back", "session", sid, "err", err)
		}
		// The execution row is gone or unreadable, so there are no facts to
		// add to the ending — but the ending is still the engine's fact, and a
		// page holding the run would otherwise keep showing it as running.
		s.publishBatchExecution(ctx, runID, event.BatchExecutionPayload{
			SessionID: sid,
			Status:    status,
			ErrorCode: code,
			ErrorText: text,
		})
		return
	}
	s.publishBatchExecution(ctx, runID, event.BatchExecutionPayload{
		TaskID:     task.ID,
		SessionID:  sid,
		Seq:        exec.Seq,
		Status:     status,
		ErrorCode:  code,
		ErrorText:  text,
		StartedAt:  exec.StartedAt,
		FinishedAt: exec.FinishedAt,
	})
	var artifacts []state.Artifact
	if s.batchArtifacts != nil {
		if _, total, err := s.batchArtifacts.ListArtifactsPage(ctx, sid, 1, 0); err != nil {
			slog.Error("batch artifacts read", "session", sid, "err", err)
		} else if total == 0 {
			// Nothing was presented, so the outputs folder is the deliverable:
			// a task that produced files must not look like it produced none.
			s.collectBatchFallbackArtifacts(ctx, sid, runID, exec.Workspace)
		}
		artifacts, _, _ = s.batchArtifacts.ListArtifactsPage(ctx, sid, batchArtifactListLimit, 0)
	}
	if s.batchOnEnd != nil {
		s.batchOnEnd(ctx, BatchExecutionEnd{
			SessionID: sid,
			RunID:     runID,
			Task:      task,
			Execution: exec,
			Status:    status,
			ErrorCode: code,
			ErrorText: text,
			Artifacts: artifacts,
		})
	}
}

// publishBatchExecution tells the surfaces watching the conversation that its
// execution moved. It is the engine speaking about its own fact, so a page
// drawing a running task never has to ask the task what it is doing: the frame
// is recorded (durable, so a page that binds later replays the last state) and
// pushed to whoever is watching now. A publish that fails costs the frame and
// nothing else — the execution is already recorded where it belongs, and the
// turn it belongs to proceeds.
func (s *Service) publishBatchExecution(ctx context.Context, runID string, payload event.BatchExecutionPayload) {
	if s == nil || s.runEventPublisher == nil {
		return
	}
	evt := event.NewRunEvent("", strings.TrimSpace(runID), strings.TrimSpace(payload.SessionID), event.RunEventBatchExecution, payload, time.Now())
	if err := s.runEventPublisher(ctx, evt); err != nil {
		slog.Warn("batch execution event not published", "session", payload.SessionID, "run_id", runID, "status", payload.Status, "err", err)
	}
}

const (
	// batchErrorTextLimit bounds the provider's sentence a failed execution
	// keeps: enough to diagnose, never a dump.
	batchErrorTextLimit = 2000
	// batchArtifactListLimit is how many artifacts one execution's report
	// carries. A task that delivers more than this is beyond what a single
	// notification can say.
	batchArtifactListLimit = 500
	// batchFallbackFileLimit bounds how many files one outputs/ folder
	// contributes, so a runaway task cannot fill the release log with rows.
	batchFallbackFileLimit = 200
)

// collectBatchFallbackArtifacts registers the files an execution left in its
// outputs/ folder as the execution's deliverables. It never follows a symlink
// and never descends into a dot-directory (that is where scratch lives), and
// it stops at batchFallbackFileLimit files.
func (s *Service) collectBatchFallbackArtifacts(ctx context.Context, sessionID, runID, workspace string) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return
	}
	outDir := filepath.Join(workspace, "outputs")
	var files []string
	_ = filepath.WalkDir(outDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable entry is skipped: the others are still deliverables.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == outDir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if len(files) >= batchFallbackFileLimit {
			return fs.SkipAll
		}
		files = append(files, p)
		return nil
	})
	sort.Strings(files)
	for _, path := range files {
		sum, kind, mimeType, preview, size, mtimeNS, err := hashArtifactFile(path)
		if err != nil {
			slog.Warn("batch fallback artifact skipped", "path", path, "err", err)
			continue
		}
		rel := filepath.Base(path)
		if r, err := filepath.Rel(workspace, path); err == nil {
			rel = filepath.ToSlash(r)
		}
		name := filepath.Base(path)
		if _, err := s.batchArtifacts.AddArtifact(ctx, state.Artifact{
			SessionID: sessionID,
			RunID:     runID,
			Path:      path,
			RelPath:   rel,
			Name:      name,
			Title:     name,
			MIME:      mimeType,
			Kind:      kind,
			Preview:   preview,
			Size:      size,
			MTimeNS:   mtimeNS,
			SHA256:    sum,
			Origin:    state.ArtifactOriginFallback,
		}); err != nil {
			slog.Error("batch fallback artifact not recorded", "session", sessionID, "path", path, "err", err)
		}
	}
}

// hashArtifactFile reads one file once: the first bytes classify it and the
// whole stream feeds the sha256 that identifies this version.
func hashArtifactFile(path string) (sum, kind, mimeType, preview string, size, mtimeNS int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", "", 0, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", "", "", "", 0, 0, err
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", "", "", "", 0, 0, err
	}
	head = head[:n]
	kind, mimeType, preview = tool.ClassifyArtifact(path, head)
	h := sha256.New()
	if _, err := h.Write(head); err != nil {
		return "", "", "", "", 0, 0, err
	}
	if _, err := io.Copy(h, f); err != nil {
		return "", "", "", "", 0, 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), kind, mimeType, preview, info.Size(), info.ModTime().UnixNano(), nil
}

// truncateRunes cuts s to at most limit runes without splitting one.
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// batchTaskInputLimit is how many input files the task context lists. Beyond
// it the context says how many more there are instead of naming them: the
// model can inspect the folder, and the paragraph must stay small.
const batchTaskInputLimit = 50

// BatchTaskContext is the task context paragraph a batch turn's environment
// carries: which task and execution this is, where it runs, and what the
// business system attached. It is returned without the wrapper tag — the rules
// hook owns <forebrain_task_context> — and it is a contract with the model, so
// every byte is deliberate:
//
//   - every dynamic value is validated as UTF-8 and XML-escaped, so a file
//     name or a path can never close the element or start an instruction;
//   - the file list is sorted by byte order and capped at 50, with the number
//     left out said explicitly;
//   - an empty inputs/ folder says "- none" and never claims truncation, and a
//     folder that cannot be read says so instead of pretending it is empty.
//
// The task's title is deliberately absent: the model already has the task's
// own words as its user message, and repeating anything else widens the
// surface a business system's text can reach.
func BatchTaskContext(task state.Task, exec state.TaskExecution) string {
	var b strings.Builder
	b.WriteString("task_id: ")
	writeBatchContextValue(&b, task.ID)
	b.WriteString("\nexecution: ")
	b.WriteString(strconv.Itoa(exec.Seq))
	b.WriteString("\ntask_workspace: ")
	writeBatchContextValue(&b, exec.Workspace)
	b.WriteString("\ninput_files:\n")
	files, err := listBatchInputFiles(exec.Workspace)
	switch {
	case err != nil:
		b.WriteString("input_files_error: ")
		writeBatchContextValue(&b, err.Error())
		b.WriteString("\n")
	case len(files) == 0:
		b.WriteString("- none\n")
	default:
		shown := files
		if len(shown) > batchTaskInputLimit {
			shown = shown[:batchTaskInputLimit]
		}
		for _, name := range shown {
			b.WriteString("- ")
			writeBatchContextValue(&b, name)
			b.WriteString("\n")
		}
		if extra := len(files) - batchTaskInputLimit; extra > 0 {
			b.WriteString("input_files_truncated: and ")
			b.WriteString(strconv.Itoa(extra))
			if extra == 1 {
				b.WriteString(" more file; inspect inputs/ for the complete set\n")
			} else {
				b.WriteString(" more files; inspect inputs/ for the complete set\n")
			}
		}
	}
	b.WriteString("deliverables: outputs/\nscratch: .work/")
	return b.String()
}

// listBatchInputFiles lists the regular files under <workspace>/inputs, as
// paths relative to the workspace in byte order, without following a symlink.
// A folder that does not exist yet is no files; one that cannot be read is an
// error, so the context never claims a task has no input when it has some.
func listBatchInputFiles(workspace string) ([]string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, nil
	}
	root := filepath.Join(workspace, "inputs")
	info, err := os.Stat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	var out []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			// A symlink is not an input: following one would let an uploaded
			// path name a file outside the workspace.
			return nil
		}
		rel, relErr := filepath.Rel(workspace, p)
		if relErr != nil {
			rel = filepath.Base(p)
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// writeBatchContextValue writes one dynamic value into the task context: valid
// UTF-8, then XML-escaped, so it is text and never markup.
func writeBatchContextValue(b *strings.Builder, value string) {
	value = strings.ToValidUTF8(value, "\uFFFD")
	_ = xml.EscapeText(b, []byte(value))
}
