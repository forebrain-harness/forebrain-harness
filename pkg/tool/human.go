// The human-interaction tool, its adapter, errors, and docs.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

const (
	toolName        = "user_interaction"
	toolDescription = "Use this tool to ask the user one or more structured questions and collect their answers. Every question always includes an 'Other' option that lets the user type a custom free-text answer instead of choosing a predefined option."

	minQuestions = 1
	maxQuestions = 4
	minOptions   = 2
	maxOptions   = 4

	maxHeaderLen       = 12
	maxOptionLabelWord = 5
	maxOtherTextLen    = 2000
)

// The bounds below mirror validateInput (minQuestions/maxQuestions,
// minOptions/maxOptions, maxHeaderLen). Declaring them in the schema instead of
// only in prose lets a caller get the shape right on the first attempt rather
// than through a rejected call.

// Input defines the user interaction request.
type Input struct {
	Questions []Question `json:"questions" jsonschema:"minItems=1,maxItems=4" jsonschema_description:"Questions to ask the user (1-4 questions)"`
}

// Question defines a single user-facing question.
type Question struct {
	Header      string   `json:"header" jsonschema:"minLength=1,maxLength=12" jsonschema_description:"Very short label (max 12 chars). Must be unique across questions."`
	Question    string   `json:"question" jsonschema:"minLength=1" jsonschema_description:"The complete question to ask the user"`
	MultiSelect bool     `json:"multiSelect" jsonschema:"description=Allow multiple selections"`
	Options     []Choice `json:"options" jsonschema:"minItems=2,maxItems=4" jsonschema_description:"Available choices (2-4 options)"`
}

// Choice defines a single selectable option for a question.
type Choice struct {
	Label       string `json:"label" jsonschema:"minLength=1" jsonschema_description:"Display text for this option (1-5 words)"`
	Description string `json:"description" jsonschema:"minLength=1" jsonschema_description:"Explanation of this option"`
	Preview     string `json:"preview,omitempty" jsonschema_description:"Optional preview content rendered when this option is focused (mockups, code snippets, comparisons)"`
	Recommended bool   `json:"recommended,omitempty" jsonschema:"description=Mark this option as the recommended default; renderers may display a (recommended) suffix"`
}

// Answer defines user selections for a question.
type Answer struct {
	Selections []string `json:"selections,omitempty" jsonschema:"description=Option labels selected by the user"`
	Other      string   `json:"other,omitempty" jsonschema:"description=Optional custom free-text answer"`
}

// Output is the structured result returned by the tool.
type Output struct {
	Answers map[string]Answer `json:"answers" jsonschema:"description=Collected user answers keyed by question header"`
}

// Interactor is the callback used to present questions and collect answers.
type Interactor func(ctx context.Context, input *Input) (map[string]Answer, error)

// Tool wraps the user interaction tool.
type Tool struct {
	tool       *llm.Tool
	interactor Interactor
}

// Option configures a Tool created by New.
type Option func(*Tool)

// WithInteractor overrides the callback used to collect user answers.
func WithInteractor(interactor Interactor) Option {
	return func(t *Tool) {
		t.interactor = interactor
	}
}

// New creates a new user interaction tool.
func New(opts ...Option) (*Tool, error) {
	t := &Tool{}

	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}

	if t.interactor == nil {
		return nil, ErrInteractorRequired
	}

	tool, err := llm.NewTool(
		toolName,
		toolDescription,
		t.ask,
	)
	if err != nil {
		return nil, err
	}

	t.tool = tool

	return t, nil
}

// Tool returns the llm.Tool representation of the user interaction tool.
func (h *Tool) Tool() *llm.Tool {
	return h.tool
}

func (h *Tool) ask(ctx context.Context, input *Input) (*Output, error) {
	if input == nil {
		return nil, ErrNilInput
	}
	if err := validateInput(input); err != nil {
		return nil, err
	}

	answers, err := h.interactor(ctx, input)
	if err != nil {
		return nil, errors.Join(ErrInteractionFailed, err)
	}

	if answers == nil {
		answers = map[string]Answer{}
	}

	if err := validateAnswers(input.Questions, answers); err != nil {
		return nil, err
	}

	return &Output{Answers: answers}, nil
}

func validateInput(input *Input) error {
	questions := input.Questions
	if len(questions) < minQuestions {
		return ErrQuestionsRequired
	}
	if len(questions) > maxQuestions {
		return ErrTooManyQuestions
	}

	seenHeaders := map[string]struct{}{}
	for _, q := range questions {
		header := strings.TrimSpace(q.Header)
		if header == "" {
			return ErrHeaderRequired
		}
		if utf8.RuneCountInString(header) > maxHeaderLen {
			return ErrHeaderTooLong
		}

		normalizedHeader := strings.ToLower(header)
		if _, exists := seenHeaders[normalizedHeader]; exists {
			return ErrDuplicateQuestionHeader
		}
		seenHeaders[normalizedHeader] = struct{}{}

		questionText := strings.TrimSpace(q.Question)
		if questionText == "" {
			return ErrQuestionTextRequired
		}

		if len(q.Options) < minOptions || len(q.Options) > maxOptions {
			return ErrInvalidOptionCount
		}

		for _, option := range q.Options {
			label := strings.TrimSpace(option.Label)
			if label == "" {
				return ErrOptionLabelRequired
			}

			words := strings.Fields(label)
			if len(words) == 0 || len(words) > maxOptionLabelWord {
				return ErrOptionLabelTooLong
			}

			if strings.TrimSpace(option.Description) == "" {
				return ErrOptionDescriptionRequired
			}
		}
	}

	return nil
}

func validateAnswers(questions []Question, answers map[string]Answer) error {
	allowed := map[string]Question{}
	for _, q := range questions {
		allowed[strings.ToLower(strings.TrimSpace(q.Header))] = q
	}

	for key, answer := range answers {
		q, exists := allowed[strings.ToLower(strings.TrimSpace(key))]
		if !exists {
			return ErrInvalidAnswerHeader
		}

		if !q.MultiSelect && len(answer.Selections) > 1 {
			return ErrMultipleSelectionsNotAllowed
		}

		optionLabels := map[string]struct{}{}
		for _, option := range q.Options {
			optionLabels[strings.ToLower(strings.TrimSpace(option.Label))] = struct{}{}
		}

		for _, selection := range answer.Selections {
			if _, ok := optionLabels[strings.ToLower(strings.TrimSpace(selection))]; !ok {
				return ErrInvalidOptionSelection
			}
		}

		if utf8.RuneCountInString(answer.Other) > maxOtherTextLen {
			return ErrOtherTextTooLong
		}
	}

	for _, q := range questions {
		key := strings.ToLower(strings.TrimSpace(q.Header))
		answer, exists := answers[key]
		if !exists {
			for k, v := range answers {
				if strings.ToLower(strings.TrimSpace(k)) == key {
					answer = v
					exists = true
					break
				}
			}
		}
		if !exists {
			return ErrAnswerRequired
		}
		hasSelection := len(answer.Selections) > 0
		hasOther := strings.TrimSpace(answer.Other) != ""
		if !hasSelection && !hasOther {
			return ErrAnswerRequired
		}
	}

	return nil
}

const userInteractionToolName = "user_interaction"

func humanInputToAskForm(in *Input) (state.AskForm, error) {
	if in == nil {
		return state.AskForm{}, fmt.Errorf("nil user interaction input")
	}
	form := state.AskForm{
		Questions: make([]state.AskQuestion, 0, len(in.Questions)),
	}
	for _, q := range in.Questions {
		id := strings.TrimSpace(q.Header)
		if id == "" {
			return state.AskForm{}, fmt.Errorf("question header required")
		}
		item := state.AskQuestion{
			ID:            id,
			Prompt:        strings.TrimSpace(q.Question),
			AllowMultiple: q.MultiSelect,
			AllowOther:    true,
			Options:       make([]state.AskOption, 0, len(q.Options)),
		}
		for _, opt := range q.Options {
			label := strings.TrimSpace(opt.Label)
			if label == "" {
				return state.AskForm{}, fmt.Errorf("question option label required")
			}
			item.Options = append(item.Options, state.AskOption{
				ID:          label,
				Label:       label,
				Description: strings.TrimSpace(opt.Description),
				Preview:     strings.TrimSpace(opt.Preview),
				Recommended: opt.Recommended,
			})
		}
		form.Questions = append(form.Questions, item)
	}
	return form, nil
}

func askAnswerJSONToHumanOutput(raw string) (*Output, error) {
	var ans state.AskAnswer
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &ans); err != nil {
		return nil, err
	}
	out := &Output{Answers: make(map[string]Answer, len(ans.Answers))}
	for _, item := range ans.Answers {
		key := strings.TrimSpace(item.QuestionID)
		if key == "" {
			continue
		}
		out.Answers[key] = Answer{
			Selections: append([]string(nil), item.OptionIDs...),
			Other:      strings.TrimSpace(item.OtherText),
		}
	}
	return out, nil
}

// ErrNilInput is returned when the tool handler receives a nil input.
var ErrNilInput = errors.New("nil input")

// ErrInteractorRequired is returned when the interactor callback is not configured.
var ErrInteractorRequired = errors.New("interactor required")

// ErrInteractionFailed is returned when answer collection fails.
var ErrInteractionFailed = errors.New("user interaction failed")

// ErrQuestionsRequired is returned when no questions are provided.
var ErrQuestionsRequired = errors.New("at least one question is required")

// ErrTooManyQuestions is returned when the number of questions exceeds the supported limit.
var ErrTooManyQuestions = errors.New("too many questions")

// ErrHeaderRequired is returned when a question header is empty.
var ErrHeaderRequired = errors.New("question header required")

// ErrHeaderTooLong is returned when a question header exceeds the maximum length.
var ErrHeaderTooLong = errors.New("question header too long")

// ErrDuplicateQuestionHeader is returned when duplicated headers are provided.
var ErrDuplicateQuestionHeader = errors.New("duplicate question header")

// ErrQuestionTextRequired is returned when a question text is empty.
var ErrQuestionTextRequired = errors.New("question text required")

// ErrInvalidOptionCount is returned when a question has an unsupported number of options.
var ErrInvalidOptionCount = errors.New("question must define between 2 and 4 options")

// ErrOptionLabelRequired is returned when an option label is empty.
var ErrOptionLabelRequired = errors.New("option label required")

// ErrOptionLabelTooLong is returned when an option label exceeds the allowed word count.
var ErrOptionLabelTooLong = errors.New("option label must contain between 1 and 5 words")

// ErrOptionDescriptionRequired is returned when an option description is empty.
var ErrOptionDescriptionRequired = errors.New("option description required")

// ErrInvalidAnswerHeader is returned when answers include unknown question headers.
var ErrInvalidAnswerHeader = errors.New("invalid answer header")

// ErrInvalidOptionSelection is returned when an answer includes an unknown option label.
var ErrInvalidOptionSelection = errors.New("invalid option selection")

// ErrMultipleSelectionsNotAllowed is returned when multiple answers are returned for a single-select question.
var ErrMultipleSelectionsNotAllowed = errors.New("multiple selections not allowed")

// ErrAnswerRequired is returned when a question receives no selection and no custom text.
var ErrAnswerRequired = errors.New("at least one selection or custom text is required")

// ErrOtherTextTooLong is returned when the custom free-text answer exceeds the maximum length.
var ErrOtherTextTooLong = errors.New("custom text too long")
