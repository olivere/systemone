package httpdebug

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestHeaderRedaction(t *testing.T) {
	t.Parallel()
	h := http.Header{"Authorization": {"secret"}, "Cookie": {"secret"}, "Proxy-Authorization": {"secret"}, "X-Key": {"secret"}, "Openai-Organization": {"secret"}, "Content-Type": {"application/json"}, "X-Request-Id": {"request-1"}, "X-Ratelimit-Remaining-Requests": {"99"}}
	clean := headers(h)
	for name, values := range clean {
		allowed := name == "Content-Type" || name == "X-Request-Id" || name == "X-Ratelimit-Remaining-Requests"
		if allowed && values[0] == "[REDACTED]" {
			t.Errorf("allowlisted header %s redacted", name)
		}
		if !allowed && (len(values) != 1 || values[0] != "[REDACTED]") {
			t.Errorf("unredacted header %s", name)
		}
	}
	if h.Get("Authorization") != "secret" {
		t.Fatal("input mutated")
	}
}

func TestNilDumper(t *testing.T) {
	t.Parallel()
	d := New("test", nil)
	if d != nil {
		t.Fatal("nil writer must yield a nil dumper")
	}
	req, err := http.NewRequest(http.MethodPost, "https://host/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Request(req, []byte("body"))
	d.Response(&http.Response{StatusCode: 200, Header: http.Header{}}, nil, false)
}

func TestLabel(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	d := New("example", &out)
	req, err := http.NewRequest(http.MethodPost, "https://user:pass@host/path?token=secret#frag", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Request(req, []byte(`{"a":1}`))
	text := out.String()
	if !strings.HasPrefix(text, "--- example request https://host/path ---\n") || strings.Contains(text, "secret") || strings.Contains(text, "pass") {
		t.Fatalf("unexpected dump: %q", text)
	}
}
