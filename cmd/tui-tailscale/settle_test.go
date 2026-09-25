package main

import (
	"strings"
	"testing"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// shortSettle makes the automatic re-read after a change immediate for the
// length of a test.
func shortSettle(t *testing.T) {
	t.Helper()
	saved := settleDelay
	settleDelay = time.Millisecond
	t.Cleanup(func() { settleDelay = saved })
}

// TestInstallTailscaleTakesEffectWithoutARestart is issue #9 on the node side:
// the client appears between two reads (the backend only finds it again when
// it re-probes), and its daemon refuses the first read after the install. The
// screen goes from "not installed" to running without restarting the tool,
// and never shows the refusal as a permission error.
func TestInstallTailscaleTakesEffectWithoutARestart(t *testing.T) {
	shortSettle(t)
	ts := tailscale.NewFake()
	ts.SetAbsent()
	probes := 0
	a := newApp(ts, headscale.NewFake(), theme.New(), nil)
	a.probe = func() []compat.Result {
		probes++
		return []compat.Result{{Backend: backendName, Version: "1.102.4",
			Status: compat.StatusTested}}
	}
	a.width, a.height = 120, 40
	a.Update(a.load()())
	if a.state.Installed || !strings.Contains(a.View(), "not installed") {
		t.Fatal("the fake should start without tailscale")
	}

	// The first read after the install is refused: the screen says the
	// daemon is not running yet, not that the user was refused.
	press(t, a, "i")
	if a.mode != modeConfirm {
		t.Fatalf("i did not open the install (status %q)", a.status)
	}
	plan := a.confirm.Payload.(tailscale.Plan)
	a.mode, a.confirm = modeBrowse, ui.Confirm{}
	_, reload := a.Update(a.runPlan(plan)())
	_, retry := a.Update(reload())
	if !a.state.Installed || a.state.PermissionDenied ||
		!strings.Contains(a.View(), "tailscaled is not running yet · r re-reads") {
		t.Errorf("first read after the install:\n%s", a.View())
	}
	if strings.Contains(a.View(), "refused this user") {
		t.Error("the refusal is shown as a permission error")
	}
	if retry == nil {
		t.Fatal("no automatic re-read was scheduled")
	}
	if a.status != "tailscale installed · j joins a tailnet" {
		t.Errorf("status = %q", a.status)
	}
	if probes == 0 || a.backendCompat.Version != "1.102.4" {
		t.Errorf("the versions were not probed again (%d, %+v)", probes, a.backendCompat)
	}

	// The one automatic re-read finds it running.
	_, reread := a.Update(retry())
	a.Update(reread())
	if !a.state.DaemonRunning || a.settling {
		t.Errorf("after the re-read: running %v, settling %v", a.state.DaemonRunning, a.settling)
	}
}

// TestInstallHeadscaleTakesEffectWithoutARestart is issue #9 on the control
// plane, driven the way a user does it: i, y, and the settle loop run by the
// test harness. headscale appears between two reads, its CLI fails once on
// the socket, and the screen lands on the installed control plane.
func TestInstallHeadscaleTakesEffectWithoutARestart(t *testing.T) {
	shortSettle(t)
	hs := headscale.NewFake()
	hs.SetAbsent()
	a := newApp(tailscale.NewFake(), hs, theme.New(), nil)
	a.width, a.height = 140, 40
	a.Update(a.load()())
	a.setScreen(screenUsers)
	if a.hsState.Present || !strings.Contains(a.View(), "headscale is not installed") {
		t.Fatal("the fake should start without headscale")
	}
	press(t, a, "i")
	if a.mode != modeConfirm {
		t.Fatalf("i did not open the install (status %q)", a.status)
	}
	press(t, a, "y")
	if !a.hsState.Present || a.hsState.NotRunning || len(a.hsState.Users) != 2 {
		t.Errorf("after the install and the re-read: present %v, notRunning %v, error %q, users %d",
			a.hsState.Present, a.hsState.NotRunning, a.hsState.Error, len(a.hsState.Users))
	}
	if view := a.View(); strings.Contains(view, "not installed") ||
		strings.Contains(view, "permission denied") {
		t.Errorf("the screen still says the old state:\n%s", view)
	}
}

// TestSocketRefusedAfterARestartIsNotRunningYet: a refused socket right after
// a change reads "not running yet"; a reload by hand shows the real answer.
func TestSocketRefusedAfterARestartIsNotRunningYet(t *testing.T) {
	a := newCPApp(t)
	a.hsState.Present = true
	a.state.DaemonRunning = true
	a.settling = true
	a.hsState.Error = "connecting to headscale: connect: permission denied"
	a.hsState.NotRunning = false
	if a.settleRead() == nil {
		t.Fatal("no re-read scheduled")
	}
	if a.hsState.Error != "headscale is not running yet · r re-reads" || !a.hsState.NotRunning {
		t.Errorf("error = %q", a.hsState.Error)
	}
	// The retry is spent: a second failure keeps the sentence and stops.
	a.hsState.Error = "connecting to headscale: connect: permission denied"
	a.hsState.NotRunning = false
	if a.settleRead() != nil || a.settling {
		t.Error("a second automatic re-read was scheduled")
	}
	press(t, a, "r")
	if a.settling {
		t.Error("r by hand still settles")
	}
}
