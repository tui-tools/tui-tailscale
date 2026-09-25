package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// createKey drives create on the keys screen through its confirm, at a given
// terminal width, and returns the app with the key's notice open.
func createKey(t *testing.T, width int) *app {
	t.Helper()
	a := newCPApp(t)
	a.width, a.height = width, 34
	a.setScreen(screenKeys)
	model, _ := a.Update(key("n"))
	a = model.(*app)
	// The input is prefilled with the first user's id; add the options.
	a = typeAndEnter(t, a, " reusable 7d")
	if a.mode != modeConfirm {
		t.Fatalf("no confirm opened (mode %d): %s", a.mode, a.status)
	}
	if !strings.Contains(a.confirm.Command, "headscale preauthkeys create --user 1 --reusable --expiration 7d") {
		t.Fatalf("preview = %q", a.confirm.Command)
	}
	a = confirmAndRun(t, a)
	if a.mode != modeNotice || !a.notice.secret {
		t.Fatalf("no key notice (mode %d): %q", a.mode, a.status)
	}
	return a
}

// TestPreAuthKeyIsShownOnceAndNotStored drives create on the keys screen and
// asserts the contract: the full key appears once, on the notice's own line,
// the status line never carries it, and the reloaded list keeps a prefix.
func TestPreAuthKeyIsShownOnceAndNotStored(t *testing.T) {
	a := createKey(t, 120)
	fullKey := a.notice.copyable
	if len(fullKey) != 88 {
		t.Fatalf("the demo key is not headscale 0.29-shaped: %q", fullKey)
	}
	if strings.Contains(a.status, fullKey) || !strings.Contains(a.status, "shown once") {
		t.Errorf("status = %q", a.status)
	}
	// At 120 columns the key is one line, flush left, whole (issue #30).
	if !hasLine(a.View(), fullKey) {
		t.Errorf("the key is not on a line of its own:\n%s", a.View())
	}
	state, _ := a.hs.Load(t.Context())
	for _, k := range state.PreAuthKeys {
		if k.KeyPrefix == fullKey {
			t.Error("the state stores the full key")
		}
	}
	// Any key closes the notice and forgets the key.
	model, _ := a.Update(key("q"))
	a = model.(*app)
	if a.mode != modeBrowse || a.notice.copyable != "" {
		t.Fatalf("the notice did not close and forget (mode %d)", a.mode)
	}
	// And the view renders prefixes, never the full key.
	a.hsState = state
	if strings.Contains(a.View(), fullKey) {
		t.Error("the keys table renders the full key")
	}
}

// hasLine reports whether view has a line that is exactly want.
func hasLine(view, want string) bool {
	for _, l := range strings.Split(view, "\n") {
		if l == want {
			return true
		}
	}
	return false
}

// TestPreAuthKeyNeverCut: in a terminal narrower than the key no part of it
// is drawn; the notice says how wide it has to be, the key appears whole once
// the window is widened, and w offers the root-only file instead (issue #30).
func TestPreAuthKeyNeverCut(t *testing.T) {
	a := createKey(t, 80)
	fullKey := a.notice.copyable
	view := a.View()
	if strings.Contains(view, fullKey[:20]) {
		t.Errorf("a part of the key is drawn at 80 columns:\n%s", view)
	}
	if !strings.Contains(view, "needs 88") || !strings.Contains(view, "w") {
		t.Errorf("the notice does not say why or what to do:\n%s", view)
	}
	model, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
	a = model.(*app)
	if !hasLine(a.View(), fullKey) {
		t.Errorf("widened to 100, the key is not shown whole:\n%s", a.View())
	}
}

// TestPreAuthKeyWrittenToFile: w previews the write to /run, the preview and
// the argv never carry the key, a cancel brings the notice back, and a
// confirmed write forgets it (issue #30).
func TestPreAuthKeyWrittenToFile(t *testing.T) {
	a := createKey(t, 80)
	fullKey := a.notice.copyable
	model, _ := a.Update(key("w"))
	a = model.(*app)
	if a.mode != modeConfirm || strings.Contains(a.confirm.Command, fullKey) ||
		strings.Contains(a.confirm.Body, fullKey) ||
		!strings.Contains(a.confirm.Command, "install -D -m 600 /dev/stdin /run/tui-tailscale/preauth-") {
		t.Fatalf("write preview (mode %d) = %q", a.mode, a.confirm.Command)
	}
	model, _ = a.Update(key("n"))
	a = model.(*app)
	if a.mode != modeNotice || a.notice.copyable != fullKey {
		t.Fatalf("a cancelled write lost the key (mode %d): %q", a.mode, a.status)
	}
	model, _ = a.Update(key("w"))
	a = model.(*app)
	a = confirmAndRun(t, a)
	if a.mode != modeBrowse || a.notice.copyable != "" || a.keyNotice.copyable != "" {
		t.Errorf("the key outlived its write (mode %d)", a.mode)
	}
	if !strings.Contains(a.status, "/run/tui-tailscale/preauth-") || strings.Contains(a.status, fullKey) {
		t.Errorf("status = %q", a.status)
	}
}

// TestDeleteNodeFlow: "x" on the control plane's nodes screen previews the
// forced delete — not the node screen's exit-node picker — and applying it
// removes the node.
func TestDeleteNodeFlow(t *testing.T) {
	a := newCPApp(t)
	a.setScreen(screenNodes)
	model, _ := a.Update(key("x"))
	a = model.(*app)
	if a.mode != modeConfirm {
		t.Fatalf("x did not open a confirm (mode %d)", a.mode)
	}
	if a.confirm.Command != "sudo -n headscale nodes delete --identifier 1 --force" {
		t.Errorf("preview = %q", a.confirm.Command)
	}
	if !a.confirm.Danger {
		t.Error("deleting a node should be a danger dialog")
	}
	a = confirmAndRun(t, a)
	state, _ := a.hs.Load(t.Context())
	for _, n := range state.Nodes {
		if n.ID == "1" {
			t.Error("node 1 survived the delete")
		}
	}
	ran := a.backend.(*tailscale.Fake).Commands()
	if len(ran) != 0 {
		t.Errorf("the node backend ran %q", ran)
	}
}

// TestRenameNodeFlow: "m" opens an input prefilled with the current name; the
// submitted name is previewed and applied.
func TestRenameNodeFlow(t *testing.T) {
	a := newCPApp(t)
	a.setScreen(screenNodes)
	model, _ := a.Update(key("m"))
	a = model.(*app)
	if a.mode != modeInput {
		t.Fatal("m did not open an input")
	}
	a = typeAndEnter(t, a, "-two") // appended to the prefilled "example-node"
	if a.mode != modeConfirm {
		t.Fatalf("no confirm opened: %s", a.status)
	}
	if !strings.Contains(a.confirm.Command, "headscale nodes rename --identifier 1 example-node-two") {
		t.Errorf("preview = %q", a.confirm.Command)
	}
	a = confirmAndRun(t, a)
	state, _ := a.hs.Load(t.Context())
	if state.Nodes[0].GivenName != "example-node-two" {
		t.Error("node 1 was not renamed")
	}
}

// TestCancellingAControlPlaneConfirmRunsNothing: a dialog dismissed runs no
// command at all, on either backend.
func TestCancellingAControlPlaneConfirmRunsNothing(t *testing.T) {
	a := newCPApp(t)
	a.setScreen(screenNodes)
	model, _ := a.Update(key("e"))
	a = model.(*app)
	if a.mode != modeConfirm || !a.confirm.Danger {
		t.Fatalf("e did not open a danger confirm (mode %d)", a.mode)
	}
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = model.(*app)
	if n := len(a.hs.(*headscale.Fake).Commands()); n != 0 {
		t.Errorf("cancelling still ran %d commands", n)
	}
}

// TestNodeKeysStayOnTheNodeScreens: the node's action keys do nothing on the
// control plane's screens, and the control plane's do nothing on the node's.
func TestNodeKeysStayOnTheNodeScreens(t *testing.T) {
	a := newCPApp(t)
	a.setScreen(screenUsers)
	for _, k := range []string{"a", "E", "d", "L", "h"} {
		model, _ := a.Update(key(k))
		a = model.(*app)
		if a.mode != modeBrowse {
			t.Errorf("%s on the users screen opened mode %d", k, a.mode)
			a.mode = modeBrowse
		}
	}
	a.setScreen(screenNode)
	for _, k := range []string{"S", "O", "F", "n", "e", "m"} {
		model, _ := a.Update(key(k))
		a = model.(*app)
		if a.mode != modeBrowse {
			t.Errorf("%s on the node screen opened mode %d", k, a.mode)
			a.mode = modeBrowse
		}
	}
}

// TestJoinOffersThisControlPlane: with headscale configured on this host, the
// join's first question is prefilled with its server_url.
func TestJoinOffersThisControlPlane(t *testing.T) {
	fake := headscale.NewFake()
	fake.SetConfig(strings.Replace(mustConfig(t, fake),
		"server_url: https://headscale.example.com",
		"server_url: https://vpn.example.org", 1))
	a := newApp(tailscale.NewFake(), fake, theme.New(), nil)
	a.width, a.height = 120, 40
	a.Update(a.load()())
	model, _ := a.Update(key("j"))
	a = model.(*app)
	if a.inputPurpose != inputJoinServer {
		t.Fatalf("j opened %v", a.inputPurpose)
	}
	if got := a.input.Model.Value(); got != "https://vpn.example.org" {
		t.Errorf("prefill = %q, want this host's server_url", got)
	}
	if !strings.Contains(a.input.Help, "This control plane") ||
		!strings.Contains(a.input.Help, "joined to https://headscale.example.com") {
		t.Errorf("help = %q", a.input.Help)
	}

	// Without a configured control plane the node's own login server stays.
	fake.SetConfig("server_url: http://127.0.0.1:8080\nlisten_addr: 127.0.0.1:8080\n")
	a.Update(a.load()())
	a.mode = modeBrowse
	model, _ = a.Update(key("j"))
	a = model.(*app)
	if got := a.input.Model.Value(); got != "https://headscale.example.com" {
		t.Errorf("prefill without a control plane = %q", got)
	}
}

// mustConfig reads back the fake's configuration.
func mustConfig(t *testing.T, fake *headscale.Fake) string {
	t.Helper()
	state, err := fake.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return state.ControlPlane.Raw
}

// TestReadinessLineOnEveryControlPlaneScreen: one line naming the next
// missing step sits on top of the users, nodes and keys screens.
func TestReadinessLineOnEveryControlPlaneScreen(t *testing.T) {
	a := newCPApp(t)
	a.width = 140
	for _, s := range []screen{screenUsers, screenNodes, screenKeys} {
		a.setScreen(s)
		if view := a.View(); !strings.Contains(view, "next step") ||
			!strings.Contains(view, "won't start at boot") {
			t.Errorf("screen %d has no readiness line:\n%s", s, view)
		}
	}
	a.hs.(*headscale.Fake).SetService("active", "enabled")
	a.Update(a.load()())
	a.setScreen(screenNodes)
	if view := a.View(); !strings.Contains(view, "routes pending approval (1)") {
		t.Errorf("with the unit enabled the routes are next:\n%s", view)
	}
	a.setScreen(screenNode)
	if strings.Contains(a.View(), "next step") {
		t.Error("the node screen carries the control plane's readiness line")
	}
}

// TestHeaderShowsBothBackends: the header names the client and headscale,
// each with its version.
func TestHeaderShowsBothBackends(t *testing.T) {
	a := newApp(tailscale.NewFake(), headscale.NewFake(), theme.New(), []compat.Result{
		{Backend: backendName, Version: "1.102.4", Status: compat.StatusTested},
		{Backend: backendHeadscale, Version: "0.29.3", Status: compat.StatusTested},
	})
	a.width, a.height = 160, 40
	a.Update(a.load()())
	header := a.header()
	for _, want := range []string{"tailscale 1.102.4", "headscale 0.29.3"} {
		if !strings.Contains(header, want) {
			t.Errorf("the header does not show %q:\n%s", want, header)
		}
	}
	b := newApp(tailscale.NewFake(), noHeadscale{headscale.NewFake()}, theme.New(), nil)
	b.width, b.height = 160, 40
	b.Update(b.load()())
	if header := b.header(); !strings.Contains(header, "headscale: not installed") {
		t.Errorf("an absent headscale is not named:\n%s", header)
	}
}

// TestInstallHeadscaleFromAControlPlaneScreen: without headscale the control
// plane's screens say how to install it, and i previews exactly those
// commands — the pinned repository first, then the package.
func TestInstallHeadscaleFromAControlPlaneScreen(t *testing.T) {
	a := newApp(tailscale.NewFake(), noHeadscale{headscale.NewFake()}, theme.New(), nil)
	a.width, a.height = 140, 40
	a.Update(a.load()())
	a.setScreen(screenUsers)
	view := a.View()
	for _, want := range []string{"headscale is not installed", "i installs it",
		"apt-get install -y headscale", "pkgs.tui.tools/pubkey.asc"} {
		if !strings.Contains(view, want) {
			t.Errorf("the users screen is missing %q:\n%s", want, view)
		}
	}
	model, _ := a.Update(key("n"))
	a = model.(*app)
	if a.mode != modeBrowse || !strings.Contains(a.status, "i installs it") {
		t.Errorf("n without headscale: mode %d, status %q", a.mode, a.status)
	}
	model, _ = a.Update(key("i"))
	a = model.(*app)
	if a.mode != modeConfirm {
		t.Fatalf("i did not open a confirm (status %q)", a.status)
	}
	for _, want := range []string{
		"sudo -n install -d -m 0755 /etc/apt/keyrings",
		"gpg --show-keys --with-colons /etc/apt/keyrings/tui-tools.asc",
		"sudo -n env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y headscale"} {
		if !strings.Contains(a.confirm.Command, want) {
			t.Errorf("the preview is missing %q:\n%s", want, a.confirm.Command)
		}
	}
	if !strings.Contains(a.confirm.Body, headscale.RepoFingerprint) {
		t.Error("the dialog does not name the pinned fingerprint")
	}
	a = confirmAndRun(t, a)
	if !strings.Contains(a.status, "headscale installed") {
		t.Errorf("status = %q", a.status)
	}
}
