package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

// The three ways a send fails, so a caller can tell "that peer is gone, forget
// it" from "try again later" from "this will never work".
var (
	// ErrPeerGone is a target that does not exist any more.
	ErrPeerGone = errors.New("peer: the peer is gone")
	// ErrTransient is a target that is busy or too slow right now.
	ErrTransient = errors.New("peer: the peer is temporarily unavailable")
	// ErrRefused is a send this package will not make as asked.
	ErrRefused = errors.New("peer: the peer refused the message")
)

// sendTimeout is the whole budget of one send: dialling and writing.
const sendTimeout = 5 * time.Second

// SendOptions are the checks one send needs beyond its target.
type SendOptions struct {
	// NoFollowSymlink refuses a target that is a symbolic link: receipts and
	// idle notices may only go to a socket this user's process created.
	NoFollowSymlink bool
	// ExpectPID, when set, requires the peer at the other end to be that pid
	// of this user.
	ExpectPID int
	// KeyDir is the rendezvous directory to look the peer's token up in. An
	// empty KeyDir sends unauthenticated, which POSIX peers accept.
	KeyDir string
	// Timeout replaces the protocol's 5s send budget.
	Timeout time.Duration
}

// Send writes one frame to a peer's socket, authenticating first when the
// peer's key file is in KeyDir.
func Send(ctx context.Context, socketPath string, f Frame, opt SendOptions) error {
	if opt.NoFollowSymlink {
		info, err := os.Lstat(socketPath)
		if err != nil {
			return classifySendError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symbolic link", ErrRefused, socketPath)
		}
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = sendTimeout
	}
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(sendCtx, "unix", socketPath)
	if err != nil {
		return classifySendError(err)
	}
	defer conn.Close()

	if opt.ExpectPID > 0 {
		unixConn, ok := conn.(*net.UnixConn)
		if !ok {
			return fmt.Errorf("%w: not a local socket connection", ErrRefused)
		}
		pid, uid, ok := peerCreds(unixConn)
		if !ok {
			return fmt.Errorf("%w: cannot read the peer's credentials", ErrRefused)
		}
		if uid != selfUID() || pid != opt.ExpectPID {
			return fmt.Errorf("%w: expected pid %d of this user, found pid %d of uid %d",
				ErrRefused, opt.ExpectPID, pid, uid)
		}
	}

	deadline := time.Now().Add(timeout)
	conn.SetWriteDeadline(deadline)
	if opt.KeyDir != "" {
		if token, ok := FindToken(socketPath, opt.KeyDir); ok {
			if err := writeFrame(conn, NewAuthFrame(token)); err != nil {
				return err
			}
		}
	}
	if err := writeFrame(conn, f); err != nil {
		return err
	}
	awaitPeerClose(conn, deadline)
	return nil
}

// awaitPeerClose half-closes our side and waits for the peer to close the
// connection.
//
// The wait is not politeness: the receiver reads our pid off the socket with
// LOCAL_PEERPID, and macOS answers ENOTCONN once the peer has closed, so a
// sender that hung up the instant its bytes were written could never be
// identified. Half-closing keeps the socket open; the receiver reads the end of
// its input, closes its side, and both sides finish promptly. The frame is
// written by then, so nothing here can fail the send.
func awaitPeerClose(conn net.Conn, deadline time.Time) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return
	}
	if err := unixConn.CloseWrite(); err != nil {
		return
	}
	conn.SetReadDeadline(deadline)
	io.Copy(io.Discard, conn)
}

// writeFrame writes one frame as one line.
func writeFrame(conn net.Conn, f Frame) error {
	line, err := f.Encode()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	if len(line)+1 > defaultMaxLine {
		return fmt.Errorf("%w: the frame is longer than a peer line", ErrRefused)
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return classifySendError(err)
	}
	return nil
}

// classifySendError puts a transport failure into one of the three classes.
func classifySendError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("%w: %v", ErrPeerGone, err)
	case isBusy(err), isTransient(err):
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
	return err
}

// isBusy reports a socket someone holds but cannot serve right now.
func isBusy(err error) bool {
	return errors.Is(err, syscall.EBUSY)
}

// isTransient reports a deadline or a would-block.
func isTransient(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, syscall.EAGAIN) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
