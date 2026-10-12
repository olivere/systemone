// Package openai connects systemone to the OpenAI Decisions API.
package openai

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/olivere/systemone"
	"github.com/olivere/systemone/internal/httpdebug"
)

const maxResponseBytes = 8 << 20

// ErrRefused reports that the model declined at least one question. The API
// may still answer the other questions, but an Ask needs every answer.
var ErrRefused = errors.New("question refused")

// Config configures the HTTP endpoint. BaseURL is an origin or path prefix;
// the backend appends /v1/decisions. An empty BaseURL selects api.openai.com.
type Config struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
	// Debug enables synchronous HTTP dumps. Nil disables debugging. Headers
	// outside a protocol allowlist are redacted, but bodies are NOT redacted and
	// may contain secrets. Each body is limited to 64 KiB. The writer must return
	// promptly; callers must synchronize writers shared by multiple backends.
	// Debug write failures do not affect evaluation results.
	Debug io.Writer
}

// Backend evaluates questions with the Decisions API. Construct it with New.
type Backend struct {
	endpoint string
	key      string
	model    string
	client   *http.Client
	debug    *httpdebug.Dumper
}

// New constructs a backend without making a network request. A custom client
// is copied to preserve its transport while disabling redirects for credentials.
func New(cfg Config) (*Backend, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.openai.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("openai/new: invalid base URL")
	}
	// Every request carries the API key, so plain HTTP must not leave the host.
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, fmt.Errorf("openai/new: base URL must use https")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("openai/new: API key required")
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, fmt.Errorf("openai/new: invalid API key")
	}
	model := cfg.Model
	if model == "" {
		model = "gpt-6-luna"
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("openai/new: empty model")
	}
	client := http.Client{Timeout: 10 * time.Second}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Backend{endpoint: strings.TrimRight(base, "/") + "/v1/decisions", key: cfg.APIKey, model: model, client: &client, debug: httpdebug.New("openai", cfg.Debug)}, nil
}

// Capabilities returns the API's limits. The guide does not publish the level
// and question ceilings; the API rejects larger arrays with HTTP 400 and code
// array_above_max_length.
func (b *Backend) Capabilities() systemone.Capabilities {
	return systemone.Capabilities{StructuredContent: true, Choice: true, Score: true, Noul: true, MaxOptions: 255, MaxLevels: 10, MaxQuestions: 200}
}

// HTTPError reports an unsuccessful HTTP response. Type and Code come from the
// API's error object when they are short identifiers. The error message is
// omitted because servers may echo credentials or application state in it.
type HTTPError struct {
	StatusCode int
	Type       string
	Code       string
	RequestID  string
}

func (e *HTTPError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "openai/evaluate: HTTP status %d", e.StatusCode)
	if e.Code != "" {
		fmt.Fprintf(&b, ", code %s", e.Code)
	}
	if e.Type != "" {
		fmt.Fprintf(&b, ", type %s", e.Type)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, ", request %s", e.RequestID)
	}
	return b.String()
}

type wireChoice struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type wireLevel struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type wireQuestion struct {
	Type         string       `json:"type"`
	Name         string       `json:"name"`
	Instructions string       `json:"instructions"`
	Choices      []wireChoice `json:"choices,omitempty"`
	Levels       []wireLevel  `json:"levels,omitempty"`
}

type wireProbability struct {
	Value       jsontext.Value `json:"value"`
	Label       *string        `json:"label"`
	Probability *float64       `json:"probability"`
}

type wireAnswer struct {
	Type          string            `json:"type"`
	Name          *string           `json:"name"`
	Probability   *float64          `json:"probability"`
	Choice        *string           `json:"choice"`
	Score         *float64          `json:"score"`
	Confidence    *float64          `json:"confidence"`
	Probabilities []wireProbability `json:"probabilities"`
}

// Evaluate sends one batch without retries and validates the returned decisions.
func (b *Backend) Evaluate(ctx context.Context, request systemone.Request) (systemone.Response, error) {
	if err := systemone.CheckRequest(request, b.Capabilities()); err != nil {
		return systemone.Response{}, err
	}
	specs := make(map[string]systemone.QuestionSpec, len(request.Questions))
	questions := make([]wireQuestion, len(request.Questions))
	for i, q := range request.Questions {
		specs[q.Key] = q
		questions[i] = encodeQuestion(q)
	}
	body, err := json.Marshal(struct {
		Model     string         `json:"model"`
		Input     string         `json:"input"`
		Questions []wireQuestion `json:"questions"`
	}{b.model, text("", systemone.Content{}, request.State), questions})
	if err != nil {
		return systemone.Response{}, fmt.Errorf("openai/evaluate: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
	if err != nil {
		return systemone.Response{}, fmt.Errorf("openai/evaluate: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.key)
	b.debug.Request(req, body)
	res, err := b.client.Do(req)
	if err != nil {
		return systemone.Response{}, fmt.Errorf("openai/evaluate: send request: %w", err)
	}
	// Closing a read-only response cannot affect the evaluation result.
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, readErr := io.ReadAll(io.LimitReader(res.Body, httpdebug.MaxBodyBytes+1))
		b.debug.Response(res, data, readErr != nil)
		return systemone.Response{}, httpError(res, data)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	b.debug.Response(res, data, err != nil)
	if err != nil {
		return systemone.Response{}, fmt.Errorf("openai/evaluate: read response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return systemone.Response{}, invalid("response exceeds 8 MiB")
	}
	var wire struct {
		Model   string       `json:"model"`
		Answers []wireAnswer `json:"answers"`
		Usage   *struct {
			InputTokens  *int64 `json:"input_tokens"`
			OutputTokens *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return systemone.Response{}, invalid("malformed JSON response")
	}
	if strings.TrimSpace(wire.Model) == "" {
		return systemone.Response{}, invalid("missing model")
	}
	if wire.Usage == nil || wire.Usage.InputTokens == nil || wire.Usage.OutputTokens == nil {
		return systemone.Response{}, invalid("incomplete usage")
	}
	response := systemone.Response{Model: wire.Model, Answers: make(map[string]systemone.Answer, len(wire.Answers)), Usage: systemone.Usage{InputTokens: *wire.Usage.InputTokens, OutputTokens: *wire.Usage.OutputTokens}}
	var refused []string
	seen := make(map[string]bool, len(wire.Answers))
	for _, w := range wire.Answers {
		if w.Name == nil {
			return systemone.Response{}, invalid("answer without name")
		}
		q, ok := specs[*w.Name]
		if !ok || seen[*w.Name] {
			return systemone.Response{}, invalid("unknown or duplicate answer name")
		}
		seen[*w.Name] = true
		if w.Type == "refusal" {
			refused = append(refused, *w.Name)
			continue
		}
		answer, err := decodeAnswer(q, w)
		if err != nil {
			return systemone.Response{}, err
		}
		response.Answers[*w.Name] = answer
	}
	if len(refused) != 0 {
		return systemone.Response{}, fmt.Errorf("openai/evaluate: %w: %q", ErrRefused, refused)
	}
	if err := systemone.ValidateResponse(request, response); err != nil {
		return systemone.Response{}, err
	}
	return response, nil
}

func encodeQuestion(q systemone.QuestionSpec) wireQuestion {
	w := wireQuestion{Name: q.Key, Instructions: text(q.Instructions, q.InstructionsContent, nil)}
	switch q.Kind {
	case systemone.ChoiceKind:
		w.Type = "choice"
		for _, option := range q.Options {
			w.Choices = append(w.Choices, wireChoice{Value: option.Value, Description: text(option.Description, option.Content, nil)})
		}
	case systemone.ScoreKind:
		w.Type = "score"
		for _, label := range levelLabels(q) {
			w.Levels = append(w.Levels, wireLevel{Label: label})
		}
	case systemone.NoulKind:
		// Predicates have no outcome descriptions, so they extend the instructions.
		w.Type = "predicate"
		if yes := text(q.Yes, q.YesContent, nil); yes != "" {
			w.Instructions += "\n\nTrue if: " + yes
		}
		if no := text(q.No, q.NoContent, nil); no != "" {
			w.Instructions += "\n\nFalse if: " + no
		}
	}
	return w
}

func levelLabels(q systemone.QuestionSpec) []string {
	if len(q.LevelContents) == 0 {
		return q.Levels
	}
	labels := make([]string, len(q.LevelContents))
	for i, level := range q.LevelContents {
		labels[i] = text("", level, nil)
	}
	return labels
}

// text returns plain text for a field the API accepts only as a string. JSON
// strings are decoded; objects and arrays are sent as their JSON text. The
// systemone constructors have already validated content and state.
func text(plain string, content systemone.Content, value jsontext.Value) string {
	if !content.IsZero() {
		value = content.JSON()
	}
	if value == nil {
		return plain
	}
	var s string
	if err := json.Unmarshal(value, &s); err == nil {
		return s
	}
	return string(bytes.TrimSpace(value))
}

func decodeAnswer(q systemone.QuestionSpec, w wireAnswer) (systemone.Answer, error) {
	answer := systemone.Answer{Source: systemone.Native}
	switch w.Type {
	case "choice":
		if w.Choice == nil || w.Confidence == nil || w.Probabilities == nil {
			return systemone.Answer{}, invalid("incomplete choice answer")
		}
		probs := make(map[string]float64, len(w.Probabilities))
		for _, p := range w.Probabilities {
			var value string
			if p.Probability == nil || json.Unmarshal(p.Value, &value) != nil {
				return systemone.Answer{}, invalid("invalid choice probability")
			}
			if _, ok := probs[value]; ok {
				return systemone.Answer{}, invalid("duplicate choice probability")
			}
			probs[value] = *p.Probability
		}
		answer.Choice = &systemone.ChoiceAnswer{Choice: *w.Choice, Probabilities: probs, Confidence: w.Confidence}
	case "score":
		labels := levelLabels(q)
		if w.Score == nil || w.Confidence == nil || len(w.Probabilities) != len(labels) {
			return systemone.Answer{}, invalid("incomplete score answer")
		}
		probs := make([]float64, len(labels))
		seen := make([]bool, len(labels))
		for _, p := range w.Probabilities {
			var i int
			if p.Probability == nil || p.Label == nil || json.Unmarshal(p.Value, &i) != nil || i < 0 || i >= len(labels) || seen[i] || *p.Label != labels[i] {
				return systemone.Answer{}, invalid("invalid score level probability")
			}
			seen[i] = true
			probs[i] = *p.Probability
		}
		answer.Score = &systemone.ScoreAnswer{Score: *w.Score, Probabilities: probs, Confidence: w.Confidence}
	case "predicate":
		if w.Probability == nil {
			return systemone.Answer{}, invalid("incomplete predicate answer")
		}
		answer.Noul = &systemone.NoulAnswer{Probability: *w.Probability}
	default:
		return systemone.Answer{}, invalid("missing or unknown answer type")
	}
	return answer, nil
}

func httpError(res *http.Response, body []byte) *HTTPError {
	e := &HTTPError{StatusCode: res.StatusCode, RequestID: identifier(res.Header.Get("X-Request-Id"))}
	var wire struct {
		Error struct {
			Type jsontext.Value `json:"type"`
			Code jsontext.Value `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &wire) == nil {
		var s string
		if json.Unmarshal(wire.Error.Type, &s) == nil {
			e.Type = identifier(s)
		}
		s = ""
		if json.Unmarshal(wire.Error.Code, &s) == nil {
			e.Code = identifier(s)
		}
	}
	return e
}

// identifier keeps short machine-readable values and drops anything that could
// carry free text from the server into error messages.
func identifier(s string) string {
	if len(s) == 0 || len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return ""
		}
	}
	return s
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func invalid(message string) error {
	return fmt.Errorf("openai/evaluate: %w: %s", systemone.ErrInvalidResponse, message)
}
