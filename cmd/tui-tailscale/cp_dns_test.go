package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// dnsApp is the demo app on the dns screen, unit enabled so the tail is the
// plain restart.
func dnsApp(t *testing.T) (*app, *headscale.Fake) {
	t.Helper()
	a, fake := fixtureApp(t, "")
	a.setScreen(screenDNS)
	return a, fake
}

// selectRow moves the dns screen's cursor to the row with that label.
func selectRow(t *testing.T, a *app, label string) {
	t.Helper()
	for i, r := range a.dnsRows() {
		if r.label == label {
			a.cursor[screenDNS] = i
			return
		}
	}
	t.Fatalf("no dns row %q", label)
}

// The screen shows the demo's split entry and record as rows of their own.
func TestDNSScreenRows(t *testing.T) {
	a, _ := dnsApp(t)
	view := a.View()
	for _, want := range []string{"magic_dns", "base_domain", "tailnet.example.com",
		"nameservers.global", "1.1.1.1, 1.0.0.1", "split corp.example.com", "10.0.0.2",
		"record grafana.tailnet.example.com", "A 100.64.0.3", "e edit"} {
		if !strings.Contains(view, want) {
			t.Errorf("the dns screen is missing %q", want)
		}
	}
}

// n adds a split domain: domain, nameservers, a one-line diff, the restart.
func TestDNSAddSplitDomain(t *testing.T) {
	a, fake := dnsApp(t)
	press(t, a, "n")
	a = pick(t, a, dnsNewSplit)
	a = typeAndEnter(t, a, "oraclevcn.com")
	a = typeAndEnter(t, a, "169.254.169.254")
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, status %q", a.mode, a.status)
	}
	removed, added := diffLines(a)
	if len(removed) != 1 || len(added) != 1 || !strings.Contains(added[0],
		`split: {"corp.example.com": ["10.0.0.2"], "oraclevcn.com": ["169.254.169.254"]}`) {
		t.Fatalf("diff -%q +%q", removed, added)
	}
	a = confirmAndRun(t, a)
	if a.mode != modeConfirm || !strings.Contains(a.confirm.Command, "systemctl restart headscale") {
		t.Fatalf("no restart after the write: %q", a.confirm.Command)
	}
	confirmAndRun(t, a)
	state, _ := fake.Load(t.Context())
	if len(state.ControlPlane.DNS.Split) != 2 {
		t.Errorf("split after = %+v", state.ControlPlane.DNS.Split)
	}
}

// e on a record edits it in place; a CNAME is refused with the reason.
func TestDNSEditRecord(t *testing.T) {
	a, fake := dnsApp(t)
	selectRow(t, a, "record grafana.tailnet.example.com")
	press(t, a, "e")
	if a.inputPurpose != inputDNSRecord || a.input.Value() != "grafana.tailnet.example.com A 100.64.0.3" {
		t.Fatalf("e: %v %q", a.inputPurpose, a.input.Value())
	}
	a.input.Model.SetValue("")
	a = typeAndEnter(t, a, "grafana.tailnet.example.com CNAME other.example.com")
	if a.inputPurpose != inputDNSRecord || !strings.Contains(a.input.Help, "only A and AAAA") {
		t.Fatalf("a CNAME was not refused: %v %q", a.inputPurpose, a.input.Help)
	}
	a.input.Model.SetValue("")
	a = typeAndEnter(t, a, "grafana.tailnet.example.com A 100.64.0.4")
	a = confirmAndRun(t, a)
	confirmAndRun(t, a)
	state, _ := fake.Load(t.Context())
	if r := state.ControlPlane.DNS.ExtraRecords; len(r) != 1 || r[0].Value != "100.64.0.4" {
		t.Errorf("records after = %+v", r)
	}
}

// x removes the selected split domain; on another row it says what x does.
func TestDNSRemove(t *testing.T) {
	a, fake := dnsApp(t)
	selectRow(t, a, "magic_dns")
	press(t, a, "x")
	if a.mode != modeBrowse || !strings.Contains(a.status, "x removes") {
		t.Errorf("x on magic_dns: mode %v, status %q", a.mode, a.status)
	}
	selectRow(t, a, "split corp.example.com")
	press(t, a, "x")
	_, added := diffLines(a)
	if len(added) != 1 || !strings.HasSuffix(added[0], "split: {}") {
		t.Fatalf("diff + %q", added)
	}
	a = confirmAndRun(t, a)
	confirmAndRun(t, a)
	state, _ := fake.Load(t.Context())
	if len(state.ControlPlane.DNS.Split) != 0 {
		t.Error("the split domain was not removed")
	}
}

// A base domain containing the server_url host is refused, as headscale would.
func TestDNSBaseDomainConflict(t *testing.T) {
	a, _ := dnsApp(t)
	selectRow(t, a, "base_domain")
	press(t, a, "e")
	a.input.Model.SetValue("")
	a = typeAndEnter(t, a, "example.com")
	if a.mode != modeInput || !strings.Contains(a.input.Help, "refuses to start") {
		t.Errorf("mode %v, help %q", a.mode, a.input.Help)
	}
}

// Global nameservers: a name is refused, addresses and a DoH URL are taken.
func TestDNSGlobalNameservers(t *testing.T) {
	a, _ := dnsApp(t)
	selectRow(t, a, "nameservers.global")
	press(t, a, "e")
	a.input.Model.SetValue("")
	a = typeAndEnter(t, a, "dns.example.com")
	if a.mode != modeInput || !strings.Contains(a.input.Help, "is not a nameserver") {
		t.Fatalf("mode %v, help %q", a.mode, a.input.Help)
	}
	a.input.Model.SetValue("")
	a = typeAndEnter(t, a, "9.9.9.9, https://dns.nextdns.io/abc123")
	_, added := diffLines(a)
	if len(added) != 1 || !strings.Contains(added[0],
		`global: ["9.9.9.9", "https://dns.nextdns.io/abc123"]`) {
		t.Errorf("diff + %q", added)
	}
}

// MagicDNS off, then the override switch: each a one-line diff.
func TestDNSSwitches(t *testing.T) {
	a, _ := dnsApp(t)
	selectRow(t, a, "magic_dns")
	press(t, a, "e")
	a = pick(t, a, pickerNo)
	_, added := diffLines(a)
	if len(added) != 1 || strings.TrimSpace(added[0]) != "magic_dns: false" {
		t.Errorf("diff + %q", added)
	}
}
