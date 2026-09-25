package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// closedFirewall is a host firewall that lets nothing in but ssh.
func closedFirewall() headscale.Firewall {
	fw, _ := headscale.ParseTuiFirewallCheck(`{"enabled": true, "model": {"Groups": [{"Name": "rules",
		"Default": {"Incoming": "deny"}, "Rules": [
		{"Action": "ALLOW", "Direction": "IN", "Proto": "tcp", "Ports": "22", "From": "Anywhere"}]}]}}`)
	fw.Launchable = true
	return fw
}

// f hands tui-firewall the ports readiness found closed, prefilled, with one
// comment for all of them (issue #32).
func TestFirewallHandOffPrefilled(t *testing.T) {
	cases := map[string]struct {
		firewall headscale.Firewall
		want     string
	}{
		"control and node closed": {closedFirewall(),
			"tui-firewall --open 443/tcp,41641/udp --comment 'tailnet control plane and node'"},
		"node closed": {headscale.DemoFirewall(),
			"tui-firewall --open 41641/udp --comment 'tailnet node'"},
	}
	for name, tc := range cases {
		a, fake := fixtureApp(t, "")
		a.hsState.Firewall = tc.firewall
		model, _ := a.Update(key("f"))
		a = model.(*app)
		if len(fake.Launched) != 1 || fake.Launched[0] != tc.want {
			t.Errorf("%s: launched %q, want %q", name, fake.Launched, tc.want)
		}
		if !strings.Contains(a.status, "--open") {
			t.Errorf("%s: status %q", name, a.status)
		}
	}
}

// Nothing closed: tui-firewall starts plain, on its rule list.
func TestFirewallHandOffNothingClosed(t *testing.T) {
	a, fake := fixtureApp(t, "")
	open, _ := headscale.ParseTuiFirewallCheck(`{"enabled": false}`)
	open.Launchable = true
	a.hsState.Firewall = open
	a.Update(key("f"))
	if len(fake.Launched) != 1 || fake.Launched[0] != "tui-firewall" {
		t.Errorf("launched %q", fake.Launched)
	}
}

// A tui-firewall older than 0.6.0 has no --open: it starts plain, and the
// status line says which ports to add once it hands the terminal back.
func TestFirewallHandOffOlderFirewall(t *testing.T) {
	a, fake := fixtureApp(t, "")
	fake.FirewallVersion = "0.5.0"
	a.hsState.Firewall = closedFirewall()
	a.Update(key("f"))
	if len(fake.Launched) != 1 || fake.Launched[0] != "tui-firewall" {
		t.Fatalf("launched %q", fake.Launched)
	}
	model, _ := a.Update(firewallDoneMsg{})
	a = model.(*app)
	if !strings.Contains(a.status, "0.5.0") || !strings.Contains(a.status, "0.6.0") ||
		!strings.Contains(a.status, "443/tcp, 41641/udp") {
		t.Errorf("status %q", a.status)
	}
}
