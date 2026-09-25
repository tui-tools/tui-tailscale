package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// A login server whose certificate does not verify stops the join at the CA
// step; trusting the CA (previewed per distribution) checks again and goes on
// with the key.
func TestJoinTrustsAPrivateCA(t *testing.T) {
	a, fake := newTestApp(t)
	press(t, a, "j")
	typeText(a, tailscale.DemoPrivateServer)
	press(t, a, "enter")
	if a.mode != modeInput || a.inputPurpose != inputJoinCA {
		t.Fatalf("mode %v / %v, status %q: want the CA step", a.mode, a.inputPurpose, a.status)
	}
	if !strings.Contains(a.input.Help, "does not verify") ||
		!strings.Contains(a.input.Help, "unable to get local issuer") {
		t.Errorf("help = %q", a.input.Help)
	}
	typeText(a, "relative/ca.crt")
	press(t, a, "enter")
	if a.inputPurpose != inputJoinCA || !strings.Contains(a.input.Help, "not an absolute") {
		t.Fatalf("a relative path was taken: %q", a.input.Help)
	}
	typeText(a, "/etc/tui-cert/ca.crt")
	press(t, a, "enter")
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the trust preview", a.mode)
	}
	want := "sudo -n install -m 644 /etc/tui-cert/ca.crt " +
		"/usr/local/share/ca-certificates/tui-tailscale-headscale.lab.internal.crt\n" +
		"$ sudo -n update-ca-certificates\n$ sudo -n systemctl restart tailscaled"
	if a.confirm.Command != want {
		t.Errorf("preview =\n%s\nwant\n%s", a.confirm.Command, want)
	}
	press(t, a, "y")
	if a.mode != modeInput || a.inputPurpose != inputJoinKey {
		t.Fatalf("after the trust step: mode %v / %v, status %q", a.mode, a.inputPurpose, a.status)
	}
	if a.join.server != tailscale.DemoPrivateServer {
		t.Error("the join lost its server")
	}
	if len(changes(fake)) != 3 {
		t.Errorf("ran %q", changes(fake))
	}
}

// Emptying the CA step stops the join, and nothing runs.
func TestJoinCAStepCancels(t *testing.T) {
	a, fake := newTestApp(t)
	press(t, a, "j")
	typeText(a, tailscale.DemoPrivateServer)
	press(t, a, "enter")
	press(t, a, "esc")
	if a.mode != modeBrowse || a.join.server != "" || len(changes(fake)) != 0 {
		t.Errorf("mode %v, join %+v, ran %q", a.mode, a.join, changes(fake))
	}
}

// f hands the terminal to tui-firewall, and the ports are read again after.
func TestFirewallHandOff(t *testing.T) {
	a, fake := fixtureApp(t, "")
	model, cmd := a.Update(key("f"))
	a = model.(*app)
	if cmd == nil || !a.busy || !strings.Contains(a.status, "tui-firewall") {
		t.Fatalf("f: busy %v, status %q", a.busy, a.status)
	}
	if len(fake.Launched) != 1 {
		t.Errorf("launched %q", fake.Launched)
	}
	model, _ = a.Update(firewallDoneMsg{})
	a = model.(*app)
	if a.busy || !a.loading {
		t.Error("the screen did not come back to a re-read")
	}
	// Without tui-firewall, f says where it comes from.
	fw := headscale.DemoFirewall()
	fw.Launchable = false
	a.hsState.Firewall = fw
	a.Update(key("f"))
	if !strings.Contains(a.status, "not installed") {
		t.Errorf("status = %q", a.status)
	}
}

// A closed control port makes f the next step, first on the hint bar.
func TestClosedPortLeadsTheHintBar(t *testing.T) {
	a, _ := fixtureApp(t, "")
	closed, _ := headscale.ParseIptablesInput("-P INPUT DROP\n")
	closed.Launchable = true
	a.hsState.Firewall = closed
	hints := a.shortHelpKeys()
	if hints[1].Key != "f" || !strings.Contains(hints[1].Desc, "next") {
		t.Errorf("hints = %+v", hints)
	}
	if !strings.Contains(a.View(), "443/tcp is closed in the host firewall") {
		t.Error("the readiness line does not say the port is closed")
	}
}
