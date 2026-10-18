// Startup confirmation for project-level MCP servers.
//
// The effective MCP list is frozen for the session, so startup — right after
// the workspace-trust prompt — is the one moment the operator can be asked
// about project entries. An answer of N, or a bare Enter, records a decline:
// the entry stays out of the session, and it is not asked about again until it
// is allowed on the project page's MCP tab or its configuration changes. An
// entry whose prompt was interrupted before an answer was read is not recorded
// at all, so the next start asks about it again.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func ensureProjectMCPConsent(in io.Reader, out io.Writer, home, cwd string) error {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	launch, err := process.ResolveConsentProjectContext(strings.TrimSpace(home), strings.TrimSpace(cwd))
	if err != nil {
		// A trust-lookup failure must not become a license to load project
		// entries: ResolveSessionMCP fails closed on the same context, so
		// skipping the prompt and continuing is the consistent choice.
		return nil
	}
	if safety.TrustedRoot(launch) == "" {
		return nil
	}
	workspace, err := process.ActiveAgentWorkspace()
	if err != nil {
		return nil
	}
	pending := process.PendingProjectMCPConsents(workspace, launch)
	if len(pending) == 0 {
		return nil
	}
	// The answer is typed at the caret, which the launch keeps hidden
	// otherwise (see launchCaret).
	_, _ = io.WriteString(out, showCaretSeq)
	defer func() { _, _ = io.WriteString(out, hideCaretSeq) }()
	_, _ = fmt.Fprintln(out, "")
	_, _ = fmt.Fprintln(out, "This project declares MCP servers. Confirm each one before it runs in this session.")
	var allowed []string
	reader := bufio.NewReader(in)
	for _, entry := range pending {
		_, _ = fmt.Fprintf(out, "\n%s\n", entry.Summary)
		_, _ = fmt.Fprintf(out, "Run it? [y/N] ")
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			allowed = append(allowed, entry.Name)
			_, _ = fmt.Fprintln(out, "  confirmed — it loads this session")
		default:
			_, _ = fmt.Fprintln(out, "  declined — it won't load; allow it later on the project page's MCP tab")
		}
	}
	_, _ = fmt.Fprintln(out, "")
	return process.DecideProjectMCPConsents(workspace, launch, allowed)
}

// ensureProjectLSPConsent is the language-server twin of the MCP prompt: the
// project file's entries apply only entry by entry, and startup is the moment
// to ask about them. An answer of N, or a bare Enter, records a decline: the
// entry does not apply, and it is not asked about again until it is allowed on
// the project page's LSP tab or its configuration changes.
func ensureProjectLSPConsent(in io.Reader, out io.Writer, home, cwd string) error {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	launch, err := process.ResolveConsentProjectContext(strings.TrimSpace(home), strings.TrimSpace(cwd))
	if err != nil {
		// A trust-lookup failure must not become a license to apply
		// project entries: the runtime fails closed on the same context,
		// so skipping the prompt and continuing is the consistent choice.
		return nil
	}
	if safety.TrustedRoot(launch) == "" {
		return nil
	}
	workspace, err := process.ActiveAgentWorkspace()
	if err != nil {
		return nil
	}
	pending := process.PendingProjectLSPConsents(workspace, launch)
	if len(pending) == 0 {
		return nil
	}
	// The answer is typed at the caret, which the launch keeps hidden
	// otherwise (see launchCaret).
	_, _ = io.WriteString(out, showCaretSeq)
	defer func() { _, _ = io.WriteString(out, hideCaretSeq) }()
	_, _ = fmt.Fprintln(out, "")
	_, _ = fmt.Fprintln(out, "This project configures language servers. Confirm each one before it runs in this session.")
	var allowed []string
	reader := bufio.NewReader(in)
	for _, entry := range pending {
		_, _ = fmt.Fprintf(out, "\n%s\n", entry.Summary)
		_, _ = fmt.Fprintf(out, "Allow it? [y/N] ")
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			allowed = append(allowed, entry.ID)
			_, _ = fmt.Fprintln(out, "  confirmed — it applies from now on")
		default:
			_, _ = fmt.Fprintln(out, "  declined — it won't apply; allow it later on the project page's LSP tab")
		}
	}
	_, _ = fmt.Fprintln(out, "")
	return process.DecideProjectLSPConsents(workspace, launch, allowed)
}
