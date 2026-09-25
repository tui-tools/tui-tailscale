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

// The control port is the one headscale binds: a port forward in front of
// the host can give clients another one in server_url. Behind a reverse proxy
// it is the proxy's, server_url's.
func TestControlPortFollowsTheBind(t *testing.T) {
	fw, _ := ParseIptablesInput("-P INPUT DROP\n-A INPUT -p tcp --dport 443 -j ACCEPT\n")
	for _, tc := range []struct {
		url, listen string
		want        int
	}{
		{"https://192.0.2.10:18443", "0.0.0.0:443", 443},
		{"https://vpn.example.com", "127.0.0.1:8080", 443},
		{"http://192.0.2.10:8080", "", 8080},
		{"https://vpn.example.com", ":8443", 8443},
	} {
		p := PortsFor(fw, tc.url, tc.listen)
		if p.ControlPort != tc.want {
			t.Errorf("%s / %s: port %d, want %d", tc.url, tc.listen, p.ControlPort, tc.want)
		}
	}
	if p := PortsFor(fw, "https://192.0.2.10:18443", "0.0.0.0:443"); p.Control != PortOpen {
		t.Errorf("443 is open: %+v", p)
	}
}

// ufw on iptables-nft, as `nft -j list ruleset` and `iptables -S` print it on
// an Ubuntu 26.04 lab guest with `ufw allow 443/tcp` and `ufw allow
// 41641/udp` (issue #24): an INPUT chain with a drop policy and nothing but
// jumps, the "not local" guard, and the accepts two chains down in
// ufw-user-input. Both reads follow the jumps to the same answer.
func TestPortsFollowUfwJumps(t *testing.T) {
	for _, tc := range []struct {
		name  string
		file  string
		parse func(string) (Firewall, bool)
	}{
		{"nft", "firewall-ufw-nft.json", ParseNftRuleset},
		{"iptables", "firewall-ufw-iptables.txt", ParseIptablesInput},
	} {
		fw, ok := tc.parse(readFixture(t, tc.file))
		if !ok {
			t.Fatalf("%s: not parsed", tc.name)
		}
		for _, want := range []struct {
			proto string
			port  int
			state PortState
		}{
			{"tcp", 443, PortOpen}, {"udp", NodePort, PortOpen}, {"tcp", 22, PortOpen},
			{"tcp", 19443, PortClosed}, {"udp", 3478, PortClosed},
		} {
			if got := fw.Check(want.proto, want.port); got != want.state {
				t.Errorf("%s: %d/%s = %s, want %s", tc.name, want.port, want.proto, got, want.state)
			}
		}
	}
}

// firewalld on Fedora 44, as `nft -j list ruleset` prints it with 443/tcp and
// 41641/udp added to the public zone: the zone is reached by a goto from the
// input chain, and the ports are accepted in filter_IN_public_allow.
func TestPortsFollowFirewalldGotos(t *testing.T) {
	fw, ok := ParseNftRuleset(readFixture(t, "firewall-firewalld-nft.json"))
	if !ok {
		t.Fatal("not parsed")
	}
	for _, want := range []struct {
		proto string
		port  int
		state PortState
	}{
		{"tcp", 443, PortOpen}, {"udp", NodePort, PortOpen}, {"tcp", 22, PortOpen},
		{"tcp", 8080, PortClosed},
	} {
		if got := fw.Check(want.proto, want.port); got != want.state {
			t.Errorf("%d/%s = %s, want %s", want.port, want.proto, got, want.state)
		}
	}
}

// A jump into a chain that is not in what was read may end anywhere: the
// answer is unknown, never the policy after it.
func TestPortsUnfollowableJumpIsUnknown(t *testing.T) {
	// The INPUT-only listing older releases read: the chains are not in it.
	only, _ := ParseIptablesInput("-P INPUT DROP\n-A INPUT -j ufw-before-input\n")
	if got := only.Check("tcp", 443); got != PortUnknown {
		t.Errorf("iptables -S INPUT with a jump: %s", got)
	}
	// A log target is not a jump: the policy still decides.
	logged, _ := ParseIptablesInput("-P INPUT DROP\n-A INPUT -j LOG --log-prefix x\n")
	if got := logged.Check("tcp", 443); got != PortClosed {
		t.Errorf("a LOG rule: %s", got)
	}
	missing, _ := ParseNftRuleset(`{"nftables": [{"chain": {"family": "ip", "table": "filter",
		"name": "INPUT", "hook": "input", "type": "filter", "policy": "drop"}},
		{"rule": {"family": "ip", "table": "filter", "chain": "INPUT",
		"expr": [{"jump": {"target": "elsewhere"}}]}}]}`)
	if got := missing.Check("tcp", 443); got != PortUnknown {
		t.Errorf("nft jump to a missing chain: %s", got)
	}
	// A goto that falls off its chain ends the calling one too; a return
	// goes back to the rule after the jump.
	gotoFw, _ := ParseIptablesInput("-P INPUT ACCEPT\n-N a\n-N b\n" +
		"-A INPUT -j a\n-A INPUT -p tcp --dport 443 -j ACCEPT\n-A INPUT -j DROP\n" +
		"-A a -p tcp --dport 22 -j RETURN\n-A a -g b\n-A a -j DROP\n" +
		"-A b -p tcp --dport 80 -j ACCEPT\n")
	for port, want := range map[int]PortState{22: PortClosed, 80: PortOpen, 443: PortOpen, 8080: PortClosed} {
		if got := gotoFw.Check("tcp", port); got != want {
			t.Errorf("goto/return: %d = %s, want %s", port, got, want)
		}
	}
	// Chains that jump to each other forever stop at the depth limit.
	loop, _ := ParseIptablesInput("-P INPUT DROP\n-N a\n-A INPUT -j a\n-A a -j a\n")
	if got := loop.Check("tcp", 443); got != PortUnknown {
		t.Errorf("a jump loop: %s", got)
	}
}
