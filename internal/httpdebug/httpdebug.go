// Package httpdebug writes HTTP dumps for backend debugging. Headers outside a
// protocol allowlist are redacted; bodies are written as they are.
package httpdebug

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
)

// MaxBodyBytes is the largest body prefix a dump contains.
const MaxBodyBytes = 64 << 10

// Dumper serializes dumps to one writer. A nil *Dumper writes nothing, so
// callers can test for nil to skip work that only debugging needs.
type Dumper struct {
	label string
	w     io.Writer
	mu    sync.Mutex
}

// New returns nil when w is nil. The label names the backend in each dump.
func New(label string, w io.Writer) *Dumper {
	if w == nil {
		return nil
	}
	return &Dumper{label: label, w: w}
}

func headers(h http.Header) http.Header {
	clean := h.Clone()
	for name := range clean {
		lower := strings.ToLower(name)
		switch lower {
		case "accept", "content-type", "content-length", "content-encoding", "transfer-encoding", "user-agent", "date", "retry-after", "request-id", "x-request-id", "openai-processing-ms", "openai-version":
		default:
			if !strings.HasPrefix(lower, "x-ratelimit-") {
				clean[name] = []string{"[REDACTED]"}
			}
		}
	}
	return clean
}

// Request dumps req with body, which the caller has already read.
func (d *Dumper) Request(req *http.Request, body []byte) {
	if d == nil {
		return
	}
	clean := req.Clone(req.Context())
	clean.Header = headers(req.Header)
	clean.Trailer = headers(req.Trailer)
	clean.URL.User = nil
	clean.URL.RawQuery = ""
	clean.URL.Fragment = ""
	clean.RequestURI = ""
	clean.Body = nil
	head, err := httputil.DumpRequest(clean, false)
	if err != nil {
		head = []byte("[headers unavailable]\n")
	}
	d.write("request "+clean.URL.String(), head, body, false)
}

// Response dumps res with body. Incomplete marks a body whose read failed.
func (d *Dumper) Response(res *http.Response, body []byte, incomplete bool) {
	if d == nil {
		return
	}
	clean := *res
	clean.Header = headers(res.Header)
	clean.Trailer = headers(res.Trailer)
	clean.Body = nil
	clean.Request = nil
	head, err := httputil.DumpResponse(&clean, false)
	if err != nil {
		head = []byte("[headers unavailable]\n")
	}
	d.write("response", head, body, incomplete)
}

func (d *Dumper) write(what string, head, body []byte, incomplete bool) {
	var dump bytes.Buffer
	fmt.Fprintf(&dump, "--- %s %s ---\n", d.label, what)
	dump.Write(head)
	dump.Write(body[:min(len(body), MaxBodyBytes)])
	dump.WriteByte('\n')
	if len(body) > MaxBodyBytes {
		fmt.Fprintf(&dump, "[body truncated after %d bytes]\n", MaxBodyBytes)
	}
	if incomplete {
		dump.WriteString("[body read incomplete]\n")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// Diagnostics are best effort and must not replace an evaluation or HTTP error.
	_, _ = d.w.Write(dump.Bytes())
}
