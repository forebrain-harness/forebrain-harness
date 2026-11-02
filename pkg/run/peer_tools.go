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
	"time"

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

// PeerDirectory is the tools' port onto the agents in other processes on this
// machine. The composition root implements it and hands it to the runner as
// Deps.Peers; nil means this machine has no peer messaging at all, which is the
// one thing that decides whether the two tools are registered. This package
// never imports the peer protocol or the process package: everything the two
// tools need from them arrives through this interface.
type PeerDirectory interface {
	// List describes the agents in other processes the caller can message.
	List(ctx context.Context, from PeerCaller) (PeerListing, error)
	// Send delivers message to the agent `to` names: a name as List printed
	// it, or a peer address.
	Send(ctx context.Context, from PeerCaller, to, message string) (PeerSendResult, error)
	// The same-tree guards (plan 198) are the tools' own port, declared beside
	// the tools: the file and shell tools ask the same questions here, and the
	// one implementation satisfies both interfaces.
	tool.PeerWorkTree
}

// PeerCaller is who is speaking, in the terms the other process needs to
// address the answer back: the conversation a receipt belongs to, and the
// roster key when the speaker is one of that conversation's subagents.
type PeerCaller struct {
	SessionID string
	AgentKey  string
}

// PeerListing is what peer_list reports besides the caller's own agents: the
// agents reachable in other processes, the ones in the caller's own directory
// tree first.
type PeerListing struct {
	Peers []PeerEntry
}

// PeerEntry is one agent of another process, as peer_list prints it. Ref is
// empty unless two reachable agents share the same name, in which case the name
// alone is ambiguous and peer_send needs the [ref] beside it.
type PeerEntry struct {
	Name      string
	Ref       string
	Kind      string
	Status    string
	Task      string
	Cwd       string
	SameTree  bool
	ActiveAgo time.Duration
}

// PeerSendResult is what a cross-process send answers: the recipient as
// peer_list printed it, and the status the recipient reported for itself.
type PeerSendResult struct {
	Name   string
	Status string
}

// peerSendInput is what the model addresses a message with.
type peerSendInput struct {
	To      string `json:"to" jsonschema_description:"The agent's name exactly as peer_list printed it"`
	Message string `json:"message" jsonschema_description:"The message, self-contained: the recipient sees nothing else you wrote"`
}

// peerAgent is one row of a roster: who can be reached, and what it is doing.
// The last five fields describe an agent of another process: which kind of
// session it belongs to, where it works, whether that is this agent's own
// directory tree, and how long ago it last did anything.
type peerAgent struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Task      string `json:"task,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	SameTree  bool   `json:"same_tree,omitempty"`
	ActiveAgo string `json:"active_ago,omitempty"`
}

// peerListAnswer is peer_list's answer: the caller's own name and everyone it
// can address. Note is the one line an answer with nobody in it carries.
type peerListAnswer struct {
	Self   string      `json:"self"`
	Agents []peerAgent `json:"agents"`
	Note   string      `json:"note,omitempty"`
}

// peerSendAnswer is peer_send's answer: where the message went. To and
// PeerStatus are set for a message that left this process, naming the recipient
// as peer_list printed it and the status it reported for itself.
type peerSendAnswer struct {
	Status     string `json:"status"`
	To         string `json:"to,omitempty"`
	PeerStatus string `json:"peer_status,omitempty"`
	Hint       string `json:"hint,omitempty"`
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
// dispatcher and to nobody else. Both then see the agents of other processes,
// which is what Deps.Peers adds: a batch task sees none of those, because it
// talks to its own agents and to nobody else.
func peerList(ctx context.Context, fac Factory) (string, error) {
	conversationSessionID := strings.TrimSpace(tool.ConversationSessionIDFromContext(ctx))
	var answer peerListAnswer
	if tool.IsSubagentRunFromContext(ctx) {
		answer = peerListAnswer{
			Self: strings.TrimSpace(tool.HookAgentIDFromContext(ctx)),
			Agents: []peerAgent{{
				Name:   peerParentName,
				Kind:   "primary",
				Status: peerDispatcherStatus(fac, conversationSessionID),
			}},
		}
	} else {
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
		answer = peerListAnswer{Self: peerParentName, Agents: agents}
	}
	peers, err := peerListOtherProcesses(ctx, fac)
	if err != nil {
		return "", err
	}
	answer.Agents = append(answer.Agents, peers...)
	if len(answer.Agents) == 0 {
		answer.Note = "No other agents are reachable right now."
	}
	b, _ := json.Marshal(answer)
	return string(b), nil
}

// peerListOtherProcesses asks the port for the agents in other processes. A
// conversation that reaches only its own agents — a batch task — has none to
// add, and a conversation with no port has nowhere to ask.
func peerListOtherProcesses(ctx context.Context, fac Factory) ([]peerAgent, error) {
	if fac.peerTargetsStayInConversation() {
		return nil, nil
	}
	port := fac.peerDirectory()
	if port == nil {
		return nil, nil
	}
	listing, err := port.List(ctx, peerCallerFromContext(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]peerAgent, 0, len(listing.Peers))
	for _, peer := range listing.Peers {
		out = append(out, peerAgent{
			Name:      peer.Name,
			Kind:      peer.Kind,
			Status:    peer.Status,
			Task:      truncateRunes(peer.Task, peerTaskPreviewRunes),
			Ref:       peer.Ref,
			Cwd:       peer.Cwd,
			SameTree:  peer.SameTree,
			ActiveAgo: peerActiveAgo(peer.ActiveAgo),
		})
	}
	return out, nil
}

// peerCallerFromContext is who is speaking, for the port: the conversation and,
// inside a subagent run, its roster key.
func peerCallerFromContext(ctx context.Context) PeerCaller {
	caller := PeerCaller{SessionID: strings.TrimSpace(tool.ConversationSessionIDFromContext(ctx))}
	if tool.IsSubagentRunFromContext(ctx) {
		caller.AgentKey = strings.TrimSpace(tool.HookAgentIDFromContext(ctx))
	}
	return caller
}

// peerActiveAgo is how long ago another process's agent last did anything, in
// the coarse unit a listing row prints. Zero means the peer did not say.
func peerActiveAgo(ago time.Duration) string {
	if ago <= 0 {
		return ""
	}
	switch {
	case ago < time.Minute:
		return "just now"
	case ago < time.Hour:
		return fmt.Sprintf("%dm", int(ago.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(ago.Hours()))
	}
}

// peerSend delivers one message: to a running subagent when the dispatching
// agent speaks, to the dispatching agent when a subagent answers, and to an
// agent of another process when the name belongs to one of those. The text the
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
		if strings.EqualFold(to, peerParentName) {
			return peerSendToDispatcher(fac, conversationSessionID, text), nil
		}
		return peerSendToOtherProcess(ctx, fac, to, message)
	}
	answer, err := peerSendToSubagent(fac, conversationSessionID, to, text)
	if errors.Is(err, errPeerNoSuchAgentInConversation) {
		return peerSendToOtherProcess(ctx, fac, to, message)
	}
	return answer, err
}

// peerSendToOtherProcess hands the message to the port when the name belongs to
// an agent of another process. A conversation that reaches only its own agents
// — a batch task — refuses the target here rather than sending it anywhere: the
// task's own agents are the only ones it can talk to, whoever else is running.
func peerSendToOtherProcess(ctx context.Context, fac Factory, to, message string) (string, error) {
	if fac.peerTargetsStayInConversation() {
		return "", fmt.Errorf("no agent named %q: batch tasks talk only to their own agents", to)
	}
	port := fac.peerDirectory()
	if port == nil {
		return "", fmt.Errorf("no agent named %q; call peer_list", to)
	}
	result, err := port.Send(ctx, peerCallerFromContext(ctx), to, message)
	if err != nil {
		return "", err
	}
	return peerAnswerJSON(peerSendAnswer{
		Status:     "delivered",
		To:         result.Name,
		PeerStatus: result.Status,
	}), nil
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
// and what it does next is the dispatcher's decision. A name the conversation's
// ledger does not hold answers errPeerNoSuchAgentInConversation, which is the
// caller's cue to look for the agent in another process.
func peerSendToSubagent(fac Factory, conversationSessionID, to, text string) (string, error) {
	ch, _, err := subagentChannelForConversation(fac.Owner, conversationSessionID, to)
	if errors.Is(err, ErrSubagentNotFound) {
		return "", errPeerNoSuchAgentInConversation
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

// errPeerNoSuchAgentInConversation says the name is not one of this
// conversation's own subagents: the caller may still find it in another
// process, so it is a lookup that failed, not an answer for the model.
var errPeerNoSuchAgentInConversation = errors.New("peer: not an agent of this conversation")

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
