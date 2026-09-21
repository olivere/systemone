// Package sonetest provides scripted backends for testing application decisions.
package sonetest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/olivere/systemone"
)

// ErrExhausted means an evaluation had no remaining scripted step.
var ErrExhausted = errors.New("sonetest: no scripted steps remaining")

type Step struct {
	Response systemone.Response
	Err      error
}

type Config struct {
	Steps []Step
	// Capabilities defaults to all question kinds and structured content with no declared limits.
	// A non-nil pointer can declare any capabilities, including none.
	Capabilities *systemone.Capabilities
}

// Fake consumes one step per uncanceled evaluation. It is safe for concurrent
// use; simultaneous calls consume steps in the order they acquire its lock.
// Scripted responses are not validated, so tests can supply malformed answers.
type Fake struct {
	mu    sync.Mutex
	caps  systemone.Capabilities
	steps []Step
	next  int
	calls []systemone.Request
}

var _ systemone.Backend = (*Fake)(nil)

func New(config Config) *Fake {
	f := &Fake{caps: systemone.Capabilities{Choice: true, Score: true, Noul: true, StructuredContent: true}}
	if config.Capabilities != nil {
		f.caps = *config.Capabilities
	}
	f.steps = make([]Step, len(config.Steps))
	for i, step := range config.Steps {
		f.steps[i] = Step{Response: cloneResponse(step.Response), Err: step.Err}
	}
	return f
}

func (f *Fake) Capabilities() systemone.Capabilities { return f.caps }

// Evaluate records uncanceled calls, including calls made after exhaustion.
// Cancellation checked after acquiring the lock leaves the script untouched.
func (f *Fake) Evaluate(ctx context.Context, request systemone.Request) (systemone.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return systemone.Response{}, err
	}
	f.calls = append(f.calls, cloneRequest(request))
	if f.next == len(f.steps) {
		return systemone.Response{}, ErrExhausted
	}
	step := f.steps[f.next]
	f.next++
	return cloneResponse(step.Response), step.Err
}

// Calls returns independent snapshots of recorded requests.
func (f *Fake) Calls() []systemone.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([]systemone.Request, len(f.calls))
	for i, request := range f.calls {
		calls[i] = cloneRequest(request)
	}
	return calls
}

func (f *Fake) Remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.steps) - f.next
}

func cloneRequest(request systemone.Request) systemone.Request {
	request.State = slices.Clone(request.State)
	request.Questions = slices.Clone(request.Questions)
	for i := range request.Questions {
		request.Questions[i].Options = slices.Clone(request.Questions[i].Options)
		request.Questions[i].Levels = slices.Clone(request.Questions[i].Levels)
		request.Questions[i].LevelContents = slices.Clone(request.Questions[i].LevelContents)
	}
	return request
}

func cloneResponse(response systemone.Response) systemone.Response {
	response.Answers = maps.Clone(response.Answers)
	for key, answer := range response.Answers {
		if answer.Choice != nil {
			choice := *answer.Choice
			choice.Probabilities = maps.Clone(choice.Probabilities)
			choice.Confidence = cloneFloat(choice.Confidence)
			answer.Choice = &choice
		}
		if answer.Score != nil {
			score := *answer.Score
			score.Probabilities = slices.Clone(score.Probabilities)
			score.Confidence = cloneFloat(score.Confidence)
			answer.Score = &score
		}
		if answer.Noul != nil {
			noul := *answer.Noul
			answer.Noul = &noul
		}
		response.Answers[key] = answer
	}
	return response
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return new(*value)
}
