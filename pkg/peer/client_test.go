package peer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSendToAMissingSocket(t *testing.T) {
	dirs := isolate(t)
	missing := filepath.Join(dirs.sockets, "nobody.sock")
	frame := NewUserFrame(UserFrameOptions{From: "uds:/tmp/x.sock", Content: "hi"})
	if err := Send(context.Background(), missing, frame, SendOptions{}); !errors.Is(err, ErrPeerGone) {
		t.Fatalf("Send to a missing socket = %v, want ErrPeerGone", err)
	}
}

func TestSendRefusesASymlinkWhenAsked(t *testing.T) {
	dirs := isolate(t)
	collector := newCollector()
	server := startServer(t, peerSocketPath(t, dirs, "real"), "", collector)
	link := peerSocketPath(t, dirs, "link")
	if err := os.Symlink(server.Path, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	frame := NewUserFrame(UserFrameOptions{From: "uds:/tmp/x.sock", Content: "hi"})
	err := Send(context.Background(), link, frame, SendOptions{NoFollowSymlink: true})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Send to a symlink = %v, want ErrRefused", err)
	}
	select {
	case in := <-collector.ch:
		t.Fatalf("a refused send still delivered: %#v", in)
	default:
	}
}

func TestSendChecksThePeerPID(t *testing.T) {
	dirs := isolate(t)
	collector := newCollector()
	server := startServer(t, peerSocketPath(t, dirs, "pid"), "", collector)

	frame := NewUserFrame(UserFrameOptions{From: "uds:/tmp/x.sock", Content: "hi"})
	if err := Send(context.Background(), server.Path, frame, SendOptions{ExpectPID: os.Getpid()}); err != nil {
		t.Fatalf("Send expecting our own pid: %v", err)
	}
	collector.next(t)

	wrong := NewUserFrame(UserFrameOptions{From: "uds:/tmp/x.sock", Content: "wrong pid"})
	if err := Send(context.Background(), server.Path, wrong, SendOptions{ExpectPID: os.Getpid() + 1}); !errors.Is(err, ErrRefused) {
		t.Fatalf("Send with a wrong expected pid = %v, want ErrRefused", err)
	}
	select {
	case in := <-collector.ch:
		t.Fatalf("a refused send was delivered: %#v", in)
	default:
	}
}

func TestSendRejectsAnUnencodableFrame(t *testing.T) {
	dirs := isolate(t)
	collector := newCollector()
	server := startServer(t, peerSocketPath(t, dirs, "shape"), "", collector)
	if err := Send(context.Background(), server.Path, Frame{Type: "nonsense"}, SendOptions{}); !errors.Is(err, ErrRefused) {
		t.Fatalf("Send of an unknown frame type = %v, want ErrRefused", err)
	}
}
