// Package systemone evaluates typed questions against application data.
// Backends supply decisions; applications own thresholds and side effects.
package systemone

import (
	"context"
	"encoding/json/jsontext"
	"errors"
)

var (
	ErrInvalidQuestion = errors.New("invalid question")
	ErrInvalidState    = errors.New("invalid state")
	ErrUnsupported     = errors.New("unsupported evaluation")
	ErrInvalidResponse = errors.New("invalid response")
	ErrNotAsked        = errors.New("question was not asked")
)

// Kind identifies the semantics of a question, independently of its transport.
type Kind string

const (
	ChoiceKind Kind = "choice"
	ScoreKind  Kind = "score"
	NoulKind   Kind = "noul"
)

// ProbabilitySource describes how the numbers were obtained, not their accuracy.
// Calibration must be measured for a particular model, task, and dataset.
type ProbabilitySource string

const (
	Unspecified ProbabilitySource = ""
	Native      ProbabilitySource = "native"
	Estimated   ProbabilitySource = "estimated"
)

// Capabilities declares supported operations and known limits. Zero ceilings
// mean no declared limit, not a guarantee of accuracy at arbitrary sizes.
type Capabilities struct {
	// StructuredContent permits the Content-based question fields, including
	// Content values that encode plain strings.
	StructuredContent bool
	Choice            bool
	Score             bool
	Noul              bool
	MaxOptions        int
	MaxLevels         int
	MaxQuestions      int
}

// Backend implementations must be safe for concurrent calls and honor context
// cancellation. Evaluate must not retain or mutate its request after returning.
type Backend interface {
	Capabilities() Capabilities
	Evaluate(context.Context, Request) (Response, error)
}

// Request is one JSON state evaluated against independent questions. State is
// a JSON string, object, or array. Questions must not depend on other answers.
// No provider model names, credentials, or HTTP fields belong in this type.
type Request struct {
	State     jsontext.Value
	Questions []QuestionSpec
}

// QuestionSpec is the backend-facing description of a typed question.
// Each Content field replaces its corresponding string; do not set both.
// Levels and LevelContents are mutually exclusive representations.
type QuestionSpec struct {
	Key                 string
	Kind                Kind
	Instructions        string
	Options             []Option[string]
	Levels              []string
	Yes                 string
	No                  string
	InstructionsContent Content `json:",omitzero"`
	LevelContents       []Content
	YesContent          Content `json:",omitzero"`
	NoContent           Content `json:",omitzero"`
}

// LevelCount returns the number of score levels in either representation.
func (q QuestionSpec) LevelCount() int {
	if len(q.LevelContents) != 0 {
		return len(q.LevelContents)
	}
	return len(q.Levels)
}

// Response contains one answer for every requested key, plus diagnostic metadata.
type Response struct {
	Answers map[string]Answer
	Model   string
	Usage   Usage
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// Answer holds exactly one result. Backends must reject missing wire fields
// rather than replacing them with zero values. Confidence may be unavailable.
type Answer struct {
	Choice *ChoiceAnswer
	Score  *ScoreAnswer
	Noul   *NoulAnswer
	Source ProbabilitySource
}

type ChoiceAnswer struct {
	Choice        string
	Probabilities map[string]float64
	Confidence    *float64
}

// ScoreAnswer.Score is the expected zero-based level, not the most likely level.
type ScoreAnswer struct {
	Score         float64
	Probabilities []float64
	Confidence    *float64
}

// NoulAnswer.Probability is P(true); it is not the certainty of the answer.
type NoulAnswer struct {
	Probability float64
}
