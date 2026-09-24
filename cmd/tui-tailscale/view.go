package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// Layout constants: the rows the body cannot use (tab bar, table header, help
// bar, status line) besides the header.
const (
	headerLines   = 2
	chromeLines   = 4
	minListHeight = 1
)

// listHeight is the number of body rows that fit on screen.
func (a *app) listHeight() int {
	return max(a.height-headerLines-chromeLines, minListHeight)
}

// View renders the whole screen.
func (a *app) View() string {
	switch a.mode {
	case modeConfirm:
		return a.confirm.View(a.theme, a.width, a.height)
	case modeInput:
		return a.input.View(a.theme, a.width, a.height)
	case modePicker:
		return a.picker.View(a.theme, a.width, a.height)
	case modeNotice:
		return a.noticeView()
	case modeHelp:
		return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center,
			ui.HelpScreen(a.theme, "tui-tailscale — keys", helpKeys(), a.width))
	default:
		return a.browseView()
	}
}

// browseView renders the tabbed main screen: header, tabs, the body, help bar
// and status line — the bands every tool in the family draws.
func (a *app) browseView() string {
	var body string
	height := a.listHeight() + 1
	switch {
	case a.loading && !a.state.Installed && !a.state.DaemonRunning && a.state.Distro.ID == "":
		body = ui.EmptyState(a.theme, "reading…", a.width, height)
	case a.loadFailed:
		body = ui.EmptyState(a.theme, "could not read — see the message below", a.width, height)
	case a.screen == tailscale.ScreenNode:
		body = a.panel(a.nodeLines(), height)
	case len(a.state.Peers) == 0:
		body = ui.EmptyState(a.theme, a.emptyPeersMessage(), a.width, height)
	default:
		body = a.peersTable()
	}
	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status, a.defaultStatus(), a.width)
	return strings.Join([]string{a.header(), a.tabsView(), body, help, status}, "\n")
}

// panel renders lines of facts into exactly height rows, cut at the width.
func (a *app) panel(lines []string, height int) string {
	out := make([]string, 0, height)
	for i := 0; i < height; i++ {
		if i < len(lines) {
			out = append(out, ui.Truncate(lines[i], a.width))
			continue
		}
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// fact renders one "label  value" line of the node panel, the value styled.
func (a *app) fact(label, value string, style lipgloss.Style) string {
	return "  " + a.theme.Muted.Render(fmt.Sprintf("%-18s", label)) + style.Render(value)
}

// factHint is a fact with the key that changes it, muted, after it.
func (a *app) factHint(label, value string, style lipgloss.Style, hint string) string {
	return a.fact(label, value, style) + a.theme.Muted.Render("   "+hint)
}

// nodeLines is the node screen: the state of the client and, when it
// answers, this node on the tailnet and the settings that decide what it
// routes. Each missing piece is said in words, with what to do about it.
func (a *app) nodeLines() []string {
	s := a.state
	t := a.theme
	if !s.Installed {
		return a.installLines()
	}
	if !s.DaemonRunning {
		lines := []string{"", a.fact("client", "installed", t.Base)}
		if a.backendCompat.Version != "" {
			lines = append(lines, a.fact("version", a.backendCompat.Version, t.Base))
		}
		return append(lines, "", "  "+t.Warn.Render(orDash(s.Error)))
	}

	n, p := s.Node, s.Prefs
	lines := []string{"", a.fact("state", stateLine(n), a.stateStyle(n.BackendState))}
	if n.AuthURL != "" {
		lines = append(lines,
			a.fact("login pending", n.AuthURL, t.Warn),
			"  "+t.Muted.Render(strings.Repeat(" ", 18)+
				"open it in a browser to finish the login; r re-reads"))
	}
	server := p.ControlURL
	kind := "self-hosted"
	if tailscale.IsTailscaleControl(server) {
		kind = "Tailscale"
		if server == "" {
			server = tailscale.DefaultControlURL
		}
	}
	if s.PrefsRead {
		lines = append(lines, a.fact("login server", server+"  ("+kind+")", t.Base))
	}
	if n.User != "" {
		lines = append(lines, a.fact("user", n.User, t.Base))
	}
	if n.HostName != "" || n.DNSName != "" {
		name := n.HostName
		if n.DNSName != "" {
			name += "  (" + n.DNSName + ")"
		}
		lines = append(lines, a.fact("hostname", name, t.Base))
	}
	if len(n.IPs) > 0 {
		lines = append(lines, a.fact("addresses", strings.Join(n.IPs, "  "), t.Base))
	}
	if n.TailnetName != "" {
		lines = append(lines, a.fact("tailnet", n.TailnetName, t.Base))
	}
	if n.MagicDNSSuffix != "" || n.MagicDNS {
		lines = append(lines, a.fact("MagicDNS", onOff(n.MagicDNS)+suffix(n.MagicDNSSuffix), t.Base))
	}

	lines = append(lines, "")
	if !s.PrefsRead {
		lines = append(lines, "  "+t.Warn.Render("settings could not be read: "+orDash(s.PrefsError)))
	} else {
		lines = append(lines,
			a.factHint("accept routes", yesNo(p.RouteAll), t.Base, "a toggles"),
			a.factHint("advertised routes", listOrDash(p.SubnetRoutes()), t.Base, "A edits"),
			a.factHint("exit node", a.exitNodeLine(), t.Base, "x picks"),
			a.factHint("offers exit node", yesNo(p.AdvertisesExitNode()), t.Base, "E toggles"),
			a.fact("accept DNS", yesNo(p.CorpDNS), t.Base),
		)
	}
	lines = append(lines, a.fact("version", orDash(n.Version), t.Base))
	for i, h := range n.Health {
		label := ""
		if i == 0 {
			label = "health"
		}
		lines = append(lines, a.fact(label, h, t.Warn))
	}
	if n.BackendState == tailscale.StateNeedsLogin && n.AuthURL == "" {
		lines = append(lines, "", "  "+t.Info.Render("this node is not logged in — j joins a tailnet"))
	}
	return lines
}

// installLines is the node screen when tailscale is absent: what to run, for
// this distribution, and the key that runs it after a preview.
func (a *app) installLines() []string {
	t := a.theme
	d := a.state.Distro
	name := d.String()
	if name == "" {
		name = "this machine"
	}
	lines := []string{"",
		"  " + t.Warn.Render("tailscale is not installed on "+name+"."),
		"",
	}
	if _, err := tailscale.BuildCommand(tailscale.Request{
		Action: tailscale.ActionInstall, Distro: d}); err != nil {
		return append(lines, "  "+t.Base.Render(err.Error()))
	}
	lines = append(lines, "  "+t.Base.Render("i installs it from the package manager, "+
		"previewed and confirmed — these commands:"), "")
	for _, cmd := range tailscale.InstallInstructions(d) {
		lines = append(lines, "    "+t.Command.Render("$ "+cmd))
	}
	return append(lines, "",
		"  "+t.Muted.Render("other distributions: "+tailscale.ManualInstallURL))
}

// exitNodeLine names the exit node in use.
func (a *app) exitNodeLine() string {
	if p, ok := a.state.CurrentExitNode(); ok {
		return p.Name() + " (" + p.IPv4() + ")"
	}
	if ip := a.state.Prefs.ExitNodeIP; ip != "" {
		return ip
	}
	if id := a.state.Prefs.ExitNodeID; id != "" {
		return "node " + id
	}
	return "none"
}

// stateLine is the backend state, with online or offline beside it.
func stateLine(n tailscale.Node) string {
	word := stateWord(n.BackendState)
	if n.BackendState == tailscale.StateRunning {
		if n.Online {
			return word + " · online"
		}
		return word + " · offline"
	}
	return word
}

// stateWord renders a backend state in words.
func stateWord(state string) string {
	switch state {
	case tailscale.StateRunning:
		return "running"
	case tailscale.StateStopped:
		return "stopped (down)"
	case tailscale.StateNeedsLogin:
		return "logged out"
	case tailscale.StateNeedsMachineAuth:
		return "waiting for approval"
	case tailscale.StateStarting:
		return "starting"
	case "", tailscale.StateNoState:
		return "no state"
	}
	return strings.ToLower(state)
}

// stateStyle colours the state line.
func (a *app) stateStyle(state string) lipgloss.Style {
	switch state {
	case tailscale.StateRunning:
		return a.theme.OK
	case tailscale.StateNeedsLogin, tailscale.StateNeedsMachineAuth:
		return a.theme.Warn
	case tailscale.StateStopped:
		return a.theme.Muted
	}
	return a.theme.Base
}

// tabsView renders the screens as one row, the current one accented.
func (a *app) tabsView() string {
	var parts []string
	for s := tailscale.Screen(0); s < tailscale.ScreenCount; s++ {
		label := " " + s.Title() + " "
		if s == tailscale.ScreenPeers && a.state.DaemonRunning {
			label = " peers (" + strconv.Itoa(len(a.state.Peers)) + ") "
		}
		if s == a.screen {
			parts = append(parts, a.theme.Accent.Render(label))
			continue
		}
		parts = append(parts, a.theme.Muted.Render(label))
	}
	return ui.Truncate(strings.Join(parts, a.theme.Muted.Render("│")), a.width)
}

// header renders the facts at the top of the screen.
func (a *app) header() string {
	s := a.state
	var facts []ui.Fact
	switch {
	case !s.Installed:
		facts = append(facts, ui.Fact{Label: "tailscale", Value: "not installed"})
	case !s.DaemonRunning:
		facts = append(facts, ui.Fact{Label: "tailscaled", Value: "not answering"})
	default:
		facts = append(facts, ui.Fact{Label: "state", Value: stateWord(s.Node.BackendState)})
		if host := tailscale.URLHost(s.Prefs.ControlURL); host != "" {
			facts = append(facts, ui.Fact{Label: "server", Value: host})
		}
		online := 0
		for _, p := range s.Peers {
			if p.Online {
				online++
			}
		}
		facts = append(facts, ui.Fact{Label: "peers",
			Value: strconv.Itoa(online) + "/" + strconv.Itoa(len(s.Peers)) + " online"})
	}
	if a.backendCompat.Backend != "" {
		facts = append(facts, ui.CompatFact(a.theme, a.backendCompat))
	}
	return ui.Header{Title: "tui-tailscale", Subtitle: a.backend.Describe(), Facts: facts}.
		Render(a.theme, a.width)
}

// defaultStatus is the hint shown when there is no message to report.
func (a *app) defaultStatus() string {
	if a.screen == tailscale.ScreenPeers {
		return strconv.Itoa(len(a.state.Peers)) + " peers  ·  ? for help"
	}
	return "every change is previewed and confirmed  ·  ? for help"
}

// emptyPeersMessage is what the peers screen says when it has no rows.
func (a *app) emptyPeersMessage() string {
	s := a.state
	switch {
	case !s.Installed:
		return "tailscale is not installed — see the node screen"
	case !s.DaemonRunning:
		return orDash(s.Error)
	case s.Node.BackendState == tailscale.StateNeedsLogin:
		return "not logged in — j joins a tailnet"
	}
	return "no other node on this tailnet yet"
}

// peersTable renders the peers, dropping columns on narrow terminals.
func (a *app) peersTable() string {
	wide, medium := a.width >= 100, a.width >= 64
	columns := []ui.Column{{Title: "NAME", Width: 16, Flex: true}}
	if wide {
		columns = append(columns, ui.Column{Title: "USER", Width: 18})
	}
	columns = append(columns, ui.Column{Title: "ADDRESS", Width: 15},
		ui.Column{Title: "STATE", Width: 8})
	if medium {
		columns = append(columns, ui.Column{Title: "OS", Width: 8},
			ui.Column{Title: "EXIT", Width: 7})
	}
	columns = append(columns, ui.Column{Title: "ROUTES", Width: 18, Flex: true})

	now := time.Now()
	rows := make([][]string, 0, len(a.state.Peers))
	styles := make([]*lipgloss.Style, 0, len(a.state.Peers))
	for _, p := range a.state.Peers {
		row := []string{p.Name()}
		if wide {
			row = append(row, orDash(p.User))
		}
		row = append(row, orDash(p.IPv4()), peerState(now, p))
		if medium {
			row = append(row, orDash(p.OS), exitText(p))
		}
		row = append(row, listOrDash(p.Routes))
		rows = append(rows, row)
		styles = append(styles, a.peerStyle(p))
	}
	return ui.Table{
		Columns: columns, Rows: rows, Styles: styles,
		Selected: a.cursor[a.screen], Offset: a.offset[a.screen], Height: a.listHeight(),
	}.Render(a.theme, a.width)
}

// peerState is online, or how long ago the peer was last seen.
func peerState(now time.Time, p tailscale.Peer) string {
	if p.Online {
		return "online"
	}
	if p.LastSeen.IsZero() || p.LastSeen.Year() < 2000 {
		return "offline"
	}
	return humanDuration(now.Sub(p.LastSeen))
}

// exitText says whether a peer offers an exit node, and whether it is the one
// in use.
func exitText(p tailscale.Peer) string {
	switch {
	case p.ExitNode:
		return "in use"
	case p.ExitNodeOption:
		return "offers"
	}
	return "-"
}

// peerStyle colours a peer row: the exit node in use accented, online OK,
// offline muted.
func (a *app) peerStyle(p tailscale.Peer) *lipgloss.Style {
	var s lipgloss.Style
	switch {
	case p.ExitNode:
		s = a.theme.Row.Foreground(a.theme.Accent.GetForeground())
	case p.Online:
		s = a.theme.Row.Foreground(a.theme.OK.GetForeground())
	default:
		s = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	}
	return &s
}

// noticeView renders the notice dialog: a title, a body, and the one key that
// closes it.
func (a *app) noticeView() string {
	t := a.theme
	inner := min(max(a.width-8, 24), 78)
	content := max(inner-t.Dialog.GetHorizontalFrameSize(), 20)
	lines := []string{}
	for _, l := range ui.Wrap(a.notice.title, content) {
		lines = append(lines, t.Title.Render(l))
	}
	lines = append(lines, "")
	for _, l := range ui.WrapBody(a.notice.body, content) {
		lines = append(lines, t.Base.Render(l))
	}
	lines = append(lines, "", t.Key.Render("any key")+" "+t.KeyDesc.Render("close"))
	return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center,
		t.Dialog.Width(inner).Render(strings.Join(lines, "\n")))
}

// shortHelpKeys is the single-line hint bar, generated from the action table.
func (a *app) shortHelpKeys() []ui.KeyHint {
	hints := []ui.KeyHint{{Key: "tab", Desc: "screen"}}
	if !a.state.Installed && !a.loading {
		hints = append(hints, ui.KeyHint{Key: "i", Desc: "install"})
	} else {
		for _, spec := range tailscale.Actions {
			if spec.Action == tailscale.ActionInstall {
				continue
			}
			hints = append(hints, ui.KeyHint{Key: spec.Key, Desc: spec.Label})
		}
	}
	return append(hints,
		ui.KeyHint{Key: "r", Desc: "reload"},
		ui.KeyHint{Key: "?", Desc: "help"},
		ui.KeyHint{Key: "q", Desc: "quit"},
	)
}

// helpKeys is the full key list. The action rows come from the action table,
// so a new action cannot be missing from the help.
func helpKeys() []ui.KeyHint {
	hints := []ui.KeyHint{
		{Key: "tab / 1…" + strconv.Itoa(int(tailscale.ScreenCount)), Desc: "switch screen (" + screenNames() + ")"},
		{Key: "↑ / ↓", Desc: "move the selection (j and h are actions here)"},
		{Key: "g / G", Desc: "first / last row"},
		{Key: "r", Desc: "reload"},
		{Key: "", Desc: ""},
	}
	for _, spec := range tailscale.Actions {
		hints = append(hints, ui.KeyHint{Key: spec.Key, Desc: spec.Help})
	}
	return append(hints,
		ui.KeyHint{Key: "", Desc: ""},
		ui.KeyHint{Key: "note", Desc: "every change is previewed and confirmed first"},
		ui.KeyHint{Key: "keys", Desc: "a pre-auth key is typed masked and never put on a"},
		ui.KeyHint{Key: "", Desc: "command line: it reaches tailscale through a root-only"},
		ui.KeyHint{Key: "", Desc: "file that is removed after the join"},
		ui.KeyHint{Key: "login", Desc: "without a key, the join shows a URL to open in a"},
		ui.KeyHint{Key: "", Desc: "browser: that is how an OIDC login happens"},
		ui.KeyHint{Key: "", Desc: ""},
		ui.KeyHint{Key: "?", Desc: "this help"},
		ui.KeyHint{Key: "q", Desc: "quit"},
	)
}

// screenNames lists the tabs, for the help screen.
func screenNames() string {
	names := make([]string, 0, tailscale.ScreenCount)
	for s := tailscale.Screen(0); s < tailscale.ScreenCount; s++ {
		names = append(names, s.Title())
	}
	return strings.Join(names, ", ")
}

// --- small formatters ---

// humanDuration renders a duration in one unit, as an age.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dmin ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// suffix renders a MagicDNS suffix in parentheses, when there is one.
func suffix(s string) string {
	if s == "" {
		return ""
	}
	return "  (" + s + ")"
}

// listOrDash renders a list compactly, or a dash when it is empty.
func listOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ", ")
}
