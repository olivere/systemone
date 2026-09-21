package systemone

import "fmt"

// Option describes one possible choice. Descriptions should be self-contained.
// Set either Description or Content, not both. Both may be empty for an option
// whose label supplies its entire meaning.
type Option[T ~string] struct {
	Value       T
	Description string
	Content     Content `json:",omitzero"`
}

func Opt[T ~string](value T, description string) Option[T] {
	return Option[T]{Value: value, Description: description}
}

// OptContent describes an option with JSON content instead of a plain string.
func OptContent[T ~string](value T, description Content) Option[T] {
	return Option[T]{Value: value, Content: description}
}

// Question is a reusable, immutable handle created by this package.
// Its identity, not just its key, associates it with a validated answer.
type Question interface {
	definition() *definition
}

type definition struct{ spec QuestionSpec }

type (
	Choice[T ~string] struct{ d *definition }
	Score             struct{ d *definition }
	Noul              struct{ d *definition }
)

func (q Choice[T]) definition() *definition { return q.d }
func (q Score) definition() *definition     { return q.d }
func (q Noul) definition() *definition      { return q.d }

func NewChoice[T ~string](key, instructions string, options ...Option[T]) (Choice[T], error) {
	spec := QuestionSpec{Key: key, Kind: ChoiceKind, Instructions: instructions}
	return newChoice(spec, options)
}

func NewChoiceContent[T ~string](key string, instructions Content, options ...Option[T]) (Choice[T], error) {
	if instructions.IsZero() {
		return Choice[T]{}, fmt.Errorf("systemone/question: %w: instructions are required", ErrInvalidQuestion)
	}
	return newChoice(QuestionSpec{Key: key, Kind: ChoiceKind, InstructionsContent: instructions}, options)
}

func newChoice[T ~string](spec QuestionSpec, options []Option[T]) (Choice[T], error) {
	for _, option := range options {
		spec.Options = append(spec.Options, Option[string]{Value: string(option.Value), Description: option.Description, Content: option.Content})
	}
	if err := validateQuestion(spec); err != nil {
		return Choice[T]{}, err
	}
	return Choice[T]{d: &definition{spec: spec}}, nil
}

// MustChoiceContent panics on an invalid static question definition.
func MustChoiceContent[T ~string](key string, instructions Content, options ...Option[T]) Choice[T] {
	q, err := NewChoiceContent(key, instructions, options...)
	if err != nil {
		panic(err)
	}
	return q
}

// MustChoice panics on an invalid static question definition.
func MustChoice[T ~string](key, instructions string, options ...Option[T]) Choice[T] {
	q, err := NewChoice(key, instructions, options...)
	if err != nil {
		panic(err)
	}
	return q
}

func NewScore(key, instructions string, levels ...string) (Score, error) {
	spec := QuestionSpec{Key: key, Kind: ScoreKind, Instructions: instructions, Levels: append([]string(nil), levels...)}
	if err := validateQuestion(spec); err != nil {
		return Score{}, err
	}
	return Score{d: &definition{spec: spec}}, nil
}

func NewScoreContent(key string, instructions Content, levels ...Content) (Score, error) {
	spec := QuestionSpec{Key: key, Kind: ScoreKind, InstructionsContent: instructions, LevelContents: append([]Content(nil), levels...)}
	if err := validateQuestion(spec); err != nil {
		return Score{}, err
	}
	return Score{d: &definition{spec: spec}}, nil
}

// MustScoreContent panics on an invalid static question definition.
func MustScoreContent(key string, instructions Content, levels ...Content) Score {
	q, err := NewScoreContent(key, instructions, levels...)
	if err != nil {
		panic(err)
	}
	return q
}

// MustScore panics on an invalid static question definition.
func MustScore(key, instructions string, levels ...string) Score {
	q, err := NewScore(key, instructions, levels...)
	if err != nil {
		panic(err)
	}
	return q
}

func NewNoul(key, instructions string) (Noul, error) {
	spec := QuestionSpec{Key: key, Kind: NoulKind, Instructions: instructions}
	if err := validateQuestion(spec); err != nil {
		return Noul{}, err
	}
	return Noul{d: &definition{spec: spec}}, nil
}

func NewNoulContent(key string, instructions Content) (Noul, error) {
	spec := QuestionSpec{Key: key, Kind: NoulKind, InstructionsContent: instructions}
	if err := validateQuestion(spec); err != nil {
		return Noul{}, err
	}
	return Noul{d: &definition{spec: spec}}, nil
}

// MustNoulContent panics on an invalid static question definition.
func MustNoulContent(key string, instructions Content) Noul {
	q, err := NewNoulContent(key, instructions)
	if err != nil {
		panic(err)
	}
	return q
}

// MustNoul panics on an invalid static question definition.
func MustNoul(key, instructions string) Noul {
	q, err := NewNoul(key, instructions)
	if err != nil {
		panic(err)
	}
	return q
}

// When returns a new handle with descriptions for true and false. Ask and In
// must use the returned handle. The original handle remains unchanged.
func (q Noul) When(yes, no string) Noul {
	if q.d == nil {
		return Noul{}
	}
	spec := q.d.spec
	spec.Yes, spec.No = yes, no
	spec.YesContent, spec.NoContent = Content{}, Content{}
	return Noul{d: &definition{spec: spec}}
}

// WhenContent returns a new handle with required JSON descriptions for both
// outcomes. Use the returned handle for both Ask and In.
func (q Noul) WhenContent(yes, no Content) (Noul, error) {
	if q.d == nil || yes.IsZero() || no.IsZero() {
		return Noul{}, fmt.Errorf("systemone/question: %w: handle and both descriptions are required", ErrInvalidQuestion)
	}
	spec := q.d.spec
	spec.Yes, spec.No = "", ""
	spec.YesContent, spec.NoContent = yes, no
	if err := validateQuestion(spec); err != nil {
		return Noul{}, err
	}
	return Noul{d: &definition{spec: spec}}, nil
}

type ChoiceResult[T ~string] struct {
	Choice        T
	Probabilities map[T]float64
	Confidence    *float64
	Source        ProbabilitySource
}

// ScoreResult retains the requested level descriptions in Levels or
// LevelContents, matching the question's representation. Both slices are copies.
type ScoreResult struct {
	Score         float64
	Probabilities []float64
	Levels        []string
	LevelContents []Content
	Confidence    *float64
	Source        ProbabilitySource
}

type NoulResult struct {
	Probability float64
	Source      ProbabilitySource
}

// In returns the result for this exact handle, or ErrNotAsked. Successful Ask
// validates all results, but cannot prove which handle callers will read later.
// Returned maps, slices, and pointers are independent copies.
func (q Choice[T]) In(answers Answers) (ChoiceResult[T], error) {
	a, err := answers.lookup(q.d)
	if err != nil {
		return ChoiceResult[T]{}, err
	}
	probabilities := make(map[T]float64, len(a.Choice.Probabilities))
	for key, p := range a.Choice.Probabilities {
		probabilities[T(key)] = p
	}
	return ChoiceResult[T]{Choice: T(a.Choice.Choice), Probabilities: probabilities, Confidence: copyFloat(a.Choice.Confidence), Source: a.Source}, nil
}

func (q Score) In(answers Answers) (ScoreResult, error) {
	a, err := answers.lookup(q.d)
	if err != nil {
		return ScoreResult{}, err
	}
	return ScoreResult{Score: a.Score.Score, Probabilities: append([]float64(nil), a.Score.Probabilities...), Levels: append([]string(nil), q.d.spec.Levels...), LevelContents: append([]Content(nil), q.d.spec.LevelContents...), Confidence: copyFloat(a.Score.Confidence), Source: a.Source}, nil
}

func (q Noul) In(answers Answers) (NoulResult, error) {
	a, err := answers.lookup(q.d)
	if err != nil {
		return NoulResult{}, err
	}
	return NoulResult{Probability: a.Noul.Probability, Source: a.Source}, nil
}

func (a Answers) lookup(d *definition) (Answer, error) {
	if d != nil {
		if answer, ok := a.values[d]; ok {
			return answer, nil
		}
		return Answer{}, fmt.Errorf("systemone/result %q: %w", d.spec.Key, ErrNotAsked)
	}
	return Answer{}, fmt.Errorf("systemone/result: %w", ErrNotAsked)
}
