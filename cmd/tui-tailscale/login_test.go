package main

import (
	"os"
	"strings"
	"testing"
	"time"

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
	if a.mode != modeBrowse {
		t.Errorf("mode = %v: the login notice should close once the login completed", a.mode)
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
