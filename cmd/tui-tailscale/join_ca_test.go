package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// A login server whose certificate does not verify stops the join at the CA
// step, which offers the CAs tui-cert keeps here first; trusting one
// (previewed per distribution) checks again and goes on with the key.
func TestJoinTrustsAPrivateCA(t *testing.T) {
	a, fake := newTestApp(t)
	press(t, a, "j")
	typeText(a, tailscale.DemoPrivateServer)
	press(t, a, "enter")
	if a.mode != modePicker || a.pickerPurpose != pickerJoinCA {
		t.Fatalf("mode %v / %v, status %q: want the CA list", a.mode, a.pickerPurpose, a.status)
	}
	if len(a.picker.Options) != 2 || !strings.HasPrefix(a.picker.Options[0],
		"homelab-ca · SHA-256 CC:70:FC:C3 · expires ") || a.picker.Options[1] != otherFile {
		t.Errorf("options = %q", a.picker.Options)
	}
	press(t, a, "enter")
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the trust preview", a.mode)
	}
	want := "sudo -n install -m 644 /etc/tui-cert/ca/homelab-ca/ca.crt " +
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

// Without tui-cert the CA step is the file picker, which says why the
// certificate failed and where a local CA comes from; it opens where
// tui-cert's export copies a CA to.
func TestJoinCAFromAFile(t *testing.T) {
	a, _ := newTestApp(t)
	a.hs.(*headscale.Fake).SetLocalPKI(headscale.LocalPKI{})
	press(t, a, "j")
	typeText(a, tailscale.DemoPrivateServer)
	press(t, a, "enter")
	if a.mode != modeFilePicker || a.inputPurpose != inputJoinCA {
		t.Fatalf("mode %v / %v, status %q: want the CA picker", a.mode, a.inputPurpose, a.status)
	}
	help := a.filePicker.Help
	for _, want := range []string{"does not verify", "unable to get local issuer",
		headscale.CertToolURL} {
		if !strings.Contains(help, want) {
			t.Errorf("help is missing %q: %q", want, help)
		}
	}
	if a.filePicker.Dir != localCADir {
		t.Errorf("the picker opened in %q", a.filePicker.Dir)
	}
	a, _ = pasteFile(t, a, "/home/user/notes.txt")
	if a.mode != modeFilePicker || !strings.Contains(a.filePicker.Message(), ".pem") {
		t.Errorf("a file of the wrong kind was taken (mode %v, message %q)",
			a.mode, a.filePicker.Message())
	}
	a, _ = pasteFile(t, a, "/home/user/homelab-ca.crt")
	if a.mode != modeConfirm || !strings.Contains(a.confirm.Command,
		"install -m 644 /home/user/homelab-ca.crt ") {
		t.Fatalf("mode = %v, preview %q", a.mode, a.confirm.Command)
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
