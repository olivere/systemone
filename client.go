package systemone

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"time"
)

type Config struct {
	Backend  Backend
	Recorder Recorder
}

// Recorder runs synchronously once per Ask, including failed calls. It must be
// concurrency-safe and return promptly. Observations omit the input state.
type Recorder func(context.Context, Observation)

type Observation struct {
	Duration time.Duration
	Response Response
	Err      error
}

type Client struct {
	backend  Backend
	recorder Recorder
}

func New(config Config) (*Client, error) {
	if isNil(config.Backend) {
		return nil, fmt.Errorf("systemone/new: backend is required")
	}
	return &Client{backend: config.Backend, recorder: config.Recorder}, nil
}

// Answers contains validated results. Its zero value contains no answers.
type Answers struct {
	values map[*definition]Answer
	model  string
	usage  Usage
}

func (a Answers) Model() string { return a.model }
func (a Answers) Usage() Usage  { return a.usage }

// Check verifies schemas and declared capabilities before processing any data.
func (c *Client) Check(questions ...Question) error {
	req, _, err := prepare(jsontext.Value(`{}`), questions)
	if err != nil {
		return err
	}
	return CheckRequest(req, c.backend.Capabilities())
}

// Ask serializes state once and evaluates all questions in one backend call.
// State must encode as a JSON string, object, or array. Callers must not mutate
// it while serialization is running. An error returns no usable answers.
func (c *Client) Ask(ctx context.Context, state any, questions ...Question) (answers Answers, err error) {
	start := time.Now()
	var response Response
	if c.recorder != nil {
		defer func() {
			c.recorder(ctx, Observation{Duration: time.Since(start), Response: cloneResponse(response), Err: err})
		}()
	}
	if err = ctx.Err(); err != nil {
		return Answers{}, err
	}
	data, marshalErr := json.Marshal(state)
	if marshalErr != nil {
		return Answers{}, fmt.Errorf("systemone/ask: %w: %w", ErrInvalidState, marshalErr)
	}
	req, definitions, err := prepare(data, questions)
	if err != nil {
		return Answers{}, err
	}
	if err = CheckRequest(req, c.backend.Capabilities()); err != nil {
		return Answers{}, err
	}
	response, err = c.backend.Evaluate(ctx, cloneRequest(req))
	if err != nil {
		return Answers{}, fmt.Errorf("systemone/ask: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return Answers{}, err
	}
	if err = ValidateResponse(req, response); err != nil {
		return Answers{}, err
	}
	response = cloneResponse(response)
	answers = Answers{values: make(map[*definition]Answer, len(definitions)), model: response.Model, usage: response.Usage}
	for _, d := range definitions {
		answers.values[d] = response.Answers[d.spec.Key]
	}
	return answers, nil
}

func prepare(state jsontext.Value, questions []Question) (Request, []*definition, error) {
	req := Request{State: state}
	definitions := make([]*definition, 0, len(questions))
	for _, q := range questions {
		if isNil(q) {
			return Request{}, nil, fmt.Errorf("systemone/ask: %w: nil handle", ErrInvalidQuestion)
		}
		d := q.definition()
		if d == nil {
			return Request{}, nil, fmt.Errorf("systemone/ask: %w: zero handle", ErrInvalidQuestion)
		}
		req.Questions = append(req.Questions, d.spec)
		definitions = append(definitions, d)
	}
	return req, definitions, nil
}
