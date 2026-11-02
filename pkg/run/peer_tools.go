// Talking to the other agent: the dispatcher and the subagents it dispatched
// address each other by name, inside the one process they share.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

const (
	// peerParentName is the name the dispatching agent answers to inside a
	// subagent: there is one, and it is the agent that started the run.
	peerParentName = "parent"

	// peerTaskPreviewRunes bounds the task text a roster row prints: a name
	// is what the dispatcher needs to address the agent, not its whole brief.
	peerTaskPreviewRunes = 120

	// peerListLimit is how many subagents a roster row lists.
	peerListLimit = 20

	peerListDescription = "List the agents you can message with peer_send, with each one's status and task. The name is the address: pass it to peer_send exactly as printed."
	peerSendDescription = "Send a message to an agent listed by peer_list. A running agent receives it at its next tool boundary. Your idle subagent takes no messages: use subagent_continue to give it new work. A reply arrives as a message from that agent."
)

// peerSendInput is what the model addresses a message with.
type peerSendInput struct {
	To      string `json:"to" jsonschema_description:"The agent's name exactly as peer_list printed it"`
	Message string `json:"message" jsonschema_description:"The message, self-contained: the recipient sees nothing else you wrote"`
}

// peerAgent is one row of a roster: who can be reached, and what it is doing.
type peerAgent struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Task   string `json:"task,omitempty"`
}

// peerListAnswer is peer_list's answer: the caller's own name and everyone it
// can address.
type peerListAnswer struct {
	Self   string      `json:"self"`
	Agents []peerAgent `json:"agents"`
}

// peerSendAnswer is peer_send's answer: where the message went.
type peerSendAnswer struct {
	Status string `json:"status"`
	Hint   string `json:"hint,omitempty"`
}

// registerPeerTools adds the two tools the agents of one conversation talk to
// each other with. They ride the subagent family's own switch: an agent with
// nobody to speak to is not offered a way to speak.
func registerPeerTools(a *agent.Agent, fac Factory) error {
	list, err := llm.NewTool("peer_list", peerListDescription, func(ctx context.Context, _ *struct{}) (string, error) {
		return peerList(ctx, fac)
	})
	if err != nil {
		return err
	}
	if err := fac.Tools.Register(a, list); err != nil {
		return err
	}
	if fac.Tools != nil {
		fac.Tools.RegisterToolMeta(event.ToolMeta{
			Name:            "peer_list",
			Description:     peerListDescription,
			Category:        "session",
			ReadOnly:        true,
			ConcurrencySafe: true,
			InputSchema:     tool.SchemaForInputType(reflect.TypeOf(struct{}{})),
		})
	}

	send, err := llm.NewTool("peer_send", peerSendDescription, func(ctx context.Context, in *peerSendInput) (string, error) {
		return peerSend(ctx, fac, in)
	})
	if err != nil {
		return err
	}
	if err := fac.Tools.Register(a, send); err != nil {
		return err
	}
	if fac.Tools != nil {
		fac.Tools.RegisterToolMeta(event.ToolMeta{
			Name:        "peer_send",
			Description: peerSendDescription,
			Category:    "session",
			ReadOnly:    true,
			InputSchema: tool.SchemaForInputType(reflect.TypeOf(peerSendInput{})),
		})
	}
	return nil
}

// peerList names the agents this caller may message. The dispatching agent sees
// the subagents of its own conversation, the ones at work first; a subagent
// sees the one agent that dispatched it, because a subagent answers to its
// dispatcher and to nobody else.
func peerList(ctx context.Context, fac Factory) (string, error) {
	conversationSessionID := strings.TrimSpace(tool.ConversationSessionIDFromContext(ctx))
	if tool.IsSubagentRunFromContext(ctx) {
		answer := peerListAnswer{
			Self: strings.TrimSpace(tool.HookAgentIDFromContext(ctx)),
			Agents: []peerAgent{{
				Name:   peerParentName,
				Kind:   "primary",
				Status: peerDispatcherStatus(fac, conversationSessionID),
			}},
		}
		b, _ := json.Marshal(answer)
		return string(b), nil
	}
	records, err := agent.ListMerged(fac.subagentScopeRoot(), agent.Query{
		SessionID: conversationSessionID,
		Limit:     peerListLimit,
	})
	if err != nil {
		return "", err
	}
	agents := make([]peerAgent, 0, len(records))
	for _, record := range records {
		agents = append(agents, peerAgent{
			Name:   subagentRosterKey(record),
			Kind:   strings.TrimSpace(record.AgentType),
			Status: peerAgentStatus(subagentPhase(fac, record)),
			Task:   truncateRunes(peerTaskText(record), peerTaskPreviewRunes),
		})
	}
	// The agents at work are the ones the dispatcher is about to ask
	// something; a finished one is history and belongs at the end.
	sortPeerAgents(agents)
	answer := peerListAnswer{Self: peerParentName, Agents: agents}
	b, _ := json.Marshal(answer)
	return string(b), nil
}

// peerSend delivers one message: to a running subagent when the dispatching
// agent speaks, to the dispatching agent when a subagent answers. The text the
// recipient reads names the sender, because the recipient never saw anything
// else its peer wrote.
func peerSend(ctx context.Context, fac Factory, in *peerSendInput) (string, error) {
	to, message := "", ""
	if in != nil {
		to = strings.TrimSpace(in.To)
		message = strings.TrimSpace(in.Message)
	}
	if message == "" {
		return "", fmt.Errorf("peer_send needs a message")
	}
	if to == "" {
		return "", fmt.Errorf("no agent named %q; call peer_list", to)
	}
	conversationSessionID := strings.TrimSpace(tool.ConversationSessionIDFromContext(ctx))
	self := peerParentName
	if tool.IsSubagentRunFromContext(ctx) {
		self = strings.TrimSpace(tool.HookAgentIDFromContext(ctx))
	}
	if strings.EqualFold(to, self) {
		return "", fmt.Errorf("peer_send cannot address %q: that is this agent", to)
	}
	text := peerMessageText(self, message)
	if tool.IsSubagentRunFromContext(ctx) {
		if !strings.EqualFold(to, peerParentName) {
			return "", fmt.Errorf("no agent named %q; call peer_list", to)
		}
		return peerSendToDispatcher(fac, conversationSessionID, text), nil
	}
	return peerSendToSubagent(fac, conversationSessionID, to, text)
}

// peerSendToDispatcher delivers a subagent's message into the conversation
// that dispatched it. The conversation's queue takes it even while it is
// closed — a batch task refuses people, not its own agents — and the message
// reaches the dispatching model at its next tool boundary. A dispatcher with
// no run in flight is not woken here: the message waits in the queue for the
// run that comes next.
func peerSendToDispatcher(fac Factory, conversationSessionID, text string) string {
	queue := fac.ownerControl().SessionQueue(conversationSessionID)
	if !queue.SteerFromAgent(Input{Text: text}) {
		return peerAnswerJSON(peerSendAnswer{
			Status: "queued",
			Hint:   "Your dispatcher is not running now; it will see this when it next runs.",
		})
	}
	signalPeerMessage(conversationSessionID)
	return peerAnswerJSON(peerSendAnswer{Status: "delivered"})
}

// peerSendToSubagent delivers the dispatching agent's message to one of its
// subagents. An idle subagent takes no message: it has handed its result back,
// and what it does next is the dispatcher's decision.
func peerSendToSubagent(fac Factory, conversationSessionID, to, text string) (string, error) {
	ch, _, err := subagentChannelForConversation(fac.Owner, conversationSessionID, to)
	if errors.Is(err, ErrSubagentNotFound) {
		return "", fmt.Errorf("no agent named %q; call peer_list", to)
	}
	if err != nil {
		return "", err
	}
	delivery, err := steerRunningSubagent(ch, Input{Text: text})
	if errors.Is(err, ErrSubagentNotRunning) {
		return peerAnswerJSON(peerSendAnswer{
			Status: "idle",
			Hint:   "Your idle subagent takes no messages; use subagent_continue to give it new work.",
		}), nil
	}
	if err != nil {
		return "", err
	}
	answer := peerSendAnswer{Status: "delivered"}
	if delivery == SubagentDeliveryQueued {
		answer.Status = "queued"
	}
	return peerAnswerJSON(answer), nil
}

func peerAnswerJSON(answer peerSendAnswer) string {
	b, _ := json.Marshal(answer)
	return string(b)
}

// peerMessageText wraps a message so the recipient reads who wrote it: a peer
// message arrives the way a person's message does, and the sender's name is
// the only thing that says which conversation it belongs to.
func peerMessageText(from, message string) string {
	return `<agent-message from="` + strings.TrimSpace(from) + `">` + message + `</agent-message>`
}

// peerTaskText is the task a roster row prints: the short title when the
// dispatcher gave one, else the brief itself.
func peerTaskText(record agent.HistoryEntry) string {
	if title := strings.TrimSpace(record.Title); title != "" {
		return title
	}
	return strings.TrimSpace(record.Task)
}

// peerAgentStatus reduces a subagent's phase to the three states a roster row
// carries. A subagent held behind an approval gate is still running: the
// dispatcher's message waits for it like any other.
func peerAgentStatus(phase string) string {
	switch phase {
	case "running", "waiting_approval":
		return "running"
	case "idle":
		return "idle"
	default:
		return "finished"
	}
}

// peerStatusRank orders a roster: the agents at work first, the idle ones next,
// the finished ones last.
func peerStatusRank(status string) int {
	switch status {
	case "running":
		return 0
	case "idle":
		return 1
	default:
		return 2
	}
}

// sortPeerAgents orders the roster in place, keeping the ledger's own order —
// newest update first — inside each rank.
func sortPeerAgents(agents []peerAgent) {
	ordered := make([]peerAgent, 0, len(agents))
	for rank := 0; rank <= 2; rank++ {
		for _, a := range agents {
			if peerStatusRank(a.Status) == rank {
				ordered = append(ordered, a)
			}
		}
	}
	copy(agents, ordered)
}

// peerDispatcherStatus says whether the conversation that dispatched this
// subagent has a run in flight, so a subagent can tell whether its dispatcher
// is listening now or will read the message on its next run.
func peerDispatcherStatus(fac Factory, conversationSessionID string) string {
	if _, phase, ok := fac.ownerControl().Find(conversationSessionID); ok && phase != Finished {
		return "running"
	}
	return "idle"
}

var (
	peerInboxMu sync.Mutex
	peerInboxes = map[string]chan struct{}{}
)

// peerInbox is one conversation's arrival signal: a subagent's message to its
// dispatcher posts to it, and the dispatcher's own wait on that subagent
// returns on it. It is keyed by the conversation, the same way a conversation's
// queue and a subagent's channel are, and lives for the life of the process.
func peerInbox(conversationSessionID string) chan struct{} {
	key := strings.TrimSpace(conversationSessionID)
	peerInboxMu.Lock()
	defer peerInboxMu.Unlock()
	inbox := peerInboxes[key]
	if inbox == nil {
		inbox = make(chan struct{}, 1)
		peerInboxes[key] = inbox
	}
	return inbox
}

// signalPeerMessage posts an arrival to the conversation. A signal nobody is
// waiting for is kept until the next wait reads past it, so posting never
// blocks the agent that just spoke.
func signalPeerMessage(conversationSessionID string) {
	inbox := peerInbox(conversationSessionID)
	select {
	case inbox <- struct{}{}:
	default:
	}
}

// subagentWaitResult is one wait's answer: the subagent's record, whether the
// wait ran out of time, and whether a message from a subagent ended it early.
type subagentWaitResult struct {
	record   agent.HistoryEntry
	timedOut bool
	messaged bool
}

// awaitSubagent blocks until the subagent finishes, one of the conversation's
// subagents writes to it, or the wait's own deadline passes. The message that
// ends the wait early is already steered into the conversation — it reaches
// the dispatching model at its next tool boundary — so the dispatcher stops
// sleeping through its own subagent's report without losing what it said.
func awaitSubagent(handle *agent.Handle, waitCtx context.Context, conversationSessionID string) subagentWaitResult {
	inbox := peerInbox(conversationSessionID)
	// An arrival from before this wait was delivered into the turn that is
	// asking; only what arrives from here on ends the block.
	select {
	case <-inbox:
	default:
	}
	done := make(chan subagentWaitResult, 1)
	go func() {
		record, err := handle.Wait(waitCtx)
		done <- subagentWaitResult{record: record, timedOut: err != nil}
	}()
	select {
	case result := <-done:
		return result
	case <-inbox:
		return subagentWaitResult{record: handle.Snapshot(), messaged: true}
	}
}
