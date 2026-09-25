package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// partialResetApp is --demo=partial-reset: tailscaled stopped and disabled,
// headscale installed with its configuration deleted (issue #18).
func partialResetApp(t *testing.T) (*app, *tailscale.Fake, *headscale.Fake) {
	t.Helper()
	shortSettle(t)
	ts, hs := tailscale.NewFake(), headscale.NewFake()
	ts.SetDaemonStopped("disabled")
	hs.SetConfigMissing()
	a := newApp(ts, hs, theme.New(), nil)
	a.width, a.height = 140, 40
	a.Update(a.load()())
	return a, ts, hs
}

// u on a stopped tailscaled previews `systemctl enable --now tailscaled`,
// runs it, re-reads through the settle logic and lands on logged out, with j
// named next. The hint bar leads with u until then.
func TestUStartsAStoppedDaemon(t *testing.T) {
	a, ts, _ := partialResetApp(t)
	view := ansi.Strip(a.View())
	if !strings.Contains(view, "tailscaled is stopped and disabled · u starts it") ||
		!strings.Contains(view, "u start tailscaled ◂ next") {
		t.Errorf("the node screen does not offer u:\n%s", view)
	}
	press(t, a, "u")
	if a.mode != modeConfirm || !strings.Contains(a.confirm.Command,
		"sudo -n systemctl enable --now tailscaled") {
		t.Fatalf("u did not preview the start: mode %v, %q (status %q)", a.mode,
			a.confirm.Command, a.status)
	}
	press(t, a, "y")
	if !a.state.DaemonRunning || a.state.LoggedIn() {
		t.Fatalf("after the start: %+v", a.state)
	}
	if a.status != "tailscaled is running · logged out · j joins a tailnet" {
		t.Errorf("status = %q", a.status)
	}
	ran := ts.Commands()
	if len(ran) == 0 || ran[len(ran)-1].String() != "systemctl enable --now tailscaled" {
		t.Errorf("ran = %v", ran)
	}
	if strings.Contains(ansi.Strip(a.View()), "start tailscaled") {
		t.Error("the hint bar still offers the start")
	}
}

// The control-plane screens say the configuration is missing, i previews the
// reinstall, and after it S is next. S itself refuses while the file is gone.
func TestIReinstallsAMissingConfig(t *testing.T) {
	a, _, _ := partialResetApp(t)
	press(t, a, "3")
	view := ansi.Strip(a.View())
	for _, want := range []string{"headscale's configuration is missing",
		"/var/lib/headscale is missing · the unit recreates it", "i reinstall headscale ◂ next"} {
		if !strings.Contains(view, want) {
			t.Errorf("the users screen lacks %q:\n%s", want, view)
		}
	}
	press(t, a, "S")
	if a.mode != modeBrowse || a.status != headscale.ConfigMissingMessage {
		t.Errorf("S with no config: mode %v, status %q", a.mode, a.status)
	}
	press(t, a, "i")
	if a.mode != modeConfirm || !strings.Contains(a.confirm.Command,
		"apt-get install --reinstall -y -o Dpkg::Options::=--force-confmiss headscale") {
		t.Fatalf("i did not preview the reinstall: %q (status %q)", a.confirm.Command, a.status)
	}
	press(t, a, "y")
	if a.hsState.ControlPlane.ConfigMissing || !strings.HasPrefix(a.status, "headscale reinstalled") {
		t.Errorf("after the reinstall: status %q, cp %+v", a.status, a.hsState.ControlPlane)
	}
	if !strings.Contains(ansi.Strip(a.View()), "S server ◂ next") {
		t.Errorf("S should be next:\n%s", ansi.Strip(a.View()))
	}
}

// --check says both: tailscale.daemon and headscale.configPresent.
func TestRunCheckPartialReset(t *testing.T) {
	ts, hs := tailscale.NewFake(), headscale.NewFake()
	ts.SetDaemonStopped("disabled")
	hs.SetConfigMissing()
	var out strings.Builder
	if err := runCheck(t.Context(), ts, hs, nil, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"daemon": "disabled"`, `"configPresent": false`,
		`"stateDirPresent": false`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--check lacks %s", want)
		}
	}
	report, _ := checkOf(t, headscale.NewFake(), nil)
	if report.Tailscale.Daemon != tailscale.DaemonRunning || !report.Headscale.ConfigPresent ||
		!report.Headscale.StateDirPresent {
		t.Errorf("the healthy demo: daemon %q, config %v, state dir %v",
			report.Tailscale.Daemon, report.Headscale.ConfigPresent,
			report.Headscale.StateDirPresent)
	}
}

// The readiness line points at u when j cannot join this host yet.
func TestReadinessPointsAtUWhenTheDaemonIsStopped(t *testing.T) {
	shortSettle(t)
	ts, hs := tailscale.NewFake(), headscale.NewFake()
	ts.SetDaemonStopped("disabled")
	hs.SetService("active", "enabled")
	hs.SetNodes(nil)
	hs.SetRegistrations(nil)
	hs.SetFirewall(headscale.Firewall{})
	a := newApp(ts, hs, theme.New(), nil)
	a.width, a.height = 200, 40
	a.Update(a.load()())
	press(t, a, "3")
	if !strings.Contains(ansi.Strip(a.View()), "u on the node screen starts tailscaled, then j") {
		t.Errorf("readiness:\n%s", ansi.Strip(a.View()))
	}
}
