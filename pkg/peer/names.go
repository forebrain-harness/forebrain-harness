package peer

import (
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// maxNameRun is the longest slug a derived name is built from, in runes.
const maxNameRun = 20

// reservedNames may not be an agent name: they are roles the surfaces and the
// protocol already speak about.
var reservedNames = map[string]bool{
	"main":      true,
	"team-lead": true,
	"user":      true,
	"system":    true,
}

// DeriveName builds a peer name from a working directory: the directory's base
// name as a slug, a dash, and two random hex digits. taken reports whether a
// candidate is already in use (compared normalized), in which case the suffix is
// rolled again.
func DeriveName(cwd string, taken func(normalized string) bool) string {
	base := slugName(filepath.Base(cwd))
	free := func(candidate string) bool {
		return taken == nil || !taken(NormalizeName(candidate))
	}
	for attempt := 0; attempt < 64; attempt++ {
		suffix, err := randomHex(1)
		if err != nil {
			break
		}
		if candidate := base + "-" + suffix; free(candidate) {
			return candidate
		}
	}
	// Two hex digits only carry 255 candidates; widen the suffix rather than
	// spin, and take the last candidate if even that space is crowded.
	candidate := base
	for attempt := 0; attempt < 64; attempt++ {
		suffix, err := randomHex(2)
		if err != nil {
			return candidate
		}
		candidate = base + "-" + suffix
		if free(candidate) {
			return candidate
		}
	}
	return candidate
}

// slugName folds a directory name into a name fragment: letters, digits, dots,
// underscores and dashes survive, everything else becomes a dash, the ends are
// trimmed, and the result is at most 20 runes. A name with no letter or digit
// falls back to "forebrain".
func slugName(s string) string {
	var b strings.Builder
	dashed := false
	for _, r := range norm.NFKC.String(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			dashed = false
			continue
		}
		if !dashed {
			b.WriteRune('-')
			dashed = true
		}
	}
	out := trimNameEnds(b.String())
	if runes := []rune(out); len(runes) > maxNameRun {
		out = trimNameEnds(string(runes[:maxNameRun]))
	}
	if !hasLetterOrDigit(out) {
		return "forebrain"
	}
	return out
}

// trimNameEnds removes the dots, underscores and dashes a slug may not begin or
// end with.
func trimNameEnds(s string) string {
	return strings.Trim(s, "._-")
}

// hasLetterOrDigit reports whether s carries any character worth naming.
func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// ValidAgentName reports whether name may be used for a forebrain agent: it has
// text, carries none of the characters the protocol gives meaning to, is not one
// of the reserved role names once normalized, and does not look like an address.
func ValidAgentName(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	if strings.ContainsAny(s, `@*"<>`) {
		return false
	}
	if reservedNames[NormalizeName(s)] {
		return false
	}
	for _, prefix := range []string{"uds:", "bridge:", "did:"} {
		if strings.HasPrefix(s, prefix) {
			return false
		}
	}
	return true
}

// Ref is the local shorthand a listing shows for an agent: the shortest prefix
// of sha256("<kind>:<id>") that no other id in all shares. It never reaches the
// wire — Claude Code computes its own — so it only has to be stable and unique
// inside this process.
func Ref(kind, id string, all []string) string {
	target := refHash(kind, id)
	others := make([]string, 0, len(all))
	for _, other := range all {
		if other != id {
			others = append(others, refHash(kind, other))
		}
	}
	for length := 6; length <= 12; length++ {
		prefix := target[:length]
		unique := true
		for _, other := range others {
			if strings.HasPrefix(other, prefix) {
				unique = false
				break
			}
		}
		if unique {
			return prefix
		}
	}
	return target[:12]
}

// refHash is the full hex digest a Ref is a prefix of.
func refHash(kind, id string) string {
	return sha256Hex(kind + ":" + id)
}
