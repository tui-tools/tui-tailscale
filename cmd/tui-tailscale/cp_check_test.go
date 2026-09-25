package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// checkOf runs --check on the two demo backends, or the ones given, and
// decodes it.
func checkOf(t *testing.T, hs headscale.Backend, probed []compat.Result) (checkReport, string) {
	t.Helper()
	var buf bytes.Buffer
	if err := runCheck(context.Background(), tailscale.NewFake(), hs, probed, &buf); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var report checkReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("the output is not JSON: %v\n%s", err, buf.String())
	}
	return report, buf.String()
}

func TestRunCheckPrintsTheControlPlane(t *testing.T) {
	report, _ := checkOf(t, headscale.NewFake(), []compat.Result{
		{Backend: backendName, Version: "1.102.4"},
		{Backend: backendHeadscale, Version: "0.29.3"},
	})
	hs := report.Headscale
	if !hs.Present || hs.Users != 2 || hs.Nodes != 3 || hs.NodesOnline != 3 ||
		hs.NodesExpired != 0 || hs.PreAuthKeys != 2 {
		t.Errorf("headscale summary is wrong: %+v", hs)
	}
	if !hs.OIDCConfigured {
		t.Error("the demo control plane is OIDC-configured; --check should say so")
	}
	// The demo's unit runs but is disabled, which --check has to say: it is
	// the state that loses the control plane at the next reboot.
	cps := hs.ControlPlane
	if cps.ServiceEnabled != "disabled" || cps.ServiceAccount != "headscale:headscale" {
		t.Errorf("unit = %q as %q", cps.ServiceEnabled, cps.ServiceAccount)
	}
	// The demo's noise key is root's, which --check names by path.
	if !cps.OwnershipChecked || cps.OwnershipOK || len(cps.OwnershipIssues) != 1 ||
		cps.OwnershipIssues[0].Path != "/var/lib/headscale/noise_private.key" {
		t.Errorf("ownership = %v %v %+v", cps.OwnershipChecked, cps.OwnershipOK,
			cps.OwnershipIssues)
	}
	// Readiness: the server is set up, and the disabled unit is next.
	r := hs.Readiness
	if !r.Installed || !r.ServerConfigured || !r.UnitRunning || r.UnitEnabled ||
		!r.OIDCConfigured || !r.FirstNode || r.RoutesPending != 1 || r.Next != headscale.NextUnit {
		t.Errorf("readiness = %+v", r)
	}
	if hs.Install != nil {
		t.Error("an installed headscale needs no install block")
	}
	if len(report.Compat) != 2 || report.Compat[1].Backend != backendHeadscale {
		t.Errorf("compat = %+v, want both backends", report.Compat)
	}
}

// TestCheckCarriesNoAddressOfThisHost is the promise the whole block is
// written around, enforced rather than asserted in a comment. --check is
// pasted into issues and scripts, so no URL and no address of this host may
// survive into it — the two server_url questions are booleans and the OIDC
// issuer is reduced to a host name.
func TestCheckCarriesNoAddressOfThisHost(t *testing.T) {
	report, out := checkOf(t, headscale.NewFake(), nil)

	// Everything the demo's configuration holds that names a machine.
	for _, forbidden := range []string{
		"https://headscale.example.com",     // server_url
		"https://idp.example.com",           // the issuer URL
		"/realms/demo",                      // the issuer's path
		"127.0.0.1:8080",                    // listen_addr
		"/etc/headscale/oidc_client_secret", // the secret path
		"100.64.0.", "fd7a:115c", "192.0.2.", "198.51.100.",
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("--check carries %q", forbidden)
		}
	}
	// A URL scheme anywhere in the block would mean one got through.
	if strings.Contains(out, "://") {
		t.Errorf("--check carries a URL:\n%s", out)
	}

	// What replaced them still answers the questions a report needs.
	cp := report.Headscale.ControlPlane
	if !cp.ServerURLSet || !cp.ServerURLHTTPS || cp.ServerURLLoopback {
		t.Errorf("the server_url booleans do not describe the demo: %+v", cp)
	}
	// The demo sits behind a reverse proxy: headscale binds loopback.
	if cp.ListenPort != 8080 || !cp.ListenLoopback {
		t.Errorf("listen port = %d, loopback = %v", cp.ListenPort, cp.ListenLoopback)
	}
	if cp.Transport != headscale.TransportReverseProxy || cp.ServerURLIsIP {
		t.Errorf("transport = %q, ip = %v", cp.Transport, cp.ServerURLIsIP)
	}
	if cp.BaseDomain != "tailnet.example.com" || cp.BaseDomainConflict || !cp.MagicDNS {
		t.Errorf("base domain = %q, conflict %v, magic %v", cp.BaseDomain,
			cp.BaseDomainConflict, cp.MagicDNS)
	}
	if cp.OIDCIssuer != "idp.example.com" || report.Headscale.OIDCIssuer != "idp.example.com" {
		t.Errorf("oidcIssuer = %q / %q, want the host alone", cp.OIDCIssuer,
			report.Headscale.OIDCIssuer)
	}
}

// TestCheckReportsAnUnreachableServerURL: the booleans have to catch the two
// failures the URL was there to reveal.
func TestCheckReportsAnUnreachableServerURL(t *testing.T) {
	for _, tc := range []struct {
		url             string
		https, loopback bool
	}{
		{"https://headscale.example.com", true, false},
		{"http://headscale.example.com", false, false},
		{"https://127.0.0.1:8080", true, true},
		{"http://localhost:8080", false, true},
	} {
		if got := headscale.ServerURLIsHTTPS(tc.url); got != tc.https {
			t.Errorf("%s: https = %v, want %v", tc.url, got, tc.https)
		}
		got := headscale.IsLoopbackHost(headscale.URLHost(tc.url))
		if got != tc.loopback {
			t.Errorf("%s: loopback = %v, want %v", tc.url, got, tc.loopback)
		}
	}
}

// TestCheckWithTheUnitStopped: --check reports the stopped unit as the
// sentence the screens show, not as the CLI's socket error, and the readiness
// names the unit as the next step.
func TestCheckWithTheUnitStopped(t *testing.T) {
	fake := headscale.NewFake()
	fake.SetService("failed", "enabled")
	report, _ := checkOf(t, fake, nil)
	hs := report.Headscale
	if !hs.NotRunning || !strings.Contains(hs.Error, "headscale has failed") || hs.Users != 0 {
		t.Errorf("stopped unit in --check: %+v", hs)
	}
	if hs.Readiness.Next != headscale.NextUnit || hs.Readiness.UnitRunning {
		t.Errorf("readiness = %+v", hs.Readiness)
	}
}

// TestCheckWithoutHeadscale: an absent control plane is a fact, with the
// commands `i` would run and "install" as the next step.
func TestCheckWithoutHeadscale(t *testing.T) {
	report, _ := checkOf(t, noHeadscale{headscale.NewFake()}, nil)
	hs := report.Headscale
	if hs.Present || hs.Install == nil || hs.Readiness.Next != headscale.NextInstall {
		t.Fatalf("headscale = %+v", hs)
	}
	if hs.Install.Distro != "ubuntu-24.04" || hs.Install.Manager != "apt" {
		t.Errorf("install = %+v", hs.Install)
	}
	if got := strings.Join(hs.Install.Commands, "\n"); !strings.Contains(got,
		"sudo apt-get install -y headscale") || !strings.Contains(got, "pkgs.tui.tools/pubkey.asc") {
		t.Errorf("commands = %s", got)
	}
}

// noHeadscale is a control plane on a machine without headscale, with the
// tui-tools repository not set up yet.
type noHeadscale struct{ *headscale.Fake }

func (noHeadscale) Load(context.Context) (headscale.State, error) {
	return headscale.State{Distro: pkgmgr.ParseOSRelease(
		"ID=ubuntu\nID_LIKE=debian\nVERSION_ID=24.04\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n")}, nil
}

// Issue #19 in --check: the first node joined with a single-use key, which is
// spent, and there is no identity provider. Identity is not the next step;
// canJoinMore says no other machine can join now, and why.
func TestRunCheckSpentKeyIsNotBlocking(t *testing.T) {
	hs := headscale.NewFake()
	hs.SetService("active", "enabled")
	hs.SetConfig("server_url: https://headscale.example.com\nlisten_addr: 127.0.0.1:8080\n" +
		"dns:\n  magic_dns: true\n  base_domain: tailnet.example.com\n")
	hs.SetPreAuthKeys([]headscale.PreAuthKey{{ID: "1", User: "ops@example.com",
		KeyPrefix: "0123456789", Used: true, Expiration: time.Now().Add(time.Hour)}})
	report, out := checkOf(t, hs, nil)
	r := report.Headscale.Readiness
	if r.Next == headscale.NextIdentity || r.CanJoinMore ||
		r.CanJoinMoreReason != headscale.JoinMoreSpent || r.Hint == "" {
		t.Errorf("readiness = %+v", r)
	}
	for _, want := range []string{`"canJoinMore": false`, `"canJoinMoreReason": "spent"`} {
		if !strings.Contains(out, want) {
			t.Errorf("--check lacks %s", want)
		}
	}
}

// The keys screen marks a spent single-use key and says how to join several
// machines with one.
func TestKeysScreenMarksASpentKey(t *testing.T) {
	a, _ := newTestApp(t)
	press(t, a, "5")
	view := ansi.Strip(a.View())
	if !strings.Contains(view, "spent") || !strings.Contains(view, "n creates a reusable one") {
		t.Errorf("the keys screen does not mark the spent key:\n%s", view)
	}
}
