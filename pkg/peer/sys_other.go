//go:build !darwin && !linux

package peer

import (
	"errors"
	"net"
	"os"
)

// errUnsupported is what every platform hook reports where the protocol is not
// implemented. Enabled is false there, so no caller reaches these.
var errUnsupported = errors.New("peer: the local peer protocol is not supported on this platform")

// selfUID is this process's user id; unsupported platforms have no peer
// service, so the value only has to be harmless.
func selfUID() int { return -1 }

// fileOwnerUID is unavailable; vetSocketDir then refuses to listen anywhere.
func fileOwnerUID(os.FileInfo) (int, bool) { return 0, false }

// processExists cannot be answered without the peer protocol's process
// facilities.
func processExists(int) bool { return false }

// procStartSys is unavailable: the same string needs a platform implementation.
func procStartSys(int) (string, bool) { return "", false }

// peerCreds cannot read peer credentials.
func peerCreds(*net.UnixConn) (pid, uid int, ok bool) { return 0, 0, false }
