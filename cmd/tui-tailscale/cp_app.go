package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// screen is one of the views the tool is made of: this host as a node, the
// rest of the tailnet as it sees it, and — on the control plane this host may
// also run — who exists, what is registered, and what can still join. The tab
// bar, the digit keys and the help screen are all generated from this list.
type screen int

const (
	// screenNode shows this host: state, login server, addresses, prefs.
	screenNode screen = iota
	// screenPeers lists the other nodes of the tailnet.
	screenPeers
	// screenUsers lists the headscale users, under the control-plane panel.
	screenUsers
	// screenNodes lists the nodes registered with headscale.
	screenNodes
	// screenKeys lists headscale's pre-authentication keys.
	screenKeys
	// screenDNS is headscale's dns: section, one row per setting.
	screenDNS
	// screenCount is the number of screens: it drives the tab bar and the
	// per-screen cursor arrays.
	screenCount
)

// title is the tab label.
func (s screen) title() string {
	switch s {
	case screenPeers:
		return "peers"
	case screenUsers:
		return "users"
	case screenNodes:
		return "nodes"
	case screenKeys:
		return "preauth keys"
	case screenDNS:
		return "dns"
	}
	return "node"
}

// controlPlane reports whether the screen is one of headscale's.
func (s screen) controlPlane() bool { return s >= screenUsers }

// This file is the control-plane half of the app: the action keys of the
// users, nodes and keys screens, and the single-command confirms they open.
// Their longer flows — S, O and F, which chain several confirms — live in
// cp_controlplane.go and cp_transport.go, and r's in cp_routes.go.

// handleControlPlaneKey dispatches the action keys of the control-plane
// screens.
func (a *app) handleControlPlaneKey(key string) tea.Cmd {
	switch key {
	case "i":
		return a.startInstallHeadscale()
	case "f":
		return a.launchFirewall()
	}
	if a.hsState.ControlPlane.ConfigMissing && (key == "S" || key == "O" || key == "e" ||
		(key == "n" && a.screen == screenDNS)) {
		// Every edit is a diff of a file that is not there.
		a.setStatus(ui.StatusWarn, headscale.ConfigMissingMessage)
		return nil
	}
	switch a.screen {
	case screenUsers:
		switch key {
		case "n":
			if !a.headscaleAnswers() {
				return nil
			}
			a.openInput(inputCreateUser, "Create user", "name", "",
				"A local Headscale user. Its identity is still owned by the IdP over OIDC.")
			return nil
		case "S":
			return a.startServerSettings()
		case "O":
			return a.startOIDCSettings()
		case "F":
			return a.startFixOwnership()
		}
	case screenNodes:
		switch key {
		case "e":
			node, ok := a.selectedNode()
			if !ok {
				return a.warnNothing()
			}
			return a.openConfirm(headscale.BuildExpireNode(node.ID))
		case "x":
			node, ok := a.selectedNode()
			if !ok {
				return a.warnNothing()
			}
			cmd, err := headscale.BuildDeleteNode(node.ID)
			return a.openConfirmWith(
				"This deletes the node from the control plane. The machine loses "+
					"access and must register again to come back.",
				cmd, err)
		case "r":
			node, ok := a.selectedNode()
			if !ok {
				return a.warnNothing()
			}
			return a.startApproveRoutes(node)
		case "R":
			return a.startRegister()
		case "m":
			node, ok := a.selectedNode()
			if !ok {
				return a.warnNothing()
			}
			a.openInput(inputRenameNode, "Rename node "+node.ID, "new-name", nodeName(node),
				"A DNS-label-shaped name: letters, digits and hyphens.")
			a.input.Payload = node.ID
			return nil
		}
	case screenDNS:
		return a.handleDNSKey(key)
	case screenKeys:
		if key == "n" {
			if !a.headscaleAnswers() {
				return nil
			}
			if len(a.hsState.Users) == 0 {
				a.setStatus(ui.StatusWarn, "create a user first — a pre-auth key belongs to one")
				return nil
			}
			a.openInput(inputCreatePreAuthKey, "Create pre-auth key",
				"user-id [reusable] [ephemeral] [expiration]", a.hsState.Users[0].ID,
				"The owner's user id (see the users tab), then optional words: "+
					"reusable, ephemeral, and an expiration like 30m, 24h or 7d (default 24h). "+
					"Without reusable the key is spent by its first join: add it to join "+
					"several machines with one key. "+
					"The key is shown once after creation and never stored.")
			return nil
		}
	}
	return nil
}

// startInstallHeadscale opens the companion install: the tui-tools repository
// when it is not configured yet, then the family's headscale package.
func (a *app) startInstallHeadscale() tea.Cmd {
	var plan headscale.Plan
	var err error
	switch {
	case a.hsState.Present && a.hsState.ControlPlane.ConfigMissing:
		// Installed, with its configuration deleted: the package puts it
		// back (issue #18).
		plan, err = headscale.BuildReinstall(a.hsState.Distro)
	case a.hsState.Present:
		a.setStatus(ui.StatusInfo, "headscale is already installed")
		return nil
	default:
		plan, err = headscale.BuildInstall(a.hsState.Distro, a.hsState.Repo)
	}
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	a.openPlanConfirm(plan)
	return nil
}

// startInstallFirewall opens the install of tui-firewall, when f finds it
// missing: the tui-tools repository when it is not configured, then the
// package.
func (a *app) startInstallFirewall() tea.Cmd {
	plan, err := headscale.BuildInstallFirewall(a.hsState.Distro, a.hsState.Repo)
	if err != nil {
		a.setStatus(ui.StatusWarn, "tui-firewall is not installed · "+err.Error())
		return nil
	}
	a.openPlanConfirm(plan)
	return nil
}

// openPlanConfirm previews a multi-step plan in one confirm.
func (a *app) openPlanConfirm(plan headscale.Plan) {
	previews := make([]string, 0, len(plan.Steps))
	for _, cmd := range plan.Steps {
		previews = append(previews, a.hs.Preview(cmd))
	}
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   plan.Title,
		Body:    plan.Body,
		Command: strings.Join(previews, "\n$ "),
		Payload: plan,
	}
}

// headscaleAnswers reports whether the headscale CLI can take a command, and
// says why not when it cannot: no control plane, or a unit that is stopped.
func (a *app) headscaleAnswers() bool {
	hs := a.hsState
	switch {
	case !hs.Present:
		a.setStatus(ui.StatusWarn, "headscale is not installed — i installs it")
		return false
	case hs.NotRunning:
		a.setStatus(ui.StatusWarn, hs.Error)
		return false
	}
	return true
}

// ranResult handles the result of one confirmed control-plane command.
func (a *app) ranResult(msg ranMsg) tea.Cmd {
	a.busy = false
	if a.keyNotice.secret && isShownOnceKeyWrite(msg.cmd) {
		return a.wroteShownOnceKey(msg)
	}
	if msg.err != nil {
		a.after = nil
		a.cpDraft.forgetSecret()
		a.setStatus(ui.StatusError, runner.FirstLine(msg.err.Error()))
		return a.reloadAfterChange()
	}

	// A chained flow takes over the next step; the reload still happens
	// underneath it.
	if next := a.after; next != nil {
		a.after = nil
		if cmd := next(msg.output); cmd != nil {
			return tea.Batch(cmd, a.reloadAfterChange())
		}
		return a.reloadAfterChange()
	}

	// A created pre-auth key is printed by headscale exactly once, and shown
	// here exactly once. It is never stored: the list keeps only the prefix,
	// like headscale's own list does.
	if isPreAuthCreate(msg.cmd) {
		a.openShownOnceKey(lastLine(msg.output))
		return a.reloadAfterChange()
	}

	summary := strings.TrimSpace(msg.output)
	if summary == "" {
		summary = "done"
	}
	a.setStatusf(ui.StatusOK, "%s: %s", msg.cmd.Description, runner.FirstLine(summary))
	return a.reloadAfterChange()
}

// openConfirmPreAuthKey parses the pre-auth key input line and opens the
// confirm. The line is the owner's user id plus optional words: reusable,
// ephemeral, and an expiration.
func (a *app) openConfirmPreAuthKey(value string) tea.Cmd {
	fields := strings.Fields(value)
	userID := fields[0]
	reusable, ephemeral := false, false
	expiration := ""
	for _, f := range fields[1:] {
		switch strings.ToLower(f) {
		case "reusable":
			reusable = true
		case "ephemeral":
			ephemeral = true
		default:
			if !headscale.ValidExpiration(f) {
				a.setStatusf(ui.StatusError, "not a valid option or expiration: %q", f)
				return nil
			}
			expiration = f
		}
	}
	create, err := headscale.BuildCreatePreAuthKey(userID, reusable, ephemeral, expiration)
	return a.openConfirmWith(
		"Creates a key that lets a machine register itself as this user, without a browser "+
			"login. Headscale prints the key once; this tool shows it once in the status "+
			"line and never stores it.",
		create, err)
}

// warnNothing sets a status and returns no command.
func (a *app) warnNothing() tea.Cmd {
	a.setStatus(ui.StatusWarn, "nothing selected")
	return nil
}

// openConfirm builds a control-plane command and opens the confirm dialog, or
// reports the build error.
func (a *app) openConfirm(cmd runner.Command, err error) tea.Cmd {
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	return a.openConfirmWith(confirmBody(cmd), cmd, nil)
}

// openConfirmWith is openConfirm with an explicit body, for the dialogs whose
// explanation carries more than the one-liner (a diff, a warning, a
// one-time-key notice).
func (a *app) openConfirmWith(body string, cmd runner.Command, err error) tea.Cmd {
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   cmd.Description,
		Body:    body,
		Command: a.hs.Preview(cmd),
		Danger:  cmd.Destructive,
		Payload: cmd,
	}
	return nil
}

// isPreAuthCreate reports whether cmd is the pre-auth key creation, whose
// output is a secret shown exactly once.
func isPreAuthCreate(cmd runner.Command) bool {
	return len(cmd.Argv) >= 3 && cmd.Argv[0] == "headscale" &&
		cmd.Argv[1] == "preauthkeys" && cmd.Argv[2] == "create"
}

// lastLine is the final non-empty line of a command's output — where a tool
// that prints one value (a pre-auth key) puts it.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// confirmBody explains an action above its command preview.
func confirmBody(cmd runner.Command) string {
	if cmd.Destructive {
		return "This changes the running configuration. Review the command below before it runs."
	}
	return "The command below will run exactly as shown."
}

// selectedNode is the node highlighted on the control plane's nodes screen.
func (a *app) selectedNode() (headscale.Node, bool) {
	i := a.cursor[screenNodes]
	if i < 0 || i >= len(a.hsState.Nodes) {
		return headscale.Node{}, false
	}
	return a.hsState.Nodes[i], true
}

// firewallDoneMsg is tui-firewall handing the terminal back.
type firewallDoneMsg struct{ err error }

// launchFirewall hands the terminal to tui-firewall, the family's tool for
// opening the ports the readiness line reports closed. Bubble Tea suspends
// this program, tui-firewall draws on the real terminal, and this screen is
// restored when it exits, then the ports are read again. It is not previewed
// as a change because it is not one: tui-firewall previews and confirms
// whatever it changes (issue #15). The binary is looked for at the moment f
// is pressed, not taken from the last read, so one installed in another
// terminal is found; when it is not there, f offers its install, previewed
// like i's (issue #25).
func (a *app) launchFirewall() tea.Cmd {
	process, err := a.hs.LaunchFirewall()
	if err != nil {
		return a.startInstallFirewall()
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "running %s…", process)
	return tea.Exec(process, func(err error) tea.Msg { return firewallDoneMsg{err: err} })
}
