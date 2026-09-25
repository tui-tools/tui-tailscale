package headscale

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// derpFixture is headscale's example configuration, whose derp: section has
// the relay off, headscale's region defaults, and documentation addresses in
// ipv4 and ipv6.
func derpFixture(t *testing.T) ControlPlane {
	t.Helper()
	data, err := os.ReadFile("testdata/headscale-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cp, err := ParseHeadscaleConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	return cp
}

// applyDERP applies a relay answer to a configuration and reads it back.
func applyDERP(t *testing.T, cp ControlPlane, s DERPSettings, serverURL string) (ControlPlane, []ConfigChange) {
	t.Helper()
	edits, err := s.Edits(cp, serverURL)
	if err != nil {
		t.Fatal(err)
	}
	updated, changes, err := EditConfig(cp.Raw, edits)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseHeadscaleConfig([]byte(updated))
	if err != nil {
		t.Fatalf("the edited file does not parse: %v\n%s", err, updated)
	}
	return after, changes
}

// derpDiff flattens a diff into its removed and added lines.
func derpDiff(changes []ConfigChange) (removed, added []string) {
	for _, c := range changes {
		for _, l := range c.Old {
			removed = append(removed, strings.TrimSpace(l))
		}
		for _, l := range c.New {
			added = append(added, strings.TrimSpace(l))
		}
	}
	return removed, added
}

// TestEmbeddedDERPIsAMinimalSplice: on headscale's example file, enabling the
// relay with a name in server_url changes three lines: enabled, and the two
// documentation addresses emptied. The region, the STUN address and the
// public map are already what they should be (issue #28).
func TestEmbeddedDERPIsAMinimalSplice(t *testing.T) {
	cp := derpFixture(t)
	if cp.DERPEmbedded || cp.DERPSTUNListenAddr != "0.0.0.0:3478" || cp.DERP.RegionID != 999 ||
		cp.DERP.IPv4 != "198.51.100.1" {
		t.Fatalf("fixture = %+v / %+v", cp.DERPEmbedded, cp.DERP)
	}
	after, changes := applyDERP(t, cp, DERPSettings{Mode: DERPEmbeddedPublic,
		STUNListenAddr: DefaultDERPSTUNListenAddr}, "https://vpn.example.com")
	removed, added := derpDiff(changes)
	wantRemoved := []string{"enabled: false", "ipv4: 198.51.100.1", "ipv6: 2001:db8::1"}
	wantAdded := []string{"enabled: true", `ipv4: ""`, `ipv6: ""`}
	if !slices.Equal(removed, wantRemoved) || !slices.Equal(added, wantAdded) {
		t.Errorf("diff -%q +%q", removed, added)
	}
	if Relays(after) != RelaysEmbeddedAndPublic || STUNPort(after) != 3478 {
		t.Errorf("after: relays %q, stun port %d", Relays(after), STUNPort(after))
	}
}

// Embedded only drops the public map; an IP literal in server_url is written
// in as the relay's address.
func TestEmbeddedOnlyDropsThePublicMap(t *testing.T) {
	cp := derpFixture(t)
	after, changes := applyDERP(t, cp, DERPSettings{Mode: DERPEmbeddedOnly,
		STUNListenAddr: "0.0.0.0:3479"}, "http://172.16.5.10:8080")
	_, added := derpDiff(changes)
	for _, want := range []string{"urls: []", `ipv4: "172.16.5.10"`, `ipv6: ""`,
		`stun_listen_addr: "0.0.0.0:3479"`} {
		if !slices.Contains(added, want) {
			t.Errorf("the diff is missing %q: %q", want, added)
		}
	}
	if Relays(after) != RelaysEmbedded || after.DERP.IPv4 != "172.16.5.10" || STUNPort(after) != 3479 {
		t.Errorf("after: relays %q, ipv4 %q", Relays(after), after.DERP.IPv4)
	}
	// Turning it off again brings the public map back: nodes need a relay.
	back, _ := applyDERP(t, after, DERPSettings{Mode: DERPPublicOnly}, "http://172.16.5.10:8080")
	if Relays(back) != RelaysPublic {
		t.Errorf("turned off: relays %q", Relays(back))
	}
}

// A file with no derp: section gets one block with every key the relay needs.
func TestEmbeddedDERPOnAFileWithout(t *testing.T) {
	cp, err := ParseHeadscaleConfig([]byte(demoHeadscaleConfig))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := applyDERP(t, cp, DERPSettings{Mode: DERPEmbeddedOnly,
		STUNListenAddr: DefaultDERPSTUNListenAddr}, "https://headscale.example.com")
	if !after.DERPEmbedded || after.DERP.RegionID != DefaultDERPRegionID ||
		after.DERP.RegionCode != DefaultDERPRegionCode || after.DERP.PrivateKeyPath == "" ||
		after.DERP.AutoAddRegion == nil || !*after.DERP.AutoAddRegion || Relays(after) != RelaysEmbedded {
		t.Errorf("after = %+v, relays %q", after.DERP, Relays(after))
	}
	if _, _, err := EditConfig(after.Raw, nil); err != nil {
		t.Error(err)
	}
}

func TestDERPSettingsRefuseABadSTUNAddress(t *testing.T) {
	for _, bad := range []string{"", "3478", "0.0.0.0:0", "-x:1", "0.0.0.0:70000"} {
		if err := (DERPSettings{Mode: DERPEmbeddedOnly, STUNListenAddr: bad}).Validate(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := (DERPSettings{Mode: "sometimes"}).Validate(); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

// TestReadinessReadsTheSTUNPort: with the relay on, readiness reads its STUN
// port, the ready line says when it is closed, and f hands it over with the
// others (issues #28 and #32).
func TestReadinessReadsTheSTUNPort(t *testing.T) {
	f := NewFake()
	f.SetService("active", "enabled")
	cfg := demoHeadscaleConfig + "\nderp:\n  server:\n    enabled: true\n    stun_listen_addr: \"0.0.0.0:3478\"\n"
	f.SetConfig(cfg)
	fw, _ := ParseTuiFirewallCheck(`{"enabled": true, "model": {"Groups": [{"Name": "rules",
		"Default": {"Incoming": "deny"}, "Rules": [
		{"Action": "ALLOW", "Direction": "IN", "Proto": "tcp", "Ports": "443", "From": "Anywhere"},
		{"Action": "ALLOW", "Direction": "IN", "Proto": "udp", "Ports": "41641", "From": "Anywhere"}]}]}}`)
	fw.Launchable = true
	f.SetFirewall(fw)
	state, err := f.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r := ReadinessFor(state, time.Now())
	if r.Ports == nil || r.Ports.STUNPort != 3478 || r.Ports.STUN != PortClosed || r.Relays != RelaysEmbeddedAndPublic {
		t.Fatalf("ports = %+v, relays %q", r.Ports, r.Relays)
	}
	if r.Next == NextReady && !strings.Contains(r.NextStep, "3478/udp") {
		t.Errorf("next step = %q", r.NextStep)
	}
	h := ClosedPorts(r)
	if args, _ := h.Args(); strings.Join(args, " ") != "--open 3478/udp --comment tailnet DERP STUN" {
		t.Errorf("hand-off = %q", args)
	}
	fw, _ = ParseTuiFirewallCheck(`{"enabled": true, "model": {"Groups": [{"Name": "rules",
		"Default": {"Incoming": "deny"}, "Rules": []}]}}`)
	state.Firewall = fw
	h = ClosedPorts(ReadinessFor(state, time.Now()))
	if args, _ := h.Args(); strings.Join(args, " ") !=
		"--open 443/tcp,41641/udp,3478/udp --comment tailnet control plane, node and DERP STUN" {
		t.Errorf("hand-off = %q", args)
	}
}

func TestDERPAddresses(t *testing.T) {
	cases := []struct{ url, v4, v6, want4, want6 string }{
		{"https://vpn.example.com", "198.51.100.1", "2001:db8::1", "", ""},
		{"https://vpn.example.com", "100.100.1.1", "", "100.100.1.1", ""},
		{"http://172.16.5.10:8080", "198.51.100.1", "2001:db8::1", "172.16.5.10", ""},
		{"https://[fd00::10]:443", "", "", "", "fd00::10"},
		{"https://vpn.example.com", "not-an-ip", "10.0.0.1", "", ""},
	}
	for _, c := range cases {
		v4, v6 := DERPAddresses(c.url, c.v4, c.v6)
		if v4 != c.want4 || v6 != c.want6 {
			t.Errorf("%s (%s, %s) = %q, %q", c.url, c.v4, c.v6, v4, v6)
		}
	}
}
