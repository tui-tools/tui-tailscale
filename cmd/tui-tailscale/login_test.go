package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// TestMain makes the pending-login re-read immediate for every test: the
// settle helper runs each scheduled tick, and three seconds per tick would
// make a pending login cost minutes.
func TestMain(m *testing.M) {
	loginPollDelay = time.Millisecond
	os.Exit(m.Run())
}

// Issue #10: once the browser login went through, the status line no longer
// carries the dead register URL but says who joined, and the notice that
// asked to open the URL closes.
func TestCompletedLoginReplacesTheURL(t *testing.T) {
	a, url := joinedWithoutKey(t)
	if a.status != url {
		t.Fatalf("status = %q, want the URL", a.status)
	}
	fake := a.backend.(*tailscale.Fake)
	fake.CompleteLogin()
	settle(a, loginPollMsg{})
	if strings.Contains(a.status, "/register/") {
		t.Errorf("status still carries the URL: %q", a.status)
	}
	if !strings.HasPrefix(a.status, "joined headscale.example.com as user@example.com") {
		t.Errorf("status = %q, want who joined", a.status)
	}
	if a.mode == modeNotice {
		t.Errorf("the login notice should close once the login completed")
	}
	// The browser join's answers are offered as a join profile now.
	if a.mode != modeInput || a.inputPurpose != inputSaveProfile {
		t.Errorf("mode = %v / %v, want the offer to save a join profile", a.mode, a.inputPurpose)
	}
	if a.state.Node.AuthURL != "" || a.state.Node.BackendState != tailscale.StateRunning {
		t.Errorf("node = %+v", a.state.Node)
	}
}

// A pending login that disappears without a login says so instead of keeping
// the URL.
func TestExpiredLoginIsSaid(t *testing.T) {
	a, _ := joinedWithoutKey(t)
	a.backend.(*tailscale.Fake).ExpireLogin()
	press(t, a, "x") // close the notice
	settle(a, loginPollMsg{})
	if !strings.Contains(a.status, "expired") {
		t.Errorf("status = %q, want the expiry said", a.status)
	}
}

// The re-read while a login is pending is automatic, and bounded: the demo
// completes its login after a few reads without anyone pressing r.
func TestPendingLoginIsReReadUntilItFlips(t *testing.T) {
	a, fake := newTestApp(t)
	fake.CompleteLoginAfter(3)
	press(t, a, "L")
	press(t, a, "y")
	press(t, a, "j")
	for i := 0; i < 6; i++ {
		press(t, a, "enter")
	}
	press(t, a, "y")
	if a.state.Node.BackendState != tailscale.StateRunning {
		t.Fatalf("the pending login was not followed to the end: %+v", a.state.Node)
	}
	if !strings.HasPrefix(a.status, "joined") {
		t.Errorf("status = %q", a.status)
	}
}

// A login nobody completes stops being polled after loginPollLimit reads.
func TestPendingLoginPollIsBounded(t *testing.T) {
	a, _ := joinedWithoutKey(t)
	if a.loginPolling {
		t.Error("a re-read is still scheduled after the bound")
	}
	if a.loginPolls != loginPollLimit {
		t.Errorf("polls = %d, want %d", a.loginPolls, loginPollLimit)
	}
	if a.status == "" || !strings.Contains(a.status, "/register/") {
		t.Errorf("status = %q: the URL stays while the login is pending", a.status)
	}
}

// Issue #20: the browser confirmed, the URL is gone, and tailscaled still
// reads NeedsLogin, then NoState and Starting before it runs. None of that is
// an expired login: the node screen waits, then says who joined.
func TestConfirmedLoginIsWaitedForThroughStarting(t *testing.T) {
	a, _ := joinedWithoutKey(t)
	fake := a.backend.(*tailscale.Fake)
	fake.SetConfirmPhases(tailscale.StateNeedsLogin, tailscale.StateNoState,
		tailscale.StateStarting, tailscale.StateStarting)
	fake.ConfirmLogin()

	// One poll at a time: every intermediate line is a wait, never "expired".
	seen := []string{}
	for i := 0; i < 10 && a.state.Node.BackendState != tailscale.StateRunning; i++ {
		_, load := a.Update(loginPollMsg{})
		if load == nil {
			t.Fatalf("poll %d scheduled no read (status %q)", i, a.status)
		}
		a.Update(load())
		seen = append(seen, a.status)
		if strings.Contains(a.status, "expired") {
			t.Fatalf("a confirmed login was called expired after %v", seen)
		}
	}
	if len(seen) < 2 || !strings.Contains(seen[0], "waiting for the node to come up") {
		t.Errorf("statuses = %q, want a wait first", seen)
	}
	if !strings.HasPrefix(a.status, "joined headscale.example.com as user@example.com") {
		t.Errorf("status = %q, want who joined (after %q)", a.status, seen)
	}
	if a.loginWaiting || a.loginPolling {
		t.Error("the wait is over once the node runs")
	}
}

// The same sequence driven by the loop, the way --demo runs it: the login
// completes by itself through Starting and ends on who joined.
func TestConfirmedLoginSettlesOnJoined(t *testing.T) {
	a, fake := newTestApp(t)
	fake.CompleteLoginAfter(2)
	fake.SetConfirmPhases(tailscale.StateNeedsLogin, tailscale.StateNoState,
		tailscale.StateStarting)
	press(t, a, "L")
	press(t, a, "y")
	press(t, a, "j")
	for i := 0; i < 6; i++ {
		press(t, a, "enter")
	}
	press(t, a, "y")
	if !strings.HasPrefix(a.status, "joined") {
		t.Errorf("status = %q, want who joined", a.status)
	}
}

// A node that leaves NeedsLogin but never reaches Running is waited for up
// to loginWaitLimit reads, then the line says where it stuck — not "expired".
func TestLoginStuckStartingGivesUp(t *testing.T) {
	a, _ := joinedWithoutKey(t)
	fake := a.backend.(*tailscale.Fake)
	phases := []string{}
	for i := 0; i < loginWaitLimit+5; i++ {
		phases = append(phases, tailscale.StateStarting)
	}
	fake.SetConfirmPhases(phases...)
	fake.ConfirmLogin()
	settle(a, loginPollMsg{})
	if !strings.Contains(a.status, "still starting") || strings.Contains(a.status, "expired") {
		t.Errorf("status = %q", a.status)
	}
	if a.loginPolling {
		t.Error("the wait is bounded")
	}
}

// Issue #20, second half: an "expired" line that a later read contradicts —
// r finds the node running after all — is replaced by who joined.
func TestReloadReplacesAStaleExpiredLine(t *testing.T) {
	a, _ := newTestApp(t)
	a.setStatus(ui.StatusWarn, loginExpiredLine)
	press(t, a, "r")
	if !strings.HasPrefix(a.status, "joined headscale.example.com as user@example.com") {
		t.Errorf("status = %q, want who joined", a.status)
	}
}

// loginVerdict: NeedsLogin gets a grace period, a node on its way up the
// longer wait.
func TestLoginVerdict(t *testing.T) {
	cases := []struct {
		state string
		polls int
		want  int
	}{
		{tailscale.StateNeedsLogin, 0, loginStillWaiting},
		{tailscale.StateNeedsLogin, loginGracePolls - 1, loginStillWaiting},
		{tailscale.StateNeedsLogin, loginGracePolls, loginExpired},
		{tailscale.StateNoState, loginGracePolls, loginExpired},
		{tailscale.StateStarting, loginGracePolls, loginStillWaiting},
		{tailscale.StateStarting, loginWaitLimit, loginGaveUp},
		{tailscale.StateNeedsMachineAuth, loginGracePolls + 1, loginStillWaiting},
	}
	for _, c := range cases {
		if got := loginVerdict(c.state, c.polls); got != c.want {
			t.Errorf("loginVerdict(%s, %d) = %d, want %d", c.state, c.polls, got, c.want)
		}
	}
}

// Issue #20, third half: on a machine that is only a node (no headscale here,
// joined to a control plane elsewhere) the control-plane tabs are dimmed and
// say where the control plane is.
func TestNodeOnlyMachineDimsTheControlPlane(t *testing.T) {
	a := newApp(tailscale.NewFake(), noHeadscale{headscale.NewFake()}, theme.New(), nil)
	a.width, a.height = 160, 40
	a.Update(a.load()())
	if !strings.Contains(ansi.Strip(a.tabsView()), "headscale (not on this machine):") {
		t.Errorf("tabs = %q", ansi.Strip(a.tabsView()))
	}
	press(t, a, "3")
	view := ansi.Strip(a.View())
	if !strings.Contains(view, "this machine is a node; the control plane is headscale.example.com") {
		t.Errorf("the users screen does not say where the control plane is:\n%s", view)
	}
	// A machine with headscale keeps the plain tabs.
	b, _ := newTestApp(t)
	if strings.Contains(ansi.Strip(b.tabsView()), "not on this machine") {
		t.Errorf("tabs = %q", ansi.Strip(b.tabsView()))
	}
}
