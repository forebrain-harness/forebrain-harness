// The managed network proxy: policy, SOCKS, hooks, certificates, and sources.
package safety

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

const (
	networkProxyActiveEnv       = "FOREBRAIN_NETWORK_PROXY_ACTIVE"
	networkAllowLocalBindingEnv = "FOREBRAIN_NETWORK_ALLOW_LOCAL_BINDING"
)

var managedProxyRegistry = struct {
	sync.Mutex
	entries map[string]*managedProxyRegistryEntry
}{entries: map[string]*managedProxyRegistryEntry{}}

type managedProxyRegistryEntry struct {
	proxy *managedNetworkProxy
	refs  int
}

type managedNetworkProxy struct {
	config        appcfg.EffectiveNetworkProxyConfig
	httpListener  net.Listener
	socksListener net.Listener
	httpServer    *http.Server
	ca            *managedCertificateAuthority
	hooks         compiledNetworkHooks
	transport     *http.Transport
	lookupNetIP   func(context.Context, string, string) ([]netip.Addr, error)

	mu         sync.RWMutex
	executions map[string]*managedNetworkExecution
	closed     bool
}

type managedNetworkExecution struct {
	policy        networkCommandPolicy
	approval      NetworkApprovalHandler
	request       NetworkApprovalRequest
	ctx           context.Context
	mu            sync.Mutex
	blocked       *NetworkDenial
	decisions     map[networkApprovalKey]NetworkApprovalDecision
	pending       map[networkApprovalKey]*pendingNetworkApproval
	udpListener   *net.UDPConn
	udpAssociated bool
	cancelOnce    sync.Once
	cancel        context.CancelFunc
}

type networkApprovalKey struct {
	host     string
	protocol NetworkApprovalProtocol
	port     int
}

type pendingNetworkApproval struct {
	done     chan struct{}
	decision NetworkApprovalDecision
	err      error
}

type managedNetworkDialTarget struct {
	execution *managedNetworkExecution
	host      string
	port      int
	protocol  NetworkApprovalProtocol
}

type managedNetworkDialTargetKey struct{}

type managedNetworkLease struct {
	key   string
	proxy *managedNetworkProxy
}

func acquireManagedNetworkProxy(config appcfg.EffectiveNetworkProxyConfig) (*managedNetworkLease, error) {
	key := managedNetworkProxyConfigKey(config)
	managedProxyRegistry.Lock()
	defer managedProxyRegistry.Unlock()
	if entry := managedProxyRegistry.entries[key]; entry != nil {
		entry.refs++
		return &managedNetworkLease{key: key, proxy: entry.proxy}, nil
	}
	proxy, err := startManagedNetworkProxy(config)
	if err != nil {
		return nil, err
	}
	managedProxyRegistry.entries[key] = &managedProxyRegistryEntry{proxy: proxy, refs: 1}
	return &managedNetworkLease{key: key, proxy: proxy}, nil
}

func managedNetworkProxyConfigKey(config appcfg.EffectiveNetworkProxyConfig) string {
	keyBytes, _ := json.Marshal(config)
	return string(keyBytes)
}

func (lease *managedNetworkLease) retain() *managedNetworkLease {
	if lease == nil || lease.proxy == nil {
		return nil
	}
	managedProxyRegistry.Lock()
	defer managedProxyRegistry.Unlock()
	entry := managedProxyRegistry.entries[lease.key]
	if entry == nil || entry.proxy != lease.proxy {
		return nil
	}
	entry.refs++
	return &managedNetworkLease{key: lease.key, proxy: lease.proxy}
}

func (lease *managedNetworkLease) close() {
	if lease == nil || lease.proxy == nil {
		return
	}
	managedProxyRegistry.Lock()
	entry := managedProxyRegistry.entries[lease.key]
	if entry != nil {
		entry.refs--
		if entry.refs <= 0 {
			delete(managedProxyRegistry.entries, lease.key)
			entry.proxy.close()
		}
	}
	managedProxyRegistry.Unlock()
	lease.proxy = nil
}

func startManagedNetworkProxy(config appcfg.EffectiveNetworkProxyConfig) (*managedNetworkProxy, error) {
	if err := validateNetworkDomainPatterns(config.Domains); err != nil {
		return nil, err
	}
	hooks, err := compileNetworkHooks(config.MITMConfig)
	if err != nil {
		return nil, err
	}
	forceLoopback := config.DangerouslyAllowAllUnixSockets || hasAllowedUnixSocket(config.UnixSockets)
	httpListener, err := listenProxyURL(config.ProxyURL, 3128, config.DangerouslyAllowNonLoopbackProxy, forceLoopback)
	if err != nil {
		return nil, fmt.Errorf("start managed HTTP proxy: %w", err)
	}
	proxy := &managedNetworkProxy{
		config: config, httpListener: httpListener, hooks: hooks,
		executions: map[string]*managedNetworkExecution{},
	}
	if config.MITM {
		proxy.ca, err = newManagedCertificateAuthority()
		if err != nil {
			_ = httpListener.Close()
			return nil, err
		}
	}
	proxy.transport = &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         proxy.dialHTTPTransport,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if !config.AllowUpstreamProxy {
		proxy.transport.Proxy = nil
	}
	proxy.httpServer = &http.Server{Handler: proxy, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = proxy.httpServer.Serve(httpListener) }()
	if config.EnableSOCKS5 {
		proxy.socksListener, err = listenProxyURL(config.SOCKSURL, 8081, config.DangerouslyAllowNonLoopbackProxy, forceLoopback)
		if err != nil {
			proxy.close()
			return nil, fmt.Errorf("start managed SOCKS5 proxy: %w", err)
		}
		go proxy.serveSOCKS5()
	}
	return proxy, nil
}

func listenProxyURL(raw string, defaultPort int, allowNonLoopback, forceLoopback bool) (net.Listener, error) {
	address, err := resolveProxyListenerAddress(raw, defaultPort)
	if err != nil {
		return nil, err
	}
	if forceLoopback || (!allowNonLoopback && !address.IP.IsLoopback()) {
		address.IP = net.IPv4(127, 0, 0, 1)
	}
	return net.ListenTCP("tcp", address)
}

func resolveProxyListenerAddress(raw string, defaultPort int) (*net.TCPAddr, error) {
	host, port, err := parseProxyHostPort(raw, defaultPort)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(stringsBeforePercent(host))
	if ip == nil {
		ip = net.IPv4(127, 0, 0, 1)
	}
	return &net.TCPAddr{IP: ip, Port: port}, nil
}

func parseProxyHostPort(raw string, defaultPort int) (string, int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", 0, fmt.Errorf("missing host in network proxy address: %s", raw)
	}
	if ip := net.ParseIP(value); ip != nil && strings.Contains(value, ":") && !strings.HasPrefix(value, "[") {
		return value, defaultPort, nil
	}
	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "http://" + candidate
	}
	if parsed, err := url.Parse(candidate); err == nil && parsed.Hostname() != "" {
		port := defaultPort
		if parsed.Port() != "" {
			if parsedPort, parseErr := strconv.Atoi(parsed.Port()); parseErr == nil && parsedPort >= 0 && parsedPort <= 65535 {
				port = parsedPort
			}
		}
		return strings.Trim(parsed.Hostname(), "[]"), port, nil
	}
	withoutScheme := value
	if _, rest, ok := strings.Cut(withoutScheme, "://"); ok {
		withoutScheme = rest
	}
	hostPort, _, _ := strings.Cut(withoutScheme, "/")
	if index := strings.LastIndexByte(hostPort, '@'); index >= 0 {
		hostPort = hostPort[index+1:]
	}
	if strings.HasPrefix(hostPort, "[") {
		if end := strings.IndexByte(hostPort, ']'); end >= 0 {
			host := hostPort[1:end]
			if host == "" {
				return "", 0, fmt.Errorf("missing host in network proxy address: %s", raw)
			}
			port := defaultPort
			if suffix := strings.TrimPrefix(hostPort[end+1:], ":"); suffix != hostPort[end+1:] {
				if parsedPort, parseErr := strconv.Atoi(suffix); parseErr == nil && parsedPort >= 0 && parsedPort <= 65535 {
					port = parsedPort
				}
			}
			return host, port, nil
		}
	}
	if strings.Count(hostPort, ":") == 1 {
		host, portText, _ := strings.Cut(hostPort, ":")
		if host == "" {
			return "", 0, fmt.Errorf("missing host in network proxy address: %s", raw)
		}
		port := defaultPort
		if parsedPort, parseErr := strconv.Atoi(portText); parseErr == nil && parsedPort >= 0 && parsedPort <= 65535 {
			port = parsedPort
		}
		return host, port, nil
	}
	if hostPort == "" {
		return "", 0, fmt.Errorf("missing host in network proxy address: %s", raw)
	}
	return hostPort, defaultPort, nil
}

func hasAllowedUnixSocket(entries map[string]appcfg.NetworkAccess) bool {
	for _, access := range entries {
		if access == appcfg.NetworkAccessAllow {
			return true
		}
	}
	return false
}

func (proxy *managedNetworkProxy) close() {
	if proxy == nil {
		return
	}
	proxy.mu.Lock()
	if proxy.closed {
		proxy.mu.Unlock()
		return
	}
	proxy.closed = true
	executions := make([]*managedNetworkExecution, 0, len(proxy.executions))
	for _, execution := range proxy.executions {
		executions = append(executions, execution)
	}
	proxy.mu.Unlock()
	for _, execution := range executions {
		execution.closeUDP()
	}
	if proxy.httpServer != nil {
		_ = proxy.httpServer.Close()
	}
	if proxy.httpListener != nil {
		_ = proxy.httpListener.Close()
	}
	if proxy.socksListener != nil {
		_ = proxy.socksListener.Close()
	}
	if proxy.transport != nil {
		proxy.transport.CloseIdleConnections()
	}
	if proxy.ca != nil {
		proxy.ca.close()
	}
}

func (proxy *managedNetworkProxy) register(ctx context.Context, policy networkCommandPolicy, approval NetworkApprovalHandler, request NetworkApprovalRequest) (string, map[string]string, *ManagedNetworkCommand, error) {
	if proxy == nil || proxy.httpListener == nil {
		return "", nil, nil, fmt.Errorf("managed network proxy is unavailable")
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", nil, nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	execution := &managedNetworkExecution{
		policy: policy, approval: approval, request: request,
		decisions: map[networkApprovalKey]NetworkApprovalDecision{}, pending: map[networkApprovalKey]*pendingNetworkApproval{},
	}
	if proxy.config.EnableSOCKS5UDP && proxy.socksListener != nil {
		tcpAddr := proxy.socksListener.Addr().(*net.TCPAddr)
		udpListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: tcpAddr.IP, Port: 0})
		if err != nil {
			return "", nil, nil, fmt.Errorf("start managed SOCKS5 UDP execution endpoint: %w", err)
		}
		execution.udpListener = udpListener
	}
	proxy.mu.Lock()
	if proxy.closed {
		proxy.mu.Unlock()
		execution.closeUDP()
		return "", nil, nil, fmt.Errorf("managed network proxy is closed")
	}
	executionCtx, cancel := context.WithCancel(ctx)
	execution.ctx = executionCtx
	execution.cancel = cancel
	proxy.executions[token] = execution
	proxy.mu.Unlock()
	if execution.udpListener != nil {
		go proxy.serveSOCKS5UDP(execution)
	}

	httpURL := proxyURLForExecution("http", proxy.httpListener.Addr(), token)
	socksURL := ""
	if proxy.socksListener != nil {
		socksURL = proxyURLForExecution("socks5h", proxy.socksListener.Addr(), token)
	}
	env := managedProxyEnvironment(httpURL, socksURL, proxy.config.AllowLocalBinding, proxy.ca, request.CommandEnv)
	command := &ManagedNetworkCommand{
		HTTPPort:            proxy.httpListener.Addr().(*net.TCPAddr).Port,
		AllowLocalBinding:   proxy.config.AllowLocalBinding,
		AllowAllUnixSockets: proxy.config.DangerouslyAllowAllUnixSockets,
	}
	for path, access := range proxy.config.UnixSockets {
		if access == appcfg.NetworkAccessAllow {
			command.AllowedUnixSockets = append(command.AllowedUnixSockets, path)
		}
	}
	if proxy.socksListener != nil {
		command.SOCKSPort = proxy.socksListener.Addr().(*net.TCPAddr).Port
	}
	if execution.udpListener != nil {
		command.SOCKSUDPPort = execution.udpListener.LocalAddr().(*net.UDPAddr).Port
	}
	return token, env, command, nil
}

func (proxy *managedNetworkProxy) unregister(token string) *NetworkDenial {
	proxy.mu.Lock()
	execution := proxy.executions[token]
	delete(proxy.executions, token)
	proxy.mu.Unlock()
	if execution == nil {
		return nil
	}
	execution.cancelOnce.Do(execution.cancel)
	execution.closeUDP()
	execution.mu.Lock()
	defer execution.mu.Unlock()
	if execution.blocked == nil {
		return nil
	}
	copy := *execution.blocked
	return &copy
}

func (proxy *managedNetworkProxy) execution(token string) *managedNetworkExecution {
	if token == "" {
		return nil
	}
	proxy.mu.RLock()
	defer proxy.mu.RUnlock()
	return proxy.executions[token]
}

func (proxy *managedNetworkProxy) executionContext(token string) context.Context {
	if execution := proxy.execution(token); execution != nil && execution.ctx != nil {
		return execution.ctx
	}
	return context.Background()
}

func (execution *managedNetworkExecution) record(denial *NetworkDenial) {
	if execution == nil || denial == nil {
		return
	}
	execution.mu.Lock()
	if execution.blocked == nil || (execution.blocked.Decision != "ask" && denial.Decision == "ask") {
		copy := *denial
		execution.blocked = &copy
	}
	execution.mu.Unlock()
}

func (execution *managedNetworkExecution) recordAndCancel(denial *NetworkDenial) {
	execution.record(denial)
	if execution != nil && denial != nil && execution.cancel != nil {
		execution.cancelOnce.Do(execution.cancel)
	}
}

func (execution *managedNetworkExecution) evaluate(host string, port int, protocol NetworkApprovalProtocol, method string) *NetworkDenial {
	if execution == nil {
		return networkBlocked(host, port, protocol, method, "", "not_allowed", "deny", "proxy_state")
	}
	denial := execution.policy.evaluate(host, port, protocol, method)
	if denial == nil {
		return nil
	}
	if denial.Decision != "ask" || execution.approval == nil {
		execution.recordAndCancel(denial)
		return denial
	}
	key := networkApprovalKey{host: denial.Host, protocol: denial.Protocol, port: denial.Port}
	execution.mu.Lock()
	if decision, ok := execution.decisions[key]; ok {
		execution.mu.Unlock()
		if decision.Allows() {
			return nil
		}
		denial.Decision = "deny"
		execution.recordAndCancel(denial)
		return denial
	}
	if pending := execution.pending[key]; pending != nil {
		execution.mu.Unlock()
		select {
		case <-execution.ctx.Done():
			denial.Decision = "deny"
			execution.recordAndCancel(denial)
			return denial
		case <-pending.done:
			if pending.err == nil && pending.decision.Allows() {
				return nil
			}
			denial.Decision = "deny"
			execution.recordAndCancel(denial)
			return denial
		}
	}
	pending := &pendingNetworkApproval{done: make(chan struct{})}
	execution.pending[key] = pending
	execution.mu.Unlock()

	request := execution.request
	request.Context = NetworkApprovalContext{Host: denial.Host, Protocol: denial.Protocol}
	request.Port = denial.Port
	pending.decision, pending.err = execution.approval(execution.ctx, request)
	execution.mu.Lock()
	delete(execution.pending, key)
	execution.decisions[key] = pending.decision
	close(pending.done)
	execution.mu.Unlock()
	if pending.err == nil && pending.decision.Allows() {
		return nil
	}
	denial.Decision = "deny"
	execution.recordAndCancel(denial)
	return denial
}

func proxyURLForExecution(scheme string, addr net.Addr, token string) string {
	return (&url.URL{Scheme: scheme, Host: addr.String(), User: url.User(token)}).String()
}

const managedProxyGitSSHCommandPrefix = "FOREBRAIN_PROXY_GIT_SSH_COMMAND=1 ssh -o ProxyCommand='nc -X 5 -x "
const managedProxyGitSSHCommandSuffix = " %h %p'"

func managedProxyEnvironment(httpURL, socksURL string, allowLocal bool, ca *managedCertificateAuthority, commandEnv []string) map[string]string {
	env := map[string]string{
		networkProxyActiveEnv:       "1",
		networkAllowLocalBindingEnv: map[bool]string{true: "1", false: "0"}[allowLocal],
		"ELECTRON_GET_USE_PROXY":    "true",
		"NODE_USE_ENV_PROXY":        "1",
	}
	for _, key := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "YARN_HTTP_PROXY", "YARN_HTTPS_PROXY",
		"npm_config_http_proxy", "npm_config_https_proxy", "npm_config_proxy", "NPM_CONFIG_HTTP_PROXY",
		"NPM_CONFIG_HTTPS_PROXY", "NPM_CONFIG_PROXY", "BUNDLE_HTTP_PROXY", "BUNDLE_HTTPS_PROXY",
		"PIP_PROXY", "DOCKER_HTTP_PROXY", "DOCKER_HTTPS_PROXY", "WS_PROXY", "WSS_PROXY", "ws_proxy", "wss_proxy",
	} {
		env[key] = httpURL
	}
	noProxy := ""
	if allowLocal {
		noProxy = "localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
	}
	for _, key := range []string{"NO_PROXY", "no_proxy", "npm_config_noproxy", "NPM_CONFIG_NOPROXY", "YARN_NO_PROXY", "BUNDLE_NO_PROXY"} {
		env[key] = noProxy
	}
	allProxy := httpURL
	if socksURL != "" {
		allProxy = socksURL
	}
	for _, key := range []string{"ALL_PROXY", "all_proxy", "FTP_PROXY", "ftp_proxy"} {
		env[key] = allProxy
	}
	if ca != nil {
		for _, key := range []string{
			"FOREBRAIN_CA_CERTIFICATE", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS",
			"GIT_SSL_CAINFO", "CARGO_HTTP_CAINFO", "PIP_CERT", "BUNDLE_SSL_CA_CERT", "npm_config_cafile", "NPM_CONFIG_CAFILE",
		} {
			if value, ok := explicitEnvValue(commandEnv, key); ok && value != "" && value != ca.bundlePath {
				continue
			}
			env[key] = ca.bundlePath
		}
	}
	if runtime.GOOS == "darwin" && socksURL != "" && shouldSetManagedGitSSHCommand(commandEnv) {
		parsed, _ := url.Parse(socksURL)
		env["GIT_SSH_COMMAND"] = managedProxyGitSSHCommandPrefix + parsed.Host + managedProxyGitSSHCommandSuffix
	}
	return env
}

func explicitEnvValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for index := len(env) - 1; index >= 0; index-- {
		if strings.HasPrefix(env[index], prefix) {
			return strings.TrimPrefix(env[index], prefix), true
		}
	}
	return "", false
}

func shouldSetManagedGitSSHCommand(commandEnv []string) bool {
	value, ok := explicitEnvValue(commandEnv, "GIT_SSH_COMMAND")
	if !ok {
		value, ok = os.LookupEnv("GIT_SSH_COMMAND")
	}
	return !ok || isManagedGitSSHCommand(value)
}

func isManagedGitSSHCommand(command string) bool {
	return strings.HasPrefix(command, managedProxyGitSSHCommandPrefix) && strings.HasSuffix(command, managedProxyGitSSHCommandSuffix)
}

func executionTokenFromHTTP(request *http.Request) string {
	value := strings.TrimSpace(request.Header.Get("Proxy-Authorization"))
	request.Header.Del("Proxy-Authorization")
	scheme, encoded, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return ""
	}
	token, _, _ := strings.Cut(string(decoded), ":")
	return token
}

func (proxy *managedNetworkProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	execution := proxy.execution(executionTokenFromHTTP(request))
	if execution == nil {
		writer.Header().Set("Proxy-Authenticate", `Basic realm="forebrain"`)
		http.Error(writer, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if socketPath := strings.TrimSpace(request.Header.Get("x-unix-socket")); socketPath != "" {
		proxy.serveUnixSocketHTTP(writer, request, execution, socketPath)
		return
	}
	if request.Method == http.MethodConnect {
		proxy.serveHTTPConnect(writer, request, execution)
		return
	}
	proxy.servePlainHTTP(writer, request, execution)
}

func (proxy *managedNetworkProxy) servePlainHTTP(writer http.ResponseWriter, request *http.Request, execution *managedNetworkExecution) {
	if err := validateAbsoluteFormHostHeader(request); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	host, port := requestTarget(request, 80)
	if denial := execution.evaluate(host, port, NetworkApprovalHTTP, request.Method); denial != nil {
		writeNetworkBlocked(writer, denial)
		return
	}
	requestContext, cancel := linkedExecutionContext(request.Context(), execution)
	defer cancel()
	outbound := request.Clone(withManagedNetworkDialTarget(requestContext, execution, host, port, NetworkApprovalHTTP))
	outbound.RequestURI = ""
	outbound.Header = request.Header.Clone()
	removeHopHeaders(outbound.Header)
	if outbound.URL.Scheme == "" {
		outbound.URL.Scheme = "http"
	}
	if outbound.URL.Host == "" {
		outbound.URL.Host = request.Host
	}
	response, err := proxy.transport.RoundTrip(outbound)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	copyHTTPResponse(writer, response)
}

func (proxy *managedNetworkProxy) serveHTTPConnect(writer http.ResponseWriter, request *http.Request, execution *managedNetworkExecution) {
	host, port := splitNetworkTarget(request.Host)
	if port == 0 {
		port = 443
	}
	if denial := execution.evaluate(host, port, NetworkApprovalHTTPS, ""); denial != nil {
		writeNetworkBlockedText(writer, denial)
		return
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		http.Error(writer, "proxy does not support CONNECT", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if buffered.Reader.Buffered() > 0 {
		_ = client.Close()
		return
	}
	stopExecutionClose := context.AfterFunc(execution.ctx, func() { _ = client.Close() })
	defer stopExecutionClose()
	_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
	if strings.EqualFold(execution.policy.config.Mode, "limited") || len(proxy.hooks[host]) > 0 {
		proxy.serveMITM(client, execution, host, port, NetworkApprovalHTTPS)
		return
	}
	requestContext, cancel := linkedExecutionContext(request.Context(), execution)
	defer cancel()
	upstream, err := proxy.dialTunnel(requestContext, execution, host, port, NetworkApprovalHTTPS)
	if err != nil {
		_ = client.Close()
		return
	}
	proxyTunnelContext(execution.ctx, client, upstream)
}

func (proxy *managedNetworkProxy) serveMITM(client net.Conn, execution *managedNetworkExecution, host string, port int, protocol NetworkApprovalProtocol) {
	defer client.Close()
	stopExecutionClose := context.AfterFunc(execution.ctx, func() { _ = client.Close() })
	defer stopExecutionClose()
	if proxy.ca == nil {
		execution.recordAndCancel(networkBlocked(host, port, protocol, "", execution.policy.config.Mode, "mitm_required", "deny", "mode_guard"))
		return
	}
	certificate, err := proxy.ca.certificateForHost(host)
	if err != nil {
		return
	}
	tlsConn := tls.Server(client, &tls.Config{Certificates: []tls.Certificate{*certificate}, MinVersion: tls.VersionTLS12})
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	reader := bufio.NewReader(tlsConn)
	for {
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		request.URL.Scheme = "https"
		request.URL.Host = net.JoinHostPort(host, strconv.Itoa(port))
		request = request.WithContext(withManagedNetworkDialTarget(execution.ctx, execution, host, port, protocol))
		request.RequestURI = ""
		request.Header.Del("Proxy-Authorization")
		if denial := execution.evaluate(host, port, protocol, request.Method); denial != nil {
			_ = writeNetworkBlockedToConn(tlsConn, denial)
			_ = request.Body.Close()
			return
		}
		action, matched, hooked := proxy.hooks.evaluate(host, request)
		if hooked && !matched {
			denial := networkBlocked(host, port, protocol, request.Method, execution.policy.config.Mode, "mitm_hook_denied", "deny", "mitm_hook")
			execution.recordAndCancel(denial)
			_ = writeNetworkBlockedToConn(tlsConn, denial)
			_ = request.Body.Close()
			return
		}
		if matched {
			action.apply(request.Header)
		}
		removeHopHeaders(request.Header)
		response, err := proxy.transport.RoundTrip(request)
		if err != nil {
			response = &http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader(err.Error()))}
		}
		_ = response.Write(tlsConn)
		_ = response.Body.Close()
		_ = request.Body.Close()
		if request.Close || response.Close {
			return
		}
	}
}

func withManagedNetworkDialTarget(ctx context.Context, execution *managedNetworkExecution, host string, port int, protocol NetworkApprovalProtocol) context.Context {
	return context.WithValue(ctx, managedNetworkDialTargetKey{}, managedNetworkDialTarget{
		execution: execution,
		host:      normalizeNetworkHost(host),
		port:      port,
		protocol:  protocol,
	})
}

func linkedExecutionContext(parent context.Context, execution *managedNetworkExecution) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if execution == nil || execution.ctx == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(execution.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (proxy *managedNetworkProxy) dialHTTPTransport(ctx context.Context, network, address string) (net.Conn, error) {
	target, ok := ctx.Value(managedNetworkDialTargetKey{}).(managedNetworkDialTarget)
	if !ok || target.execution == nil || target.host == "" || target.port <= 0 {
		return nil, fmt.Errorf("network target metadata is missing")
	}
	addressHost, addressPort := splitNetworkTarget(address)
	if addressHost != target.host || addressPort != target.port {
		if !proxy.config.AllowUpstreamProxy {
			return nil, fmt.Errorf("network target does not match approved request")
		}
		return (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
	}
	return proxy.dialCheckedTarget(ctx, target.execution, target.host, target.port, target.protocol)
}

type resolvedNetworkTarget struct {
	address string
	ip      netip.Addr
}

func (proxy *managedNetworkProxy) resolveNetworkTarget(ctx context.Context, host string, port int) ([]resolvedNetworkTarget, error) {
	host = normalizeNetworkHost(host)
	if host == "" || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid network target")
	}
	if address, err := netip.ParseAddr(strings.Split(host, "%")[0]); err == nil {
		return []resolvedNetworkTarget{{address: net.JoinHostPort(host, strconv.Itoa(port)), ip: address.Unmap()}}, nil
	}
	lookup := proxy.lookupNetIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	addresses, err := lookup(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	resolved := make([]resolvedNetworkTarget, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() {
			continue
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		resolved = append(resolved, resolvedNetworkTarget{
			address: net.JoinHostPort(address.String(), strconv.Itoa(port)),
			ip:      address,
		})
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("network target did not resolve")
	}
	return resolved, nil
}

func (proxy *managedNetworkProxy) dialCheckedTarget(ctx context.Context, execution *managedNetworkExecution, host string, port int, protocol NetworkApprovalProtocol) (net.Conn, error) {
	if execution == nil {
		return nil, fmt.Errorf("network execution is missing")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addresses, err := proxy.resolveNetworkTarget(dialCtx, host, port)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	var lastErr error
	policyRejected := false
	for _, target := range addresses {
		if isNonPublicIP(target.ip) && !execution.policy.allowsResolvedNonPublic(host, port, protocol, target.ip) {
			policyRejected = true
			continue
		}
		conn, dialErr := dialer.DialContext(dialCtx, "tcp", target.address)
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if policyRejected {
		execution.recordAndCancel(networkBlocked(host, port, protocol, "", execution.policy.config.Mode, "not_allowed_local", "deny", "baseline_policy"))
		return nil, fmt.Errorf("network target rejected by policy")
	}
	return nil, fmt.Errorf("network target is unavailable")
}

func (proxy *managedNetworkProxy) dialCheckedUDP(ctx context.Context, execution *managedNetworkExecution, host string, port int) (*net.UDPConn, error) {
	if execution == nil {
		return nil, fmt.Errorf("network execution is missing")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addresses, err := proxy.resolveNetworkTarget(dialCtx, host, port)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{}
	var lastErr error
	policyRejected := false
	for _, target := range addresses {
		if isNonPublicIP(target.ip) && !execution.policy.allowsResolvedNonPublic(host, port, NetworkApprovalSOCKS5UDP, target.ip) {
			policyRejected = true
			continue
		}
		conn, dialErr := dialer.DialContext(dialCtx, "udp", target.address)
		if dialErr != nil {
			lastErr = dialErr
			continue
		}
		udpConn, ok := conn.(*net.UDPConn)
		if !ok {
			_ = conn.Close()
			lastErr = fmt.Errorf("UDP dial returned %T", conn)
			continue
		}
		return udpConn, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if policyRejected {
		execution.recordAndCancel(networkBlocked(host, port, NetworkApprovalSOCKS5UDP, "", execution.policy.config.Mode, "not_allowed_local", "deny", "baseline_policy"))
		return nil, fmt.Errorf("network target rejected by policy")
	}
	return nil, fmt.Errorf("network target is unavailable")
}

func (proxy *managedNetworkProxy) dialTunnel(ctx context.Context, execution *managedNetworkExecution, host string, port int, protocol NetworkApprovalProtocol) (net.Conn, error) {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	if !proxy.config.AllowUpstreamProxy {
		return proxy.dialCheckedTarget(ctx, execution, host, port, protocol)
	}
	proxyURL, _ := http.ProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: "https", Host: address}})
	if proxyURL == nil {
		return proxy.dialCheckedTarget(ctx, execution, host, port, protocol)
	}
	if proxyURL.Scheme != "http" && proxyURL.Scheme != "https" {
		return nil, fmt.Errorf("unsupported upstream proxy scheme %q", proxyURL.Scheme)
	}
	conn, err := (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, "tcp", proxyURL.Host)
	if err != nil {
		return nil, err
	}
	headers := ""
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
		headers = "Proxy-Authorization: Basic " + credentials + "\r\n"
	}
	_, err = io.WriteString(conn, "CONNECT "+address+" HTTP/1.1\r\nHost: "+address+"\r\n"+headers+"\r\n")
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode/100 != 2 {
		_ = conn.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("upstream proxy CONNECT failed: %s", response.Status)
	}
	_ = response.Body.Close()
	return conn, nil
}

func proxyTunnel(left, right net.Conn) {
	var once sync.Once
	closeBoth := func() { _ = left.Close(); _ = right.Close() }
	copySide := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		once.Do(closeBoth)
	}
	go copySide(left, right)
	copySide(right, left)
}

func proxyTunnelContext(ctx context.Context, left, right net.Conn) {
	stop := context.AfterFunc(ctx, func() {
		_ = left.Close()
		_ = right.Close()
	})
	defer stop()
	proxyTunnel(left, right)
}

func requestTarget(request *http.Request, defaultPort int) (string, int) {
	target := request.URL.Host
	if target == "" {
		target = request.Host
	}
	host, port := splitNetworkTarget(target)
	if port == 0 {
		port = defaultPort
	}
	return host, port
}

func validateAbsoluteFormHostHeader(request *http.Request) error {
	if request == nil || request.URL == nil || request.URL.Scheme == "" {
		return nil
	}
	targetHost := normalizeNetworkHost(request.URL.Hostname())
	if targetHost == "" {
		return fmt.Errorf("invalid request target")
	}
	hostHeader := strings.TrimSpace(request.Host)
	if hostHeader == "" {
		return nil
	}
	parsedHeader, err := url.Parse("//" + hostHeader)
	if err != nil || parsedHeader.User != nil || parsedHeader.Hostname() == "" || parsedHeader.Path != "" {
		return fmt.Errorf("invalid Host header")
	}
	if normalizeNetworkHost(parsedHeader.Hostname()) != targetHost {
		return fmt.Errorf("Host header does not match request target")
	}
	targetPortRaw := request.URL.Port()
	headerPortRaw := parsedHeader.Port()
	if headerPortRaw != "" {
		if targetPortRaw == "" || headerPortRaw != targetPortRaw {
			return fmt.Errorf("Host header does not match request target")
		}
		return nil
	}
	if targetPortRaw == "" {
		return nil
	}
	targetPort, err := strconv.Atoi(targetPortRaw)
	if err != nil || targetPort != defaultNetworkPort(request.URL.Scheme) {
		return fmt.Errorf("Host header does not match request target")
	}
	return nil
}

func defaultNetworkPort(scheme string) int {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "http":
		return 80
	case "https":
		return 443
	default:
		return 0
	}
}

func writeNetworkBlocked(writer http.ResponseWriter, denial *NetworkDenial) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("x-proxy-error", networkBlockedHeader(denial.Reason))
	writer.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"status": "blocked", "host": denial.Host, "reason": denial.Reason,
		"decision": denial.Decision, "source": denial.Source,
		"protocol": networkPolicyProtocol(denial.Protocol), "port": denial.Port,
		"message": networkBlockedMessage(denial.Reason),
	})
}

func writeNetworkBlockedWithoutDetails(writer http.ResponseWriter, denial *NetworkDenial) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("x-proxy-error", networkBlockedHeader(denial.Reason))
	writer.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"status": "blocked", "host": denial.Host, "reason": denial.Reason,
	})
}

func writeNetworkBlockedText(writer http.ResponseWriter, denial *NetworkDenial) {
	writer.Header().Set("Content-Type", "text/plain")
	writer.Header().Set("x-proxy-error", networkBlockedHeader(denial.Reason))
	writer.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(writer, networkBlockedMessage(denial.Reason))
}

func writeNetworkBlockedToConn(conn net.Conn, denial *NetworkDenial) error {
	body := networkBlockedMessage(denial.Reason)
	response := &http.Response{
		StatusCode: http.StatusForbidden, Status: "403 Forbidden", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": []string{"text/plain"}, "x-proxy-error": []string{networkBlockedHeader(denial.Reason)}, "Content-Length": []string{strconv.Itoa(len(body))}, "Connection": []string{"close"}},
		Body:   io.NopCloser(strings.NewReader(body)), Close: true,
	}
	return response.Write(conn)
}

func networkBlockedHeader(reason string) string {
	switch reason {
	case "not_allowed", "not_allowed_local":
		return "blocked-by-allowlist"
	case "denied":
		return "blocked-by-denylist"
	case "method_not_allowed":
		return "blocked-by-method-policy"
	case "mitm_hook_denied":
		return "blocked-by-mitm-hook"
	case "mitm_required":
		return "blocked-by-mitm-required"
	default:
		return "blocked-by-policy"
	}
}

func networkBlockedMessage(reason string) string {
	switch reason {
	case "not_allowed":
		return "Domain not in allowlist."
	case "not_allowed_local":
		return "Sandbox policy blocks local/private network addresses."
	case "denied":
		return "Domain denied by the sandbox policy."
	case "method_not_allowed":
		return "Method not allowed in limited mode."
	case "mitm_hook_denied":
		return "HTTPS request denied by MITM hook policy."
	case "mitm_required":
		return "MITM required for limited HTTPS."
	case "proxy_disabled":
		return "network proxy is disabled"
	default:
		return "Request blocked by network policy."
	}
}

func networkPolicyProtocol(protocol NetworkApprovalProtocol) string {
	switch protocol {
	case NetworkApprovalHTTPS:
		return "https_connect"
	default:
		return string(protocol)
	}
}

func copyHTTPResponse(writer http.ResponseWriter, response *http.Response) {
	for key, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func removeHopHeaders(headers http.Header) {
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		headers.Del(key)
	}
}

func (proxy *managedNetworkProxy) serveUnixSocketHTTP(writer http.ResponseWriter, request *http.Request, execution *managedNetworkExecution, socketPath string) {
	request.Header.Del("x-unix-socket")
	if strings.EqualFold(execution.policy.config.Mode, "limited") && !networkMethodAllowed(request.Method) {
		denial := networkBlocked("unix-socket", 0, NetworkApprovalHTTP, request.Method, execution.policy.config.Mode, "method_not_allowed", "deny", "mode_guard")
		execution.recordAndCancel(denial)
		writeNetworkBlockedWithoutDetails(writer, denial)
		return
	}
	if runtime.GOOS != "darwin" {
		denial := networkBlocked("unix-socket", 0, NetworkApprovalHTTP, request.Method, execution.policy.config.Mode, "unix_socket_unsupported", "deny", "proxy_state")
		execution.recordAndCancel(denial)
		writer.Header().Set("Content-Type", "text/plain")
		writer.WriteHeader(http.StatusNotImplemented)
		_, _ = io.WriteString(writer, "unix sockets unsupported")
		return
	}
	if !proxy.unixSocketAllowed(socketPath) {
		denial := networkBlocked("unix-socket", 0, NetworkApprovalHTTP, request.Method, execution.policy.config.Mode, "not_allowed", "deny", "proxy_state")
		execution.recordAndCancel(denial)
		writeNetworkBlockedWithoutDetails(writer, denial)
		return
	}
	dialContext, cancel := linkedExecutionContext(request.Context(), execution)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: 30 * time.Second}).DialContext(dialContext, "unix", socketPath)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Close()
	outbound := request.Clone(request.Context())
	outbound.RequestURI = ""
	removeHopHeaders(outbound.Header)
	if err := outbound.Write(conn); err != nil {
		http.Error(writer, err.Error(), http.StatusBadGateway)
		return
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), outbound)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	copyHTTPResponse(writer, response)
}

func (proxy *managedNetworkProxy) unixSocketAllowed(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	if proxy.config.DangerouslyAllowAllUnixSockets {
		return true
	}
	requested := filepath.Clean(path)
	requestedCanonical, _ := filepath.EvalSymlinks(requested)
	for allowed, access := range proxy.config.UnixSockets {
		if access != appcfg.NetworkAccessAllow || !filepath.IsAbs(allowed) {
			continue
		}
		allowed = filepath.Clean(allowed)
		if allowed == requested {
			return true
		}
		if requestedCanonical == "" {
			continue
		}
		if allowedCanonical, err := filepath.EvalSymlinks(allowed); err == nil && allowedCanonical == requestedCanonical {
			return true
		}
	}
	return false
}

var _ http.Handler = (*managedNetworkProxy)(nil)

const networkAccessRuleTool = NetworkAccessPermissionTool

type networkPermissionRule struct {
	protocol NetworkApprovalProtocol
	host     string
	port     int
	allow    bool
	source   PermissionSource
}

type networkCommandPolicy struct {
	config   appcfg.EffectiveNetworkProxyConfig
	rules    []networkPermissionRule
	approved *ApprovedNetworkAccess
	canAsk   bool
}

func networkRulesFromSnapshot(snapshot Snapshot) []networkPermissionRule {
	var out []networkPermissionRule
	for _, behavior := range []PermissionBehavior{BehaviorDeny, BehaviorAllow} {
		for source, byBehavior := range snapshot.Rules {
			if source == SourceSession {
				continue
			}
			// Opening egress is permitting, so it is only honoured from a
			// source allowed to permit. A deny from any source still narrows.
			// The store already filters these out, so this is defence in depth
			// for snapshots assembled elsewhere.
			if behavior == BehaviorAllow && !MayPermit(source) {
				continue
			}
			for _, rule := range byBehavior[behavior] {
				if !strings.EqualFold(strings.TrimSpace(rule.ToolName), networkAccessRuleTool) {
					continue
				}
				parsed, ok := parseNetworkPermissionRule(rule.RuleContent)
				if !ok {
					continue
				}
				parsed.allow = behavior == BehaviorAllow
				parsed.source = source
				out = append(out, parsed)
			}
		}
	}
	return out
}

func networkRulesFromSession(rules []SessionNetworkRule) []networkPermissionRule {
	out := make([]networkPermissionRule, 0, len(rules))
	for _, rule := range rules {
		host := normalizeNetworkHost(rule.Context.Host)
		if host == "" || !rule.Context.Protocol.Valid() || rule.Port < 0 || rule.Port > 65535 {
			continue
		}
		out = append(out, networkPermissionRule{
			protocol: rule.Context.Protocol, host: host, port: rule.Port,
			allow: rule.Allow, source: SourceSession,
		})
	}
	return out
}

func networkDenyRulesOnly(rules []networkPermissionRule) []networkPermissionRule {
	out := rules[:0]
	for _, rule := range rules {
		if !rule.allow {
			out = append(out, rule)
		}
	}
	return out
}

func parseNetworkPermissionRule(raw string) (networkPermissionRule, bool) {
	raw = strings.TrimSpace(raw)
	protocolRaw, target, ok := strings.Cut(raw, "://")
	if !ok {
		return networkPermissionRule{}, false
	}
	protocol := NetworkApprovalProtocol(strings.ToLower(strings.TrimSpace(protocolRaw)))
	if !protocol.Valid() {
		return networkPermissionRule{}, false
	}
	host, port := splitNetworkTarget(target)
	if host == "" {
		return networkPermissionRule{}, false
	}
	return networkPermissionRule{protocol: protocol, host: host, port: port}, true
}

func splitNetworkTarget(target string) (string, int) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", 0
	}
	if host, portRaw, err := net.SplitHostPort(target); err == nil {
		port, err := strconv.Atoi(portRaw)
		if err == nil && port > 0 && port <= 65535 {
			return normalizeNetworkHost(host), port
		}
	}
	return normalizeNetworkHost(target), 0
}

func (p networkCommandPolicy) evaluate(host string, port int, protocol NetworkApprovalProtocol, method string) *NetworkDenial {
	host = normalizeNetworkHost(host)
	if !p.config.Enabled {
		return networkBlocked(host, port, protocol, method, p.config.Mode, "proxy_disabled", "deny", "proxy_state")
	}
	if host == "" || !protocol.Valid() {
		return networkBlocked(host, port, protocol, method, p.config.Mode, "not_allowed", "deny", "baseline_policy")
	}

	denied := p.matchesDomain(host, port, protocol, false)
	if denied {
		return networkBlocked(host, port, protocol, method, p.config.Mode, "denied", "deny", "baseline_policy")
	}
	allowed := p.matchesDomain(host, port, protocol, true)
	if !p.config.AllowLocalBinding && networkTargetIsLocal(host, port) {
		if !isLiteralLocalHost(host) || !p.explicitlyAllowsLocalLiteral(host, port, protocol) {
			return networkBlocked(host, port, protocol, method, p.config.Mode, "not_allowed_local", "deny", "baseline_policy")
		}
	}
	if !allowed {
		decision, source := "deny", "baseline_policy"
		if p.canAsk {
			decision, source = "ask", "decider"
		}
		return networkBlocked(host, port, protocol, method, p.config.Mode, "not_allowed", decision, source)
	}
	if strings.EqualFold(p.config.Mode, "limited") && !networkMethodAllowed(method) {
		return networkBlocked(host, port, protocol, method, p.config.Mode, "method_not_allowed", "deny", "mode_guard")
	}
	return nil
}

func (p networkCommandPolicy) matchesDomain(host string, port int, protocol NetworkApprovalProtocol, allow bool) bool {
	for pattern, access := range p.config.Domains {
		if (access == appcfg.NetworkAccessAllow) != allow {
			continue
		}
		if domainPatternMatches(pattern, host) {
			return true
		}
	}
	for _, rule := range p.rules {
		if rule.allow != allow {
			continue
		}
		if rule.protocol != protocol || (rule.port != 0 && rule.port != port) {
			continue
		}
		if domainPatternMatches(rule.host, host) {
			return true
		}
	}
	if allow && p.approved != nil {
		approvedHost := normalizeNetworkHost(p.approved.Context.Host)
		if approvedHost == host && p.approved.Context.Protocol == protocol &&
			(p.approved.Port == 0 || p.approved.Port == port) {
			return true
		}
	}
	return false
}

func (p networkCommandPolicy) explicitlyAllowsLocalLiteral(host string, port int, protocol NetworkApprovalProtocol) bool {
	for pattern, access := range p.config.Domains {
		if access == appcfg.NetworkAccessAllow && normalizeNetworkHost(pattern) == host && !strings.ContainsAny(pattern, "*?[") {
			return true
		}
	}
	for _, rule := range p.rules {
		if rule.allow && rule.source != SourceSession && rule.host == host && rule.protocol == protocol &&
			(rule.port == 0 || rule.port == port) && !strings.ContainsAny(rule.host, "*?[") {
			return true
		}
	}
	return false
}

func (p networkCommandPolicy) allowsResolvedNonPublic(host string, port int, protocol NetworkApprovalProtocol, address netip.Addr) bool {
	if p.config.AllowLocalBinding {
		return true
	}
	address = address.Unmap()
	host = normalizeNetworkHost(host)
	hostIP, err := netip.ParseAddr(strings.Split(host, "%")[0])
	targetMatches := err == nil && hostIP.Unmap() == address
	if host == "localhost" && address.IsLoopback() {
		targetMatches = true
	}
	return targetMatches && p.explicitlyAllowsLocalLiteral(host, port, protocol)
}

func networkMethodAllowed(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "", "GET", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

func networkBlocked(host string, port int, protocol NetworkApprovalProtocol, method, mode, reason, decision, source string) *NetworkDenial {
	return &NetworkDenial{
		Host: normalizeNetworkHost(host), Reason: reason, Method: strings.ToUpper(strings.TrimSpace(method)),
		Mode: strings.ToLower(strings.TrimSpace(mode)), Protocol: protocol, Decision: decision,
		Source: source, Port: port,
	}
}

func normalizeNetworkHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end >= 0 {
			host = host[1:end]
		}
	} else if strings.Count(host, ":") == 1 {
		host, _, _ = strings.Cut(host, ":")
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if ip, scope, ok := strings.Cut(host, "%25"); ok && net.ParseIP(ip) != nil {
		return ip + "%" + scope
	}
	return host
}

func domainPatternMatches(pattern, host string) bool {
	pattern = normalizeNetworkHost(pattern)
	host = normalizeNetworkHost(host)
	if pattern == "" || host == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if base := strings.TrimPrefix(pattern, "**."); base != pattern {
		return host == base || domainGlobMatches("*."+base, host)
	}
	if strings.HasPrefix(pattern, "*.") && host == strings.TrimPrefix(pattern, "*.") {
		return false
	}
	return domainGlobMatches(pattern, host)
}

func domainGlobMatches(pattern, host string) bool {
	matched, _ := path.Match(pattern, host)
	return matched
}

func networkTargetIsLocal(host string, port int) bool {
	if isLiteralLocalHost(host) {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		// A name that did not resolve here — a slow resolver, a timeout, a
		// name that does not exist — is not known to be local, and calling it
		// local turned an unlisted public host into a hard denial the user was
		// never asked about. Nothing is lost by not guessing: the dial resolves
		// the name again and refuses any non-public address it lands on
		// (dialCheckedTarget), so a name can only ever reach what it actually
		// resolves to.
		return false
	}
	for _, addr := range addrs {
		if isNonPublicIP(addr) {
			return true
		}
	}
	_ = port
	return false
}

func isLiteralLocalHost(host string) bool {
	host = normalizeNetworkHost(host)
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(strings.Split(host, "%")[0])
	return err == nil && isNonPublicIP(addr)
}

func isNonPublicIP(addr netip.Addr) bool {
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	if addr.Is4() {
		for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
			prefix := netip.MustParsePrefix(raw)
			if prefix.Contains(addr) {
				return true
			}
		}
	}
	return false
}

func validateNetworkDomainPatterns(domains map[string]appcfg.NetworkAccess) error {
	for pattern, access := range domains {
		pattern = normalizeNetworkDomainPattern(pattern)
		if pattern == "" {
			return fmt.Errorf("network domain pattern is empty")
		}
		if access == appcfg.NetworkAccessDeny && isGlobalNetworkDomainPattern(pattern) {
			return fmt.Errorf("global wildcard is not supported in the network denylist")
		}
		if _, err := path.Match(pattern, "example.com"); err != nil {
			return fmt.Errorf("invalid network domain pattern %q: %w", pattern, err)
		}
	}
	return nil
}

func normalizeNetworkDomainPattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "*" {
		return pattern
	}
	prefix := ""
	switch {
	case strings.HasPrefix(pattern, "**."):
		prefix, pattern = "**.", strings.TrimPrefix(pattern, "**.")
	case strings.HasPrefix(pattern, "*."):
		prefix, pattern = "*.", strings.TrimPrefix(pattern, "*.")
	}
	return prefix + normalizeNetworkHost(pattern)
}

func isGlobalNetworkDomainPattern(pattern string) bool {
	pattern = normalizeNetworkDomainPattern(pattern)
	return pattern == "*" || pattern == "**.*"
}

const (
	socksVersion5       = 5
	socksAuthNone       = 0
	socksAuthUserPass   = 2
	socksAuthNoMethods  = 255
	socksCommandConnect = 1
	socksCommandUDP     = 3
)

func (proxy *managedNetworkProxy) serveSOCKS5() {
	for {
		conn, err := proxy.socksListener.Accept()
		if err != nil {
			return
		}
		go proxy.handleSOCKS5(conn)
	}
}

func (proxy *managedNetworkProxy) handleSOCKS5(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(conn)
	token, err := readSOCKS5Authentication(reader, conn, func(token string) bool {
		return proxy.execution(token) != nil
	})
	if err != nil {
		return
	}
	execution := proxy.execution(token)
	if execution == nil {
		return
	}
	stopExecutionClose := context.AfterFunc(execution.ctx, func() { _ = conn.Close() })
	defer stopExecutionClose()
	command, host, port, err := readSOCKS5Request(reader)
	if err != nil {
		_ = writeSOCKS5Reply(conn, 1, nil)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	switch command {
	case socksCommandConnect:
		proxy.handleSOCKS5Connect(conn, execution, host, port)
	case socksCommandUDP:
		proxy.handleSOCKS5UDPAssociate(conn, execution, host, port)
	default:
		_ = writeSOCKS5Reply(conn, 7, nil)
	}
}

func readSOCKS5Authentication(reader *bufio.Reader, conn net.Conn, validToken func(string) bool) (string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != socksVersion5 {
		return "", fmt.Errorf("invalid SOCKS5 greeting")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return "", err
	}
	method := byte(socksAuthNoMethods)
	if containsByte(methods, socksAuthUserPass) {
		method = socksAuthUserPass
	}
	if _, err := conn.Write([]byte{socksVersion5, method}); err != nil || method == socksAuthNoMethods {
		return "", fmt.Errorf("SOCKS5 authentication unavailable")
	}
	if version, err := reader.ReadByte(); err != nil || version != 1 {
		return "", fmt.Errorf("invalid SOCKS5 username authentication")
	}
	usernameLength, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	username := make([]byte, int(usernameLength))
	if _, err := io.ReadFull(reader, username); err != nil {
		return "", err
	}
	passwordLength, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	password := make([]byte, int(passwordLength))
	if _, err := io.ReadFull(reader, password); err != nil {
		return "", err
	}
	token := string(username)
	if token == "" || validToken == nil || !validToken(token) {
		_, _ = conn.Write([]byte{1, 1})
		return "", fmt.Errorf("invalid SOCKS5 credentials")
	}
	if _, err := conn.Write([]byte{1, 0}); err != nil {
		return "", err
	}
	return token, nil
}

func readSOCKS5Request(reader *bufio.Reader) (byte, string, int, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != socksVersion5 {
		return 0, "", 0, fmt.Errorf("invalid SOCKS5 request")
	}
	host, err := readSOCKS5Address(reader, header[3])
	if err != nil {
		return 0, "", 0, err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return 0, "", 0, err
	}
	return header[1], normalizeNetworkHost(host), int(binary.BigEndian.Uint16(portBytes)), nil
}

func readSOCKS5Address(reader io.Reader, addressType byte) (string, error) {
	switch addressType {
	case 1:
		value := make([]byte, net.IPv4len)
		_, err := io.ReadFull(reader, value)
		return net.IP(value).String(), err
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(reader, length[:]); err != nil {
			return "", err
		}
		value := make([]byte, int(length[0]))
		_, err := io.ReadFull(reader, value)
		return string(value), err
	case 4:
		value := make([]byte, net.IPv6len)
		_, err := io.ReadFull(reader, value)
		return net.IP(value).String(), err
	default:
		return "", fmt.Errorf("unsupported SOCKS5 address type")
	}
}

func (proxy *managedNetworkProxy) handleSOCKS5Connect(client net.Conn, execution *managedNetworkExecution, host string, port int) {
	if strings.EqualFold(execution.policy.config.Mode, "limited") && port != 443 {
		denial := networkBlocked(host, port, NetworkApprovalSOCKS5TCP, "", execution.policy.config.Mode, "method_not_allowed", "deny", "mode_guard")
		execution.recordAndCancel(denial)
		_ = writeSOCKS5Reply(client, 2, nil)
		return
	}
	if denial := execution.evaluate(host, port, NetworkApprovalSOCKS5TCP, ""); denial != nil {
		_ = writeSOCKS5Reply(client, 2, nil)
		return
	}
	_ = client.SetDeadline(time.Time{})
	if err := writeSOCKS5Reply(client, 0, proxy.socksListener.Addr()); err != nil {
		return
	}
	if strings.EqualFold(execution.policy.config.Mode, "limited") || len(proxy.hooks[host]) > 0 {
		proxy.serveMITM(client, execution, host, port, NetworkApprovalSOCKS5TCP)
		return
	}
	upstream, err := proxy.dialTunnel(execution.ctx, execution, host, port, NetworkApprovalSOCKS5TCP)
	if err != nil {
		return
	}
	proxyTunnelContext(execution.ctx, client, upstream)
}

func (proxy *managedNetworkProxy) handleSOCKS5UDPAssociate(client net.Conn, execution *managedNetworkExecution, host string, port int) {
	listener := execution.udpEndpoint()
	if listener == nil || !proxy.config.EnableSOCKS5UDP {
		_ = writeSOCKS5Reply(client, 7, nil)
		return
	}
	if strings.EqualFold(execution.policy.config.Mode, "limited") {
		denial := networkBlocked(host, port, NetworkApprovalSOCKS5UDP, "", execution.policy.config.Mode, "method_not_allowed", "deny", "mode_guard")
		execution.recordAndCancel(denial)
		_ = writeSOCKS5Reply(client, 2, nil)
		return
	}
	if !execution.beginUDPAssociation() {
		_ = writeSOCKS5Reply(client, 1, nil)
		return
	}
	defer execution.endUDPAssociation()
	if err := writeSOCKS5Reply(client, 0, listener.LocalAddr()); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	_, _ = io.Copy(io.Discard, client)
}

func (proxy *managedNetworkProxy) serveSOCKS5UDP(execution *managedNetworkExecution) {
	listener := execution.udpEndpoint()
	if listener == nil {
		return
	}
	buffer := make([]byte, 65535)
	for {
		count, client, err := listener.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		if !execution.hasUDPAssociation() {
			continue
		}
		packet := append([]byte(nil), buffer[:count]...)
		go proxy.handleSOCKS5UDPPacket(execution, listener, client, packet)
	}
}

func (proxy *managedNetworkProxy) handleSOCKS5UDPPacket(execution *managedNetworkExecution, listener *net.UDPConn, client *net.UDPAddr, packet []byte) {
	if execution == nil || len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return
	}
	reader := bytesReader(packet[3:])
	addressType, err := reader.ReadByte()
	if err != nil {
		return
	}
	host, err := readSOCKS5Address(reader, addressType)
	if err != nil {
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	host = normalizeNetworkHost(host)
	if denial := execution.evaluate(host, port, NetworkApprovalSOCKS5UDP, ""); denial != nil {
		return
	}
	upstream, err := proxy.dialCheckedUDP(execution.ctx, execution, host, port)
	if err != nil {
		return
	}
	defer upstream.Close()
	_ = upstream.SetDeadline(time.Now().Add(30 * time.Second))
	payload, _ := io.ReadAll(reader)
	if _, err := upstream.Write(payload); err != nil {
		return
	}
	response := make([]byte, 65507)
	count, source, err := upstream.ReadFromUDP(response)
	if err != nil {
		return
	}
	header := encodeSOCKS5UDPHeader(source.IP.String(), source.Port)
	_, _ = listener.WriteToUDP(append(header, response[:count]...), client)
}

func (execution *managedNetworkExecution) beginUDPAssociation() bool {
	if execution == nil {
		return false
	}
	execution.mu.Lock()
	defer execution.mu.Unlock()
	if execution.udpAssociated || execution.udpListener == nil {
		return false
	}
	execution.udpAssociated = true
	return true
}

func (execution *managedNetworkExecution) endUDPAssociation() {
	if execution == nil {
		return
	}
	execution.mu.Lock()
	execution.udpAssociated = false
	execution.mu.Unlock()
}

func (execution *managedNetworkExecution) hasUDPAssociation() bool {
	if execution == nil {
		return false
	}
	execution.mu.Lock()
	defer execution.mu.Unlock()
	return execution.udpAssociated
}

func (execution *managedNetworkExecution) udpEndpoint() *net.UDPConn {
	if execution == nil {
		return nil
	}
	execution.mu.Lock()
	defer execution.mu.Unlock()
	return execution.udpListener
}

func (execution *managedNetworkExecution) closeUDP() {
	if execution == nil {
		return
	}
	execution.mu.Lock()
	listener := execution.udpListener
	execution.udpListener = nil
	execution.udpAssociated = false
	execution.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
}

func writeSOCKS5Reply(writer io.Writer, status byte, address net.Addr) error {
	host := "0.0.0.0"
	port := 0
	if address != nil {
		host, port = splitNetworkTarget(address.String())
	}
	encoded := encodeSOCKS5Address(host, port)
	_, err := writer.Write(append([]byte{socksVersion5, status, 0}, encoded...))
	return err
}

func encodeSOCKS5UDPHeader(host string, port int) []byte {
	return append([]byte{0, 0, 0}, encodeSOCKS5Address(host, port)...)
}

func encodeSOCKS5Address(host string, port int) []byte {
	var out []byte
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			out = append([]byte{1}, v4...)
		} else {
			out = append([]byte{4}, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			host = host[:255]
		}
		out = append([]byte{3, byte(len(host))}, []byte(host)...)
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	return append(out, portBytes...)
}

func containsByte(values []byte, target byte) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type byteSliceReader struct {
	data []byte
	off  int
}

func bytesReader(data []byte) *byteSliceReader { return &byteSliceReader{data: data} }

func (r *byteSliceReader) Read(buffer []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	count := copy(buffer, r.data[r.off:])
	r.off += count
	return count, nil
}

func (r *byteSliceReader) ReadByte() (byte, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	value := r.data[r.off]
	r.off++
	return value, nil
}

type compiledNetworkHooks map[string][]compiledNetworkHook

type compiledNetworkHook struct {
	methods []string
	paths   []networkValueMatcher
	query   map[string][]networkValueMatcher
	headers map[string][]networkValueMatcher
	action  compiledNetworkAction
}

type compiledNetworkAction struct {
	strip  []string
	inject map[string]string
}

type networkValueMatcher struct {
	literal string
	regex   *regexp.Regexp
	prefix  bool
}

func compileNetworkHooks(config *appcfg.NetworkMITMConfig) (compiledNetworkHooks, error) {
	out := compiledNetworkHooks{}
	if config == nil {
		return out, nil
	}
	for name, raw := range config.Hooks {
		host := normalizeNetworkHost(raw.Host)
		if host == "" || strings.Contains(host, "*") {
			return nil, fmt.Errorf("network MITM hook %q requires an exact host", name)
		}
		if len(raw.Methods) == 0 || len(raw.PathPrefixes) == 0 || len(raw.Action) == 0 {
			return nil, fmt.Errorf("network MITM hook %q requires methods, path_prefixes, and action", name)
		}
		if raw.Body != nil {
			return nil, fmt.Errorf("network MITM hook %q body matching is not supported", name)
		}
		hook := compiledNetworkHook{query: map[string][]networkValueMatcher{}, headers: map[string][]networkValueMatcher{}}
		for _, method := range raw.Methods {
			method = strings.ToUpper(strings.TrimSpace(method))
			if method == "" {
				return nil, fmt.Errorf("network MITM hook %q contains an empty method", name)
			}
			hook.methods = append(hook.methods, method)
		}
		for _, pattern := range raw.PathPrefixes {
			matcher, err := compileNetworkMatcher(pattern, true)
			if err != nil || pattern == "" || pattern == "literal:" {
				return nil, fmt.Errorf("network MITM hook %q has invalid path matcher %q", name, pattern)
			}
			hook.paths = append(hook.paths, matcher)
		}
		for key, values := range raw.Query {
			if key == "" || len(values) == 0 {
				return nil, fmt.Errorf("network MITM hook %q has an invalid query constraint", name)
			}
			matchers, err := compileNetworkMatchers(values, false)
			if err != nil {
				return nil, fmt.Errorf("network MITM hook %q query %q: %w", name, key, err)
			}
			hook.query[key] = matchers
		}
		for key, values := range raw.Headers {
			key = http.CanonicalHeaderKey(key)
			if !validHTTPHeaderName(key) {
				return nil, fmt.Errorf("network MITM hook %q has an invalid header constraint", name)
			}
			matchers, err := compileNetworkMatchers(values, false)
			if err != nil {
				return nil, fmt.Errorf("network MITM hook %q header %q: %w", name, key, err)
			}
			hook.headers[key] = matchers
		}
		for _, actionName := range raw.Action {
			action, ok := config.Actions[actionName]
			if !ok {
				continue
			}
			compiled, err := compileNetworkAction(action)
			if err != nil {
				return nil, fmt.Errorf("network MITM action %q: %w", actionName, err)
			}
			hook.action.strip = append(hook.action.strip, compiled.strip...)
			if hook.action.inject == nil {
				hook.action.inject = map[string]string{}
			}
			for header, value := range compiled.inject {
				hook.action.inject[header] = value
			}
		}
		out[host] = append(out[host], hook)
	}
	return out, nil
}

func compileNetworkAction(raw appcfg.NetworkMITMAction) (compiledNetworkAction, error) {
	out := compiledNetworkAction{inject: map[string]string{}}
	for _, name := range raw.StripRequestHeaders {
		name = http.CanonicalHeaderKey(name)
		if !validHTTPHeaderName(name) {
			return compiledNetworkAction{}, fmt.Errorf("invalid header name %q", name)
		}
		out.strip = append(out.strip, name)
	}
	for _, header := range raw.InjectRequestHeaders {
		name := http.CanonicalHeaderKey(header.Name)
		if !validHTTPHeaderName(name) {
			return compiledNetworkAction{}, fmt.Errorf("invalid header name %q", header.Name)
		}
		var secret string
		switch {
		case header.SecretEnvVar != nil && header.SecretFile == nil:
			key := *header.SecretEnvVar
			if strings.TrimSpace(key) == "" {
				return compiledNetworkAction{}, fmt.Errorf("secret_env_var must not be empty")
			}
			value, ok := os.LookupEnv(key)
			if !ok {
				return compiledNetworkAction{}, fmt.Errorf("missing required environment variable %s", key)
			}
			secret = value
		case header.SecretFile != nil && header.SecretEnvVar == nil:
			path := *header.SecretFile
			if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
				return compiledNetworkAction{}, fmt.Errorf("secret_file must be an absolute path")
			}
			value, err := os.ReadFile(path)
			if err != nil {
				return compiledNetworkAction{}, err
			}
			secret = strings.TrimSpace(string(value))
		default:
			return compiledNetworkAction{}, fmt.Errorf("expected exactly one of secret_env_var or secret_file")
		}
		prefix := ""
		if header.Prefix != nil {
			prefix = *header.Prefix
		}
		value := prefix + secret
		if strings.ContainsAny(value, "\r\n") {
			return compiledNetworkAction{}, fmt.Errorf("invalid value for injected header %q", name)
		}
		out.inject[name] = value
	}
	if len(out.strip) == 0 && len(out.inject) == 0 {
		return compiledNetworkAction{}, fmt.Errorf("action must define at least one operation")
	}
	return out, nil
}

func (hooks compiledNetworkHooks) evaluate(host string, request *http.Request) (compiledNetworkAction, bool, bool) {
	entries, hooked := hooks[normalizeNetworkHost(host)]
	if !hooked {
		return compiledNetworkAction{}, false, false
	}
	for _, hook := range entries {
		if hook.matches(request) {
			return hook.action, true, true
		}
	}
	return compiledNetworkAction{}, false, true
}

func (hook compiledNetworkHook) matches(request *http.Request) bool {
	method := strings.ToUpper(request.Method)
	requestPath := request.URL.EscapedPath()
	if !containsString(hook.methods, method) || !safeAuthorizationPath(requestPath) {
		return false
	}
	pathOK := false
	for _, matcher := range hook.paths {
		if matcher.matches(requestPath) {
			pathOK = true
			break
		}
	}
	if !pathOK {
		return false
	}
	for name, matchers := range hook.query {
		if !anyNetworkValueMatches(matchers, request.URL.Query()[name]) {
			return false
		}
	}
	for name, matchers := range hook.headers {
		values := request.Header.Values(name)
		if len(values) == 0 || (len(matchers) > 0 && !anyNetworkValueMatches(matchers, values)) {
			return false
		}
	}
	return true
}

func (action compiledNetworkAction) apply(headers http.Header) {
	for _, name := range action.strip {
		headers.Del(name)
	}
	for name, value := range action.inject {
		headers.Set(name, value)
	}
}

func compileNetworkMatchers(values []string, path bool) ([]networkValueMatcher, error) {
	out := make([]networkValueMatcher, 0, len(values))
	for _, value := range values {
		matcher, err := compileNetworkMatcher(value, path)
		if err != nil {
			return nil, err
		}
		out = append(out, matcher)
	}
	return out, nil
}

func compileNetworkMatcher(value string, path bool) (networkValueMatcher, error) {
	if literal, ok := strings.CutPrefix(value, "literal:"); ok {
		return networkValueMatcher{literal: literal, prefix: path}, nil
	}
	pattern, glob := strings.CutPrefix(value, "pattern:")
	if !glob {
		return networkValueMatcher{literal: value, prefix: path}, nil
	}
	if pattern == "" {
		return networkValueMatcher{}, fmt.Errorf("glob pattern must not be empty")
	}
	regex, err := compileNetworkGlob(pattern, path)
	if err != nil {
		return networkValueMatcher{}, err
	}
	return networkValueMatcher{regex: regex}, nil
}

func compileNetworkGlob(pattern string, path bool) (*regexp.Regexp, error) {
	characters := []rune(pattern)
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(characters); i++ {
		switch characters[i] {
		case '*':
			if i+1 < len(characters) && characters[i+1] == '*' {
				i++
				b.WriteString(".*")
			} else if path {
				b.WriteString("[^/]*")
			} else {
				b.WriteString(".*")
			}
		case '?':
			if path {
				b.WriteString("[^/]")
			} else {
				b.WriteByte('.')
			}
		case '\\':
			if i+1 >= len(characters) {
				return nil, fmt.Errorf("dangling glob escape")
			}
			i++
			b.WriteString(regexp.QuoteMeta(string(characters[i])))
		case '[':
			end, class, err := compileNetworkGlobClass(characters, i+1)
			if err != nil {
				return nil, err
			}
			b.WriteString(class)
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(characters[i])))
		}
	}
	b.WriteByte('$')
	return regexp.Compile(b.String())
}

func compileNetworkGlobClass(pattern []rune, start int) (int, string, error) {
	if start >= len(pattern) {
		return 0, "", fmt.Errorf("unterminated glob character class")
	}
	var b strings.Builder
	b.WriteByte('[')
	if pattern[start] == '!' || pattern[start] == '^' {
		b.WriteByte('^')
		start++
	}
	hasValue := false
	for index := start; index < len(pattern); index++ {
		switch pattern[index] {
		case ']':
			if !hasValue {
				return 0, "", fmt.Errorf("empty glob character class")
			}
			b.WriteByte(']')
			return index, b.String(), nil
		case '\\':
			if index+1 >= len(pattern) {
				return 0, "", fmt.Errorf("dangling glob escape")
			}
			index++
			b.WriteByte('\\')
			b.WriteRune(pattern[index])
			hasValue = true
		default:
			if pattern[index] == '^' && !hasValue {
				b.WriteString("\\^")
			} else {
				b.WriteRune(pattern[index])
			}
			hasValue = true
		}
	}
	return 0, "", fmt.Errorf("unterminated glob character class")
}

func (matcher networkValueMatcher) matches(value string) bool {
	if matcher.regex != nil {
		return matcher.regex.MatchString(value)
	}
	if matcher.prefix {
		return strings.HasPrefix(value, matcher.literal)
	}
	return value == matcher.literal
}

func anyNetworkValueMatches(matchers []networkValueMatcher, values []string) bool {
	for _, value := range values {
		for _, matcher := range matchers {
			if matcher.matches(value) {
				return true
			}
		}
	}
	return false
}

func safeAuthorizationPath(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if !safeAuthorizationPathSegment(segment) {
			return false
		}
	}
	return true
}

func safeAuthorizationPathSegment(segment string) bool {
	bytes := []byte(segment)
	decodedDots := 0
	hasNonDot := false
	for index := 0; index < len(bytes); {
		switch bytes[index] {
		case '.':
			decodedDots++
			index++
		case '\\':
			return false
		case '%':
			if index+2 >= len(bytes) {
				return false
			}
			high, highOK := decodeNetworkHexDigit(bytes[index+1])
			low, lowOK := decodeNetworkHexDigit(bytes[index+2])
			if !highOK || !lowOK {
				return false
			}
			decoded := high<<4 | low
			switch decoded {
			case '%', '/', '\\':
				return false
			case '.':
				decodedDots++
			default:
				hasNonDot = true
			}
			index += 3
		default:
			hasNonDot = true
			index++
		}
	}
	return hasNonDot || (decodedDots != 1 && decodedDots != 2)
}

func decodeNetworkHexDigit(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		if !(ch >= 'A' && ch <= 'Z') && !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", ch) {
			return false
		}
	}
	return true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type managedCertificateAuthority struct {
	cert       *x509.Certificate
	key        *ecdsa.PrivateKey
	pem        []byte
	bundlePath string
	mu         sync.Mutex
	leaves     map[string]*tls.Certificate
}

func newManagedCertificateAuthority() (*managedCertificateAuthority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomCertificateSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Forebrain Harness Managed Network CA"},
		NotBefore:    now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	dir, err := os.MkdirTemp("", "forebrain-network-ca-")
	if err != nil {
		return nil, err
	}
	bundlePath := filepath.Join(dir, "ca-bundle.pem")
	if err := os.WriteFile(bundlePath, certPEM, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &managedCertificateAuthority{cert: cert, key: key, pem: certPEM, bundlePath: bundlePath, leaves: map[string]*tls.Certificate{}}, nil
}

func (ca *managedCertificateAuthority) close() {
	if ca != nil && ca.bundlePath != "" {
		_ = os.RemoveAll(filepath.Dir(ca.bundlePath))
	}
}

func (ca *managedCertificateAuthority) certificateForHost(host string) (*tls.Certificate, error) {
	if ca == nil || ca.cert == nil || ca.key == nil {
		return nil, fmt.Errorf("managed certificate authority is unavailable")
	}
	host = normalizeNetworkHost(host)
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if cert := ca.leaves[host]; cert != nil {
		return cert, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomCertificateSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(stringsBeforePercent(host)); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca.pem...)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	ca.leaves[host] = &tlsCert
	return &tlsCert, nil
}

func randomCertificateSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func stringsBeforePercent(value string) string {
	for i := range value {
		if value[i] == '%' {
			return value[:i]
		}
	}
	return value
}

type windowsNetworkPolicy struct {
	Offline          bool
	AllowAllLoopback bool
	AllowedTCPPorts  []int
	AllowedUDPPorts  []int
}

func windowsNetworkPolicyForRequest(req CommandRequest) windowsNetworkPolicy {
	if req.AllowNetwork && req.ManagedNetwork == nil {
		return windowsNetworkPolicy{}
	}
	policy := windowsNetworkPolicy{Offline: true}
	if req.ManagedNetwork == nil {
		return policy
	}
	policy.AllowAllLoopback = req.ManagedNetwork.AllowLocalBinding
	policy.AllowedTCPPorts = normalizedWindowsProxyPorts(
		req.ManagedNetwork.HTTPPort,
		req.ManagedNetwork.SOCKSPort,
	)
	policy.AllowedUDPPorts = normalizedWindowsProxyPorts(req.ManagedNetwork.SOCKSUDPPort)
	return policy
}

func normalizedWindowsProxyPorts(ports ...int) []int {
	seen := map[int]struct{}{}
	for _, port := range ports {
		if port > 0 && port <= 65535 {
			seen[port] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for port := range seen {
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}

// blockedWindowsTCPPorts returns a Windows Firewall port expression covering
// every port except the loopback proxy ports. An empty string means no TCP
// loopback block is needed because every port is allowed.
func blockedWindowsTCPPorts(allowed []int) string {
	allowed = normalizedWindowsProxyPorts(allowed...)
	if len(allowed) == 0 {
		return "1-65535"
	}
	var ranges []string
	start := 1
	for _, port := range allowed {
		if port > start {
			ranges = append(ranges, formatWindowsPortRange(start, port-1))
		}
		start = port + 1
	}
	if start <= 65535 {
		ranges = append(ranges, formatWindowsPortRange(start, 65535))
	}
	return strings.Join(ranges, ",")
}

func formatWindowsPortRange(first, last int) string {
	if first == last {
		return fmt.Sprintf("%d", first)
	}
	return fmt.Sprintf("%d-%d", first, last)
}
