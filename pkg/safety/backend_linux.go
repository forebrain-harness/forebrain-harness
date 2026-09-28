//go:build linux

package safety

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"golang.org/x/sys/unix"
)

func runBubblewrapCommand(ctx context.Context, cfg *appcfg.Root, req CommandRequest, workDir string) (CommandResult, error) {
	bwrapPath, err := lookBubblewrap()
	if err != nil {
		return CommandResult{}, &UnavailableError{Reason: "bubblewrap_not_found"}
	}
	markers, err := prepareLinuxMissingDenyMarkers(req)
	if err != nil {
		return CommandResult{}, err
	}
	defer cleanupLinuxMissingDenyMarkers(markers)
	for _, marker := range markers {
		req.DeniedReadablePaths = appendUniquePath(req.DeniedReadablePaths, marker.path)
	}
	var bridge *linuxNetworkBridge
	if req.ManagedNetwork != nil {
		bridge, err = prepareLinuxNetworkBridge(*req.ManagedNetwork)
		if err != nil {
			return CommandResult{}, err
		}
		defer bridge.close()
	}
	cmd := exec.Command(bwrapPath, buildBubblewrapArgsWithBridge(cfg, req, workDir, bridge)...)
	cmd.Env = home.SafeSubprocessEnv(nil, home.Options{ExplicitEnv: explicitCommandEnv(req.Env, nil)})
	capture := newCommandCapture(req)
	cmd.Stdout = capture.writer(OutputStreamStdout)
	cmd.Stderr = capture.writer(OutputStreamStderr)
	setupProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = capture.result()
		return CommandResult{ExitCode: 1}, err
	}
	err, stopErr := waitForCommandContext(ctx, cmd)
	return finalizeExec(capture.result(), err, stopErr)
}

func finalizeExec(res CommandResult, err, stopErr error) (CommandResult, error) {
	res.ExitCode = 0
	if err == nil {
		return res, nil
	}
	if errors.Is(stopErr, context.Canceled) || errors.Is(stopErr, context.DeadlineExceeded) {
		if errors.Is(stopErr, context.DeadlineExceeded) {
			res.ExitCode = 124
			return res, &TimeoutError{Result: res}
		}
		res.ExitCode = 1
		return res, context.Canceled
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			signal := int(status.Signal())
			res.ExitCode = 128 + signal
			return res, &SignalError{Signal: signal}
		}
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	res.ExitCode = 1
	return res, err
}

func lookBubblewrap() (string, error) {
	if p, err := exec.LookPath("bwrap"); err == nil {
		return p, nil
	}
	return exec.LookPath("bubblewrap")
}

func buildBubblewrapArgs(cfg *appcfg.Root, req CommandRequest, workDir string) []string {
	return buildBubblewrapArgsWithBridge(cfg, req, workDir, nil)
}

func buildBubblewrapArgsWithBridge(cfg *appcfg.Root, req CommandRequest, workDir string, bridge *linuxNetworkBridge) []string {
	args := bubblewrapBaseArgs(cfg, req, workDir)
	if bridge != nil {
		args = append(args, "--ro-bind", bridge.dir, bridge.dir)
		return append(args, bridge.helperPath, linuxNetworkBridgeArgument, bridge.encodedSpec, "/bin/sh", "-lc", req.Command)
	}
	return append(args, "/bin/sh", "-lc", req.Command)
}

func bubblewrapBaseArgs(cfg *appcfg.Root, req CommandRequest, workDir string) []string {
	req = constrainRequestToFilesystemProfile(req, workDir)
	args := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",
	}
	if !req.AllowNetwork || req.ManagedNetwork != nil {
		args = append(args, "--unshare-net")
	}
	if filesystemRulesContainRootRead(req) {
		args = append(args, "--ro-bind", "/", "/")
	} else {
		args = append(args, "--tmpfs", "/")
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev")
	args = appendFilesystemPolicyArgs(args, req)
	return append(args, "--chdir", workDir)
}

type filesystemMountAccess uint8

const (
	filesystemMountRead filesystemMountAccess = iota
	filesystemMountWrite
	filesystemMountDeny
)

type filesystemMountRule struct {
	path     string
	access   filesystemMountAccess
	priority uint8
}

var linuxPlatformDefaultReadRoots = []string{
	"/bin", "/sbin", "/usr", "/etc", "/lib", "/lib64", "/nix/store", "/run/current-system/sw",
}

func filesystemRulesContainRootRead(req CommandRequest) bool {
	for _, paths := range [][]string{req.AdditionalReadablePaths, req.AdditionalWritablePaths} {
		for _, raw := range paths {
			if filepath.Clean(strings.TrimSpace(raw)) == string(filepath.Separator) {
				return true
			}
		}
	}
	return false
}

func appendFilesystemPolicyArgs(args []string, req CommandRequest) []string {
	var rules []filesystemMountRule
	if req.IncludePlatformDefaults {
		for _, path := range linuxPlatformDefaultReadRoots {
			rules = append(rules, filesystemMountRule{path: path, access: filesystemMountRead, priority: 0})
		}
	}
	for _, path := range req.AdditionalReadablePaths {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountRead, priority: 0})
	}
	for _, path := range req.AdditionalWritablePaths {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountWrite, priority: 1})
	}
	for _, path := range req.DeniedWritablePaths {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountRead, priority: 2})
	}
	for _, path := range req.DeniedReadablePaths {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountDeny, priority: 3})
	}

	normalized := make([]filesystemMountRule, 0, len(rules))
	seen := map[string]filesystemMountRule{}
	for _, rule := range rules {
		path := strings.TrimSpace(rule.path)
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		rule.path = abs
		if current, ok := seen[abs]; ok && current.priority >= rule.priority {
			continue
		}
		seen[abs] = rule
	}
	for _, rule := range seen {
		normalized = append(normalized, rule)
	}
	sort.Slice(normalized, func(i, j int) bool {
		leftDepth := pathComponentCount(normalized[i].path)
		rightDepth := pathComponentCount(normalized[j].path)
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		if normalized[i].path != normalized[j].path {
			return normalized[i].path < normalized[j].path
		}
		return normalized[i].access < normalized[j].access
	})
	for _, rule := range normalized {
		if rule.path == string(filepath.Separator) && rule.access == filesystemMountRead {
			continue
		}
		info, err := os.Stat(rule.path)
		if err != nil {
			continue
		}
		switch rule.access {
		case filesystemMountRead:
			args = append(args, "--ro-bind", rule.path, rule.path)
		case filesystemMountWrite:
			args = append(args, "--bind", rule.path, rule.path)
		case filesystemMountDeny:
			if info.IsDir() {
				args = append(args, "--tmpfs", rule.path, "--remount-ro", rule.path)
			} else {
				args = append(args, "--ro-bind", "/dev/null", rule.path)
			}
		}
	}
	return args
}

const linuxNetworkBridgeArgument = "__forebrain_internal_network_bridge__"

type linuxNetworkBridgeSpec struct {
	Routes []linuxNetworkBridgeRoute `json:"routes"`
}

type linuxNetworkBridgeRoute struct {
	Port       int    `json:"port"`
	StreamPath string `json:"stream_path"`
	PacketPath string `json:"packet_path"`
}

type linuxNetworkBridge struct {
	dir         string
	helperPath  string
	encodedSpec string
	closers     []io.Closer
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func prepareLinuxNetworkBridge(command ManagedNetworkCommand) (*linuxNetworkBridge, error) {
	ports := []int{command.HTTPPort, command.SOCKSPort, command.SOCKSUDPPort}
	seen := map[int]struct{}{}
	valid := ports[:0]
	for _, port := range ports {
		if port <= 0 || port > 65535 {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		valid = append(valid, port)
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("managed proxy mode requires loopback proxy endpoints")
	}
	sort.Ints(valid)
	dir, err := os.MkdirTemp("", "forebrain-network-bridge-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	bridge := &linuxNetworkBridge{dir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	bridge.cancel = cancel
	spec := linuxNetworkBridgeSpec{}
	for index, port := range valid {
		route := linuxNetworkBridgeRoute{
			Port:       port,
			StreamPath: filepath.Join(dir, "stream-"+strconv.Itoa(index)+".sock"),
			PacketPath: filepath.Join(dir, "packet-"+strconv.Itoa(index)+".sock"),
		}
		streamListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: route.StreamPath, Net: "unix"})
		if err != nil {
			bridge.close()
			return nil, err
		}
		_ = os.Chmod(route.StreamPath, 0o600)
		packetListener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: route.PacketPath, Net: "unixgram"})
		if err != nil {
			_ = streamListener.Close()
			bridge.close()
			return nil, err
		}
		_ = os.Chmod(route.PacketPath, 0o600)
		bridge.closers = append(bridge.closers, streamListener, packetListener)
		bridge.wg.Add(2)
		go bridge.runHostStreamBridge(ctx, streamListener, port)
		go bridge.runHostPacketBridge(ctx, packetListener, port)
		spec.Routes = append(spec.Routes, route)
	}
	helperPath := filepath.Join(dir, "bridge-helper")
	if err := installBridgeHelper(helperPath); err != nil {
		bridge.close()
		return nil, err
	}
	bridge.helperPath = helperPath
	raw, err := json.Marshal(spec)
	if err != nil {
		bridge.close()
		return nil, err
	}
	bridge.encodedSpec = base64.RawURLEncoding.EncodeToString(raw)
	return bridge, nil
}

func installBridgeHelper(target string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.Link(executable, target); err == nil {
		return nil
	}
	source, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := destination.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (bridge *linuxNetworkBridge) close() {
	if bridge == nil {
		return
	}
	if bridge.cancel != nil {
		bridge.cancel()
	}
	for _, closer := range bridge.closers {
		_ = closer.Close()
	}
	bridge.wg.Wait()
	if bridge.dir != "" {
		_ = os.RemoveAll(bridge.dir)
	}
}

func (bridge *linuxNetworkBridge) runHostStreamBridge(ctx context.Context, listener *net.UnixListener, port int) {
	defer bridge.wg.Done()
	for {
		unixConn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		go func() {
			tcpConn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				_ = unixConn.Close()
				return
			}
			proxyTunnel(unixConn, tcpConn)
		}()
	}
}

func (bridge *linuxNetworkBridge) runHostPacketBridge(ctx context.Context, listener *net.UnixConn, port int) {
	defer bridge.wg.Done()
	buffer := make([]byte, 65535)
	for {
		count, peer, err := listener.ReadFromUnix(buffer)
		if err != nil {
			return
		}
		packet := append([]byte(nil), buffer[:count]...)
		go func() {
			upstream, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
			if err != nil {
				return
			}
			defer upstream.Close()
			_ = upstream.SetDeadline(deadlineFromContext(ctx))
			if _, err := upstream.Write(packet); err != nil {
				return
			}
			response := make([]byte, 65535)
			count, err := upstream.Read(response)
			if err == nil {
				_, _ = listener.WriteToUnix(response[:count], peer)
			}
		}()
	}
}

func deadlineFromContext(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(30 * time.Second)
}

func RunInternalNetworkBridge() (int, bool) {
	if len(os.Args) < 5 || os.Args[1] != linuxNetworkBridgeArgument {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(os.Args[2])
	if err != nil {
		return 125, true
	}
	var spec linuxNetworkBridgeSpec
	if json.Unmarshal(raw, &spec) != nil || len(spec.Routes) == 0 {
		return 125, true
	}
	if err := enableLinuxLoopback(); err != nil {
		return 125, true
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var closers []io.Closer
	for _, route := range spec.Routes {
		stream, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(route.Port)))
		if err != nil {
			return 125, true
		}
		packet, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: route.Port})
		if err != nil {
			_ = stream.Close()
			return 125, true
		}
		closers = append(closers, stream, packet)
		go runNamespaceStreamBridge(ctx, stream, route.StreamPath)
		go runNamespacePacketBridge(ctx, packet, route.PacketPath)
	}
	command := exec.Command(os.Args[3], os.Args[4:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := command.Start(); err != nil {
		return 125, true
	}
	go func() {
		for sig := range signals {
			if signalValue, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(-command.Process.Pid, signalValue)
			}
		}
	}()
	err = command.Wait()
	for _, closer := range closers {
		_ = closer.Close()
	}
	if err == nil {
		return 0, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 125, true
}

func enableLinuxLoopback() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, request); err != nil {
		return err
	}
	request.SetUint16(request.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request)
}

func runNamespaceStreamBridge(ctx context.Context, listener net.Listener, path string) {
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			host, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
			if err != nil {
				_ = client.Close()
				return
			}
			proxyTunnel(client, host)
		}()
	}
}

func runNamespacePacketBridge(ctx context.Context, listener *net.UDPConn, path string) {
	buffer := make([]byte, 65535)
	for {
		count, client, err := listener.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		packet := append([]byte(nil), buffer[:count]...)
		go func() {
			localPath := "@forebrain-packet-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			unixConn, err := net.DialUnix("unixgram", &net.UnixAddr{Name: localPath, Net: "unixgram"}, &net.UnixAddr{Name: path, Net: "unixgram"})
			if err != nil {
				return
			}
			defer unixConn.Close()
			_ = unixConn.SetDeadline(deadlineFromContext(ctx))
			if _, err := unixConn.Write(packet); err != nil {
				return
			}
			response := make([]byte, 65535)
			count, err := unixConn.Read(response)
			if err == nil {
				_, _ = listener.WriteToUDP(response[:count], client)
			}
		}()
	}
}

type linuxMissingDenyMarker struct {
	path  string
	isDir bool
	dev   uint64
	ino   uint64
}

func prepareLinuxMissingDenyMarkers(req CommandRequest) ([]linuxMissingDenyMarker, error) {
	var markers []linuxMissingDenyMarker
	seen := map[string]struct{}{}
	for _, raw := range append(append([]string(nil), req.DeniedWritablePaths...), req.DeniedReadablePaths...) {
		target, err := filepath.Abs(strings.TrimSpace(raw))
		if err != nil || target == string(filepath.Separator) || target == "" {
			continue
		}
		target = filepath.Clean(target)
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		if _, err := os.Lstat(target); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			cleanupLinuxMissingDenyMarkers(markers)
			return nil, err
		}
		if !linuxPathMayBeWritten(req, target) {
			continue
		}
		missing, final, err := firstMissingLinuxPathComponent(target)
		if err != nil {
			cleanupLinuxMissingDenyMarkers(markers)
			return nil, err
		}
		marker, created, err := createLinuxMissingDenyMarker(missing, !final)
		if err != nil {
			cleanupLinuxMissingDenyMarkers(markers)
			return nil, fmt.Errorf("protect missing denied path %s: %w", target, err)
		}
		if created {
			markers = append(markers, marker)
		}
	}
	return markers, nil
}

func linuxPathMayBeWritten(req CommandRequest, target string) bool {
	if req.Profile.normalized() == ProfileWorkspaceWrite {
		workDir, err := filepath.Abs(strings.TrimSpace(req.WorkDir))
		if err == nil && linuxPathContains(filepath.Clean(workDir), target) {
			return true
		}
	}
	for _, raw := range req.AdditionalWritablePaths {
		root, err := filepath.Abs(strings.TrimSpace(raw))
		if err == nil && linuxPathContains(filepath.Clean(root), target) {
			return true
		}
	}
	return false
}

func linuxPathContains(root, target string) bool {
	if root == target {
		return true
	}
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func firstMissingLinuxPathComponent(target string) (string, bool, error) {
	volume := filepath.VolumeName(target)
	current := volume + string(filepath.Separator)
	components := strings.Split(strings.TrimPrefix(target, current), string(filepath.Separator))
	for index, component := range components {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return current, index == len(components)-1, nil
		}
		if err != nil {
			return "", false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("path ancestor is a symbolic link: %s", current)
		}
		if !info.IsDir() && index != len(components)-1 {
			return "", false, fmt.Errorf("path ancestor is not a directory: %s", current)
		}
	}
	return target, true, nil
}

func createLinuxMissingDenyMarker(path string, directory bool) (linuxMissingDenyMarker, bool, error) {
	if directory {
		if err := os.Mkdir(path, 0o000); err != nil {
			if os.IsExist(err) {
				return linuxMissingDenyMarker{}, false, nil
			}
			return linuxMissingDenyMarker{}, false, err
		}
	} else {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o000)
		if err != nil {
			if os.IsExist(err) {
				return linuxMissingDenyMarker{}, false, nil
			}
			return linuxMissingDenyMarker{}, false, err
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return linuxMissingDenyMarker{}, false, err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return linuxMissingDenyMarker{}, false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		_ = os.Remove(path)
		return linuxMissingDenyMarker{}, false, fmt.Errorf("path identity is unavailable")
	}
	return linuxMissingDenyMarker{path: path, isDir: directory, dev: uint64(stat.Dev), ino: stat.Ino}, true, nil
}

func cleanupLinuxMissingDenyMarkers(markers []linuxMissingDenyMarker) {
	for index := len(markers) - 1; index >= 0; index-- {
		marker := markers[index]
		info, err := os.Lstat(marker.path)
		if err != nil || info.IsDir() != marker.isDir || info.Mode()&os.ModeSymlink != 0 || (!marker.isDir && info.Size() != 0) {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || uint64(stat.Dev) != marker.dev || stat.Ino != marker.ino {
			continue
		}
		_ = os.Chmod(marker.path, 0o700)
		_ = os.Remove(marker.path)
	}
}
