// The same-tree guards (plan 198): the identity ledger that says which agent
// wrote which file, and the three questions the file and shell tools ask of it.
//
// The ledger is machine-wide and shared by every forebrain of this user, the
// way the tool-writes ledger (plan 186) is: two processes with different
// FOREBRAIN_HOME values still write the same work tree, so what one wrote must
// be visible to the other. It is a separate directory all the same, because a
// reader of the older ledger parses two columns and would choke on four.
//
// A line is <unixNano>\t<quoted path>\t<quoted agent address>\t<quoted name>,
// where the address is "<process uds address>#<agent id>". One line appended
// with O_APPEND is atomic, one shard per minute is never appended to again, and
// anything that fails is silence: these are guards, and a guard that cannot read
// its ledger falls back to the behaviour without one.
//
// Nothing here runs a command or reads a repository. The ledger, this process's
// own registration and its peers' records are the whole of the evidence
// (design §7.1, plan 198 §8).
package process

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

const (
	// agentTouchRetention is how long a write is remembered: the window the
	// same-tree guards report within.
	agentTouchRetention = 30 * time.Minute
	// agentTouchSweepEvery keeps each process from scanning the directory more
	// than once a minute.
	agentTouchSweepEvery = time.Minute
	// agentTouchShardSuffix names one minute of writes.
	agentTouchShardSuffix = ".log"
	// agentTouchDirEnv overrides agentTouchLedgerDir. Tests install it so a run
	// neither writes to nor reads from the machine-wide directory.
	agentTouchDirEnv = "FOREBRAIN_AGENT_TOUCHES_DIR"
	// peerWorkTreeRecentFiles is how many paths one peer's summary lists.
	peerWorkTreeRecentFiles = 8
)

// agentTouchLedgerDir is the directory every forebrain of this user shares,
// whatever FOREBRAIN_HOME each was started with. An unisolated test binary
// writes nothing: a case that forgot the override must not append to the
// developer's own ledger (the guard peer.Enabled uses for its directories).
func agentTouchLedgerDir() (string, bool) {
	if override := strings.TrimSpace(os.Getenv(agentTouchDirEnv)); override != "" {
		return filepath.Clean(override), true
	}
	if testing.Testing() {
		return "", false
	}
	cache, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cache) == "" {
		return "", false
	}
	return filepath.Join(cache, "forebrain", "agent-touches"), true
}

func agentTouchShardName(minute int64) string {
	return strconv.FormatInt(minute, 10) + agentTouchShardSuffix
}

// agentTouchShardMinute reads the minute a shard file names; anything else in
// the directory is not ours to interpret.
func agentTouchShardMinute(name string) (int64, bool) {
	digits, ok := strings.CutSuffix(name, agentTouchShardSuffix)
	if !ok {
		return 0, false
	}
	minute, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return minute, true
}

// agentTouch is one line of the ledger: who wrote what, and when.
type agentTouch struct {
	at      time.Time
	path    string
	addr    string // the process address the agent wrote from
	agentID string
	name    string
}

// agentAddress is the ledger's own spelling of an agent: the address its process
// answers on, and which of that process's agents wrote.
func agentAddress(addr, agentID string) string {
	return strings.TrimSpace(addr) + "#" + strings.TrimSpace(agentID)
}

// splitAgentAddress undoes agentAddress. No wire address contains '#'
// (peer.addressByteAllowed), so the first one is the separator.
func splitAgentAddress(spelled string) (addr, agentID string, ok bool) {
	addr, agentID, ok = strings.Cut(spelled, "#")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(addr), strings.TrimSpace(agentID), true
}

// appendAgentTouch adds one line to the shard for at's minute, then sweeps
// expired shards. Quoting keeps a path that holds a newline or a tab from
// breaking the line structure, and a reader that catches a torn line ignores it.
func appendAgentTouch(dir string, touch agentTouch, at time.Time) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	line := strconv.FormatInt(at.UnixNano(), 10) + "\t" +
		strconv.Quote(touch.path) + "\t" +
		strconv.Quote(agentAddress(touch.addr, touch.agentID)) + "\t" +
		strconv.Quote(touch.name) + "\n"
	f, err := os.OpenFile(filepath.Join(dir, agentTouchShardName(at.Unix()/60)),
		os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	sweepAgentTouchesOnce(dir, at)
}

// The sweep is rate-limited per process: a write every few hundred milliseconds
// must not cost a directory scan every time.
var (
	agentTouchSweepMu sync.Mutex
	agentTouchSweptAt time.Time
)

func sweepAgentTouchesOnce(dir string, now time.Time) {
	agentTouchSweepMu.Lock()
	if !agentTouchSweptAt.IsZero() && now.Sub(agentTouchSweptAt) < agentTouchSweepEvery {
		agentTouchSweepMu.Unlock()
		return
	}
	agentTouchSweptAt = now
	agentTouchSweepMu.Unlock()
	sweepAgentTouches(dir, now)
}

// sweepAgentTouches deletes every shard whose minute is past the retention
// window, keeping a minute of slack so a reader that started just before the
// boundary still finds the shards its window covers.
func sweepAgentTouches(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-agentTouchRetention-time.Minute).Unix() / 60
	for _, entry := range entries {
		minute, ok := agentTouchShardMinute(entry.Name())
		if !ok || minute > cutoff {
			continue
		}
		os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// agentTouchesBetween reads every write recorded in (from, to], oldest first.
func agentTouchesBetween(from, to time.Time) []agentTouch {
	dir, ok := agentTouchLedgerDir()
	if !ok {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	first, last := from.Unix()/60, to.Unix()/60
	var out []agentTouch
	for _, entry := range entries {
		minute, ok := agentTouchShardMinute(entry.Name())
		if !ok || minute < first || minute > last {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			touch, ok := parseAgentTouch(line)
			if !ok {
				continue
			}
			if touch.at.After(from) && !touch.at.After(to) {
				out = append(out, touch)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.After(out[j].at) })
	return out
}

// parseAgentTouch reads one ledger line. A line that is torn, or that an older
// or newer writer spelled differently, is not ours to interpret.
func parseAgentTouch(line string) (agentTouch, bool) {
	var touch agentTouch
	parts := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
	if len(parts) != 4 {
		return touch, false
	}
	nano, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return touch, false
	}
	path, err := strconv.Unquote(parts[1])
	if err != nil || strings.TrimSpace(path) == "" {
		return touch, false
	}
	addr, agentID, ok := splitAgentAddress(unquoteOr(parts[2]))
	if !ok || agentID == "" {
		return touch, false
	}
	touch = agentTouch{at: time.Unix(0, nano), path: filepath.Clean(path), addr: addr, agentID: agentID, name: unquoteOr(parts[3])}
	return touch, true
}

// unquoteOr reads one quoted column, standing in the raw text when the quoting
// is broken: a name that lost its quotes still says more than no name at all.
func unquoteOr(column string) string {
	if text, err := strconv.Unquote(column); err == nil {
		return strings.TrimSpace(text)
	}
	return strings.Trim(strings.TrimSpace(column), `"`)
}

// resolveTouchPath is the path a ledger line holds: absolute, with links
// resolved the way the root it is compared against resolved. A path that no
// longer exists — a delete, a rename, a file that is about to appear — still has
// a directory that does, so that directory is what gets resolved; comparing a
// resolved root against an unresolved path would hide every write to a file that
// is not on disk at the moment the guard asks.
func resolveTouchPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	dir, base := filepath.Split(path)
	if dir != "" && base != "" {
		if resolved, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
			return filepath.Join(resolved, base)
		}
	}
	return path
}

// pathUnderRoot reports whether path is root or inside it, both already
// resolved.
func pathUnderRoot(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// workTree is one agent's directory as the peer guards see it: root and
// everything inside it, except a linked git worktree that starts below root.
// Such a worktree — one under the repository's .worktrees, where agents create
// them — is a checkout of its own, so an agent there and one in the main
// checkout never edit the same files (owner 2026-11-09). A submodule stays part
// of the tree that contains it. The linked-worktree answers are memoized per
// directory, so filtering a whole ledger costs one check per directory.
type workTree struct {
	root   string
	linked map[string]bool
}

// newWorkTree is the work tree at root, already resolved.
func newWorkTree(root string) *workTree {
	return &workTree{root: root, linked: map[string]bool{}}
}

// contains reports whether path, already resolved, is in the tree: under root
// with no linked worktree top between them. root itself may be a linked
// worktree — that is the tree.
func (w *workTree) contains(path string) bool {
	if !pathUnderRoot(w.root, path) {
		return false
	}
	for dir := path; dir != w.root && len(dir) > len(w.root); dir = filepath.Dir(dir) {
		linked, seen := w.linked[dir]
		if !seen {
			linked = tool.IsLinkedWorktreeTop(dir)
			w.linked[dir] = linked
		}
		if linked {
			return false
		}
	}
	return true
}

// guardIdentity is who is asking: the address this process answers on, the agent
// inside it, and the name to print. The address is read from the running
// registration, which remembers every address this process has answered on — so
// a write made before a registration mode switch moved the socket is still
// recognised as this process's own.
func (o *peerOutbound) guardIdentity(ctx context.Context) (addr, agentID, name string) {
	caller := run.PeerCaller{
		SessionID: tool.ConversationSessionIDFromContext(ctx),
		AgentKey:  subagentKeyFromContext(ctx),
	}
	agentID = caller.SessionID
	if caller.AgentKey != "" {
		agentID = agentID + "/" + caller.AgentKey
	}
	svc := o.service()
	if svc == nil {
		return "", agentID, ""
	}
	svc.mu.Lock()
	addr = svc.addr
	name = svc.name
	svc.mu.Unlock()
	// The name the recipient would read: the extension block's own entry for the
	// calling agent when this process lists one.
	if view, err := o.view(caller.SessionID); err == nil {
		if senderName, ok := view.agentName(agentID); ok {
			name = senderName
		}
	}
	return addr, agentID, name
}

// ownAddress reports whether addr is one this process has answered on.
func (o *peerOutbound) ownAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	svc := o.service()
	if addr == "" || svc == nil {
		return false
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return svc.ownAddresses[addr]
}

// isOwnTouch reports whether a ledger line is this process's own agent writing:
// the same agent id, from an address this process answered on. A write from
// before a mode switch moved the socket is still ours.
func (o *peerOutbound) isOwnTouch(touch agentTouch, agentID string) bool {
	if agentID == "" || touch.agentID != agentID {
		return false
	}
	return o.ownAddress(touch.addr)
}

// NoteWrite records that the calling agent's file tool wrote absPath. A process
// that is not registered right now writes nothing: without an address the line
// could not be attributed to anybody, and this process would later read its own
// write back as another agent's.
func (o *peerOutbound) NoteWrite(ctx context.Context, absPath string, at time.Time) {
	if o == nil {
		return
	}
	addr, agentID, name := o.guardIdentity(ctx)
	if addr == "" || agentID == "" {
		return
	}
	path := resolveTouchPath(absPath)
	if path == "" {
		return
	}
	dir, ok := agentTouchLedgerDir()
	if !ok {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	appendAgentTouch(dir, agentTouch{path: path, addr: addr, agentID: agentID, name: name}, at)
}

// TouchesBetween answers with the other agents' writes under root in (from, to],
// newest first. The calling agent's own writes are not among them, whatever
// address it wrote from.
func (o *peerOutbound) TouchesBetween(ctx context.Context, root string, from, to time.Time) []tool.PeerTouch {
	if o == nil {
		return nil
	}
	_, agentID, _ := o.guardIdentity(ctx)
	resolvedRoot := resolvePath(root)
	if resolvedRoot == "" {
		return nil
	}
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() || !to.After(from) {
		return nil
	}
	tasks := o.peerTasksByAddress()
	touches := agentTouchesBetween(from, to)
	tree := newWorkTree(resolvedRoot)
	out := make([]tool.PeerTouch, 0, len(touches))
	for _, touch := range touches {
		if o.isOwnTouch(touch, agentID) || !tree.contains(touch.path) {
			continue
		}
		out = append(out, tool.PeerTouch{
			Path:      relativeToRoot(resolvedRoot, touch.path),
			AgentName: touch.name,
			Task:      tasks[agentAddress(touch.addr, touch.agentID)],
			At:        touch.at,
		})
	}
	return out
}

// PeerFileTouch is one file-tool write under a root, as the web's "who is
// editing this directory" panel reads it (plan 200 §3): the path relative to
// the root, which agent wrote it, and when. AgentID is the forebrain agent id
// the ledger recorded, so a surface can tell its own conversation's writes from
// another process's; the surface decides what "its own" means, because the
// ledger only knows processes.
type PeerFileTouch struct {
	Path      string
	AgentName string
	AgentID   string
	At        time.Time
}

// RecentPeerFileTouches lists every file-tool write under root inside the
// ledger's retention window, newest first — this process's own agents included,
// which is what distinguishes it from TouchesBetween (plan 200 §3: the panel
// shows who is editing a file, and "who" includes the reader). A process that
// is not registered as a peer has nothing to attribute its own writes to, so a
// write of its own reads as another agent's; the panel is a view, never a guard.
func (env *Environment) RecentPeerFileTouches(root string) []PeerFileTouch {
	if env == nil {
		return nil
	}
	resolvedRoot := resolvePath(root)
	if resolvedRoot == "" {
		return nil
	}
	now := time.Now()
	touches := agentTouchesBetween(now.Add(-agentTouchRetention), now)
	tree := newWorkTree(resolvedRoot)
	list := make([]PeerFileTouch, 0, len(touches))
	for _, touch := range touches {
		if !tree.contains(touch.path) {
			continue
		}
		list = append(list, PeerFileTouch{
			Path:      relativeToRoot(resolvedRoot, touch.path),
			AgentName: touch.name,
			AgentID:   touch.agentID,
			At:        touch.at,
		})
	}
	return list
}

// WorkTreePeers lists the other live agents whose directory overlaps root, with
// the files each wrote recently. A peer this process can reach but the ledger
// never saw — a Claude Code session, or one that has not written a file yet —
// still appears, because it is in the tree; it just has nothing to report.
func (o *peerOutbound) WorkTreePeers(ctx context.Context, root string) []tool.WorkTreePeer {
	return o.peersUnder(ctx, root)
}

// peersUnder answers the same-tree question: path containment, design §2.2.
func (o *peerOutbound) peersUnder(ctx context.Context, root string) []tool.WorkTreePeer {
	if o == nil {
		return nil
	}
	resolvedRoot := resolvePath(root)
	if resolvedRoot == "" {
		return nil
	}
	caller := run.PeerCaller{
		SessionID: tool.ConversationSessionIDFromContext(ctx),
		AgentKey:  subagentKeyFromContext(ctx),
	}
	view, err := o.view(caller.SessionID)
	if err != nil {
		// This process's registration is not what makes the tree shared: the
		// rendezvous directory is readable without it, so a service that never
		// started still answers (plan 237 §2).
		o.warnUnavailableOnce(err)
		return o.workTreePeersFromDirectory(resolvedRoot)
	}
	self := view.selfID(caller)
	recent := o.recentFilesByAddress(resolvedRoot, time.Now())
	out := make([]tool.WorkTreePeer, 0, 4)
	for _, candidate := range view.candidates() {
		if candidate.agentID != "" && candidate.agentID == self {
			continue
		}
		files, _ := recent[agentAddress(peer.EncodeUDS(candidate.socket), candidate.agentID)]
		if !sameWorkTree(candidate.cwd, resolvedRoot) {
			continue
		}
		out = append(out, tool.WorkTreePeer{
			Name:        candidate.name,
			Kind:        candidate.kind,
			Status:      candidate.status,
			Task:        candidate.task,
			RecentFiles: files.paths,
			MoreFiles:   files.more,
		})
	}
	return out
}

// workTreePeersFromDirectory answers "who else is in this tree" from the
// rendezvous directory alone, for a process whose peer service is not running.
// Any process of this user can read that directory — it is how the candidate
// list is built — so the guard sees the truth even when this process never
// registered. Only the filesystem is read: a guard may not dial another
// process's socket to decide whether to ask the person (plan 237 §2).
func (o *peerOutbound) workTreePeersFromDirectory(resolvedRoot string) []tool.WorkTreePeer {
	cc := false
	if o.env != nil {
		// The same decision targetMode makes: the interop switch on and Claude
		// Code detected. The other mode's directory is not read — two toggles
		// that disagree cannot see each other.
		cc = o.env.peersConfigSnapshot().enabled() && detectClaudeCode()
	}
	selfPID := os.Getpid()
	selfStart, _ := peer.ProcStart(selfPID)
	recent := o.recentFilesByAddress(resolvedRoot, time.Now())
	out := make([]tool.WorkTreePeer, 0, 4)
	for _, rec := range peer.ReadRecords(peer.NewLayout(cc).Dir) {
		if rec.PID == selfPID && rec.ProcStart == selfStart {
			continue // this process's own record
		}
		if !recordLiveWithoutSocket(rec) || !sameWorkTree(rec.Cwd, resolvedRoot) {
			continue
		}
		if rec.Forebrain == nil {
			// A record without the extension block is not a forebrain
			// process, and only the Claude Code directory holds those.
			if !cc {
				continue
			}
			name := strings.TrimSpace(rec.Name)
			if name == "" {
				name = peerSlug(filepath.Base(strings.TrimSpace(rec.Cwd)))
			}
			out = append(out, tool.WorkTreePeer{Name: name, Kind: peerKindAgentSess, Status: rec.Status})
			continue
		}
		surface := strings.TrimSpace(rec.Forebrain.Surface)
		for _, agent := range rec.Forebrain.Agents {
			cwd := strings.TrimSpace(agent.Root)
			if cwd == "" {
				cwd = rec.Cwd
			}
			if !sameWorkTree(cwd, resolvedRoot) {
				continue
			}
			files := recent[agentAddress(peer.EncodeUDS(rec.MessagingSocketPath), strings.TrimSpace(agent.ID))]
			out = append(out, tool.WorkTreePeer{
				Name:        strings.TrimSpace(agent.Name),
				Kind:        peerKindOf(surface, agent),
				Status:      strings.TrimSpace(agent.Status),
				Task:        strings.TrimSpace(agent.Task),
				RecentFiles: files.paths,
				MoreFiles:   files.more,
			})
		}
	}
	return out
}

// recordLiveWithoutSocket answers the only question the fallback needs —
// does the process still exist — without dialling anything.
func recordLiveWithoutSocket(r peer.Record) bool {
	if r.PIDDomain != "" && r.PIDDomain != peer.PIDDomain() {
		return true // another pid domain: take its pid at its word
	}
	if r.ProcStart == "" {
		return true
	}
	current, ok := peer.ProcStart(r.PID)
	return ok && current == r.ProcStart
}

// peerTasksByAddress maps each reachable peer agent's ledger address to the task
// it says it is working on, so a hint can name what the other agent is doing.
func (o *peerOutbound) peerTasksByAddress() map[string]string {
	out := map[string]string{}
	view, err := o.view("")
	if err != nil {
		return out
	}
	for _, candidate := range view.candidates() {
		if candidate.agentID == "" || candidate.task == "" {
			continue
		}
		out[agentAddress(peer.EncodeUDS(candidate.socket), candidate.agentID)] = candidate.task
	}
	return out
}

// peerRecentFiles is one agent's recent writes under a root: the newest ones it
// names, and how many the cap leaves out.
type peerRecentFiles struct {
	paths []string
	more  int
}

// recentFilesByAddress collects, for every agent that wrote under root inside
// the retention window, the paths it wrote, newest first, at most
// peerWorkTreeRecentFiles of them, plus how many the cap leaves out.
func (o *peerOutbound) recentFilesByAddress(root string, now time.Time) map[string]peerRecentFiles {
	paths := map[string][]string{}
	seen := map[string]map[string]bool{}
	tree := newWorkTree(root)
	for _, touch := range agentTouchesBetween(now.Add(-agentTouchRetention), now) {
		if !tree.contains(touch.path) {
			continue
		}
		key := agentAddress(touch.addr, touch.agentID)
		if seen[key] == nil {
			seen[key] = map[string]bool{}
		}
		relative := relativeToRoot(root, touch.path)
		if seen[key][relative] {
			continue
		}
		seen[key][relative] = true
		paths[key] = append(paths[key], relative)
	}
	out := make(map[string]peerRecentFiles, len(paths))
	for key, list := range paths {
		entry := peerRecentFiles{paths: list}
		if len(list) > peerWorkTreeRecentFiles {
			entry = peerRecentFiles{paths: list[:peerWorkTreeRecentFiles], more: len(list) - peerWorkTreeRecentFiles}
		}
		out[key] = entry
	}
	return out
}

// relativeToRoot is path as root sees it: the spelling the guards print, so the
// model reads a project path rather than a home directory's.
func relativeToRoot(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}

// subagentKeyFromContext is the roster key a subagent run answers to.
func subagentKeyFromContext(ctx context.Context) string {
	if !tool.IsSubagentRunFromContext(ctx) {
		return ""
	}
	return tool.HookAgentIDFromContext(ctx)
}

// agentName is the name this process's extension block gives one of its own
// agents, for a ledger line that has to name its writer the way peers read it.
func (v *peerOutboundView) agentName(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false
	}
	for _, agent := range v.agents {
		if strings.TrimSpace(agent.ID) == id {
			return strings.TrimSpace(agent.Name), true
		}
	}
	return "", false
}
