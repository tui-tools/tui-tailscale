package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// TestSEnablesTheEmbeddedRelay drives S to its relay step on headscale's
// example file over plain http: the embedded relay alone, a STUN address,
// and a confirm whose diff enables it, drops the public map and writes the
// IP of server_url in, with the STUN port and the TLS caveat said (issue #28).
func TestSEnablesTheEmbeddedRelay(t *testing.T) {
	a, fake := fixtureApp(t, "headscale-config.yaml")
	a = startS(t, a, headscale.TransportPlainHTTP)
	a = clearAndType(t, a, "http://172.16.5.10:8080")
	a = clearAndType(t, a, "0.0.0.0:8080")
	a = clearAndType(t, a, "tailnet.internal")
	if a.mode != modePicker || a.pickerPurpose != pickerRelays {
		t.Fatalf("no relay step (mode %d, status %q)", a.mode, a.status)
	}
	for _, o := range a.picker.Options {
		if o == relayPublicOnly {
			t.Error("turning the relay off is offered while it is off")
		}
	}
	a = pick(t, a, relayEmbeddedOnly)
	if a.inputPurpose != inputDERPSTUN || a.input.Model.Value() != "0.0.0.0:3478" {
		t.Fatalf("no STUN step (purpose %d, value %q)", a.inputPurpose, a.input.Model.Value())
	}
	a = clearAndType(t, a, "3478")
	if a.inputPurpose != inputDERPSTUN || !strings.Contains(a.status, "stun_listen_addr") {
		t.Fatalf("a bad STUN address was not refused at its step: %q", a.status)
	}
	a = clearAndType(t, a, "0.0.0.0:3478")
	if a.mode != modeConfirm {
		t.Fatalf("no confirm (mode %d, status %q)", a.mode, a.status)
	}
	removed, added := diffLines(a)
	for _, want := range []string{"    enabled: true", `    ipv4: "172.16.5.10"`,
		`    ipv6: ""`, "  urls: []"} {
		if !contains(added, want) {
			t.Errorf("the diff is missing %q:\n%s", want, a.confirm.Body)
		}
	}
	if !contains(removed, "    enabled: false") {
		t.Errorf("the diff does not replace enabled: false:\n%s", a.confirm.Body)
	}
	for _, want := range []string{"3478/udp", "no fallback relay", "WARNING: server_url is not https"} {
		if !strings.Contains(a.confirm.Body, want) {
			t.Errorf("the dialog does not say %q:\n%s", want, a.confirm.Body)
		}
	}
	a = confirmAndRun(t, a)
	state, err := fake.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if headscale.Relays(state.ControlPlane) != headscale.RelaysEmbedded {
		t.Errorf("relays after the write = %q", headscale.Relays(state.ControlPlane))
	}
	// Now on, S offers to turn it off.
	a.hsState = state
	a.mode = modeBrowse
	a = startS(t, a, headscale.TransportPlainHTTP)
	a = enter(t, clearAndType(t, a, "http://172.16.5.10:8080"))
	a = clearAndType(t, a, "tailnet.internal")
	if !contains(a.picker.Options, relayPublicOnly) {
		t.Errorf("options = %q", a.picker.Options)
	}
}
