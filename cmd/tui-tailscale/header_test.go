package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// TestHeaderNeverWraps: the header is two rows at any width, each cut at the
// width with a trailing ellipsis rather than wrapped, so the title row stays
// on screen (issue #31). The facts are the ones from the issue: a node of a
// control plane on another machine, whose second row is 136 columns.
func TestHeaderNeverWraps(t *testing.T) {
	a, _ := newTestApp(t)
	a.loading = false
	// A node only: headscale is not on this machine.
	a.hsState = headscale.State{}
	a.backendCompat = compat.Result{Backend: backendName, Version: "1.102.4"}
	a.state.Prefs.ControlURL = "https://vpn.lab.internal"
	if a.remoteControlPlane() == "" {
		t.Fatal("the fixture is not a node of a remote control plane")
	}
	full := ansi.Strip(strings.Split(a.fitHeaderAt(1000), "\n")[1])
	if !strings.Contains(full, "control: headscale: not installed (node only)") {
		t.Fatalf("fixture header = %q", full)
	}
	for _, width := range []int{80, 100, 120} {
		for _, s := range []screen{screenNode, screenPeers, screenUsers} {
			a.width, a.height = width, 34
			a.setScreen(s)
			header := ansi.Strip(a.header())
			rows := strings.Split(header, "\n")
			if len(rows) != headerLines {
				t.Errorf("%d cols, screen %d: header has %d rows:\n%s", width, s, len(rows), header)
				continue
			}
			for _, r := range rows {
				if lipgloss.Width(r) > width {
					t.Errorf("%d cols: row wider than the terminal: %q", width, r)
				}
			}
			if !strings.Contains(rows[0], "tui-tailscale") {
				t.Errorf("%d cols: no title row: %q", width, rows[0])
			}
			if lipgloss.Width(full)+2 > width && !strings.HasSuffix(strings.TrimRight(rows[1], " "), "…") {
				t.Errorf("%d cols: a cut row carries no marker: %q", width, rows[1])
			}
			view := strings.Split(ansi.Strip(a.View()), "\n")
			if len(view) > a.height || !strings.Contains(view[0], "tui-tailscale") {
				t.Errorf("%d cols, screen %d: %d rows, first %q", width, s, len(view), view[0])
			}
		}
	}
}

// fitHeaderAt renders the header at another width, leaving the app's alone.
func (a *app) fitHeaderAt(width int) string {
	saved := a.width
	defer func() { a.width = saved }()
	a.width = width
	return a.header()
}
