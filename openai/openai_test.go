package openai

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/olivere/systemone"
)

func mixedRequest() systemone.Request {
	return systemone.Request{State: []byte(`{"ticket":"refund"}`), Questions: []systemone.QuestionSpec{
		{Key: "team", Kind: systemone.ChoiceKind, Instructions: "Which team?", Options: []systemone.Option[string]{{Value: "billing", Description: "Invoices"}, {Value: "technical"}}},
		{Key: "urgency", Kind: systemone.ScoreKind, Instructions: "Urgency?", Levels: []string{"Low", "High"}},
		{Key: "refund", Kind: systemone.NoulKind, Instructions: "Refund?", Yes: "Requested", No: "Not requested"},
	}}
}

const mixedResponse = `{"model":"gpt-6-luna","answers":[` +
	`{"type":"choice","name":"team","choice":"billing","probabilities":[{"value":"billing","probability":0.9},{"value":"technical","probability":0.1}],"confidence":0.8},` +
	`{"type":"score","name":"urgency","score":0.3,"probabilities":[{"value":0,"label":"Low","probability":0.7},{"value":1,"label":"High","probability":0.3}],"confidence":0.4},` +
	`{"type":"predicate","name":"refund","probability":0.95}],` +
	`"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":10}}`

func testBackend(t *testing.T, handler http.HandlerFunc) *Backend {
	t.Helper()
	server := httptest.NewTestServer(t, handler)
	// The in-memory network accepts any address; plain HTTP needs a loopback IP.
	backend, err := New(Config{BaseURL: "http://127.0.0.1", APIKey: "test-key", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func respond(t *testing.T, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Error(err)
		}
	}
}

// checkPayload compares the decoded request body with want, independent of key order.
func checkPayload(t *testing.T, r *http.Request, want string) {
	t.Helper()
	var got, wantValue map[string]any
	if err := json.UnmarshalRead(r.Body, &got); err != nil {
		t.Error(err)
		return
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantValue) {
		t.Errorf("payload = %#v, want %#v", got, wantValue)
	}
}

func TestEvaluateMixed(t *testing.T) {
	t.Parallel()
	backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/decisions" || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s, headers %v", r.Method, r.URL.Path, r.Header)
		}
		checkPayload(t, r, `{"model":"gpt-6-luna","input":"{\"ticket\":\"refund\"}","questions":[`+
			`{"type":"choice","name":"team","instructions":"Which team?","choices":[{"value":"billing","description":"Invoices"},{"value":"technical"}]},`+
			`{"type":"score","name":"urgency","instructions":"Urgency?","levels":[{"label":"Low"},{"label":"High"}]},`+
			`{"type":"predicate","name":"refund","instructions":"Refund?\n\nTrue if: Requested\n\nFalse if: Not requested"}]}`)
		respond(t, mixedResponse)(w, r)
	})
	response, err := backend.Evaluate(t.Context(), mixedRequest())
	if err != nil {
		t.Fatal(err)
	}
	team, urgency, refund := response.Answers["team"], response.Answers["urgency"], response.Answers["refund"]
	if response.Model != "gpt-6-luna" || response.Usage.InputTokens != 10 || response.Usage.OutputTokens != 0 {
		t.Fatalf("unexpected metadata: %#v", response)
	}
	if team.Choice.Choice != "billing" || team.Choice.Probabilities["technical"] != 0.1 || *team.Choice.Confidence != 0.8 || team.Source != systemone.Native {
		t.Fatalf("unexpected choice: %#v", team)
	}
	if urgency.Score.Score != 0.3 || !reflect.DeepEqual(urgency.Score.Probabilities, []float64{0.7, 0.3}) || *urgency.Score.Confidence != 0.4 {
		t.Fatalf("unexpected score: %#v", urgency.Score)
	}
	if refund.Noul.Probability != 0.95 || refund.Source != systemone.Native {
		t.Fatalf("unexpected predicate: %#v", refund)
	}
}

func TestStringStateAndOutcomes(t *testing.T) {
	t.Parallel()
	request := mixedRequest()
	request.State = []byte(`"Please refund me."`)
	request.Questions = request.Questions[2:]
	request.Questions[0].Yes = ""
	backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		checkPayload(t, r, `{"model":"gpt-6-luna","input":"Please refund me.","questions":[{"type":"predicate","name":"refund","instructions":"Refund?\n\nFalse if: Not requested"}]}`)
		respond(t, `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"refund","probability":1}],"usage":{"input_tokens":1,"output_tokens":0}}`)(w, r)
	})
	if _, err := backend.Evaluate(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestStructuredContent(t *testing.T) {
	t.Parallel()
	request := mixedRequest()
	request.State = []byte(`["first", {"second": 2}]`)
	request.Questions[0].Instructions = ""
	request.Questions[0].InstructionsContent = systemone.MustContent(map[string]any{"question": "Which team?"})
	request.Questions[0].Options[0].Description = ""
	request.Questions[0].Options[0].Content = systemone.MustContent([]string{"Invoices", "Refunds"})
	request.Questions[1].Instructions = ""
	request.Questions[1].InstructionsContent = systemone.MustContent("Urgency?")
	request.Questions[1].Levels = nil
	request.Questions[1].LevelContents = []systemone.Content{systemone.MustContent("Low"), systemone.MustContent(map[string]string{"description": "High"})}
	request.Questions[2].Yes, request.Questions[2].No = "", ""
	request.Questions[2].YesContent = systemone.MustContent(map[string]string{"means": "Requested"})
	request.Questions[2].NoContent = systemone.MustContent("Not requested")
	backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		checkPayload(t, r, `{"model":"gpt-6-luna","input":"[\"first\", {\"second\": 2}]","questions":[`+
			`{"type":"choice","name":"team","instructions":"{\"question\":\"Which team?\"}","choices":[{"value":"billing","description":"[\"Invoices\",\"Refunds\"]"},{"value":"technical"}]},`+
			`{"type":"score","name":"urgency","instructions":"Urgency?","levels":[{"label":"Low"},{"label":"{\"description\":\"High\"}"}]},`+
			`{"type":"predicate","name":"refund","instructions":"Refund?\n\nTrue if: {\"means\":\"Requested\"}\n\nFalse if: Not requested"}]}`)
		respond(t, strings.Replace(mixedResponse, `"label":"High"`, `"label":"{\"description\":\"High\"}"`, 1))(w, r)
	})
	if !backend.Capabilities().StructuredContent {
		t.Fatal("structured content capability missing")
	}
	if _, err := backend.Evaluate(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestRejectInvalidResponse(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"duplicate key":           strings.Replace(mixedResponse, `"probability":0.95`, `"probability":0.95,"probability":0.1`, 1),
		"missing type":            strings.Replace(mixedResponse, `"type":"predicate",`, ``, 1),
		"unknown type":            strings.Replace(mixedResponse, `"type":"predicate"`, `"type":"noul"`, 1),
		"wrong type":              strings.Replace(mixedResponse, `"type":"predicate","name":"refund","probability":0.95`, `"type":"predicate","name":"team","probability":0.95`, 1),
		"missing name":            strings.Replace(mixedResponse, `"name":"refund",`, ``, 1),
		"null name":               strings.Replace(mixedResponse, `"name":"refund"`, `"name":null`, 1),
		"unknown name":            strings.Replace(mixedResponse, `"name":"refund"`, `"name":"other"`, 1),
		"missing probability":     strings.Replace(mixedResponse, `,"probability":0.95`, ``, 1),
		"null probability":        strings.Replace(mixedResponse, `"probability":0.95`, `"probability":null`, 1),
		"probability above one":   strings.Replace(mixedResponse, `"probability":0.95`, `"probability":1.5`, 1),
		"missing choice":          strings.Replace(mixedResponse, `"choice":"billing",`, ``, 1),
		"boolean choice":          strings.Replace(mixedResponse, `"choice":"billing"`, `"choice":true`, 1),
		"wrong choice":            strings.Replace(mixedResponse, `"choice":"billing"`, `"choice":"missing"`, 1),
		"non-maximal choice":      strings.Replace(mixedResponse, `"choice":"billing"`, `"choice":"technical"`, 1),
		"boolean choice value":    strings.Replace(mixedResponse, `"value":"technical"`, `"value":false`, 1),
		"duplicate choice value":  strings.Replace(mixedResponse, `"value":"technical"`, `"value":"billing"`, 1),
		"missing choice entry":    strings.Replace(mixedResponse, `,{"value":"technical","probability":0.1}`, ``, 1),
		"null choice probability": strings.Replace(mixedResponse, `"value":"technical","probability":0.1`, `"value":"technical","probability":null`, 1),
		"missing confidence":      strings.Replace(mixedResponse, `,"confidence":0.8`, ``, 1),
		"null confidence":         strings.Replace(mixedResponse, `"confidence":0.4`, `"confidence":null`, 1),
		"missing score":           strings.Replace(mixedResponse, `"score":0.3,`, ``, 1),
		"inconsistent score":      strings.Replace(mixedResponse, `"score":0.3`, `"score":0.9`, 1),
		"fractional level":        strings.Replace(mixedResponse, `"value":1,`, `"value":1.5,`, 1),
		"level out of range":      strings.Replace(mixedResponse, `"value":1,`, `"value":2,`, 1),
		"duplicate level":         strings.Replace(mixedResponse, `"value":1,`, `"value":0,`, 1),
		"wrong level label":       strings.Replace(mixedResponse, `"label":"High"`, `"label":"Medium"`, 1),
		"missing level label":     strings.Replace(mixedResponse, `"label":"High",`, ``, 1),
		"missing level":           strings.Replace(mixedResponse, `,{"value":1,"label":"High","probability":0.3}`, ``, 1),
		"missing model":           strings.Replace(mixedResponse, `"model":"gpt-6-luna",`, ``, 1),
		"null model":              strings.Replace(mixedResponse, `"model":"gpt-6-luna"`, `"model":null`, 1),
		"missing usage":           mixedResponse[:strings.Index(mixedResponse, `,"usage"`)] + `}`,
		"missing usage count":     strings.Replace(mixedResponse, `"output_tokens":0,`, ``, 1),
		"negative usage":          strings.Replace(mixedResponse, `"input_tokens":10`, `"input_tokens":-1`, 1),
		"missing answer":          strings.Replace(mixedResponse, `,{"type":"predicate","name":"refund","probability":0.95}`, ``, 1),
		"duplicate answer":        strings.Replace(mixedResponse, `{"type":"predicate","name":"refund","probability":0.95}`, `{"type":"predicate","name":"refund","probability":0.95},{"type":"predicate","name":"refund","probability":0.95}`, 1),
		"trailing value":          mixedResponse + ` {}`,
		"malformed":               `{"model":`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if body == mixedResponse {
				t.Fatal("replacement did not apply")
			}
			backend := testBackend(t, respond(t, body))
			_, err := backend.Evaluate(t.Context(), mixedRequest())
			if !errors.Is(err, systemone.ErrInvalidResponse) {
				t.Fatalf("error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestRefusal(t *testing.T) {
	t.Parallel()
	body := strings.Replace(mixedResponse, `{"type":"predicate","name":"refund","probability":0.95}`, `{"type":"refusal","name":"refund"}`, 1)
	body = strings.Replace(body, `{"type":"choice","name":"team","choice":"billing","probabilities":[{"value":"billing","probability":0.9},{"value":"technical","probability":0.1}],"confidence":0.8}`, `{"type":"refusal","name":"team"}`, 1)
	backend := testBackend(t, respond(t, body))
	_, err := backend.Evaluate(t.Context(), mixedRequest())
	if !errors.Is(err, ErrRefused) || errors.Is(err, systemone.ErrInvalidResponse) || !strings.Contains(err.Error(), `["team" "refund"]`) {
		t.Fatalf("error = %v, want ErrRefused naming both questions", err)
	}
}

func TestHTTPError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status          int
		body            string
		wantType, wantC string
	}{
		{401, `{"error":{"message":"Incorrect API key provided: secret","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`, "invalid_request_error", "invalid_api_key"},
		{429, `{"error":{"message":"quota secret","type":"insufficient_quota","code":null}}`, "insufficient_quota", ""},
		{400, `{"error":{"message":"secret","type":"invalid request error","code":"secret ticket text"}}`, "", ""},
		{500, `private ticket and secret`, "", ""},
		{529, ``, "", ""},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			t.Parallel()
			calls := 0
			backend := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("X-Request-Id", "req_123")
				w.WriteHeader(tc.status)
				if _, err := fmt.Fprint(w, tc.body); err != nil {
					t.Error(err)
				}
			})
			_, err := backend.Evaluate(t.Context(), mixedRequest())
			httpErr, ok := errors.AsType[*HTTPError](err)
			if !ok || httpErr.StatusCode != tc.status || httpErr.Type != tc.wantType || httpErr.Code != tc.wantC || httpErr.RequestID != "req_123" || calls != 1 {
				t.Fatalf("error %#v, calls %d", httpErr, calls)
			}
			if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "req_123") {
				t.Fatalf("error text = %q", err)
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
		if r.URL.Path != "/v1/decisions" {
			t.Error("followed redirect with credentials")
		}
		w.Header().Set("Location", "/unexpected")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { t.Error("caller redirect policy used"); return nil }
	backend, err := New(Config{BaseURL: "http://127.0.0.1", HTTPClient: client, APIKey: "test"})
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
	for _, cfg := range []Config{{}, {APIKey: " "}, {APIKey: "bad\nkey"}, {APIKey: "k", Model: " "}, {APIKey: "k", BaseURL: "ftp://host"}, {APIKey: "k", BaseURL: "https://user:pass@host"}, {APIKey: "k", BaseURL: "https://host?secret=value"}, {APIKey: "k", BaseURL: "https://host#fragment"}, {APIKey: "k", BaseURL: "/relative"}, {APIKey: "k", BaseURL: "http://proxy.internal"}, {APIKey: "k", BaseURL: "http://10.0.0.1"}, {APIKey: "k", BaseURL: "http://localhost:8000"}} {
		if _, err := New(cfg); err == nil {
			t.Errorf("accepted invalid config: %#v", cfg)
		}
	}
	backend, err := New(Config{APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.endpoint != "https://api.openai.com/v1/decisions" || backend.model != "gpt-6-luna" || backend.client.Timeout == 0 {
		t.Fatalf("unexpected default backend: %#v", backend)
	}
	caps := backend.Capabilities()
	if caps.MaxOptions != 255 || caps.MaxLevels != 10 || caps.MaxQuestions != 200 || !caps.Choice || !caps.Score || !caps.Noul {
		t.Fatalf("unexpected capabilities: %#v", caps)
	}
	custom, err := New(Config{APIKey: "test", BaseURL: "http://127.0.0.1:8000/proxy/", Model: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	if custom.endpoint != "http://127.0.0.1:8000/proxy/v1/decisions" || custom.model != "custom" {
		t.Fatalf("unexpected custom backend: %#v", custom)
	}
	for _, base := range []string{"http://127.0.0.1:8080", "http://[::1]:8080", "https://proxy.internal"} {
		if _, err := New(Config{APIKey: "test", BaseURL: base}); err != nil {
			t.Errorf("rejected %s: %v", base, err)
		}
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
	request = mixedRequest()
	request.Questions[1].Levels = strings.Fields("0 1 2 3 4 5 6 7 8 9 10")
	if _, err := backend.Evaluate(t.Context(), request); !errors.Is(err, systemone.ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDebug(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	backend, err := New(Config{APIKey: "secret-api-key", Debug: &output, HTTPClient: &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
		header := http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"req_123"}, "Openai-Organization": {"secret-org"}, "Set-Cookie": {"secret-cookie"}}
		return &http.Response{StatusCode: 200, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: header, Body: io.NopCloser(strings.NewReader(mixedResponse)), Request: req}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Evaluate(t.Context(), mixedRequest()); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"--- openai request https://api.openai.com/v1/decisions ---", "--- openai response ---", "Authorization: [REDACTED]", "X-Request-Id: req_123", `"name":"team"`, `"model":"gpt-6-luna"`} {
		if !strings.Contains(text, want) {
			t.Errorf("dump missing %q: %s", want, text)
		}
	}
	for _, secret := range []string{"secret-api-key", "secret-org", "secret-cookie"} {
		if strings.Contains(text, secret) {
			t.Errorf("dump exposed %q", secret)
		}
	}
}

func FuzzResponse(f *testing.F) {
	f.Add(mixedResponse)
	f.Add(`{"model":"m","answers":[{"type":"refusal","name":"team"}],"usage":{"input_tokens":0,"output_tokens":0}}`)
	f.Add(`{"answers":[{"type":"predicate","name":"refund","probability":null}]}`)
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > maxResponseBytes {
			t.Skip()
		}
		backend := testBackend(t, respond(t, body))
		response, err := backend.Evaluate(t.Context(), mixedRequest())
		if err == nil {
			if err := systemone.ValidateResponse(mixedRequest(), response); err != nil {
				t.Fatalf("accepted invalid response: %v", err)
			}
		}
	})
}
