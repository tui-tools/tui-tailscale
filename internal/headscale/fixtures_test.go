package headscale

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFixture reads one file from testdata.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // testdata is in the repository
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// allowedRanges are the address blocks a fixture or the demo may carry: the
// documentation ranges (RFC 5737 for IPv4, RFC 3849 for IPv6), and the two
// ranges every tailnet numbers its nodes from — Tailscale's CGNAT block and
// its ULA — which are the same on every tailnet and so name none of them.
var allowedRanges = []netip.Prefix{
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// TestFixturesCarryNoRealAddress decodes each node fixture the way the parser
// does and fails on any address outside the allowed ranges. It is the promise
// that a fixture pasted from a real machine was scrubbed first.
func TestFixturesCarryNoRealAddress(t *testing.T) {
	files, err := filepath.Glob("testdata/headscale-nodes*")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path) //nolint:gosec // testdata is in the repository
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			nodes, err := ParseNodes(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			for _, n := range nodes {
				for _, a := range prefixAddrs(append(append(append([]string{},
					n.IPAddresses...), n.AvailableRoutes...), n.ApprovedRoutes...)) {
					assertAllowedAddress(t, a)
				}
			}
		})
	}
}

// TestDemoDataCarriesNoRealAddress holds the same promise for the hand-written
// demo state behind --demo, and its configuration.
func TestDemoDataCarriesNoRealAddress(t *testing.T) {
	state := demoState()
	for _, n := range state.Nodes {
		for _, a := range prefixAddrs(append(append(append([]string{},
			n.IPAddresses...), n.AvailableRoutes...), n.ApprovedRoutes...)) {
			assertAllowedAddress(t, a)
		}
	}
	cp, err := ParseHeadscaleConfig([]byte(demoHeadscaleConfig))
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{cp.ServerURL, cp.OIDC.Issuer} {
		if !strings.HasSuffix(URLHost(url), "example.com") {
			t.Errorf("demo URL %q is not an example.com name", url)
		}
	}
}

// TestFixturesCarryNoHostName checks the other half of the promise on whatever
// machine the suite runs on.
func TestFixturesCarryNoHostName(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || len(host) < 4 {
		t.Skip("this machine has no host name to look for")
	}
	files, _ := filepath.Glob("testdata/*")
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // testdata is in the repository
		if err != nil {
			continue
		}
		if strings.Contains(string(data), host) {
			t.Errorf("%s carries this machine's host name", filepath.Base(path))
		}
	}
}

func assertAllowedAddress(t *testing.T, addr netip.Addr) {
	t.Helper()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsUnspecified() {
		return
	}
	for _, prefix := range allowedRanges {
		if prefix.Contains(addr) {
			return
		}
	}
	t.Errorf("%s is neither a documentation nor a tailnet address: replace it with one "+
		"from 192.0.2.0/24, 198.51.100.0/24, 2001:db8::/32 or 100.64.0.0/10", addr)
}

// prefixAddrs extracts the address from each CIDR or bare address.
func prefixAddrs(entries []string) []netip.Addr {
	var addrs []netip.Addr
	for _, e := range entries {
		if p, err := netip.ParsePrefix(e); err == nil {
			addrs = append(addrs, p.Addr())
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil {
			addrs = append(addrs, a)
		}
	}
	return addrs
}
