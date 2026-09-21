package jev

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/olivere/systemone"
)

func mixedRequest() systemone.Request {
	return systemone.Request{State: []byte(`{"ticket":"refund"}`), Questions: []systemone.QuestionSpec{
		{Key: "team", Kind: systemone.ChoiceKind, Instructions: "Which team?", Options: []systemone.Option[string]{{Value: "billing", Description: "Invoices"}, {Value: "technical", Description: "Bugs"}}},
		{Key: "urgency", Kind: systemone.ScoreKind, Instructions: "Urgency?", Levels: []string{"Low", "High"}},
		{Key: "refund", Kind: systemone.NoulKind, Instructions: "Refund?", Yes: "Requested", No: "Not requested"},
	}}
}

const mixedResponse = `{"model":"jev-test","usage":{"input_tokens":10,"output_tokens":0},"answers":{"team":{"type":"choice","choice":"billing","probabilities":{"billing":1,"technical":0},"confidence":0},"urgency":{"type":"score","score":0,"probabilities":{"0":1,"1":0},"legend":{"0":"Low","1":"High"},"confidence":0},"refund":{"type":"noul","noul":0}}}`

func testBackend(t *testing.T, handler http.HandlerFunc) *Backend {
	t.Helper()
	server := httptest.NewTestServer(t, handler)
	backend, err := New(Config{BaseURL: server.URL, APIKey: "test-key", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func TestEvaluateMixed(t *testing.T) {
	t.Parallel()
	backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s, headers %v", r.Method, r.URL.Path, r.Header)
		}
		var got map[string]any
		if err := json.UnmarshalRead(r.Body, &got); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var want map[string]any
		if err := json.Unmarshal([]byte(`{"model":"jev-latest","state":{"ticket":"refund"},"questions":{"team":{"type":"choice","instructions":"Which team?","criteria":{"billing":"Invoices","technical":"Bugs"}},"urgency":{"type":"score","instructions":"Urgency?","criteria":["Low","High"]},"refund":{"type":"noul","instructions":"Refund?","criteria":{"true":"Requested","false":"Not requested"}}}}`), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("payload = %#v, want %#v", got, want)
		}
		if _, err := fmt.Fprint(w, mixedResponse); err != nil {
			t.Error(err)
		}
	})
	response, err := backend.Evaluate(t.Context(), mixedRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.Model != "jev-test" || response.Usage.InputTokens != 10 || response.Answers["team"].Choice.Choice != "billing" || response.Answers["urgency"].Score.Score != 0 || response.Answers["refund"].Noul.Probability != 0 || response.Answers["team"].Source != systemone.Unspecified {
		t.Fatalf("unexpected response: %#v", response)
	}
	if *response.Answers["team"].Choice.Confidence != 0 {
		t.Fatal("zero confidence lost")
	}
}

func TestRejectInvalidResponse(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"duplicate key":         strings.Replace(mixedResponse, `"noul":0`, `"noul":0,"noul":1`, 1),
		"missing type":          strings.Replace(mixedResponse, `"type":"noul",`, ``, 1),
		"missing noul":          strings.Replace(mixedResponse, `,"noul":0`, ``, 1),
		"null noul":             strings.Replace(mixedResponse, `"noul":0`, `"noul":null`, 1),
		"missing choice":        strings.Replace(mixedResponse, `"choice":"billing",`, ``, 1),
		"null choice":           strings.Replace(mixedResponse, `"choice":"billing"`, `"choice":null`, 1),
		"missing confidence":    strings.Replace(mixedResponse, `,"confidence":0`, ``, 1),
		"null confidence":       strings.Replace(mixedResponse, `"confidence":0`, `"confidence":null`, 1),
		"missing score":         strings.Replace(mixedResponse, `"score":0,`, ``, 1),
		"null score":            strings.Replace(mixedResponse, `"score":0`, `"score":null`, 1),
		"null probability":      strings.Replace(mixedResponse, `"technical":0`, `"technical":null`, 1),
		"null probabilities":    strings.Replace(mixedResponse, `{"billing":1,"technical":0}`, `null`, 1),
		"missing probabilities": strings.Replace(mixedResponse, `"probabilities":{"billing":1,"technical":0},`, ``, 1),
		"invalid score key":     strings.Replace(mixedResponse, `"probabilities":{"0":1`, `"probabilities":{"00":1`, 1),
		"missing legend":        strings.Replace(mixedResponse, `"legend":{"0":"Low","1":"High"},`, ``, 1),
		"null legend entry":     strings.Replace(mixedResponse, `"0":"Low"`, `"0":null`, 1),
		"numeric legend entry":  strings.Replace(mixedResponse, `"0":"Low"`, `"0":0`, 1),
		"boolean legend entry":  strings.Replace(mixedResponse, `"0":"Low"`, `"0":false`, 1),
		"missing model":         strings.Replace(mixedResponse, `"model":"jev-test",`, ``, 1),
		"null model":            strings.Replace(mixedResponse, `"model":"jev-test"`, `"model":null`, 1),
		"missing usage":         strings.Replace(mixedResponse, `"usage":{"input_tokens":10,"output_tokens":0},`, ``, 1),
		"null usage":            strings.Replace(mixedResponse, `"usage":{"input_tokens":10,"output_tokens":0}`, `"usage":null`, 1),
		"missing usage count":   strings.Replace(mixedResponse, `,"output_tokens":0`, ``, 1),
		"wrong choice":          strings.Replace(mixedResponse, `"choice":"billing"`, `"choice":"missing"`, 1),
		"missing answer":        strings.Replace(mixedResponse, `,"refund":{"type":"noul","noul":0}`, ``, 1),
		"trailing value":        mixedResponse + ` {}`,
		"malformed":             `{"model":`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
				if _, err := fmt.Fprint(w, body); err != nil {
					t.Error(err)
				}
			})
			_, err := backend.Evaluate(t.Context(), mixedRequest())
			if !errors.Is(err, systemone.ErrInvalidResponse) {
				t.Fatalf("error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestStructuredContent(t *testing.T) {
	t.Parallel()
	request := mixedRequest()
	request.Questions[0].Instructions = ""
	request.Questions[0].InstructionsContent = systemone.MustContent(map[string]any{
		"question": "Which team?", "focus": []string{"ticket", "customer"},
	})
	request.Questions[0].Options[0].Description = ""
	request.Questions[0].Options[0].Content = systemone.MustContent(map[string]any{"covers": "Invoices", "excludes": []string{"Bugs"}})
	request.Questions[0].Options[1].Description = ""
	request.Questions[1].Instructions = ""
	request.Questions[1].InstructionsContent = systemone.MustContent([]string{"Urgency?", "Consider the ticket only"})
	request.Questions[1].Levels = nil
	request.Questions[1].LevelContents = []systemone.Content{
		systemone.MustContent(map[string]string{"description": "Low"}),
		systemone.MustContent([]string{"High", "Immediate action"}),
	}
	request.Questions[2].Instructions = ""
	request.Questions[2].InstructionsContent = systemone.MustContent("Refund?")
	request.Questions[2].Yes, request.Questions[2].No = "", ""
	request.Questions[2].YesContent = systemone.MustContent(map[string]string{"means": "Requested"})
	request.Questions[2].NoContent = systemone.MustContent([]string{"Not requested"})
	backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.UnmarshalRead(r.Body, &got); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var want map[string]any
		if err := json.Unmarshal([]byte(`{"model":"jev-latest","state":{"ticket":"refund"},"questions":{"team":{"type":"choice","instructions":{"question":"Which team?","focus":["ticket","customer"]},"criteria":{"billing":{"covers":"Invoices","excludes":["Bugs"]},"technical":null}},"urgency":{"type":"score","instructions":["Urgency?","Consider the ticket only"],"criteria":[{"description":"Low"},["High","Immediate action"]]},"refund":{"type":"noul","instructions":"Refund?","criteria":{"true":{"means":"Requested"},"false":["Not requested"]}}}}`), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("payload = %#v, want %#v", got, want)
		}
		body := strings.Replace(mixedResponse, `"legend":{"0":"Low","1":"High"}`, `"legend":{"0":{"description":"Low"},"1":["High","Immediate action"]}`, 1)
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Error(err)
		}
	})
	if !backend.Capabilities().StructuredContent {
		t.Fatal("structured content capability missing")
	}
	response, err := backend.Evaluate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Answers["urgency"].Score.Score != 0 {
		t.Fatal("unexpected score")
	}
}

func TestHTTPError(t *testing.T) {
	t.Parallel()
	for _, status := range []int{401, 422, 429, 529} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				if _, err := fmt.Fprint(w, "private ticket and secret"); err != nil {
					t.Error(err)
				}
			})
			_, err := backend.Evaluate(t.Context(), mixedRequest())
			httpErr, ok := errors.AsType[*HTTPError](err)
			if !ok || httpErr.StatusCode != status || strings.Contains(err.Error(), "secret") || calls != 1 {
				t.Fatalf("error %v, calls %d", err, calls)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled request reached server") })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := backend.Evaluate(ctx, mixedRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestResponseLimit(t *testing.T) {
	t.Parallel()
	backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		// A server can observe a closed stream when the reader reaches its limit.
		_, _ = fmt.Fprint(w, strings.Repeat(" ", maxResponseBytes+1))
	})
	_, err := backend.Evaluate(t.Context(), mixedRequest())
	if !errors.Is(err, systemone.ErrInvalidResponse) {
		t.Fatalf("error = %v", err)
	}
}

func TestRedirectRejected(t *testing.T) {
	t.Parallel()
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Error("followed redirect with credentials")
		}
		w.Header().Set("Location", "/unexpected")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { t.Error("caller redirect policy used"); return nil }
	backend, err := New(Config{BaseURL: server.URL, HTTPClient: client, APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Evaluate(t.Context(), mixedRequest())
	httpErr, ok := errors.AsType[*HTTPError](err)
	if !ok || httpErr.StatusCode != 307 {
		t.Fatalf("error = %v", err)
	}
	if client.CheckRedirect == nil || backend.client == client {
		t.Fatal("original client changed")
	}
}

func TestConfig(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{{}, {BaseURL: "ftp://host"}, {BaseURL: "https://user:pass@host"}, {BaseURL: "https://host?secret=value"}, {BaseURL: "https://host#fragment"}, {BaseURL: "/relative"}, {APIKey: "bad\nkey"}} {
		if _, err := New(cfg); err == nil {
			t.Errorf("accepted invalid config: %#v", cfg)
		}
	}
	backend, err := New(Config{APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.source != systemone.Native || backend.Capabilities().MaxOptions != 255 || backend.Capabilities().MaxLevels != 10 {
		t.Fatalf("unexpected default backend: %#v", backend)
	}
	custom, err := New(Config{BaseURL: "http://localhost:8000", Model: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	if custom.source != systemone.Unspecified || custom.Capabilities().MaxOptions != 0 || custom.model != "custom" {
		t.Fatalf("unexpected custom backend: %#v", custom)
	}
}

func TestCustomConfigAndOptionalCriteria(t *testing.T) {
	t.Parallel()
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/v1/systemone" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected custom request: %s %v", r.URL.Path, r.Header)
		}
		var payload struct {
			Model     string                    `json:"model"`
			Questions map[string]map[string]any `json:"questions"`
		}
		if err := json.UnmarshalRead(r.Body, &payload); err != nil {
			t.Error(err)
		}
		if _, ok := payload.Questions["refund"]["criteria"]; ok {
			t.Error("empty criteria should be omitted")
		}
		if payload.Model != "local-model" {
			t.Errorf("model = %q", payload.Model)
		}
		if _, err := fmt.Fprint(w, `{"model":"local-model","usage":{"input_tokens":0,"output_tokens":0},"answers":{"refund":{"type":"noul","noul":0.5}}}`); err != nil {
			t.Error(err)
		}
	}))
	source := systemone.Estimated
	caps := systemone.Capabilities{Noul: true, MaxQuestions: 1}
	backend, err := New(Config{BaseURL: server.URL + "/proxy/", Model: "local-model", HTTPClient: server.Client(), ProbabilitySource: &source, Capabilities: &caps})
	if err != nil {
		t.Fatal(err)
	}
	// Construction takes values so later caller mutation cannot change the backend.
	source = systemone.Native
	caps.Noul = false
	request := mixedRequest()
	request.Questions = request.Questions[2:]
	request.Questions[0].Yes = ""
	request.Questions[0].No = ""
	response, err := backend.Evaluate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Answers["refund"].Source != systemone.Estimated {
		t.Fatal("source override lost")
	}
}

func TestInvalidRequestDoesNotSend(t *testing.T) {
	t.Parallel()
	backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request reached server") })
	request := mixedRequest()
	request.State = []byte(`null`)
	if _, err := backend.Evaluate(t.Context(), request); !errors.Is(err, systemone.ErrInvalidState) {
		t.Fatalf("error = %v", err)
	}
	request = mixedRequest()
	request.Questions = append(request.Questions, request.Questions[0])
	if _, err := backend.Evaluate(t.Context(), request); !errors.Is(err, systemone.ErrInvalidQuestion) {
		t.Fatalf("error = %v", err)
	}
	backend.capabilities.MaxQuestions = 1
	if _, err := backend.Evaluate(t.Context(), mixedRequest()); !errors.Is(err, systemone.ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
}

func FuzzResponse(f *testing.F) {
	f.Add(mixedResponse)
	f.Add(`{"answers":{"refund":{"type":"noul","noul":null}}}`)
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > maxResponseBytes {
			t.Skip()
		}
		backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
			if _, err := fmt.Fprint(w, body); err != nil {
				t.Error(err)
			}
		})
		response, err := backend.Evaluate(t.Context(), mixedRequest())
		if err == nil {
			if err := systemone.ValidateResponse(mixedRequest(), response); err != nil {
				t.Fatalf("accepted invalid response: %v", err)
			}
		}
	})
}
