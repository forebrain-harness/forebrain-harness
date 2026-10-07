// Error types, usage-carrying wrappers, and provider API error mapping.
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrSchemaAdditionalPropertiesSet is returned when an object schema explicitly
// sets additionalProperties in a way that is incompatible with strict schemas.
var ErrSchemaAdditionalPropertiesSet = errors.New(
	"additionalProperties should not be set for object types. " +
		"This could be because you configured additional properties to be allowed. " +
		"If you really need this, update the function or output tool to not use a strict schema",
)

// SchemaExpectedMapError is returned when schema normalization expects a JSON
// object (map[string]any) but receives a different type.
type SchemaExpectedMapError struct {
	Got  any
	Path []string
}

func (e *SchemaExpectedMapError) Error() string {
	return fmt.Sprintf("expected %#v to be a map[string]any, path=%+v", e.Got, e.Path)
}

// SchemaNonStringRefError is returned when a schema contains a $ref value that
// is not a string.
type SchemaNonStringRefError struct {
	RawRef any
}

func (e *SchemaNonStringRefError) Error() string {
	return fmt.Sprintf("received non-string $ref: %#v", e.RawRef)
}

// SchemaUnexpectedRefFormatError is returned when a schema $ref does not use a
// supported format.
type SchemaUnexpectedRefFormatError struct {
	Ref string
}

func (e *SchemaUnexpectedRefFormatError) Error() string {
	return fmt.Sprintf("unexpected $ref format: expected `#/` prefix in $ref value %q", e.Ref)
}

// SchemaNonDictionaryWhileResolvingRefError is returned when resolving a $ref
// encounters a non-map value while walking the path.
type SchemaNonDictionaryWhileResolvingRefError struct {
	Ref      string
	Resolved any
}

func (e *SchemaNonDictionaryWhileResolvingRefError) Error() string {
	return fmt.Sprintf("encountered non-dictionary entry while resolving $ref %q: %#v", e.Ref, e.Resolved)
}

// ToolNilInputTypeError is returned when a tool's input type parameter has a nil zero value.
type ToolNilInputTypeError struct {
	ToolName string
}

func (e *ToolNilInputTypeError) Error() string {
	return fmt.Sprintf("failed to infer tool input type for %q: type parameter has nil zero value", e.ToolName)
}

// ErrToolNameRequired is returned by NewTool when the tool name is empty.
var ErrToolNameRequired = errors.New("tool name is required")

// ToolSchemaTransformError is returned when transforming a jsonschema.Schema to a map fails.
type ToolSchemaTransformError struct {
	Err error
}

func (e *ToolSchemaTransformError) Error() string {
	return fmt.Sprintf("failed to transform function tool jsonschema.Schema to map: %v", e.Err)
}

func (e *ToolSchemaTransformError) Unwrap() error {
	return e.Err
}

// ToolSchemaStrictnessError is returned when ensuring strictness of a tool's JSON schema fails.
type ToolSchemaStrictnessError struct {
	Err error
}

func (e *ToolSchemaStrictnessError) Error() string {
	return fmt.Sprintf("failed to ensure strictness of function tool json schema: %v", e.Err)
}

func (e *ToolSchemaStrictnessError) Unwrap() error {
	return e.Err
}

// ToolArgumentParseError is returned when parsing tool arguments from JSON fails.
//
// Arguments that are not valid JSON at all never reach the model again: every
// provider adapter replays such a call with empty arguments ({}), see
// SanitizeToolCallArguments. A bare decoder message ("invalid character 't'
// after object key") then describes text the model can no longer see, next to
// a call that looks well-formed — so the model retries blind, or probes the
// tool with a placeholder call that really runs. Error therefore quotes the
// spot where the arguments broke, from Arguments.
type ToolArgumentParseError struct {
	Err error
	// Arguments is the raw argument text the model sent.
	Arguments string
}

func (e *ToolArgumentParseError) Error() string {
	var syntaxErr *json.SyntaxError
	if !errors.As(e.Err, &syntaxErr) {
		return fmt.Sprintf("failed to parse arguments: %v", e.Err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "failed to parse arguments: not valid JSON — %v at byte %d", e.Err, syntaxErr.Offset)
	if near := argumentsExcerpt(e.Arguments, int(syntaxErr.Offset)); near != "" {
		fmt.Fprintf(&b, " (near `%s`)", near)
	}
	if at, token, ok := firstToolCallMarkup(e.Arguments); ok {
		fmt.Fprintf(&b, "; tool-call markup %s was written into the JSON at byte %d (`%s`)", token, at+1, argumentsExcerpt(e.Arguments, at+len(token)))
	}
	b.WriteString(". The tool did not run and the call shows as {} in the conversation; re-issue the complete call with well-formed JSON arguments (string values JSON-escaped, no markup inside them), not a placeholder call.")
	return b.String()
}

// toolCallMarkup lists the tool-call template tokens of models whose native
// call format is XML-like: GLM's <arg_key>/<arg_value> and the
// <function=…>/<parameter=…> form also read by synthesizeToolCallsFromReasoning.
// When a model loses track of a long JSON value, it can fall back to these
// tokens mid-value, and the provider passes the text through as the arguments.
var toolCallMarkup = []string{
	"<tool_call>", "</tool_call>",
	"<arg_key>", "</arg_key>", "<arg_value>", "</arg_value>",
	"<function=", "</function>", "<parameter=", "</parameter>",
}

// firstToolCallMarkup reports the earliest tool-call markup token in args.
func firstToolCallMarkup(args string) (at int, token string, ok bool) {
	at = -1
	for _, candidate := range toolCallMarkup {
		if i := strings.Index(args, candidate); i >= 0 && (at < 0 || i < at) {
			at, token = i, candidate
		}
	}
	return at, token, at >= 0
}

// argumentsExcerpt returns the text of args that ends end bytes in, with a
// little of what follows, on one line and cut at rune boundaries.
func argumentsExcerpt(args string, end int) string {
	const before, after = 60, 20
	end = min(max(end, 0), len(args))
	start := max(end-before, 0)
	stop := min(end+after, len(args))
	for start > 0 && !utf8.RuneStart(args[start]) {
		start++
	}
	for stop < len(args) && !utf8.RuneStart(args[stop]) {
		stop++
	}
	excerpt := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, args[start:stop])
	if start > 0 {
		excerpt = "…" + excerpt
	}
	if stop < len(args) {
		excerpt += "…"
	}
	return excerpt
}

func (e *ToolArgumentParseError) Unwrap() error {
	return e.Err
}

type ExecuteError struct {
	err   error
	usage *Usage
}

func (e *ExecuteError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *ExecuteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// NewExecuteError wraps err with usage, always creating a new wrapper even
// when err already carries one.
//
// This is deliberately not the same as WithUsageError, which keeps the usage
// already attached deeper in the chain. UsageFromError matches the outermost
// wrapper, so the two differ in which measurement wins when a call is wrapped
// twice — and usage accounting feeds the cache hit rate, which this project
// will not let drift silently. Callers that are recording the usage of the
// attempt they just made want this one.
func NewExecuteError(err error, usage *Usage) error {
	if err == nil {
		return nil
	}
	if usage == nil {
		return err
	}
	return &ExecuteError{err: err, usage: usage}
}

func WithUsageError(err error, usage *Usage) error {
	if err == nil {
		return nil
	}
	if usage == nil {
		return err
	}
	var existing *ExecuteError
	if errors.As(err, &existing) {
		if existing.usage == nil {
			existing.usage = usage
		}
		return existing
	}
	return &ExecuteError{err: err, usage: usage}
}

func UsageFromError(err error) *Usage {
	var wrapped *ExecuteError
	if !errors.As(err, &wrapped) || wrapped == nil || wrapped.usage == nil {
		return nil
	}
	cp := *wrapped.usage
	return &cp
}

func UsageFromErrorForSupervisor(err error) *Usage {
	return UsageFromError(err)
}

func WrapErrorWithUsageForTest(err error, usage *Usage) error {
	return WithUsageError(err, usage)
}

func APIErrorIsBlankModelOutput(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(err.Error()))
	if s == "" {
		return false
	}
	return strings.Contains(s, "completed without output") ||
		strings.Contains(s, "empty output")
}

// ErrInvalidBufferSize is returned when bufferSize is negative.
var ErrInvalidBufferSize = errors.New("middleware: buffer size must be non-negative")

// ErrInvalidMaxAttempts is returned when maxAttempts is less than 1.
var ErrInvalidMaxAttempts = errors.New("middleware: maxAttempts must be at least 1")

// MaxAttemptsExceededError is returned by the Retry middleware when all
// attempts have been exhausted. Unwrapping it yields the last underlying error.
type MaxAttemptsExceededError struct {
	// Attempts is the total number of attempts that were made.
	Attempts int
	// Err is the last error returned by the inner LLM.
	Err error
}

// Error implements the error interface.
func (e *MaxAttemptsExceededError) Error() string {
	return fmt.Sprintf("middleware: all %d attempts failed: %v", e.Attempts, e.Err)
}

// Unwrap returns the last underlying error so callers can use errors.Is/As.
func (e *MaxAttemptsExceededError) Unwrap() error {
	return e.Err
}
