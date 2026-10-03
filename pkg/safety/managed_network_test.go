package safety

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func TestManagedProxyRequiresExecutionAuthentication(t *testing.T) {
	proxy, token := newManagedProxyTestInstance(t)
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(target.Close)

	unauthenticatedProxy := &url.URL{Scheme: "http", Host: proxy.httpListener.Addr().String()}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(unauthenticatedProxy)}}
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("unauthenticated status=%d", response.StatusCode)
	}
	if response.Header.Get("Proxy-Authenticate") == "" {
		t.Fatal("missing Proxy-Authenticate challenge")
	}

	conn, err := net.Dial("tcp", proxy.socksListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{socksVersion5, 1, socksAuthNone}); err != nil {
		t.Fatal(err)
	}
	var selection [2]byte
	if _, err := io.ReadFull(conn, selection[:]); err != nil {
		t.Fatal(err)
	}
	if selection != [2]byte{socksVersion5, socksAuthNoMethods} {
		t.Fatalf("unauthenticated method selection=%v", selection)
	}

	invalid, err := net.Dial("tcp", proxy.socksListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer invalid.Close()
	reader := bufio.NewReader(invalid)
	if _, err := invalid.Write([]byte{socksVersion5, 1, socksAuthUserPass}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(reader, selection[:]); err != nil {
		t.Fatal(err)
	}
	credentials := []byte{1, 7}
	credentials = append(credentials, []byte("invalid")...)
	credentials = append(credentials, 0)
	if _, err := invalid.Write(credentials); err != nil {
		t.Fatal(err)
	}
	var authStatus [2]byte
	if _, err := io.ReadFull(reader, authStatus[:]); err != nil {
		t.Fatal(err)
	}
	if authStatus != [2]byte{1, 1} {
		t.Fatalf("invalid credential status=%v", authStatus)
	}

	authenticatedProxy := &url.URL{Scheme: "http", Host: proxy.httpListener.Addr().String(), User: url.User(token)}
	authenticatedClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(authenticatedProxy)}}
	authenticatedResponse, err := authenticatedClient.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer authenticatedResponse.Body.Close()
	body, err := io.ReadAll(authenticatedResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if authenticatedResponse.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("authenticated response status=%d body=%q", authenticatedResponse.StatusCode, body)
	}
}

func TestManagedProxyRetainedLeaseSurvivesManagerLeaseReplacement(t *testing.T) {
	config := appcfg.EffectiveNetworkProxyConfig{
		Enabled: true, ProxyURL: "127.0.0.1:0", Mode: "full",
		Domains: map[string]appcfg.NetworkAccess{"example.com": appcfg.NetworkAccessAllow},
	}
	managerLease, err := acquireManagedNetworkProxy(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(managerLease.close)
	commandLease := managerLease.retain()
	if commandLease == nil || commandLease.proxy != managerLease.proxy {
		t.Fatal("failed to retain managed proxy lease")
	}
	t.Cleanup(commandLease.close)
	proxy := commandLease.proxy
	managerLease.close()
	proxy.mu.RLock()
	closed := proxy.closed
	proxy.mu.RUnlock()
	if closed {
		commandLease.close()
		t.Fatal("proxy closed while a command lease was active")
	}
	commandLease.close()
	proxy.mu.RLock()
	closed = proxy.closed
	proxy.mu.RUnlock()
	if !closed {
		t.Fatal("proxy remained open after its final lease closed")
	}
}

func TestManagedProxyHTTPConnectSOCKSTCPAndUDP(t *testing.T) {
	proxy, token := newManagedProxyTestInstance(t)
	tcpAddress := startManagedProxyTCPEcho(t)
	udpAddress := startManagedProxyUDPEcho(t)

	t.Run("HTTP CONNECT", func(t *testing.T) {
		conn, err := net.Dial("tcp", proxy.httpListener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		auth := base64.StdEncoding.EncodeToString([]byte(token + ":"))
		if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", tcpAddress, tcpAddress, auth); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(conn)
		response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT status=%d", response.StatusCode)
		}
		assertManagedProxyEcho(t, conn, reader)
	})

	t.Run("SOCKS5 TCP", func(t *testing.T) {
		conn, reader := dialAuthenticatedSOCKS5(t, proxy.socksListener.Addr().String(), token)
		defer conn.Close()
		host, port := splitNetworkTarget(tcpAddress)
		request := append([]byte{socksVersion5, socksCommandConnect, 0}, encodeSOCKS5Address(host, port)...)
		if _, err := conn.Write(request); err != nil {
			t.Fatal(err)
		}
		status, _, _ := readSOCKS5Reply(t, reader)
		if status != 0 {
			t.Fatalf("SOCKS5 CONNECT status=%d", status)
		}
		assertManagedProxyEcho(t, conn, reader)
	})

	t.Run("SOCKS5 UDP", func(t *testing.T) {
		udpClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer udpClient.Close()
		control, reader := dialAuthenticatedSOCKS5(t, proxy.socksListener.Addr().String(), token)
		defer control.Close()
		clientAddress := udpClient.LocalAddr().(*net.UDPAddr)
		// Deliberately advertise a different port to exercise the safe IP-only
		// attribution used by the Linux network-namespace packet bridge.
		advertisedPort := clientAddress.Port + 1
		if advertisedPort > 65535 {
			advertisedPort = clientAddress.Port - 1
		}
		request := append([]byte{socksVersion5, socksCommandUDP, 0}, encodeSOCKS5Address(clientAddress.IP.String(), advertisedPort)...)
		if _, err := control.Write(request); err != nil {
			t.Fatal(err)
		}
		status, relayHost, relayPort := readSOCKS5Reply(t, reader)
		if status != 0 {
			t.Fatalf("SOCKS5 UDP ASSOCIATE status=%d", status)
		}
		targetHost, targetPort := splitNetworkTarget(udpAddress)
		packet := append(encodeSOCKS5UDPHeader(targetHost, targetPort), []byte("ping")...)
		relay := &net.UDPAddr{IP: net.ParseIP(relayHost), Port: relayPort}
		if _, err := udpClient.WriteToUDP(packet, relay); err != nil {
			t.Fatal(err)
		}
		if err := udpClient.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 1024)
		count, _, err := udpClient.ReadFromUDP(response)
		if err != nil {
			t.Fatal(err)
		}
		responseReader := bufio.NewReader(bytes.NewReader(response[:count]))
		var reserved [3]byte
		if _, err := io.ReadFull(responseReader, reserved[:]); err != nil || reserved != [3]byte{} {
			t.Fatalf("invalid UDP response header=%v err=%v", reserved, err)
		}
		addressType, err := responseReader.ReadByte()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := readSOCKS5Address(responseReader, addressType); err != nil {
			t.Fatal(err)
		}
		var portBytes [2]byte
		if _, err := io.ReadFull(responseReader, portBytes[:]); err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(responseReader)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != "ping" {
			t.Fatalf("UDP payload=%q", payload)
		}
	})
}

func TestManagedProxyRejectsAmbiguousUDPAssociation(t *testing.T) {
	proxy, token := newManagedProxyTestInstance(t)
	first, firstReader := dialAuthenticatedSOCKS5(t, proxy.socksListener.Addr().String(), token)
	defer first.Close()
	request := append([]byte{socksVersion5, socksCommandUDP, 0}, encodeSOCKS5Address("0.0.0.0", 0)...)
	if _, err := first.Write(request); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := readSOCKS5Reply(t, firstReader); status != 0 {
		t.Fatalf("first UDP ASSOCIATE status=%d", status)
	}

	second, secondReader := dialAuthenticatedSOCKS5(t, proxy.socksListener.Addr().String(), token)
	defer second.Close()
	if _, err := second.Write(request); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := readSOCKS5Reply(t, secondReader); status == 0 {
		t.Fatal("ambiguous UDP association was accepted")
	}
}

func TestManagedProxyUDPUsesPerExecutionEndpoints(t *testing.T) {
	proxy, firstToken := newManagedProxyTestInstance(t)
	secondToken, _, secondCommand, err := proxy.register(
		context.Background(), networkCommandPolicy{config: proxy.config}, nil, NetworkApprovalRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proxy.unregister(secondToken) })
	first := proxy.execution(firstToken)
	second := proxy.execution(secondToken)
	if first == nil || first.udpListener == nil || second == nil || second.udpListener == nil {
		t.Fatal("registered executions are missing UDP endpoints")
	}
	firstPort := first.udpListener.LocalAddr().(*net.UDPAddr).Port
	if firstPort == secondCommand.SOCKSUDPPort {
		t.Fatalf("executions shared UDP endpoint %d", firstPort)
	}
}

func TestManagedProxyClosesAuthenticatedSOCKSTunnelOnUnregister(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := target.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	proxy, token := newManagedProxyTestInstance(t)
	client, reader := dialAuthenticatedSOCKS5(t, proxy.socksListener.Addr().String(), token)
	defer client.Close()
	host, port := splitNetworkTarget(target.Addr().String())
	request := append([]byte{socksVersion5, socksCommandConnect, 0}, encodeSOCKS5Address(host, port)...)
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := readSOCKS5Reply(t, reader); status != 0 {
		t.Fatalf("SOCKS5 CONNECT status=%d", status)
	}
	select {
	case upstream := <-accepted:
		defer upstream.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not connect to the target")
	}

	proxy.unregister(token)
	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if _, err := client.Read(one[:]); err == nil {
		t.Fatal("authenticated tunnel remained open after its execution was unregistered")
	}
}

func newManagedProxyTestInstance(t *testing.T) (*managedNetworkProxy, string) {
	t.Helper()
	config := appcfg.EffectiveNetworkProxyConfig{
		Enabled: true, ProxyURL: "127.0.0.1:0", EnableSOCKS5: true,
		SOCKSURL: "127.0.0.1:0", EnableSOCKS5UDP: true, Mode: "full",
		Domains: map[string]appcfg.NetworkAccess{"127.0.0.1": appcfg.NetworkAccessAllow},
	}
	proxy, err := startManagedNetworkProxy(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.close)
	token, _, _, err := proxy.register(context.Background(), networkCommandPolicy{config: config}, nil, NetworkApprovalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proxy.unregister(token) })
	return proxy, token
}

func dialAuthenticatedSOCKS5(t *testing.T, address, token string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	if _, err := conn.Write([]byte{socksVersion5, 1, socksAuthUserPass}); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	var selection [2]byte
	if _, err := io.ReadFull(reader, selection[:]); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if selection != [2]byte{socksVersion5, socksAuthUserPass} {
		conn.Close()
		t.Fatalf("SOCKS5 method selection=%v", selection)
	}
	credentials := []byte{1, byte(len(token))}
	credentials = append(credentials, []byte(token)...)
	credentials = append(credentials, 0)
	if _, err := conn.Write(credentials); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	var status [2]byte
	if _, err := io.ReadFull(reader, status[:]); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if status != [2]byte{1, 0} {
		conn.Close()
		t.Fatalf("SOCKS5 auth status=%v", status)
	}
	return conn, reader
}

func readSOCKS5Reply(t *testing.T, reader *bufio.Reader) (byte, string, int) {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		t.Fatal(err)
	}
	if header[0] != socksVersion5 || header[2] != 0 {
		t.Fatalf("invalid SOCKS5 reply header=%v", header)
	}
	host, err := readSOCKS5Address(reader, header[3])
	if err != nil {
		t.Fatal(err)
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(reader, portBytes[:]); err != nil {
		t.Fatal(err)
	}
	return header[1], host, int(binary.BigEndian.Uint16(portBytes[:]))
}

func startManagedProxyTCPEcho(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func startManagedProxyUDPEcho(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buffer := make([]byte, 65535)
		for {
			count, client, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(buffer[:count], client)
		}
	}()
	return conn.LocalAddr().String()
}

func assertManagedProxyEcho(t *testing.T, conn net.Conn, reader io.Reader) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	var response [4]byte
	if _, err := io.ReadFull(reader, response[:]); err != nil {
		t.Fatal(err)
	}
	if string(response[:]) != "ping" {
		t.Fatalf("echo=%q", response)
	}
}

func TestNetworkSessionRuleUsesExactProtocolAndPort(t *testing.T) {
	policy := networkCommandPolicy{
		config: appcfg.EffectiveNetworkProxyConfig{Enabled: true, Mode: "full", AllowLocalBinding: true},
		rules: []networkPermissionRule{{
			protocol: NetworkApprovalHTTPS, host: "api.example.com", port: 443,
			allow: true, source: SourceSession,
		}},
	}
	if denial := policy.evaluate("api.example.com", 443, NetworkApprovalHTTPS, "GET"); denial != nil {
		t.Fatalf("exact target denied: %+v", denial)
	}
	if denial := policy.evaluate("api.example.com", 80, NetworkApprovalHTTP, "GET"); denial == nil || denial.Reason != "not_allowed" {
		t.Fatalf("different target must be denied: %+v", denial)
	}
}

func TestResolvedPrivateAddressRequiresMatchingExplicitLocalTarget(t *testing.T) {
	policy := networkCommandPolicy{
		config: appcfg.EffectiveNetworkProxyConfig{Enabled: true, Mode: "full", Domains: map[string]appcfg.NetworkAccess{
			"public-name.example": appcfg.NetworkAccessAllow,
		}},
	}
	if policy.allowsResolvedNonPublic("public-name.example", 443, NetworkApprovalHTTPS, netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("a hostname resolving to loopback must not pass the connection-time check")
	}
	policy.config.Domains["127.0.0.1"] = appcfg.NetworkAccessAllow
	if !policy.allowsResolvedNonPublic("127.0.0.1", 443, NetworkApprovalHTTPS, netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("an explicitly allowed matching local literal must pass")
	}
}

func TestCheckedDialRejectsPrivateResolutionBeforeConnecting(t *testing.T) {
	proxy := &managedNetworkProxy{
		lookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	}
	execution := &managedNetworkExecution{policy: networkCommandPolicy{
		config: appcfg.EffectiveNetworkProxyConfig{Enabled: true, Mode: "full", Domains: map[string]appcfg.NetworkAccess{
			"public-name.example": appcfg.NetworkAccessAllow,
		}},
	}}
	_, err := proxy.dialCheckedTarget(context.Background(), execution, "public-name.example", 443, NetworkApprovalHTTPS)
	if err == nil || !strings.Contains(err.Error(), "rejected by policy") {
		t.Fatalf("err=%v", err)
	}
	if execution.blocked == nil || execution.blocked.Reason != "not_allowed_local" {
		t.Fatalf("blocked=%+v", execution.blocked)
	}
}

func TestCheckedUDPDialRejectsPrivateResolutionBeforeConnecting(t *testing.T) {
	proxy := &managedNetworkProxy{
		lookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	}
	execution := &managedNetworkExecution{policy: networkCommandPolicy{
		config: appcfg.EffectiveNetworkProxyConfig{Enabled: true, Mode: "full", Domains: map[string]appcfg.NetworkAccess{
			"public-name.example": appcfg.NetworkAccessAllow,
		}},
	}}
	_, err := proxy.dialCheckedUDP(context.Background(), execution, "public-name.example", 53)
	if err == nil || !strings.Contains(err.Error(), "rejected by policy") {
		t.Fatalf("err=%v", err)
	}
	if execution.blocked == nil || execution.blocked.Protocol != NetworkApprovalSOCKS5UDP || execution.blocked.Reason != "not_allowed_local" {
		t.Fatalf("blocked=%+v", execution.blocked)
	}
}

func TestAbsoluteFormHostHeaderValidation(t *testing.T) {
	for _, test := range []struct {
		name, target, host string
		wantErr            bool
	}{
		{name: "matching default", target: "http://example.com/path", host: "example.com"},
		{name: "matching explicit", target: "http://example.com:8080/path", host: "example.com:8080"},
		{name: "different host", target: "http://allowed.example/path", host: "blocked.example", wantErr: true},
		{name: "missing nondefault port", target: "http://example.com:8080/path", host: "example.com", wantErr: true},
		{name: "extra default port", target: "http://example.com/path", host: "example.com:80", wantErr: true},
		{name: "omitted explicit default port", target: "http://example.com:80/path", host: "example.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, err := url.Parse(test.target)
			if err != nil {
				t.Fatal(err)
			}
			err = validateAbsoluteFormHostHeader(&http.Request{URL: target, Host: test.host})
			if (err != nil) != test.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestPersistedNetworkRuleUsesExactProtocolAndPort(t *testing.T) {
	policy := networkCommandPolicy{
		config: appcfg.EffectiveNetworkProxyConfig{Enabled: true, Mode: "full", AllowLocalBinding: true},
		rules: []networkPermissionRule{{
			protocol: NetworkApprovalHTTPS, host: "api.example.com", port: 443,
			allow: true, source: SourceLocalSettings,
		}},
	}
	if denial := policy.evaluate("api.example.com", 443, NetworkApprovalHTTPS, "GET"); denial != nil {
		t.Fatalf("persisted exact target denied: %+v", denial)
	}
	if denial := policy.evaluate("api.example.com", 80, NetworkApprovalHTTP, "GET"); denial == nil || denial.Reason != "not_allowed" {
		t.Fatalf("persisted rule widened across protocols: %+v", denial)
	}
	if denial := policy.evaluate("api.example.com", 8443, NetworkApprovalHTTPS, "GET"); denial == nil || denial.Reason != "not_allowed" {
		t.Fatalf("persisted rule widened across ports: %+v", denial)
	}
}

func TestSessionApprovalCannotOverrideLocalAddressGuard(t *testing.T) {
	policy := networkCommandPolicy{
		config: appcfg.EffectiveNetworkProxyConfig{Enabled: true, Mode: "full"},
		rules: []networkPermissionRule{{
			protocol: NetworkApprovalHTTP, host: "127.0.0.1", port: 80,
			allow: true, source: SourceSession,
		}},
	}
	if denial := policy.evaluate("127.0.0.1", 80, NetworkApprovalHTTP, "GET"); denial == nil || denial.Reason != "not_allowed_local" {
		t.Fatalf("local target guard was bypassed: %+v", denial)
	}
}

// A name the resolver could not answer for is not known to be local: it is
// an unlisted host like any other, so the user is asked about it rather than
// refused outright. The dial still refuses any non-public address it resolves
// to then.
func TestUnresolvedHostIsAskedAboutNotDeniedAsLocal(t *testing.T) {
	policy := networkCommandPolicy{config: appcfg.EffectiveNetworkProxyConfig{Enabled: true, Mode: "full"}, canAsk: true}
	denial := policy.evaluate("no-such-host.invalid", 443, NetworkApprovalHTTPS, "GET")
	if denial == nil || denial.Reason != "not_allowed" || denial.Decision != "ask" {
		t.Fatalf("denial = %+v, want an approval question about an unlisted host", denial)
	}
}

func TestDisabledManagedProxyFailsClosed(t *testing.T) {
	policy := networkCommandPolicy{config: appcfg.EffectiveNetworkProxyConfig{Enabled: false, Mode: "full"}, canAsk: true}
	if denial := policy.evaluate("example.com", 80, NetworkApprovalHTTP, "GET"); denial == nil || denial.Reason != "proxy_disabled" || denial.Decision != "deny" {
		t.Fatalf("denial=%+v", denial)
	}
}

func TestNetworkRulesIgnoreUnscopedSessionStoreEntries(t *testing.T) {
	snapshot := Snapshot{Rules: map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue{
		SourceSession:       {BehaviorAllow: {{ToolName: NetworkAccessPermissionTool, RuleContent: "https://api.example.com:443"}}},
		SourceLocalSettings: {BehaviorAllow: {{ToolName: NetworkAccessPermissionTool, RuleContent: "https://persisted.example.com"}}},
	}}
	rules := networkRulesFromSnapshot(snapshot)
	if len(rules) != 1 || rules[0].host != "persisted.example.com" {
		t.Fatalf("rules=%+v", rules)
	}
}

func networkSnapshot(rules map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue) Snapshot {
	return Snapshot{Rules: rules}
}

// A repository can narrow egress but never open it: otherwise a trusted repo
// could grant itself network access simply by committing a rule.
func TestNetworkRulesIgnoreProjectAllowButHonourProjectDeny(t *testing.T) {
	// Rule content is "<protocol>://<host>"; anything else is not parsed at all.
	rule := func(host string) []PermissionRuleValue {
		return []PermissionRuleValue{{
			ToolName:    string(NetworkAccessPermissionTool),
			RuleContent: "https://" + host,
		}}
	}
	snapshot := networkSnapshot(map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue{
		SourceProjectSettings: {
			BehaviorAllow: rule("evil.example.com"),
			BehaviorDeny:  rule("blocked.example.com"),
		},
		SourceLocalSettings: {
			BehaviorAllow: rule("allowed.example.com"),
		},
	})

	got := networkRulesFromSnapshot(snapshot)

	var sawProjectAllow, sawProjectDeny, sawLocalAllow bool
	for _, r := range got {
		switch {
		case r.source == SourceProjectSettings && r.allow:
			sawProjectAllow = true
		case r.source == SourceProjectSettings && !r.allow:
			sawProjectDeny = true
		case r.source == SourceLocalSettings && r.allow:
			sawLocalAllow = true
		}
	}
	if sawProjectAllow {
		t.Error("a project allow rule opened network egress")
	}
	if !sawProjectDeny {
		t.Error("a project deny rule should still narrow egress")
	}
	if !sawLocalAllow {
		t.Error("the operator's own allow rule was dropped")
	}
}
