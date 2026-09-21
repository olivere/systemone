package jev

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httputil"
	"strings"
)

const maxDebugBodyBytes = 64 << 10

func debugHeaders(headers http.Header) http.Header {
	clean := headers.Clone()
	for name := range clean {
		switch strings.ToLower(name) {
		case "accept", "content-type", "content-length", "content-encoding", "transfer-encoding", "user-agent", "date", "retry-after", "request-id", "x-request-id":
		default:
			clean[name] = []string{"[REDACTED]"}
		}
	}
	return clean
}

func (b *Backend) debugRequest(req *http.Request, body []byte) {
	if b.debug == nil {
		return
	}
	clean := req.Clone(req.Context())
	clean.Header = debugHeaders(req.Header)
	clean.Trailer = debugHeaders(req.Trailer)
	clean.URL.User = nil
	clean.URL.RawQuery = ""
	clean.URL.Fragment = ""
	clean.RequestURI = ""
	clean.Body = nil
	headers, err := httputil.DumpRequest(clean, false)
	if err != nil {
		headers = []byte("[headers unavailable]\n")
	}
	b.writeDebug("request "+clean.URL.String(), headers, body, false)
}

func (b *Backend) debugResponse(res *http.Response, body []byte, incomplete bool) {
	if b.debug == nil {
		return
	}
	clean := *res
	clean.Header = debugHeaders(res.Header)
	clean.Trailer = debugHeaders(res.Trailer)
	clean.Body = nil
	clean.Request = nil
	headers, err := httputil.DumpResponse(&clean, false)
	if err != nil {
		headers = []byte("[headers unavailable]\n")
	}
	b.writeDebug("response", headers, body, incomplete)
}

func (b *Backend) writeDebug(label string, headers, body []byte, incomplete bool) {
	var dump bytes.Buffer
	fmt.Fprintf(&dump, "--- jev %s ---\n", label)
	dump.Write(headers)
	dump.Write(body[:min(len(body), maxDebugBodyBytes)])
	dump.WriteByte('\n')
	if len(body) > maxDebugBodyBytes {
		dump.WriteString("[body truncated after 65536 bytes]\n")
	}
	if incomplete {
		dump.WriteString("[body read incomplete]\n")
	}
	b.debugMu.Lock()
	defer b.debugMu.Unlock()
	// Diagnostics are best effort and must not replace an evaluation or HTTP error.
	_, _ = b.debug.Write(dump.Bytes())
}
