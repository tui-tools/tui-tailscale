package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// This file renders the control-plane screens: users (under the panel that
// shows what /etc/headscale/config.yaml says), nodes and pre-auth keys, with
// the readiness line on top of all three.

// controlPlaneNote is the one-line explanation of how identity works, shown on
// every control-plane screen: there is no web admin to log into, because login
// is OIDC in the client's own browser against the IdP.
const controlPlaneNote = "identity: OIDC login in the client's browser against your IdP · the server has no web admin"

// cpBody renders the table of a control-plane screen, or what stands in for it.
func (a *app) cpBody() string {
	height := a.listHeight() + 1
	switch {
	case a.loading && a.rowCount() == 0 && !a.hsState.Present:
		return ui.EmptyState(a.theme, "reading…", a.width, height)
	case a.loadFailed && a.rowCount() == 0:
		return ui.EmptyState(a.theme, "could not read — see the message below", a.width, height)
	case !a.hsState.Present:
		return a.panel(a.hsInstallLines(), height)
	case a.rowCount() == 0:
		return ui.EmptyState(a.theme, a.cpEmptyMessage(), a.width, height)
	}
	columns, rows, styles := a.cpTableData()
	return ui.Table{
		Columns: columns, Rows: rows, Styles: styles,
		Selected: a.cursor[a.screen], Offset: a.offset[a.screen], Height: a.listHeight(),
	}.Render(a.theme, a.width)
}

// hsInstallLines is a control-plane screen when headscale is absent: what to
// run, for this distribution, and the key that runs it after a preview.
func (a *app) hsInstallLines() []string {
	t := a.theme
	name := a.hsState.Distro.String()
	if name == "" {
		name = "this machine"
	}
	lines := []string{"",
		"  " + t.Warn.Render("headscale is not installed on "+name+"."),
		"",
		"  " + t.Base.Render("It comes from the tui-tools repository (pkgs.tui.tools), the "+
			"family's source-built mirror of the upstream release."),
	}
	if _, err := headscale.BuildInstall(a.hsState.Distro, a.hsState.Repo); err != nil {
		return append(lines, "", "  "+t.Base.Render(err.Error()))
	}
	lines = append(lines, "  "+t.Base.Render("i installs it, previewed and confirmed — "+
		"these commands:"), "")
	for _, cmd := range headscale.InstallInstructions(a.hsState.Distro, a.hsState.Repo) {
		lines = append(lines, "    "+t.Command.Render("$ "+cmd))
	}
	return append(lines, "",
		"  "+t.Muted.Render("the package does not start the unit: S configures and starts it"))
}

// cpEmptyMessage is what a control-plane screen shows when it has no rows.
func (a *app) cpEmptyMessage() string {
	hs := a.hsState
	if a.screen == screenDNS {
		reason := hs.ControlPlane.Error
		if reason == "" {
			reason = "it could not be read"
		}
		return headscale.HeadscaleConfigPath + " is not readable (" + reason + ") · run with sudo"
	}
	switch {
	case hs.NotRunning:
		// Nothing failed: the CLI was not asked, because the unit is stopped.
		// The message says how to start it.
		return hs.Error
	case hs.Error != "":
		return "could not read Headscale: " + hs.Error
	}
	return "nothing here yet"
}

// noteLines is what sits between the tabs and the table on a control-plane
// screen: the readiness line, the panel on the users screen (which is where
// the control plane is configured from), the identity note, and on the nodes
// screen the selected node's routes spelled out.
func (a *app) noteLines() []string {
	if !a.screen.controlPlane() {
		return nil
	}
	lines := []string{a.readinessLine()}
	if !a.hsState.Present {
		return lines
	}
	note := a.theme.Muted.Render(ui.Truncate(controlPlaneNote, a.width))
	switch a.screen {
	case screenNodes:
		// The selected node's routes, spelled out: the table cell only has
		// room for the counts.
		if node, ok := a.selectedNode(); ok && len(headscale.NodeRoutes(node)) > 0 {
			routes := "routes of " + nodeName(node) + ": " + headscale.RoutesText(node) +
				" — r approves or revokes"
			return append(lines, note, a.theme.Muted.Render(ui.Truncate(routes, a.width)))
		}
		return append(lines, note)
	case screenDNS:
		return append(lines, a.theme.Muted.Render(ui.Truncate("dns: section of "+
			headscale.HeadscaleConfigPath+" · every change is a diff of that file, then a "+
			"restart", a.width)))
	case screenUsers:
		for _, line := range a.controlPlanePanel() {
			lines = append(lines, a.theme.Muted.Render(ui.Truncate(line, a.width)))
		}
	}
	return append(lines, note)
}

// readinessLine is the guided half of the control plane: the next missing
// step, in order, with the key that does it.
func (a *app) readinessLine() string {
	r := headscale.ReadinessFor(a.hsState, time.Now())
	style := a.theme.Warn
	label := "next step  "
	if r.Next == headscale.NextReady {
		style, label = a.theme.OK, "readiness  "
	}
	return ui.Truncate(a.theme.Muted.Render(label)+style.Render(r.NextStep), a.width)
}

// controlPlanePanel renders what /etc/headscale/config.yaml says. It is plain
// text rather than a table because these are facts about one thing, not rows.
func (a *app) controlPlanePanel() []string {
	cp := a.hsState.ControlPlane
	lines := []string{"control plane · " + orDash(cp.ConfigPath)}
	if service := serviceLine(cp); service != "" {
		lines = append(lines, service)
	}
	if !cp.Readable {
		reason := cp.Error
		if reason == "" {
			reason = "not read"
		}
		return append(lines, "  "+reason+" — it must be readable before it can be edited")
	}
	oidc := cp.OIDC
	server := "  server_url  " + orDash(cp.ServerURL) +
		"   listen_addr " + orDash(cp.ListenAddr) +
		"   base_domain " + orDash(cp.BaseDomain)
	if a.serverURLWarning(cp.ServerURL) != "" {
		server += "   ⚠"
	}
	if own := ownershipLine(cp); own != "" {
		lines = append(lines, own)
	}
	lines = append(lines,
		server,
		"  transport   "+headscale.TransportNote(cp),
		"  redirect    "+redirectLine(cp),
		"  oidc        issuer "+orDash(oidc.Issuer)+
			" · client_id "+orDash(oidc.ClientID)+" · "+secretState(oidc),
		"  allowed     domains "+wordsOrDash(oidc.AllowedDomains)+
			" AND groups "+wordsOrDash(oidc.AllowedGroups)+
			" AND users "+wordsOrDash(oidc.AllowedUsers)+
			" (every non-empty list must match)",
	)
	// The allow-list mistakes headscale makes silently, at the login: a
	// groups list the IdP never satisfies, users the domains refuse.
	for _, w := range headscale.OIDCWarnings(oidc) {
		lines = append(lines, "  ⚠           "+w)
	}
	return append(lines,
		"  scope       "+wordsOrDash(oidc.Scope)+
			" · pkce "+onOff(oidc.PKCE)+
			" · only_start_if_oidc_is_available "+yesNo(oidc.OnlyStartIfAvailable),
	)
}

// redirectLine is the redirect URI an OAuth client for this server has to be
// registered with, and whether an IdP will accept it: the value an operator
// otherwise has to work out and type into the IdP's console by hand.
func redirectLine(cp headscale.ControlPlane) string {
	uri := headscale.RedirectURI(cp.ServerURL)
	if uri == "" {
		return "-"
	}
	host := headscale.URLHost(cp.ServerURL)
	switch {
	case !headscale.ServerURLIsHTTPS(cp.ServerURL):
		return uri + " — most IdPs (Google included) refuse an http redirect"
	case headscale.IsIPHost(host):
		return uri + " — most IdPs (Google included) refuse a redirect on an IP"
	}
	return uri + " — register it as the OAuth client's redirect URI"
}

// ownershipLine says whether headscale can read its own files. A mismatch is
// named by its first path, because the first one is usually the whole story
// (a root-run `headscale` created the key and the database together).
func ownershipLine(cp headscale.ControlPlane) string {
	own := cp.Ownership
	switch {
	case !own.Checked:
		return ""
	case own.OK():
		return "  ownership   state, secret and backup owned as expected"
	}
	first := own.Issues[0]
	line := "  ownership   ⚠ " + first.Path + " is " + first.Owner + ", want " + first.Want
	if more := len(own.Issues) - 1; more > 0 {
		line += fmt.Sprintf(" (+%d more)", more)
	}
	return line + " — F fixes it"
}

// serviceLine is the state of the unit that reads the configuration: whether
// it runs, whether it starts at boot, and the account it runs as. Each half is
// a way the control plane silently goes away: a unit that is not active, a
// unit that is disabled and so gone after the next reboot, and an account that
// cannot read the files the tool writes for it.
func serviceLine(cp headscale.ControlPlane) string {
	parts := []string{}
	if cp.ServiceState != "" {
		parts = append(parts, cp.ServiceState)
	}
	if cp.ServiceEnabled != "" {
		enabled := cp.ServiceEnabled
		if headscale.ServiceNeedsEnable(enabled) {
			enabled += " (won't start at boot)"
		}
		parts = append(parts, enabled)
	}
	if cp.ServiceUser != "" {
		// The account matters because the client secret file is written owned
		// by it: this is the value that has to be right for the restart.
		parts = append(parts, "runs as "+serviceAccount(cp))
	}
	if len(parts) == 0 {
		return ""
	}
	return "  service     headscale " + strings.Join(parts, " · ")
}

// secretState says whether a client secret is configured, and never more than
// that. An inline secret is called out: it is readable by everyone who can read
// config.yaml, which is the reason this tool writes it to its own file.
func secretState(o headscale.OIDCConfig) string {
	switch {
	case o.ClientSecretInline:
		return "secret INLINE in config.yaml — replace it with O"
	case o.ClientSecretSet:
		return "secret set (" + orDash(o.ClientSecretPath) + ")"
	default:
		return "secret not set"
	}
}

// cpTableData builds the columns, rows and per-row styles for the current
// control-plane screen.
func (a *app) cpTableData() ([]ui.Column, [][]string, []*lipgloss.Style) {
	switch a.screen {
	case screenNodes:
		return a.nodesTable()
	case screenKeys:
		return a.keysTable()
	case screenDNS:
		return a.dnsTable()
	}
	return a.usersTable()
}

func (a *app) usersTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "ID", Width: 4},
		{Title: "NAME", Width: 14, Flex: true},
		{Title: "PROVIDER", Width: 10},
		{Title: "EMAIL", Width: 22},
		{Title: "CREATED", Width: 10},
	}
	users := a.hsState.Users
	now := time.Now()
	rows := make([][]string, 0, len(users))
	for _, u := range users {
		rows = append(rows, []string{
			u.ID, u.Name, orDash(u.Provider), orDash(u.Email), ago(now, u.CreatedAt),
		})
	}
	return columns, rows, nil
}

func (a *app) nodesTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "ID", Width: 3},
		{Title: "NODE", Width: 12},
		{Title: "USER", Width: 8},
		{Title: "ADDRESSES", Width: 12},
		{Title: "LAST SEEN", Width: 10},
		{Title: "STATE", Width: 8},
		// Advertised routes and whether each is approved; the exit routes
		// read as one "exit node". It takes the width that is left, because
		// it is the column that grows.
		{Title: "ROUTES", Width: 24, Flex: true},
	}
	nodes := a.hsState.Nodes
	now := time.Now()
	rows := make([][]string, 0, len(nodes))
	styles := make([]*lipgloss.Style, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, []string{
			n.ID, nodeName(n), orDash(n.User),
			strings.Join(n.IPAddresses, ", "),
			ago(now, n.LastSeen), nodeState(now, n), headscale.RoutesSummary(n),
		})
		styles = append(styles, a.nodeStyle(now, n))
	}
	return columns, rows, styles
}

func (a *app) keysTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "ID", Width: 4},
		{Title: "USER", Width: 10, Flex: true},
		{Title: "PREFIX", Width: 12},
		{Title: "REUSABLE", Width: 8},
		{Title: "USED", Width: 5},
		{Title: "EXPIRES", Width: 11},
	}
	keys := a.hsState.PreAuthKeys
	now := time.Now()
	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, []string{
			k.ID, orDash(k.User), orDash(k.KeyPrefix),
			yesNo(k.Reusable), yesNo(k.Used), expiryText(now, k.Expiration),
		})
	}
	return columns, rows, nil
}

// nodeStyle colours a node row: online reads OK, expired reads danger.
func (a *app) nodeStyle(now time.Time, n headscale.Node) *lipgloss.Style {
	if !n.Expiry.IsZero() && n.Expiry.Before(now) {
		s := a.theme.Row.Foreground(a.theme.Danger.GetForeground())
		return &s
	}
	if n.Online {
		s := a.theme.Row.Foreground(a.theme.OK.GetForeground())
		return &s
	}
	s := a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	return &s
}

// cpHelpKeys is the control-plane screen's hint bar keys.
func (a *app) cpHelpKeys() []ui.KeyHint {
	if !a.hsState.Present && !a.loading {
		return []ui.KeyHint{{Key: "i", Desc: "install headscale"}}
	}
	switch a.screen {
	case screenUsers:
		return []ui.KeyHint{{Key: "n", Desc: "new user"},
			{Key: "S", Desc: "server"}, {Key: "O", Desc: "oidc"},
			{Key: "F", Desc: "fix owner"}}
	case screenNodes:
		return []ui.KeyHint{{Key: "r", Desc: "routes"},
			{Key: "e", Desc: "expire"}, {Key: "m", Desc: "rename"},
			{Key: "x", Desc: "delete"}}
	case screenKeys:
		return []ui.KeyHint{{Key: "n", Desc: "new key"}}
	case screenDNS:
		return []ui.KeyHint{{Key: "e", Desc: "edit"}, {Key: "n", Desc: "add"},
			{Key: "x", Desc: "remove"}}
	}
	return nil
}

// --- small formatters ---

func nodeName(n headscale.Node) string {
	if n.GivenName != "" {
		return n.GivenName
	}
	return n.Name
}

func nodeState(now time.Time, n headscale.Node) string {
	if !n.Expiry.IsZero() && n.Expiry.Before(now) {
		return "expired"
	}
	if n.Online {
		return "online"
	}
	return "offline"
}

func expiryText(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	if t.Before(now) {
		return "expired"
	}
	return "in " + durationUnit(t.Sub(now))
}

// ago renders how long ago a moment was, in one unit.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return durationUnit(now.Sub(t)) + " ago"
}

// durationUnit renders a duration in one unit, without "ago".
func durationUnit(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "min"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}

// wordsOrDash renders a configuration list space-separated, the way the
// control-plane panel shows allow lists and scopes, or a dash when empty.
func wordsOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, " ")
}
