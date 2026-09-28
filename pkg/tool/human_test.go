package tool

import (
	"context"
	"errors"
	"testing"
)

func TestAdapterPassthroughDescriptionPreviewRecommended(t *testing.T) {
	in := &Input{
		Questions: []Question{{
			Header:   "qid",
			Question: "Pick one",
			Options: []Choice{
				{
					Label:       "A",
					Description: "first option",
					Preview:     "preview-a",
					Recommended: true,
				},
				{
					Label:       "B",
					Description: "second option",
				},
			},
		}},
	}
	form, err := humanInputToAskForm(in)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if len(form.Questions) != 1 || len(form.Questions[0].Options) != 2 {
		t.Fatalf("unexpected form shape: %+v", form)
	}
	o0 := form.Questions[0].Options[0]
	if o0.Description != "first option" {
		t.Errorf("opt0.desc=%q", o0.Description)
	}
	if o0.Preview != "preview-a" {
		t.Errorf("opt0.preview=%q", o0.Preview)
	}
	if !o0.Recommended {
		t.Errorf("opt0.recommended=false want true")
	}
	o1 := form.Questions[0].Options[1]
	if o1.Description != "second option" || o1.Preview != "" || o1.Recommended {
		t.Errorf("opt1 unexpected: %+v", o1)
	}
	if !form.Questions[0].AllowOther {
		t.Error("expected AllowOther=true on every question")
	}
}

func TestNewRequiresInteractor(t *testing.T) {
	t.Parallel()

	_, err := New()
	if !errors.Is(err, ErrInteractorRequired) {
		t.Fatalf("expected ErrInteractorRequired, got %v", err)
	}
}

func TestAskNilInput(t *testing.T) {
	t.Parallel()

	tool, err := New(WithInteractor(func(context.Context, *Input) (map[string]Answer, error) {
		return map[string]Answer{}, nil
	}))
	if err != nil {
		t.Fatalf("unexpected error creating tool: %v", err)
	}

	_, err = tool.ask(context.Background(), nil)
	if !errors.Is(err, ErrNilInput) {
		t.Fatalf("expected ErrNilInput, got %v", err)
	}
}

func TestAskReturnsStructuredAnswers(t *testing.T) {
	t.Parallel()

	input := &Input{Questions: []Question{
		{
			Header:      "Approve",
			Question:    "Apply this action?",
			MultiSelect: false,
			Options: []Choice{
				{Label: "Approve", Description: "Apply the action"},
				{Label: "Skip", Description: "Skip this action"},
			},
		},
	}}

	tool, err := New(WithInteractor(func(context.Context, *Input) (map[string]Answer, error) {
		return map[string]Answer{
			"Approve": {
				Selections: []string{"Approve"},
			},
		}, nil
	}))
	if err != nil {
		t.Fatalf("unexpected error creating tool: %v", err)
	}

	out, err := tool.ask(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected ask error: %v", err)
	}

	answer, ok := out.Answers["Approve"]
	if !ok {
		t.Fatalf("expected answer for Approve header")
	}
	if len(answer.Selections) != 1 || answer.Selections[0] != "Approve" {
		t.Fatalf("unexpected selections: %#v", answer.Selections)
	}
}

func TestAskSupportsMultiSelect(t *testing.T) {
	t.Parallel()

	input := &Input{Questions: []Question{
		{
			Header:      "Features",
			Question:    "Which features do you want to enable?",
			MultiSelect: true,
			Options: []Choice{
				{Label: "Lint", Description: "Enable lint checks"},
				{Label: "Tests", Description: "Enable test checks"},
				{Label: "Coverage", Description: "Enable coverage checks"},
			},
		},
	}}

	tool, err := New(WithInteractor(func(context.Context, *Input) (map[string]Answer, error) {
		return map[string]Answer{
			"Features": {
				Selections: []string{"Lint", "Tests"},
			},
		}, nil
	}))
	if err != nil {
		t.Fatalf("unexpected error creating tool: %v", err)
	}

	out, err := tool.ask(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected ask error: %v", err)
	}

	if got := len(out.Answers["Features"].Selections); got != 2 {
		t.Fatalf("expected 2 selections, got %d", got)
	}
}

func TestAskPropagatesInteractorFailure(t *testing.T) {
	t.Parallel()

	interactorErr := errors.New("backend unavailable")

	tool, err := New(WithInteractor(func(context.Context, *Input) (map[string]Answer, error) {
		return nil, interactorErr
	}))
	if err != nil {
		t.Fatalf("unexpected error creating tool: %v", err)
	}

	_, err = tool.ask(context.Background(), &Input{Questions: []Question{
		{
			Header:      "Decision",
			Question:    "Proceed?",
			MultiSelect: false,
			Options:     []Choice{{Label: "Yes", Description: "Proceed"}, {Label: "No", Description: "Do not proceed"}},
		},
	}})
	if !errors.Is(err, ErrInteractionFailed) {
		t.Fatalf("expected ErrInteractionFailed, got %v", err)
	}
	if !errors.Is(err, interactorErr) {
		t.Fatalf("expected wrapped interactor error, got %v", err)
	}
}

func TestAskRejectsInvalidAnswerSelection(t *testing.T) {
	t.Parallel()

	tool, err := New(WithInteractor(func(context.Context, *Input) (map[string]Answer, error) {
		return map[string]Answer{
			"Decision": {
				Selections: []string{"Maybe"},
			},
		}, nil
	}))
	if err != nil {
		t.Fatalf("unexpected error creating tool: %v", err)
	}

	_, err = tool.ask(context.Background(), &Input{Questions: []Question{
		{
			Header:      "Decision",
			Question:    "Proceed?",
			MultiSelect: false,
			Options:     []Choice{{Label: "Yes", Description: "Proceed"}, {Label: "No", Description: "Do not proceed"}},
		},
	}})
	if !errors.Is(err, ErrInvalidOptionSelection) {
		t.Fatalf("expected ErrInvalidOptionSelection, got %v", err)
	}
}

func TestAskAcceptsOtherTextOnly(t *testing.T) {
	t.Parallel()

	input := &Input{Questions: []Question{
		{
			Header:   "Color",
			Question: "Which color?",
			Options: []Choice{
				{Label: "Red", Description: "r"},
				{Label: "Blue", Description: "b"},
			},
		},
	}}

	tool, err := New(WithInteractor(func(context.Context, *Input) (map[string]Answer, error) {
		return map[string]Answer{
			"Color": {Other: "Green"},
		}, nil
	}))
	if err != nil {
		t.Fatalf("unexpected error creating tool: %v", err)
	}

	out, err := tool.ask(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected ask error: %v", err)
	}
	if out.Answers["Color"].Other != "Green" {
		t.Fatalf("expected Other=Green, got %q", out.Answers["Color"].Other)
	}
}

func TestAskRejectsEmptyAnswer(t *testing.T) {
	t.Parallel()

	tool, err := New(WithInteractor(func(context.Context, *Input) (map[string]Answer, error) {
		return map[string]Answer{
			"Decision": {},
		}, nil
	}))
	if err != nil {
		t.Fatalf("unexpected error creating tool: %v", err)
	}

	_, err = tool.ask(context.Background(), &Input{Questions: []Question{
		{
			Header:   "Decision",
			Question: "Proceed?",
			Options:  []Choice{{Label: "Yes", Description: "y"}, {Label: "No", Description: "n"}},
		},
	}})
	if !errors.Is(err, ErrAnswerRequired) {
		t.Fatalf("expected ErrAnswerRequired, got %v", err)
	}
}

func TestAskRejectsMissingAnswer(t *testing.T) {
	t.Parallel()

	tool, err := New(WithInteractor(func(context.Context, *Input) (map[string]Answer, error) {
		return map[string]Answer{}, nil
	}))
	if err != nil {
		t.Fatalf("unexpected error creating tool: %v", err)
	}

	_, err = tool.ask(context.Background(), &Input{Questions: []Question{
		{
			Header:   "Decision",
			Question: "Proceed?",
			Options:  []Choice{{Label: "Yes", Description: "y"}, {Label: "No", Description: "n"}},
		},
	}})
	if !errors.Is(err, ErrAnswerRequired) {
		t.Fatalf("expected ErrAnswerRequired, got %v", err)
	}
}
