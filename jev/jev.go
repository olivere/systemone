// Package jev connects systemone to TypeSafe AI and compatible HTTP servers.
package jev

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olivere/systemone"
)

const maxResponseBytes = 8 << 20

// Config configures the HTTP endpoint. BaseURL is an origin or path prefix;
// the backend appends /v1/systemone. An empty BaseURL selects TypeSafe AI.
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
	// ProbabilitySource overrides the default: Native for TypeSafe AI and
	// Unspecified for custom servers. Native does not imply calibration.
	ProbabilitySource *systemone.ProbabilitySource
	// Capabilities overrides the known limits, for compatible servers.
	Capabilities *systemone.Capabilities
}

// Backend evaluates questions over HTTP. Construct it with New.
type Backend struct {
	endpoint     string
	key          string
	model        string
	client       *http.Client
	source       systemone.ProbabilitySource
	capabilities systemone.Capabilities
	debug        io.Writer
	debugMu      sync.Mutex
}

// New constructs a backend without making a network request. A custom client
// is copied to preserve its transport while disabling redirects for credentials.
func New(cfg Config) (*Backend, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.typesafe.ai"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("jev/new: invalid base URL")
	}
	official := u.Scheme == "https" && u.Host == "api.typesafe.ai" && strings.TrimRight(u.Path, "/") == ""
	if official && strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("jev/new: API key required")
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, fmt.Errorf("jev/new: invalid API key")
	}
	model := cfg.Model
	if model == "" {
		model = "jev-latest"
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("jev/new: empty model")
	}
	source := systemone.Unspecified
	if official {
		source = systemone.Native
	}
	if cfg.ProbabilitySource != nil {
		source = *cfg.ProbabilitySource
	}
	if source != systemone.Unspecified && source != systemone.Native && source != systemone.Estimated {
		return nil, fmt.Errorf("jev/new: invalid probability source")
	}
	client := http.Client{Timeout: 10 * time.Second}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	capabilities := systemone.Capabilities{Choice: true, Score: true, Noul: true, StructuredContent: true}
	if official {
		capabilities.MaxOptions = 255
		capabilities.MaxLevels = 10
	}
	if cfg.Capabilities != nil {
		capabilities = *cfg.Capabilities
	}
	if capabilities.MaxOptions < 0 || capabilities.MaxLevels < 0 || capabilities.MaxQuestions < 0 {
		return nil, fmt.Errorf("jev/new: invalid capabilities")
	}
	return &Backend{endpoint: strings.TrimRight(base, "/") + "/v1/systemone", key: cfg.APIKey, model: model, client: &client, source: source, capabilities: capabilities, debug: cfg.Debug}, nil
}

// Capabilities returns the documented API limits. No question-count ceiling is published.
func (b *Backend) Capabilities() systemone.Capabilities {
	return b.capabilities
}

// HTTPError reports an unsuccessful HTTP response. Response bodies are omitted
// because servers may echo credentials or application state in error messages.
type HTTPError struct{ StatusCode int }

func (e *HTTPError) Error() string { return fmt.Sprintf("jev/evaluate: HTTP status %d", e.StatusCode) }

type wireQuestion struct {
	Type         systemone.Kind `json:"type"`
	Instructions any            `json:"instructions"`
	Criteria     any            `json:"criteria,omitempty"`
}

type wireAnswer struct {
	Type          systemone.Kind            `json:"type"`
	Choice        *string                   `json:"choice"`
	Score         *float64                  `json:"score"`
	Noul          *float64                  `json:"noul"`
	Confidence    *float64                  `json:"confidence"`
	Probabilities map[string]*float64       `json:"probabilities"`
	Legend        map[string]jsontext.Value `json:"legend"`
}

// Evaluate sends one batch without retries and validates the returned decisions.
func (b *Backend) Evaluate(ctx context.Context, request systemone.Request) (systemone.Response, error) {
	if err := systemone.CheckRequest(request, b.Capabilities()); err != nil {
		return systemone.Response{}, err
	}
	questions := make(map[string]wireQuestion, len(request.Questions))
	for _, q := range request.Questions {
		w := wireQuestion{Type: q.Kind, Instructions: contentValue(q.Instructions, q.InstructionsContent)}
		switch q.Kind {
		case systemone.ChoiceKind:
			criteria := make(map[string]any, len(q.Options))
			for _, option := range q.Options {
				if option.Description == "" && option.Content.IsZero() {
					criteria[option.Value] = nil
				} else {
					criteria[option.Value] = contentValue(option.Description, option.Content)
				}
			}
			w.Criteria = criteria
		case systemone.ScoreKind:
			w.Criteria = q.Levels
			if len(q.LevelContents) != 0 {
				levels := make([]jsontext.Value, len(q.LevelContents))
				for i, level := range q.LevelContents {
					levels[i] = level.JSON()
				}
				w.Criteria = levels
			}
		case systemone.NoulKind:
			if q.Yes != "" || q.No != "" || !q.YesContent.IsZero() || !q.NoContent.IsZero() {
				w.Criteria = map[string]any{"true": contentValue(q.Yes, q.YesContent), "false": contentValue(q.No, q.NoContent)}
			}
		}
		questions[q.Key] = w
	}
	body, err := json.Marshal(struct {
		Model     string                  `json:"model"`
		State     jsontext.Value          `json:"state"`
		Questions map[string]wireQuestion `json:"questions"`
	}{b.model, request.State, questions})
	if err != nil {
		return systemone.Response{}, fmt.Errorf("jev/evaluate: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
	if err != nil {
		return systemone.Response{}, fmt.Errorf("jev/evaluate: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if b.key != "" {
		req.Header.Set("Authorization", "Bearer "+b.key)
	}
	b.debugRequest(req, body)
	res, err := b.client.Do(req)
	if err != nil {
		return systemone.Response{}, fmt.Errorf("jev/evaluate: send request: %w", err)
	}
	// Closing a read-only response cannot affect the evaluation result.
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if b.debug != nil {
			data, readErr := io.ReadAll(io.LimitReader(res.Body, maxDebugBodyBytes+1))
			b.debugResponse(res, data, readErr != nil)
		}
		return systemone.Response{}, &HTTPError{StatusCode: res.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	b.debugResponse(res, data, err != nil)
	if err != nil {
		return systemone.Response{}, fmt.Errorf("jev/evaluate: read response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return systemone.Response{}, invalid("response exceeds 8 MiB")
	}
	var wire struct {
		Model   string                `json:"model"`
		Answers map[string]wireAnswer `json:"answers"`
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
	for key, w := range wire.Answers {
		answer := systemone.Answer{Source: b.source}
		switch w.Type {
		case systemone.ChoiceKind:
			if w.Choice == nil || w.Confidence == nil || w.Probabilities == nil {
				return systemone.Response{}, invalid("incomplete choice answer")
			}
			probs := make(map[string]float64, len(w.Probabilities))
			for key, value := range w.Probabilities {
				if value == nil {
					return systemone.Response{}, invalid("null probability")
				}
				probs[key] = *value
			}
			answer.Choice = &systemone.ChoiceAnswer{Choice: *w.Choice, Probabilities: probs, Confidence: w.Confidence}
		case systemone.ScoreKind:
			if w.Score == nil || w.Confidence == nil || w.Probabilities == nil || w.Legend == nil || len(w.Legend) != len(w.Probabilities) {
				return systemone.Response{}, invalid("incomplete score answer")
			}
			probs := make([]float64, len(w.Probabilities))
			for key, value := range w.Probabilities {
				i, err := strconv.Atoi(key)
				if err != nil || i < 0 || i >= len(probs) || strconv.Itoa(i) != key || value == nil {
					return systemone.Response{}, invalid("invalid score level probability")
				}
				legend := bytes.TrimSpace(w.Legend[key])
				if len(legend) == 0 || (legend[0] != '"' && legend[0] != '{' && legend[0] != '[') {
					return systemone.Response{}, invalid("invalid score legend")
				}
				probs[i] = *value
			}
			answer.Score = &systemone.ScoreAnswer{Score: *w.Score, Probabilities: probs, Confidence: w.Confidence}
		case systemone.NoulKind:
			if w.Noul == nil {
				return systemone.Response{}, invalid("incomplete noul answer")
			}
			answer.Noul = &systemone.NoulAnswer{Probability: *w.Noul}
		default:
			return systemone.Response{}, invalid("missing or unknown answer type")
		}
		response.Answers[key] = answer
	}
	if err := systemone.ValidateResponse(request, response); err != nil {
		return systemone.Response{}, err
	}
	return response, nil
}

func invalid(message string) error {
	return fmt.Errorf("jev/evaluate: %w: %s", systemone.ErrInvalidResponse, message)
}

func contentValue(text string, content systemone.Content) any {
	if !content.IsZero() {
		return content.JSON()
	}
	return text
}
