package headscale

import (
	"strings"
	"testing"
)

// The same cloud-image input chain — 443/tcp open, 41641/udp not, everything
// else rejected at the end — read three ways gives the same answer.
func TestPortsFromEverySource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		file   string
		parse  func(string) (Firewall, bool)
		source string
		node   PortState
	}{
		{"tui-firewall", "firewall-tui-firewall.json", ParseTuiFirewallCheck, SourceTuiFirewall, PortClosed},
		{"nft", "firewall-nft.json", ParseNftRuleset, SourceNftables, PortClosed},
		{"iptables", "firewall-iptables.txt", ParseIptablesInput, SourceIptables, PortOpen},
	} {
		fw, ok := tc.parse(readFixture(t, tc.file))
		if !ok || fw.Source != tc.source {
			t.Fatalf("%s: not parsed (%+v)", tc.name, fw)
		}
		if got := fw.Check("tcp", 443); got != PortOpen {
			t.Errorf("%s: 443/tcp = %s, want open", tc.name, got)
		}
		if got := fw.Check("tcp", 8080); got != PortClosed {
			t.Errorf("%s: 8080/tcp = %s, want closed (the final reject)", tc.name, got)
		}
		if got := fw.Check("udp", NodePort); got != tc.node {
			t.Errorf("%s: 41641/udp = %s, want %s", tc.name, got, tc.node)
		}
	}
}

func TestPortsEdgeCases(t *testing.T) {
	// No firewall read: nothing is claimed.
	if (Firewall{}).Check("tcp", 443) != PortUnknown {
		t.Error("an unread firewall claims an answer")
	}
	// A disabled tui-firewall refuses nothing.
	off, _ := ParseTuiFirewallCheck(`{"enabled": false, "model": {"Groups": []}}`)
	if off.Check("tcp", 443) != PortOpen {
		t.Error("a disabled firewall closes a port")
	}
	// A policy of drop with nothing opened closes it.
	drop, _ := ParseIptablesInput("-P INPUT DROP\n-A INPUT -i lo -j ACCEPT\n")
	if drop.Check("tcp", 443) != PortClosed {
		t.Error("policy DROP")
	}
	// A multiport list and a range open what they name.
	multi, _ := ParseIptablesInput("-P INPUT DROP\n" +
		"-A INPUT -p tcp -m multiport --dports 80,443 -j ACCEPT\n" +
		"-A INPUT -p udp --dport 41000:42000 -j ACCEPT\n")
	if multi.Check("tcp", 443) != PortOpen || multi.Check("udp", NodePort) != PortOpen ||
		multi.Check("tcp", 22) != PortClosed {
		t.Error("multiport / range")
	}
	// A rule limited to a source does not open the port for clients.
	src, _ := ParseIptablesInput("-P INPUT DROP\n-A INPUT -s 10.0.0.0/16 -p tcp --dport 443 -j ACCEPT\n")
	if src.Check("tcp", 443) != PortClosed {
		t.Error("a source-limited rule opened the port")
	}
	// An nft set of ports.
	set, _ := ParseNftRuleset(`{"nftables": [{"chain": {"family": "inet", "table": "f", "name": "in",
		"hook": "input", "policy": "drop"}}, {"rule": {"family": "inet", "table": "f", "chain": "in",
		"expr": [{"match": {"op": "==", "left": {"payload": {"protocol": "tcp", "field": "dport"}},
		"right": {"set": [80, 443]}}}, {"accept": null}]}}]}`)
	if set.Check("tcp", 443) != PortOpen || set.Check("udp", NodePort) != PortClosed {
		t.Error("nft set")
	}
	// No input chain at all filters nothing.
	none, _ := ParseNftRuleset(`{"nftables": []}`)
	if none.Check("udp", NodePort) != PortOpen {
		t.Error("an empty rule set closes a port")
	}
}

// A closed control port is the step after the unit.
func TestReadinessPortsStep(t *testing.T) {
	f := NewFake()
	f.SetService("active", "enabled")
	closed, _ := ParseIptablesInput("-P INPUT ACCEPT\n-A INPUT -p tcp --dport 22 -j ACCEPT\n" +
		"-A INPUT -j REJECT\n")
	closed.Launchable = true
	f.SetFirewall(closed)
	state, _ := f.Load(t.Context())
	r := ReadinessFor(state, state.Nodes[0].LastSeen)
	if r.Next != NextPorts || r.Ports == nil || r.Ports.ControlPort != 443 ||
		!strings.Contains(r.NextStep, "443/tcp is closed") || !strings.Contains(r.NextStep, "f opens") {
		t.Errorf("readiness = %+v", r)
	}
	// The demo's own firewall: 443 open, 41641 closed, which is only a note.
	f.SetFirewall(DemoFirewall())
	state, _ = f.Load(t.Context())
	r = ReadinessFor(state, state.Nodes[0].LastSeen)
	if r.Next == NextPorts || r.Ports.Control != PortOpen || r.Ports.Node != PortClosed {
		t.Errorf("demo readiness = %+v", r)
	}
}
