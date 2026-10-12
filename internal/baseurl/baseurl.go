// Package baseurl validates backend base URLs before credentials are sent to them.
package baseurl

import (
	"errors"
	"net/netip"
	"net/url"
)

var (
	ErrInvalid = errors.New("invalid base URL")
	// ErrInsecure rejects a plain http URL that would carry an API key off the host.
	ErrInsecure = errors.New("base URL must use https or a loopback IP address with an API key")
)

// Parse accepts an absolute http or https origin or path prefix without
// userinfo, query, or fragment. With hasKey, plain http is accepted only for a
// loopback IP literal. Host names, including localhost, are rejected: Go
// resolves localhost through DNS when /etc/hosts lacks it, which would send the
// key off the host in cleartext.
func Parse(raw string, hasKey bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrInvalid
	}
	if hasKey && u.Scheme == "http" {
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || !ip.IsLoopback() {
			return nil, ErrInsecure
		}
	}
	return u, nil
}
