package peer

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// replyAcrossDefaultDirs is the peer feature a process publishes when it
// accepts replies from the default socket directories as well as its own.
const replyAcrossDefaultDirs = "reply_across_default_dirs"

// EncodeUDS is a socket path as a wire address: "uds:" plus the path, with
// every byte outside the address character set written as %XX (uppercase).
func EncodeUDS(path string) string {
	var b strings.Builder
	b.WriteString("uds:")
	for i := 0; i < len(path); i++ {
		c := path[i]
		if addressByteAllowed(c) {
			b.WriteByte(c)
			continue
		}
		const hexDigits = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0xf])
	}
	return b.String()
}

// addressByteAllowed reports whether one UTF-8 byte may appear unencoded in an
// address.
func addressByteAllowed(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case ':', '_', '/', '.', '-':
		return true
	}
	return false
}

// DecodeAddress splits a wire address into its scheme and its target. The uds
// target is percent-decoded back into a path; bridge and did addresses are
// returned as they are (this package cannot send to them).
func DecodeAddress(addr string) (scheme, target string) {
	for _, known := range []string{"uds:", "bridge:", "did:"} {
		if rest, ok := strings.CutPrefix(addr, known); ok {
			scheme = strings.TrimSuffix(known, ":")
			if scheme != "uds" {
				return scheme, rest
			}
			decoded, err := url.PathUnescape(rest)
			if err != nil {
				return scheme, rest
			}
			return scheme, decoded
		}
	}
	return "", ""
}

// addressPattern is the shape of every address this protocol accepts.
var addressPattern = regexp.MustCompile(`^(?:uds|bridge|did):[A-Za-z0-9%:_/.\-]{1,200}$`)

// ValidAddress reports whether addr is a well-formed peer address.
func ValidAddress(addr string) bool {
	return addressPattern.MatchString(addr)
}

// The default socket directories a reply may cross into when the replying party
// published reply_across_default_dirs. A capture group, where there is one, is
// the user id the directory belongs to.
var replyDirPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^/tmp/cc-socks(?:-(\d+))?$`),
	regexp.MustCompile(`^/private/tmp/cc-socks(?:-(\d+))?$`),
	regexp.MustCompile(`^/run/user/(\d+)/cc-socks$`),
	regexp.MustCompile(`^/tmp/fb-socks-(\d+)$`),
	regexp.MustCompile(`^/private/tmp/fb-socks-(\d+)$`),
	regexp.MustCompile(`^/run/user/(\d+)/fb-socks$`),
}

// replySocketNamePattern is the file name of a socket a reply may be sent to.
var replySocketNamePattern = regexp.MustCompile(`^(?:\d+(?:-[0-9a-f]{8})?|[0-9a-f]{1,16})\.sock$`)

// ReplyTargetAllowed reports whether target is a socket this process may send a
// receipt or idle notice to. ownSocket is the socket the frame arrived on, and
// ownUID this process's user: replies stay inside our own directory, or reach a
// default directory of the same user when the peer published
// reply_across_default_dirs.
func ReplyTargetAllowed(target, ownSocket string, ownUID int, peerFeatures []string) bool {
	if !strings.HasSuffix(target, ".sock") {
		return false
	}
	targetPath := filepath.Clean(target)
	ownPath := filepath.Clean(ownSocket)
	if filepath.Dir(targetPath) == filepath.Dir(ownPath) {
		return true
	}
	if !hasFeature(peerFeatures, replyAcrossDefaultDirs) {
		return false
	}
	dirUID, ok := replyDirUID(filepath.Dir(targetPath))
	if !ok {
		return false
	}
	if dirUID >= 0 && dirUID != ownUID {
		return false
	}
	return replySocketNamePattern.MatchString(filepath.Base(targetPath))
}

// replyDirUID reports whether dir is one of the default socket directories, and
// which user's it is (-1 when the name does not say, as for the shared
// /tmp/cc-socks).
func replyDirUID(dir string) (int, bool) {
	for _, pattern := range replyDirPatterns {
		match := pattern.FindStringSubmatch(dir)
		if match == nil {
			continue
		}
		if len(match) < 2 || match[1] == "" {
			return -1, true
		}
		uid := 0
		for i := 0; i < len(match[1]); i++ {
			uid = uid*10 + int(match[1][i]-'0')
		}
		return uid, true
	}
	return 0, false
}

// hasFeature reports whether features contains want.
func hasFeature(features []string, want string) bool {
	for _, feature := range features {
		if feature == want {
			return true
		}
	}
	return false
}
