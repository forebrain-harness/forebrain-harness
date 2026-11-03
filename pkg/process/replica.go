// Replica registration (plan 213 §3.1, design §2.7.1): what a process records
// about itself in fb_run_owners so that the other replicas sharing its database
// can tell whether a running conversation belongs here and, when it does not,
// where to forward to.
package process

import (
	"log/slog"
	"net"
	"os"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// replicaRegistration resolves the kind and the address this process records.
// kind is the explicit OpenOptions value when the surface sets one; otherwise
// it follows the session source, because the terminal and the gateway are the
// only two processes that open one. A terminal serves no internal endpoint, so
// its address is empty no matter what the configuration says.
func replicaRegistration(kind, source string, cfg appcfg.Root) (string, string) {
	if resolved := strings.TrimSpace(kind); resolved != "" {
		if resolved == state.RunOwnerKindTerminal {
			return resolved, ""
		}
		return resolved, gatewayInternalAddr(cfg)
	}
	if source == memory.SessionSourceTUI {
		return state.RunOwnerKindTerminal, ""
	}
	return state.RunOwnerKindGateway, gatewayInternalAddr(cfg)
}

// gatewayInternalAddr is the address the other replicas reach this gateway at:
// FOREBRAIN_INTERNAL_ADDR when the deployment sets it — a Kubernetes pod's own
// IP, injected through the downward API — and otherwise the gateway's listen
// address with a wildcard host replaced by this machine's first non-loopback
// IPv4. An address the other replicas cannot use is worse than none, so when no
// such address can be found the column is left empty and the deployment is
// told once.
func gatewayInternalAddr(cfg appcfg.Root) string {
	if v := strings.TrimSpace(os.Getenv("FOREBRAIN_INTERNAL_ADDR")); v != "" {
		return v
	}
	listen := strings.TrimSpace(cfg.Gateway.HTTPAddr)
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		slog.Warn("cannot derive the replica internal address from gateway.http_addr; set FOREBRAIN_INTERNAL_ADDR",
			"http_addr", listen, "err", err)
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		resolved, ok := firstNonLoopbackIPv4()
		if !ok {
			slog.Warn("cannot derive the replica internal address: this host has no non-loopback IPv4; set FOREBRAIN_INTERNAL_ADDR")
			return ""
		}
		host = resolved
	}
	if port == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(host, port)
}

// firstNonLoopbackIPv4 is the first non-loopback IPv4 address on an interface,
// which is the address another host reaches this one at.
func firstNonLoopbackIPv4() (string, bool) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", false
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		return ipNet.IP.String(), true
	}
	return "", false
}

// replicaHost is the name this machine is known by, which is what tells one
// host's replicas from another's when they share a database.
func replicaHost() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}
