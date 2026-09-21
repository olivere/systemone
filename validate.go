package systemone

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// ProbabilityTolerance permits rounding in returned distributions and expected
// scores. Values outside [0,1] are always rejected; no values are normalized.
const ProbabilityTolerance = 0.001

func nonempty(s string) bool { return utf8.ValidString(s) && strings.TrimSpace(s) != "" }

func validateQuestion(q QuestionSpec) error {
	bad := func(reason string) error {
		return fmt.Errorf("systemone/question %q: %w: %s", q.Key, ErrInvalidQuestion, reason)
	}
	if !nonempty(q.Key) || !validDescription(q.Instructions, q.InstructionsContent, true) {
		return bad("key and instructions must be nonempty UTF-8")
	}
	if len(q.Levels) != 0 && len(q.LevelContents) != 0 {
		return bad("levels must use only one representation")
	}
	hasOutcomes := q.Yes != "" || q.No != "" || !q.YesContent.IsZero() || !q.NoContent.IsZero()
	switch q.Kind {
	case ChoiceKind:
		if len(q.Options) < 2 {
			return bad("choice needs at least two options")
		}
		if q.LevelCount() != 0 || hasOutcomes {
			return bad("choice has unrelated fields")
		}
		seen := make(map[string]bool, len(q.Options))
		for _, option := range q.Options {
			if !nonempty(option.Value) || !validDescription(option.Description, option.Content, false) || seen[option.Value] {
				return bad("invalid or duplicate option")
			}
			seen[option.Value] = true
		}
	case ScoreKind:
		if q.LevelCount() < 2 {
			return bad("score needs at least two levels")
		}
		if len(q.Options) != 0 || hasOutcomes {
			return bad("score has unrelated fields")
		}
		for _, level := range q.Levels {
			if !nonempty(level) {
				return bad("levels must be nonempty UTF-8")
			}
		}
		for _, level := range q.LevelContents {
			if level.IsZero() {
				return bad("level content must be present")
			}
		}
	case NoulKind:
		if len(q.Options) != 0 || q.LevelCount() != 0 {
			return bad("noul has unrelated fields")
		}
		if !validDescription(q.Yes, q.YesContent, false) || !validDescription(q.No, q.NoContent, false) {
			return bad("descriptions must be UTF-8")
		}
	default:
		return bad("unknown kind")
	}
	return nil
}

func validDescription(text string, content Content, required bool) bool {
	if !content.IsZero() {
		return text == ""
	}
	if required {
		return nonempty(text)
	}
	return utf8.ValidString(text)
}

func hasContent(q QuestionSpec) bool {
	if !q.InstructionsContent.IsZero() || len(q.LevelContents) != 0 || !q.YesContent.IsZero() || !q.NoContent.IsZero() {
		return true
	}
	for _, option := range q.Options {
		if !option.Content.IsZero() {
			return true
		}
	}
	return false
}

// ValidateRequest checks domain invariants without contacting a backend.
func ValidateRequest(req Request) error {
	state := bytes.TrimSpace(req.State)
	if len(state) == 0 || !req.State.IsValid() || (state[0] != '"' && state[0] != '{' && state[0] != '[') {
		return fmt.Errorf("systemone/request: %w: expected JSON string, object, or array", ErrInvalidState)
	}
	if len(req.Questions) == 0 {
		return fmt.Errorf("systemone/request: %w: no questions", ErrInvalidQuestion)
	}
	keys := make(map[string]bool, len(req.Questions))
	for _, q := range req.Questions {
		if err := validateQuestion(q); err != nil {
			return err
		}
		if keys[q.Key] {
			return fmt.Errorf("systemone/request: %w: duplicate key %q", ErrInvalidQuestion, q.Key)
		}
		keys[q.Key] = true
	}
	return nil
}

// CheckRequest also checks a backend's declared capabilities.
func CheckRequest(req Request, caps Capabilities) error {
	if err := ValidateRequest(req); err != nil {
		return err
	}
	if caps.MaxOptions < 0 || caps.MaxLevels < 0 || caps.MaxQuestions < 0 {
		return fmt.Errorf("systemone/check: %w: negative capability ceiling", ErrUnsupported)
	}
	if caps.MaxQuestions > 0 && len(req.Questions) > caps.MaxQuestions {
		return fmt.Errorf("systemone/check: %w: question ceiling %d", ErrUnsupported, caps.MaxQuestions)
	}
	for _, q := range req.Questions {
		if hasContent(q) && !caps.StructuredContent {
			return fmt.Errorf("systemone/check %q: %w: structured content", q.Key, ErrUnsupported)
		}
		unsupported := q.Kind == ChoiceKind && (!caps.Choice || caps.MaxOptions > 0 && len(q.Options) > caps.MaxOptions) ||
			q.Kind == ScoreKind && (!caps.Score || caps.MaxLevels > 0 && q.LevelCount() > caps.MaxLevels) ||
			q.Kind == NoulKind && !caps.Noul
		if unsupported {
			return fmt.Errorf("systemone/check %q: %w", q.Key, ErrUnsupported)
		}
	}
	return nil
}

// ValidateResponse checks that every question has exactly one matching answer.
// It checks mathematical consistency, not correctness or calibration.
func ValidateResponse(req Request, response Response) error {
	if err := ValidateRequest(req); err != nil {
		return err
	}
	if len(response.Answers) != len(req.Questions) {
		return fmt.Errorf("systemone/response: %w: answer count mismatch", ErrInvalidResponse)
	}
	if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 {
		return fmt.Errorf("systemone/response: %w: negative usage", ErrInvalidResponse)
	}
	for _, q := range req.Questions {
		a, ok := response.Answers[q.Key]
		if !ok {
			return fmt.Errorf("systemone/response %q: %w: missing answer", q.Key, ErrInvalidResponse)
		}
		if err := validateAnswer(q, a); err != nil {
			return fmt.Errorf("systemone/response %q: %w: %s", q.Key, ErrInvalidResponse, err)
		}
	}
	return nil
}

func probability(p float64) bool { return !math.IsNaN(p) && p >= 0 && p <= 1 }
func confidence(p *float64) bool { return p == nil || probability(*p) }

func validateAnswer(q QuestionSpec, a Answer) error {
	if a.Source != Unspecified && a.Source != Native && a.Source != Estimated {
		return fmt.Errorf("unknown probability source")
	}
	switch q.Kind {
	case ChoiceKind:
		if a.Choice == nil || a.Score != nil || a.Noul != nil {
			return fmt.Errorf("expected only a choice answer")
		}
		r := a.Choice
		if !confidence(r.Confidence) || len(r.Probabilities) != len(q.Options) {
			return fmt.Errorf("invalid confidence or option count")
		}
		chosen, ok := r.Probabilities[r.Choice]
		if !ok {
			return fmt.Errorf("selected option missing from distribution")
		}
		var sum float64
		for _, option := range q.Options {
			p, ok := r.Probabilities[option.Value]
			if !ok || !probability(p) || p > chosen+ProbabilityTolerance {
				return fmt.Errorf("invalid distribution or selected option is not maximal")
			}
			sum += p
		}
		if math.Abs(sum-1) > ProbabilityTolerance {
			return fmt.Errorf("probabilities do not sum to one")
		}
	case ScoreKind:
		if a.Score == nil || a.Choice != nil || a.Noul != nil {
			return fmt.Errorf("expected only a score answer")
		}
		r := a.Score
		if !confidence(r.Confidence) || len(r.Probabilities) != q.LevelCount() || math.IsNaN(r.Score) || r.Score < 0 || r.Score > float64(q.LevelCount()-1) {
			return fmt.Errorf("invalid confidence, score, or level count")
		}
		var sum, expected float64
		for i, p := range r.Probabilities {
			if !probability(p) {
				return fmt.Errorf("invalid probability")
			}
			sum += p
			expected += float64(i) * p
		}
		if math.Abs(sum-1) > ProbabilityTolerance || math.Abs(expected-r.Score) > ProbabilityTolerance*float64(q.LevelCount()) {
			return fmt.Errorf("inconsistent score distribution")
		}
	case NoulKind:
		if a.Noul == nil || a.Choice != nil || a.Score != nil || !probability(a.Noul.Probability) {
			return fmt.Errorf("expected only a valid noul answer")
		}
	}
	return nil
}
