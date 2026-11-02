package peer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// maxKeySize is the size past which a key file is not ours to interpret.
const maxKeySize = 4096

// tokenBytes is the entropy behind a peer token; 16 bytes is 32 hex digits,
// which is the shape the authentication line carries.
const tokenBytes = 16

// NewToken is a fresh peer token: 16 random bytes as lowercase hex.
func NewToken() string {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// KeyFile is the JSON body of a key file. The token authenticates a sender to
// the socket the file names; procStart and pidDomain let a sender confirm the
// file belongs to the process that is listening now rather than to a reused
// pid.
type KeyFile struct {
	PeerToken string `json:"peerToken"`
	ProcStart string `json:"procStart"`
	PIDDomain string `json:"pidDomain"`
}

// WriteKey writes the key file for (pid, socketPath) into dir: the token for
// this process's own socket, 0600, atomically.
func WriteKey(dir string, pid int, socketPath, token string) error {
	canonical, ok := canonicalSocketPath(socketPath)
	if !ok {
		return os.ErrInvalid
	}
	content := KeyFile{PeerToken: token, PIDDomain: PIDDomain()}
	content.ProcStart, _ = ProcStart(pid)
	data, err := json.Marshal(content)
	if err != nil {
		return err
	}
	return writeFileAtomic(dir, keyFileName(pid, canonical), data, 0o600)
}

// FindToken looks in dir for the key file that belongs to socketPath and
// returns its token. A key file only counts when its pid is alive and its
// recorded start time still matches, so a stale file left by a reused pid
// cannot hand a sender someone else's token.
func FindToken(socketPath, dir string) (string, bool) {
	canonical, ok := canonicalSocketPath(socketPath)
	if !ok {
		return "", false
	}
	hash := socketPathHash(canonical)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		pid, ok := keyPID(entry.Name(), hash)
		if !ok || entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxKeySize {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var key KeyFile
		if err := json.Unmarshal(data, &key); err != nil || key.PeerToken == "" {
			continue
		}
		if !processExists(pid) {
			continue
		}
		if key.ProcStart != "" {
			current, ok := ProcStart(pid)
			if !ok || current != key.ProcStart {
				continue
			}
		}
		return key.PeerToken, true
	}
	return "", false
}

// keyNamePattern matches a key file name; the hash group is captured because a
// lookup already knows the hash it wants.
var keyNamePattern = regexp.MustCompile(`^(\d+)\.[0-9a-f]{64}\.key$`)

// keyPID reads the pid out of a key file name, provided the name carries the
// hash we are looking for.
func keyPID(name, hash string) (int, bool) {
	if !keyNamePattern.MatchString(name) {
		return 0, false
	}
	pidText, rest, ok := cutByte(name, '.')
	if !ok || rest != hash+".key" {
		return 0, false
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// keyFileName is <pid>.<sha256hex(canonical socket path)>.key.
func keyFileName(pid int, canonical string) string {
	return strconv.Itoa(pid) + "." + socketPathHash(canonical) + ".key"
}

// socketPathHash is the sha256 of a canonical socket path, as lowercase hex.
func socketPathHash(canonical string) string {
	return sha256Hex(canonical)
}

// sha256Hex is the sha256 of s as lowercase hex.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// canonicalSocketPath is the absolute, cleaned form of a socket path — the same
// path whichever process names it, which is what makes the key file findable.
func canonicalSocketPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	return filepath.Clean(absolute), true
}

// cutByte splits s at its first occurrence of sep.
func cutByte(s string, sep byte) (before, after string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}
