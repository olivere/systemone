package baseurl

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw    string
		hasKey bool
		want   error
	}{
		{"https://api.openai.com", true, nil},
		{"https://proxy.internal/prefix/", true, nil},
		{"http://proxy.internal:8000", false, nil},
		{"http://localhost:8000", false, nil},
		{"http://127.0.0.1:8000", true, nil},
		{"http://127.0.0.255", true, nil},
		{"http://[::1]:8000", true, nil},
		{"http://[::1%25en0]:8000", true, nil},
		{"http://[::ffff:127.0.0.1]", true, nil},
		{"HTTP://127.0.0.1", true, nil},

		// localhost resolves through DNS when /etc/hosts lacks it.
		{"http://localhost:8000", true, ErrInsecure},
		{"http://LOCALHOST", true, ErrInsecure},
		{"http://localhost.", true, ErrInsecure},
		{"http://foo.localhost", true, ErrInsecure},
		{"http://localhost.example.com", true, ErrInsecure},
		{"http://127.0.0.1.evil.com", true, ErrInsecure},
		{"http://proxy.internal", true, ErrInsecure},
		{"http://10.0.0.1", true, ErrInsecure},
		{"http://[::ffff:10.0.0.1]", true, ErrInsecure},
		{"http://0.0.0.0", true, ErrInsecure},
		// Shorthand IPv4 forms are host names to Go's resolver.
		{"http://127.1", true, ErrInsecure},
		{"http://0x7f.1", true, ErrInsecure},
		{"http://017700000001", true, ErrInsecure},

		{"", false, ErrInvalid},
		{"/relative", false, ErrInvalid},
		{"ftp://host", false, ErrInvalid},
		{"https://user:pass@host", true, ErrInvalid},
		{"http://127.0.0.1%2f@evil.com", true, ErrInvalid},
		{"http://localhost:80@evil.com", true, ErrInvalid},
		{"http://evil.com#@127.0.0.1", true, ErrInvalid},
		{"https://host?secret=value", true, ErrInvalid},
		{"https://host#fragment", true, ErrInvalid},
		{"http://[::1]evil", true, ErrInvalid},
		{"http://127.0.0.1\\@evil.com", true, ErrInvalid},
		{" http://127.0.0.1", true, ErrInvalid},
	} {
		u, err := Parse(tc.raw, tc.hasKey)
		if !errors.Is(err, tc.want) || (err == nil) != (u != nil) {
			t.Errorf("Parse(%q, %v) = %v, %v; want error %v", tc.raw, tc.hasKey, u, err, tc.want)
		}
	}
}
