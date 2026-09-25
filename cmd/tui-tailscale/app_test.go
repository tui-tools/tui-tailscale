package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// newTestApp builds the app on the demo backend and loads it synchronously.
func newTestApp(t *testing.T) (*app, *tailscale.Fake) {
	t.Helper()
	fake := tailscale.NewFake()
	a := newApp(fake, headscale.NewFake(), theme.New(), nil)
	a.width, a.height = 120, 40
	a.Update(a.load()())
	return a, fake
}

// press sends one key and runs whatever command it returned, the way the
// Bubble Tea loop would, until the model settles.
func press(t *testing.T, a *app, key string) {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	settle(a, msg)
}

// typeText types a string into an open input, replacing what it held (the
// join form prefills the current login server).
func typeText(a *app, text string) {
	a.input.Model.SetValue("")
	for _, r := range text {
		settle(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// settle feeds a message and every message its commands produce back into
// the model. Batched commands are unrolled; a quit ends it.
func settle(a *app, msg tea.Msg) {
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		_, cmd := a.Update(next)
		if a.mode == modeInput {
			// An open text input only ever returns its cursor blink, a
			// command that sleeps; nothing the app acts on comes from it.
			continue
		}
		queue = append(queue, drain(cmd)...)
	}
}

// drain runs a command and returns the messages it produced.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch m := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range m {
			out = append(out, drain(c)...)
		}
		return out
	case tea.QuitMsg:
		return nil
	}
	if _, blink := msg.(interface{ Tag() int }); blink {
		return nil
	}
	return []tea.Msg{msg}
}

func TestNothingRunsWithoutConfirmation(t *testing.T) {
	a, fake := newTestApp(t)
	for _, key := range []string{"a", "E", "d", "L"} {
		press(t, a, key)
		if a.mode != modeConfirm {
			t.Fatalf("%s: mode = %v, want the confirm dialog", key, a.mode)
		}
		press(t, a, "n")
		if a.mode != modeBrowse {
			t.Fatalf("%s: the dialog did not close", key)
		}
	}
	if n := len(fake.Commands()); n != 0 {
		t.Errorf("%d commands ran without a confirmation", n)
	}
}

func TestConfirmedPreviewIsWhatRuns(t *testing.T) {
	a, fake := newTestApp(t)
	press(t, a, "a")
	preview := a.confirm.Command
	if preview != "sudo -n tailscale set --accept-routes=false" {
		t.Fatalf("preview = %q", preview)
	}
	press(t, a, "y")
	ran := fake.Commands()
	if len(ran) != 1 || fake.Preview(ran[0]) != preview {
		t.Fatalf("ran %q, the preview promised %q", ran, preview)
	}
	if a.state.Prefs.RouteAll {
		t.Error("the state was not re-read after the change")
	}
}

// The join without a key: six answers, one dialog previewing the whole plan,
// and after it the login URL in a notice and in the status line.
func TestBrowserJoinShowsTheLoginURL(t *testing.T) {
	a, fake := newTestApp(t)
	press(t, a, "L")
	press(t, a, "y")
	if a.state.Node.BackendState != tailscale.StateNeedsLogin {
		t.Fatalf("state after logout = %q", a.state.Node.BackendState)
	}
	ranBefore := len(fake.Commands())

	press(t, a, "j")
	if a.inputPurpose != inputJoinServer {
		t.Fatalf("j opened %v", a.inputPurpose)
	}
	typeText(a, "https://headscale.example.com")
	press(t, a, "enter") // server
	press(t, a, "enter") // no key
	press(t, a, "enter") // no hostname
	press(t, a, "enter") // accept routes: current (yes)
	press(t, a, "enter") // no routes
	press(t, a, "enter") // exit node: current (no)
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the confirm", a.mode)
	}
	// A logged-out node has no name to prefill, so no --hostname: the machine's
	// own name is used.
	want := "sudo -n tailscale up --login-server=https://headscale.example.com " +
		"--accept-routes --timeout=20s --reset"
	if a.confirm.Command != want {
		t.Errorf("preview =\n  %s\nwant\n  %s", a.confirm.Command, want)
	}
	press(t, a, "y")
	if len(fake.Commands()) != ranBefore+1 {
		t.Errorf("the join ran %d commands, want 1", len(fake.Commands())-ranBefore)
	}
	url := tailscale.DemoLoginServer + tailscale.DemoRegisterPath
	if a.mode != modeNotice || a.notice.copyable != url {
		t.Errorf("mode = %v, notice = %q: want the login URL", a.mode, a.notice.body)
	}
	if !strings.Contains(a.status, url) {
		t.Errorf("status = %q, want the login URL", a.status)
	}
	if !strings.Contains(a.View(), url) {
		t.Error("the notice does not show the URL")
	}
	press(t, a, "x")
	if a.mode != modeBrowse || !strings.Contains(a.View(), url) {
		t.Error("the node screen should keep the pending login URL")
	}
}

// The join with a key: the key is typed masked, never shown, and the preview
// names the file, not the value.
func TestKeyJoinKeepsTheKeyOffScreen(t *testing.T) {
	const key = "example-preauth-key-000000"
	a, fake := newTestApp(t)
	press(t, a, "j")
	typeText(a, "https://headscale.example.com")
	press(t, a, "enter")
	typeText(a, key)
	if strings.Contains(a.View(), key) {
		t.Fatal("the key is echoed while it is typed")
	}
	press(t, a, "enter")
	press(t, a, "enter")
	press(t, a, "enter")
	typeText(a, "192.0.2.0/24")
	press(t, a, "enter")
	press(t, a, "enter")
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the confirm", a.mode)
	}
	if strings.Contains(a.confirm.Command, key) || strings.Contains(a.View(), key) {
		t.Fatal("the key is in the confirm dialog")
	}
	if a.join.key != "" {
		t.Error("the draft still holds the key once the plan is built")
	}
	for _, want := range []string{"install -m 600 /dev/stdin /run/tui-tailscale.authkey",
		"--authkey=file:/run/tui-tailscale.authkey", "sysctl -w net.ipv4.ip_forward=1",
		"rm -f /run/tui-tailscale.authkey"} {
		if !strings.Contains(a.confirm.Command, want) {
			t.Errorf("the preview is missing %q:\n%s", want, a.confirm.Command)
		}
	}
	press(t, a, "y")
	ran := fake.Commands()
	if len(ran) != 5 || ran[0].Stdin != key || ran[4].Argv[0] != "rm" {
		t.Errorf("ran %q", ran)
	}
	if a.state.Node.BackendState != tailscale.StateRunning {
		t.Errorf("state = %q", a.state.Node.BackendState)
	}
}

func TestCancellingTheJoinForgetsTheKey(t *testing.T) {
	a, fake := newTestApp(t)
	press(t, a, "j")
	typeText(a, "https://headscale.example.com")
	press(t, a, "enter")
	typeText(a, "0123456789abcdef")
	press(t, a, "enter")
	press(t, a, "esc")
	if a.join.key != "" {
		t.Error("a cancelled form kept the key")
	}
	if len(fake.Commands()) != 0 {
		t.Error("a cancelled form ran something")
	}
}

func TestJoinRejectsABadServerAndAsksAgain(t *testing.T) {
	a, _ := newTestApp(t)
	press(t, a, "j")
	typeText(a, "headscale.example.com")
	press(t, a, "enter")
	if a.mode != modeInput || a.inputPurpose != inputJoinServer {
		t.Fatalf("mode = %v / %v, want the server asked again", a.mode, a.inputPurpose)
	}
	if !strings.Contains(a.input.Help, "not an http") {
		t.Errorf("help = %q, want the problem", a.input.Help)
	}
}

func TestExitNodePicker(t *testing.T) {
	a, fake := newTestApp(t)
	press(t, a, "x")
	if a.mode != modePicker {
		t.Fatalf("mode = %v", a.mode)
	}
	if got := strings.Join(a.picker.Options, "|"); got != noExitNode+"|exit-gateway  100.64.0.2" {
		t.Errorf("options = %q: only the peer offering an exit node, and none", got)
	}
	press(t, a, "down")
	press(t, a, "enter")
	if a.confirm.Command != "sudo -n tailscale set --exit-node=100.64.0.2" {
		t.Fatalf("preview = %q", a.confirm.Command)
	}
	press(t, a, "y")
	if len(fake.Commands()) != 1 {
		t.Fatal("the exit node was not set")
	}
	if p, ok := a.state.CurrentExitNode(); !ok || p.Name() != "exit-gateway" {
		t.Errorf("exit node = %+v %v", p, ok)
	}
}

func TestAdvertiseRoutesAddsForwarding(t *testing.T) {
	a, _ := newTestApp(t)
	press(t, a, "A")
	typeText(a, "192.0.2.0/24")
	press(t, a, "enter")
	want := "sudo -n install -m 644 /dev/stdin /etc/sysctl.d/99-tailscale.conf\n" +
		"$ sudo -n sysctl -w net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1\n" +
		"$ sudo -n tailscale set --advertise-routes=192.0.2.0/24"
	if a.confirm.Command != want {
		t.Errorf("preview =\n%s\nwant\n%s", a.confirm.Command, want)
	}
}

func TestDownThenUp(t *testing.T) {
	a, _ := newTestApp(t)
	press(t, a, "u")
	if a.mode != modeBrowse || !strings.Contains(a.status, "already up") {
		t.Errorf("u on a running node: mode %v, status %q", a.mode, a.status)
	}
	press(t, a, "d")
	if !a.confirm.Danger {
		t.Error("down is destructive: it can end a session over the tailnet")
	}
	press(t, a, "y")
	press(t, a, "u")
	press(t, a, "y")
	if a.state.Node.BackendState != tailscale.StateRunning {
		t.Errorf("state = %q", a.state.Node.BackendState)
	}
}

// An absent client: the node screen says how to install it, and i previews
// exactly those commands.
func TestNotInstalled(t *testing.T) {
	a := newApp(notInstalled{tailscale.NewFake()}, headscale.NewFake(), theme.New(),
		[]compat.Result{})
	a.width, a.height = 120, 40
	a.Update(a.load()())
	view := a.View()
	for _, want := range []string{"not installed", "apt-get install -y tailscale",
		"pkgs.tailscale.com/stable/ubuntu/noble"} {
		if !strings.Contains(view, want) {
			t.Errorf("the node screen is missing %q", want)
		}
	}
	press(t, a, "j")
	if a.mode != modeBrowse || !strings.Contains(a.status, "i installs it") {
		t.Errorf("j without tailscale: mode %v, status %q", a.mode, a.status)
	}
	press(t, a, "i")
	if a.mode != modeConfirm || !strings.Contains(a.confirm.Command, "apt-get install -y tailscale") {
		t.Errorf("i: mode %v, preview %q", a.mode, a.confirm.Command)
	}
}

// notInstalled is a backend on a machine without tailscale.
type notInstalled struct{ *tailscale.Fake }

func (notInstalled) Load(context.Context) (tailscale.State, error) {
	return tailscale.State{Distro: tailscale.ParseDistro(
		"ID=ubuntu\nID_LIKE=debian\nVERSION_ID=24.04\nVERSION_CODENAME=noble\n" +
			"PRETTY_NAME=\"Ubuntu 24.04 LTS\"\n")}, nil
}

func (notInstalled) Describe() string { return "tailscale is not installed" }

func TestViewsRenderAtEveryWidth(t *testing.T) {
	a, _ := newTestApp(t)
	for _, width := range []int{40, 80, 120} {
		a.width = width
		for _, screen := range []string{"1", "2", "3", "4", "5", "6"} {
			press(t, a, screen)
			if out := a.View(); out == "" {
				t.Errorf("empty view at %d columns", width)
			}
		}
	}
	press(t, a, "?")
	if !strings.Contains(a.View(), "join a tailnet") {
		t.Error("the help screen is not generated from the action table")
	}
}

// joinedWithoutKey drives the demo to a pending browser login and returns the
// app with the notice open, and the login URL.
func joinedWithoutKey(t *testing.T) (*app, string) {
	t.Helper()
	a, _ := newTestApp(t)
	press(t, a, "L")
	press(t, a, "y")
	press(t, a, "j")
	for i := 0; i < 6; i++ {
		press(t, a, "enter")
	}
	press(t, a, "y")
	if a.mode != modeNotice {
		t.Fatalf("mode = %v, want the login notice", a.mode)
	}
	return a, tailscale.DemoLoginServer + tailscale.DemoRegisterPath
}

// plainLines renders the view and returns its lines without styling.
func plainLines(a *app) []string {
	return strings.Split(ansi.Strip(a.View()), "\n")
}

// frameRunes are the characters a dialog border is drawn with; a line that
// carries one of them next to the URL would copy it along.
const frameRunes = "│─╭╮╰╯┃━"

// The URL of issue #3: it sits on a line of its own, whole, flush left and
// outside the frame, at 120 and 60 columns. At 40 the demo URL no longer fits:
// the line is still unframed (the terminal cuts it), and the notice says so
// and points at --check.
func TestLoginNoticeKeepsTheURLCopyable(t *testing.T) {
	for _, width := range []int{120, 60, 40} {
		a, url := joinedWithoutKey(t)
		a.width, a.height = width, 30
		var found string
		for _, line := range plainLines(a) {
			if strings.Contains(line, "/register/") {
				found = line
			}
		}
		if found != url {
			t.Errorf("%d columns: the URL line is %q, want exactly %q (one line, no "+
				"indent, no frame)", width, found, url)
		}
		if strings.ContainsAny(found, frameRunes) {
			t.Errorf("%d columns: the URL line carries a frame: %q", width, found)
		}
		// Read the prose the way a person does: without the frame.
		prose := strings.Map(func(r rune) rune {
			if strings.ContainsRune(frameRunes, r) {
				return ' '
			}
			return r
		}, ansi.Strip(a.View()))
		view := strings.Join(strings.Fields(prose), " ")
		cut := strings.Contains(view, "narrower than the URL")
		if fits := len(url) <= width; cut == fits {
			t.Errorf("%d columns (URL %d): the notice says cut = %v", width, len(url), cut)
		}
	}
}

// The status line and the node screen carry the URL alone on their line too.
func TestPendingLoginURLOnTheStatusLineAndNodeScreen(t *testing.T) {
	a, url := joinedWithoutKey(t)
	if a.status != url {
		t.Errorf("status = %q, want the URL alone", a.status)
	}
	press(t, a, "x") // close the notice
	a.width = 120
	found := false
	for _, line := range plainLines(a) {
		if line == url {
			found = true
		}
	}
	if !found {
		t.Error("the node screen should show the URL on a line of its own")
	}
}
