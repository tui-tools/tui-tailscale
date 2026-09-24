package tailscale

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// This file validates the login server URL the join form takes. The rules are
// tui-vpn's, which validates the same URL from the other side (headscale's
// server_url), so a URL one tool accepts the other accepts too: an http(s)
// URL of safe characters, whose host is an IP literal that parses or a DNS
// name whose last label is not all digits, and whose port, when it names
// one, is a port. The character check alone let http://203.0.113.1000
// through, and a client then fails on a DNS lookup for a name that looks like
// an address.

// serverURLPattern is a plain http(s) URL with no room for anything that could
// become a second argument.
var serverURLPattern = regexp.MustCompile(`^https?://[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+$`)

// dnsLabel is one label of a DNS name.
var dnsLabel = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidServerURL reports whether s is a plausible login server URL.
func ValidServerURL(s string) bool { return ServerURLProblem(s) == "" }

// ServerURLProblem says why s cannot be a login server URL, or "" when it can.
func ServerURLProblem(s string) string {
	if s == "" || strings.ContainsAny(s, "\n\r") || !serverURLPattern.MatchString(s) {
		return fmt.Sprintf("not an http:// or https:// URL: %q", s)
	}
	authority := urlAuthority(s)
	if strings.Contains(authority, "@") {
		return "a login server URL carries no user name or password"
	}
	host := URLHost(s)
	// A port is whatever follows the host; URLHost dropped it, so what is left
	// after the host has to be empty or ":<port>".
	rest := strings.TrimPrefix(authority, "["+host+"]")
	if rest == authority {
		rest = strings.TrimPrefix(authority, host)
	}
	if rest != "" {
		port, err := strconv.Atoi(strings.TrimPrefix(rest, ":"))
		if !strings.HasPrefix(rest, ":") || err != nil || port < 1 || port > 65535 {
			return fmt.Sprintf("%q does not end in a valid port", authority)
		}
	}
	if strings.HasPrefix(authority, "[") {
		host = "[" + host + "]"
	}
	return HostProblem(host)
}

// HostProblem says why host cannot be the host of a URL, or "" when it can. It
// accepts an IPv4 or IPv6 literal (with or without the brackets a URL puts
// around IPv6) and a DNS name.
func HostProblem(host string) string {
	if host == "" {
		return "the host is empty"
	}
	if strings.HasPrefix(host, "[") || strings.Contains(host, ":") {
		literal := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if addr, err := netip.ParseAddr(literal); err == nil && addr.Is6() && addr.Zone() == "" {
			return ""
		}
		return fmt.Sprintf("%s is not a valid IPv6 address", host)
	}
	if len(host) > 253 {
		return "the host name is longer than 253 characters"
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	last := labels[len(labels)-1]
	if allDigits(last) {
		// A dotted-number host is an IPv4 address or nothing.
		if addr, err := netip.ParseAddr(host); err == nil && addr.Is4() {
			return ""
		}
		return fmt.Sprintf("%s is not a valid IPv4 address (four numbers from 0 to 255), "+
			"and no DNS name ends in a number", host)
	}
	for _, label := range labels {
		if !dnsLabel.MatchString(label) {
			return fmt.Sprintf("%s is not a valid host name (label %q)", host, label)
		}
	}
	return ""
}

// allDigits reports whether s is a non-empty run of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// urlAuthority is the host[:port] part of a URL.
func urlAuthority(rawURL string) string {
	rest := rawURL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// URLHost is the host of a URL, without port, brackets or user info.
func URLHost(rawURL string) string {
	rest := urlAuthority(rawURL)
	if i := strings.LastIndexByte(rest, '@'); i >= 0 {
		rest = rest[i+1:]
	}
	// A bracketed IPv6 literal keeps its brackets off, and its colons in.
	if strings.HasPrefix(rest, "[") {
		if end := strings.IndexByte(rest, ']'); end >= 0 {
			return rest[1:end]
		}
		return rest
	}
	if i := strings.LastIndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// IsHTTPS reports whether a URL is https.
func IsHTTPS(rawURL string) bool {
	return strings.HasPrefix(strings.ToLower(rawURL), "https://")
}

// IsTailscaleControl reports whether a login server is Tailscale's own
// coordination server rather than a self-hosted one.
func IsTailscaleControl(rawURL string) bool {
	host := strings.ToLower(URLHost(rawURL))
	return rawURL == "" || host == "controlplane.tailscale.com" || host == "login.tailscale.com"
}
