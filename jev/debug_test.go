package jev

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type debugTransport func(*http.Request) (*http.Response, error)

func (f debugTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type observedBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}
func (b *observedBody) Close() error { b.closed = true; return nil }

func TestDebugHTTP(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"success", 200, mixedResponse}, {"malformed", 200, `{"incomplete":`}, {"HTTP error", 422, `{"detail":"bad criteria"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			responseHeaders := http.Header{"Content-Type": {"application/json"}, "Retry-After": {"3"}, "Set-Cookie": {"session=secret-cookie"}, "X-Api-Key": {"secret-custom"}, "Location": {"https://host/?token=secret-location"}}
			backend, err := New(Config{APIKey: "secret-api-key", Debug: &output, HTTPClient: &http.Client{Transport: debugTransport(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "Bearer secret-api-key" {
					t.Error("authorization mutated")
				}
				body, err := io.ReadAll(req.Body)
				if err != nil || !bytes.Contains(body, []byte(`"ticket":"refund"`)) {
					t.Errorf("request body changed: %s, %v", body, err)
				}
				return &http.Response{StatusCode: tc.status, Status: fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: responseHeaders, Trailer: http.Header{"X-Token": {"secret-trailer"}}, Body: io.NopCloser(strings.NewReader(tc.body)), ContentLength: int64(len(tc.body)), Request: req}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = backend.Evaluate(t.Context(), mixedRequest())
			if tc.name == "success" && err != nil {
				t.Fatal(err)
			}
			if tc.name != "success" && err == nil {
				t.Fatal("expected error")
			}
			text := output.String()
			for _, want := range []string{"https://api.typesafe.ai/v1/systemone", "POST /v1/systemone", fmt.Sprintf("HTTP/1.1 %d", tc.status), `"ticket":"refund"`, tc.body, "Authorization: [REDACTED]", "Retry-After: 3"} {
				if !strings.Contains(text, want) {
					t.Errorf("dump missing %q: %s", want, text)
				}
			}
			for _, secret := range []string{"secret-api-key", "secret-cookie", "secret-custom", "secret-location", "secret-trailer"} {
				if strings.Contains(text, secret) {
					t.Errorf("dump exposed %q", secret)
				}
			}
			if responseHeaders.Get("Set-Cookie") != "session=secret-cookie" {
				t.Fatal("response headers mutated")
			}
		})
	}
}

func TestDebugErrorBodyLimits(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			var output bytes.Buffer
			var debug io.Writer
			if enabled {
				debug = &output
			}
			body := &observedBody{reader: strings.NewReader(strings.Repeat("x", maxDebugBodyBytes+200))}
			backend, err := New(Config{APIKey: "test", Debug: debug, HTTPClient: &http.Client{Transport: debugTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 429, Status: "429 Too Many Requests", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Body: body}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = backend.Evaluate(t.Context(), mixedRequest())
			httpErr, ok := errors.AsType[*HTTPError](err)
			if !ok || httpErr.StatusCode != 429 {
				t.Fatalf("error = %v", err)
			}
			wantRead := 0
			if enabled {
				wantRead = maxDebugBodyBytes + 1
			}
			if body.read != wantRead || !body.closed {
				t.Fatalf("read=%d closed=%v", body.read, body.closed)
			}
			if enabled && (!strings.Contains(output.String(), "[body truncated") || strings.Contains(output.String(), strings.Repeat("x", maxDebugBodyBytes+1))) {
				t.Fatal("body truncation missing")
			}
			if !enabled && output.Len() != 0 {
				t.Fatal("unexpected debug output")
			}
		})
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestDebugFailuresDoNotReplaceHTTPError(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	backend, err := New(Config{APIKey: "test", Debug: &output, HTTPClient: &http.Client{Transport: debugTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 529, Body: io.NopCloser(failedReader{})}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Evaluate(t.Context(), mixedRequest())
	if httpErr, ok := errors.AsType[*HTTPError](err); !ok || httpErr.StatusCode != 529 {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(output.String(), "[body read incomplete]") {
		t.Fatal("missing incomplete-body marker")
	}
	backend.debug = failedWriter{}
	_, err = backend.Evaluate(t.Context(), mixedRequest())
	if _, ok := errors.AsType[*HTTPError](err); !ok {
		t.Fatalf("writer replaced error: %v", err)
	}
}

func TestDebugConcurrencyAndRequestTruncation(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	backend, err := New(Config{APIKey: "test", Debug: &output, HTTPClient: &http.Client{Transport: debugTransport(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil || len(body) <= maxDebugBodyBytes {
			t.Errorf("full request not preserved: size %d, %v", len(body), err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(mixedResponse))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	request := mixedRequest()
	request.State = []byte(`"` + strings.Repeat("a", maxDebugBodyBytes+100) + `"`)
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			if _, err := backend.Evaluate(t.Context(), request); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if strings.Count(output.String(), "--- jev request") != 12 || strings.Count(output.String(), "--- jev response") != 12 || strings.Count(output.String(), "[body truncated") != 12 {
		t.Fatal("missing concurrent dump entries")
	}
	backend.debug = failedWriter{}
	if _, err := backend.Evaluate(t.Context(), request); err != nil {
		t.Fatalf("writer failure affected success: %v", err)
	}
}

func TestDebugHeaderRedaction(t *testing.T) {
	t.Parallel()
	headers := http.Header{"Authorization": {"secret"}, "Cookie": {"secret"}, "Proxy-Authorization": {"secret"}, "X-Key": {"secret"}, "Content-Type": {"application/json"}, "X-Request-Id": {"request-1"}}
	clean := debugHeaders(headers)
	for name, values := range clean {
		if name != "Content-Type" && name != "X-Request-Id" && (len(values) != 1 || values[0] != "[REDACTED]") {
			t.Errorf("unredacted header %s", name)
		}
	}
	if headers.Get("Authorization") != "secret" {
		t.Fatal("input mutated")
	}
}
