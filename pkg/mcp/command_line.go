// The one-line stdio target a settings field carries.
//
// A form has a single text input for what is really a command plus arguments,
// and a hand-written configuration has the same problem in reverse: the pair
// has to survive being shown as one line and typed back. These two functions
// are that round trip, and they are inverses: FormatStdioTarget quotes every
// argument that would otherwise split or lose a quote, so splitting the
// rendered line returns the exact command and arguments it was given. An
// argument that merely contains a space is one argument, not two.
//
// This is deliberately not pkg/safety's word splitter: that one keeps quotes
// and comments so a classifier can see what was typed, which is the opposite of
// what a configuration file needs — the quotes here are the user's, and what
// reaches exec must be the value they meant.
package mcp

import (
	"fmt"
	"strings"
)

// SplitStdioTarget parses a stdio target line into the command and its
// arguments. Whitespace separates arguments; single and double quotes group,
// a backslash escapes the next character (inside double quotes as well), and an
// unclosed quote is an error rather than a silent partial parse — a command
// that cannot be read back is a command the user must fix.
func SplitStdioTarget(target string) (string, []string, error) {
	fields, err := splitStdioFields(target)
	if err != nil {
		return "", nil, err
	}
	if len(fields) == 0 {
		return "", nil, nil
	}
	return fields[0], fields[1:], nil
}

// FormatStdioTarget renders a command and its arguments as the one line a
// settings field shows. Arguments are quoted only when they need it, so a plain
// command reads the way it was written.
func FormatStdioTarget(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteStdioField(command))
	for _, arg := range args {
		parts = append(parts, quoteStdioField(arg))
	}
	return strings.Join(parts, " ")
}

func splitStdioFields(target string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	started := false
	quote := byte(0)
	for i := 0; i < len(target); i++ {
		c := target[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(target) {
				i++
				cur.WriteByte(target[i])
				started = true
				continue
			}
			if c == quote {
				quote = 0
				started = true
				continue
			}
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			quote = c
			started = true
		case c == '\\' && i+1 < len(target):
			i++
			cur.WriteByte(target[i])
			started = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if started {
				fields = append(fields, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote in command", quote)
	}
	if started {
		fields = append(fields, cur.String())
	}
	return fields, nil
}

// quoteStdioField wraps a field in double quotes when leaving it bare would
// change what it means: whitespace would split it, and a quote or backslash
// would be read as syntax rather than as itself.
func quoteStdioField(field string) string {
	if field != "" && !strings.ContainsAny(field, " \t\n\r\"'\\") {
		return field
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(field); i++ {
		if c := field[i]; c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(field[i])
	}
	b.WriteByte('"')
	return b.String()
}
