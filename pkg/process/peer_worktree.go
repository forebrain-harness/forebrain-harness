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
	out := make([]tool.PeerTouch, 0, len(touches))
	for _, touch := range touches {
		if o.isOwnTouch(touch, agentID) || !pathUnderRoot(resolvedRoot, touch.path) {
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
	list := make([]PeerFileTouch, 0, len(touches))
	for _, touch := range touches {
		if !pathUnderRoot(resolvedRoot, touch.path) {
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
	return o.peersUnder(ctx, root, false)
}

// SweepPeers lists the other live agents whose uncommitted work a git sweep run
// in root could discard. It asks a narrower question than WorkTreePeers does,
// and deliberately so: path containment (design §2.2) is right for saying who is
// in the same directory tree — the agents of a linked work tree nested in
// another tree do want to know about each other — but it is wrong for a sweep,
// because the nested tree has its own working tree and index. A checkout in
// `repo/.worktrees/232` cannot touch what an agent has in `repo`.
//
// So the answer here is git working tree identity, plus the ledger: a subagent
// registered under the main tree that is actually writing files inside the work
// tree has its work there, and only the ledger can say so.
func (o *peerOutbound) SweepPeers(ctx context.Context, root string) []tool.WorkTreePeer {
	return o.peersUnder(ctx, root, true)
}

// peersUnder answers both same-tree questions from one enumeration: sweep picks
// the git identity rule, everything else the containment rule.
func (o *peerOutbound) peersUnder(ctx context.Context, root string, sweep bool) []tool.WorkTreePeer {
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
		return nil
	}
	self := view.selfID(caller)
	recent := o.recentFilesByAddress(resolvedRoot, time.Now())
	out := make([]tool.WorkTreePeer, 0, 4)
	for _, candidate := range view.candidates() {
		if candidate.agentID != "" && candidate.agentID == self {
			continue
		}
		files, wrote := recent[agentAddress(peer.EncodeUDS(candidate.socket), candidate.agentID)]
		if sweep {
			// Recent writes under root are evidence of work in root wherever
			// the agent registered; without them, only the same git working
			// tree counts.
			if !wrote && !sameGitWorkTree(candidate.cwd, resolvedRoot) {
				continue
			}
		} else if !sameWorkTree(candidate.cwd, resolvedRoot) {
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

// sameGitWorkTree reports whether two directories are one git working tree:
// both sit under the same nearest `.git` entry. That is what tells
// `repo/.worktrees/232` apart from `repo`, which path containment cannot. A path
// outside any repository falls back to containment, so a plain directory still
// behaves as it did — and a sweep is asked about rather than missed.
func sameGitWorkTree(a, b string) bool {
	left, right := resolvePath(a), resolvePath(b)
	if left == "" || right == "" {
		return false
	}
	leftRoot, leftOK := workTreeRoot(left)
	rightRoot, rightOK := workTreeRoot(right)
	if !leftOK || !rightOK {
		return sameWorkTree(left, right)
	}
	return leftRoot == rightRoot
}

// workTreeRoot is the nearest directory at or above dir that holds a `.git`
// entry: the root of the working tree dir belongs to. A linked work tree and a
// submodule spell that entry as a file rather than a directory, which is why
// only its existence counts. Nothing here opens a repository or runs a command,
// per the design's §7.1: the filesystem's names are the whole evidence.
func workTreeRoot(dir string) (string, bool) {
	dir = resolvePath(dir)
	for dir != "" {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	return "", false
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
	for _, touch := range agentTouchesBetween(now.Add(-agentTouchRetention), now) {
		if !pathUnderRoot(root, touch.path) {
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
