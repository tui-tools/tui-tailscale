package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// mode is the dialog currently open. It is separate from the screen, which is
// the tab the browse view shows.
type mode int

const (
	modeBrowse mode = iota
	modeConfirm
	modeInput
	modePicker
	modeHelp
	// modeNotice is a message with nothing to decide: the login URL a
	// browser join prints.
	modeNotice
)

// inputPurpose records what an open text input is collecting.
type inputPurpose int

const (
	inputNone inputPurpose = iota
	// The join form, in the order the fields are asked for.
	inputJoinServer
	inputJoinKey
	inputJoinHostname
	inputJoinRoutes
	// The one-setting edits.
	inputRoutes
	inputHostname
)

// pickerPurpose records what an open picker is choosing.
type pickerPurpose int

const (
	pickerNone pickerPurpose = iota
	pickerJoinAcceptRoutes
	pickerJoinExitNode
	pickerExitNode
)

// pickerYes and pickerNo are the two options of a boolean picker; noExitNode
// is the exit-node picker's way out.
const (
	pickerYes  = "yes"
	pickerNo   = "no"
	noExitNode = "none — traffic leaves this machine directly"
)

// planTimeout bounds a whole plan. Each command is bounded by its own runner
// too; this is the outer limit for an install, which downloads.
const planTimeout = 20 * time.Minute

// cleanupTimeout bounds each cleanup command, on a context of its own so a
// plan that ran out of time still removes what it left behind.
const cleanupTimeout = 30 * time.Second

// joinDraft collects the join form's answers across its steps. The key is
// held only between the step that asks for it and the one that builds the
// plan, and is forgotten on every way out of the form.
type joinDraft struct {
	server       string
	key          string
	hostname     string
	acceptRoutes bool
	routes       []string
}

// app is the Bubble Tea model.
type app struct {
	backend       tailscale.Backend
	theme         theme.Theme
	backendCompat compat.Result

	state tailscale.State

	width, height int
	screen        tailscale.Screen
	// cursor and offset are per screen, so switching tabs keeps each one's
	// position.
	cursor [tailscale.ScreenCount]int
	offset [tailscale.ScreenCount]int

	mode          mode
	confirm       ui.Confirm
	input         ui.Input
	inputPurpose  inputPurpose
	picker        ui.Picker
	pickerPurpose pickerPurpose
	// exitChoices maps an exit-node picker option to the address it stands
	// for.
	exitChoices map[string]string
	join        joinDraft
	notice      struct{ title, body string }

	status     string
	statusKind ui.StatusKind
	loading    bool
	loadFailed bool
	busy       bool
}

// loadedMsg carries the result of a read.
type loadedMsg struct {
	state tailscale.State
	err   error
}

// planRanMsg carries the result of a confirmed plan.
type planRanMsg struct {
	plan   tailscale.Plan
	output string
	err    error
	// cleanupErr is a cleanup command that failed: for a join, a key file
	// left behind, which the user has to hear about.
	cleanupErr error
}

// newApp builds the model around a backend.
func newApp(backend tailscale.Backend, th theme.Theme, backendCompat compat.Result) *app {
	a := &app{backend: backend, theme: th, backendCompat: backendCompat,
		width: 80, height: 24, loading: true}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first read.
func (a *app) Init() tea.Cmd { return a.load() }

// load reads the current state in the background.
func (a *app) load() tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		state, err := backend.Load(ctx)
		return loadedMsg{state: state, err: err}
	}
}

// runPlan executes a confirmed plan in the background: its steps in order,
// stopping at the first failure, then its cleanup whatever happened.
func (a *app) runPlan(plan tailscale.Plan) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), planTimeout)
		defer cancel()
		var out strings.Builder
		var runErr error
		for _, cmd := range plan.Steps {
			text, err := backend.Run(ctx, cmd)
			if text != "" {
				out.WriteString(text)
				out.WriteString("\n")
			}
			if err != nil {
				runErr = err
				break
			}
		}
		var cleanupErr error
		for _, cmd := range plan.Cleanup {
			cctx, ccancel := context.WithTimeout(context.Background(), cleanupTimeout)
			if _, err := backend.Run(cctx, cmd); err != nil && cleanupErr == nil {
				cleanupErr = err
			}
			ccancel()
		}
		return planRanMsg{plan: plan, output: out.String(), err: runErr, cleanupErr: cleanupErr}
	}
}

// setStatus records a message for the status line.
func (a *app) setStatus(kind ui.StatusKind, message string) {
	a.status = message
	a.statusKind = kind
}

// setStatusf records a formatted message for the status line.
func (a *app) setStatusf(kind ui.StatusKind, format string, args ...any) {
	a.setStatus(kind, fmt.Sprintf(format, args...))
}

// Update is the main event loop.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampCursor()
		return a, nil

	case loadedMsg:
		a.loading = false
		if msg.err != nil {
			a.loadFailed = true
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.loadFailed = false
		a.state = msg.state
		a.clampCursor()
		return a, nil

	case planRanMsg:
		a.busy = false
		a.loading = true
		a.planResult(msg)
		return a, a.load()

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	if a.mode == modeInput {
		cmd, _ := a.input.Update(msg)
		return a, cmd
	}
	return a, nil
}

// planResult turns a plan's outcome into the status line — and, for a join
// that printed a login URL, into the dialog that says to open it.
func (a *app) planResult(msg planRanMsg) {
	if msg.plan.Action == tailscale.ActionJoin {
		text := msg.output
		if msg.err != nil {
			text += "\n" + msg.err.Error()
		}
		if url := tailscale.ParseLoginURL(text); url != "" {
			// The client stopped waiting (--timeout), which it reports as a
			// failure; the login itself is pending in tailscaled, and this URL
			// is how it finishes.
			a.setStatusf(ui.StatusWarn, "login pending — open %s in a browser", url)
			a.openNotice("Log in to finish joining",
				"tailscale is waiting for a login. Open this URL in a browser — on any "+
					"machine — and log in (with a self-hosted control plane, this is the "+
					"OIDC login of your identity provider):\n\n  "+url+"\n\nThe node "+
					"joins as soon as the login completes; r re-reads it. The URL also "+
					"stays on the node screen until then.")
			if msg.cleanupErr != nil {
				a.setStatus(ui.StatusError, cleanupMessage(msg.cleanupErr))
			}
			return
		}
	}
	switch {
	case msg.cleanupErr != nil:
		a.setStatus(ui.StatusError, cleanupMessage(msg.cleanupErr))
	case msg.err != nil:
		a.setStatus(ui.StatusError, runner.FirstLine(msg.err.Error()))
	default:
		summary := strings.TrimSpace(msg.output)
		if summary == "" {
			summary = "done"
		}
		a.setStatusf(ui.StatusOK, "%s: %s", msg.plan.Title, runner.FirstLine(summary))
	}
}

// cleanupMessage is the status line for a cleanup that failed.
func cleanupMessage(err error) string {
	return "cleanup failed — remove " + tailscale.AuthKeyPath + " by hand: " +
		runner.FirstLine(err.Error())
}

// handleKey routes a key press to the open dialog or the browse view.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		a.join = joinDraft{}
		return a, tea.Quit
	}
	if a.busy {
		// A command is running: swallow input rather than queueing surprises.
		return a, nil
	}
	switch a.mode {
	case modeConfirm:
		return a.handleConfirm(msg)
	case modeInput:
		return a.handleInput(msg)
	case modePicker:
		return a.handlePicker(msg)
	case modeHelp, modeNotice:
		a.mode = modeBrowse
		return a, nil
	default:
		return a.handleBrowseKey(msg)
	}
}

// handleConfirm resolves the confirm dialog. This is the only path to a change.
func (a *app) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.confirm.Update(msg)
	if !a.confirm.Done {
		return a, nil
	}
	a.mode = modeBrowse
	confirmed := a.confirm.Confirmed
	plan, ok := a.confirm.Payload.(tailscale.Plan)
	a.confirm = ui.Confirm{}
	if !confirmed || !ok {
		// The plan — and a pre-auth key on its stdin — goes with the dialog.
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "running %s…", plan.Title)
	return a, a.runPlan(plan)
}

// handleInput resolves an open text input.
func (a *app) handleInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		return a, cmd
	}
	value := strings.TrimSpace(a.input.Value())
	accepted := a.input.Accepted
	purpose := a.inputPurpose
	a.input = ui.Input{}
	a.inputPurpose = inputNone
	a.mode = modeBrowse
	if !accepted || (value == "" && !acceptsEmpty(purpose)) {
		a.cancelled()
		return a, nil
	}
	switch purpose {
	case inputJoinServer:
		a.tookJoinServer(value)
	case inputJoinKey:
		a.tookJoinKey(value)
	case inputJoinHostname:
		a.tookJoinHostname(value)
	case inputJoinRoutes:
		a.tookJoinRoutes(value)
	case inputRoutes:
		a.openPlan(tailscale.BuildCommand(tailscale.Request{
			Action: tailscale.ActionAdvertiseRoutes, Routes: tailscale.SplitList(value)}))
	case inputHostname:
		a.openPlan(tailscale.BuildCommand(tailscale.Request{
			Action: tailscale.ActionHostname, Hostname: value}))
	}
	return a, nil
}

// acceptsEmpty reports whether an empty answer is a real answer rather than a
// cancel: no pre-auth key (log in with a browser), the OS hostname, no routes.
func acceptsEmpty(purpose inputPurpose) bool {
	switch purpose {
	case inputJoinKey, inputJoinHostname, inputJoinRoutes, inputRoutes:
		return true
	}
	return false
}

// cancelled abandons whatever form was open, forgetting a typed key with it.
func (a *app) cancelled() {
	a.join = joinDraft{}
	a.exitChoices = nil
	a.setStatus(ui.StatusInfo, "cancelled")
}

// handlePicker resolves an open picker.
func (a *app) handlePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.picker.Update(msg)
	if !a.picker.Done {
		return a, nil
	}
	accepted, choice := a.picker.Accepted, a.picker.Selected()
	purpose := a.pickerPurpose
	a.picker = ui.Picker{}
	a.pickerPurpose = pickerNone
	a.mode = modeBrowse
	if !accepted || choice == "" {
		a.cancelled()
		return a, nil
	}
	switch purpose {
	case pickerJoinAcceptRoutes:
		a.join.acceptRoutes = choice == pickerYes
		a.askJoinRoutes()
	case pickerJoinExitNode:
		a.confirmJoin(choice == pickerYes)
	case pickerExitNode:
		ip := a.exitChoices[choice]
		a.exitChoices = nil
		a.openPlan(tailscale.BuildCommand(tailscale.Request{
			Action: tailscale.ActionExitNode, ExitNode: ip}))
	}
	return a, nil
}

// handleBrowseKey handles the tabbed browse view. j and h are actions here —
// join and hostname — so the selection moves with the arrows.
func (a *app) handleBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.mode = modeHelp
		return a, nil
	case "tab", "right":
		a.setScreen((a.screen + 1) % tailscale.ScreenCount)
		return a, nil
	case "shift+tab", "left":
		a.setScreen((a.screen + tailscale.ScreenCount - 1) % tailscale.ScreenCount)
		return a, nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// A digit jumps to a screen, so a new tab needs no new key binding.
		if n := tailscale.Screen(key[0] - '1'); n < tailscale.ScreenCount {
			a.setScreen(n)
		}
		return a, nil
	case "down":
		a.moveCursor(1)
		return a, nil
	case "up":
		a.moveCursor(-1)
		return a, nil
	case "g", "home":
		a.cursor[a.screen], a.offset[a.screen] = 0, 0
		return a, nil
	case "G", "end":
		a.cursor[a.screen] = max(a.rowCount()-1, 0)
		a.clampCursor()
		return a, nil
	case "pgdown", "ctrl+f":
		a.moveCursor(a.listHeight())
		return a, nil
	case "pgup", "ctrl+b":
		a.moveCursor(-a.listHeight())
		return a, nil
	case "r", "ctrl+r":
		a.loading = true
		return a, a.load()
	}
	if spec, ok := tailscale.ActionFor(key); ok {
		a.startAction(spec.Action)
	}
	return a, nil
}

// startAction opens the first dialog of an action: a confirm for the ones
// with nothing to ask, the first question for the others.
func (a *app) startAction(action tailscale.Action) {
	if action == tailscale.ActionInstall {
		if a.state.Installed {
			a.setStatus(ui.StatusInfo, "tailscale is already installed")
			return
		}
		a.openPlan(tailscale.BuildCommand(tailscale.Request{
			Action: action, Distro: a.state.Distro}))
		return
	}
	if !a.nodeReachable() {
		return
	}
	s := a.state
	switch action {
	case tailscale.ActionJoin:
		a.startJoin()
	case tailscale.ActionAcceptRoutes:
		if a.prefsReadable() {
			a.openPlan(tailscale.BuildCommand(tailscale.Request{
				Action: action, Enable: !s.Prefs.RouteAll}))
		}
	case tailscale.ActionAdvertiseRoutes:
		if a.prefsReadable() {
			a.openInput(inputRoutes, "Advertise subnet routes", "192.168.1.0/24, 10.0.0.0/24",
				strings.Join(s.Prefs.SubnetRoutes(), ", "),
				"The subnets other nodes can reach through this one, comma-separated. "+
					"Empty stops advertising any. The exit node is a separate switch (E).")
		}
	case tailscale.ActionExitNode:
		a.openExitNodePicker()
	case tailscale.ActionAdvertiseExitNode:
		if a.prefsReadable() {
			a.openPlan(tailscale.BuildCommand(tailscale.Request{
				Action: action, Enable: !s.Prefs.AdvertisesExitNode()}))
		}
	case tailscale.ActionHostname:
		current := s.Prefs.Hostname
		if current == "" {
			current = s.Node.HostName
		}
		a.openInput(inputHostname, "Hostname", "example-node", current,
			"The name this node registers with; its MagicDNS name follows it.")
	case tailscale.ActionDown:
		if s.Node.BackendState != tailscale.StateRunning &&
			s.Node.BackendState != tailscale.StateStarting {
			a.setStatusf(ui.StatusInfo, "the node is not up (%s)", stateWord(s.Node.BackendState))
			return
		}
		a.openPlan(tailscale.BuildCommand(tailscale.Request{Action: action}))
	case tailscale.ActionUp:
		switch s.Node.BackendState {
		case tailscale.StateStopped:
			a.openPlan(tailscale.BuildCommand(tailscale.Request{Action: action}))
		case tailscale.StateNeedsLogin, tailscale.StateNoState:
			a.setStatus(ui.StatusWarn, "the node is not logged in — j joins a tailnet")
		default:
			a.setStatusf(ui.StatusInfo, "the node is already up (%s)", stateWord(s.Node.BackendState))
		}
	case tailscale.ActionLogout:
		if !s.LoggedIn() && s.Node.AuthURL == "" {
			a.setStatus(ui.StatusInfo, "the node is not logged in")
			return
		}
		a.openPlan(tailscale.BuildCommand(tailscale.Request{Action: action}))
	}
}

// nodeReachable reports whether tailscale can be driven at all, and says why
// not in the status line when it cannot.
func (a *app) nodeReachable() bool {
	s := a.state
	switch {
	case a.loading && !s.Installed && !a.loadFailed:
		a.setStatus(ui.StatusInfo, "still reading…")
		return false
	case !s.Installed:
		a.setStatus(ui.StatusWarn, "tailscale is not installed — i installs it")
		return false
	case !s.DaemonRunning:
		msg := s.Error
		if msg == "" {
			msg = "tailscaled did not answer"
		}
		a.setStatus(ui.StatusWarn, msg)
		return false
	}
	return true
}

// prefsReadable reports whether the node's settings were read, which a toggle
// needs: flipping a switch whose position is unknown is a guess.
func (a *app) prefsReadable() bool {
	if a.state.PrefsRead {
		return true
	}
	a.setStatusf(ui.StatusWarn, "the node's settings could not be read: %s",
		orDash(a.state.PrefsError))
	return false
}

// openPlan opens the confirm dialog for a built plan, or reports why it could
// not be built.
func (a *app) openPlan(plan tailscale.Plan, err error) {
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return
	}
	previews := make([]string, 0, len(plan.Commands()))
	for _, cmd := range plan.Commands() {
		previews = append(previews, a.backend.Preview(cmd))
	}
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   plan.Title,
		Body:    plan.Body,
		Command: strings.Join(previews, "\n$ "),
		Danger:  plan.Destructive,
		Payload: plan,
	}
}

// openInput opens a text input for one purpose.
func (a *app) openInput(purpose inputPurpose, title, placeholder, value, help string) {
	a.input = ui.NewInput(title, placeholder, value)
	a.input.Help = help
	a.inputPurpose = purpose
	a.mode = modeInput
}

// openPicker opens a yes/no picker.
func (a *app) openPicker(purpose pickerPurpose, title string, current bool) {
	choice := pickerNo
	if current {
		choice = pickerYes
	}
	a.picker = ui.NewPicker(title, []string{pickerYes, pickerNo}, choice)
	a.pickerPurpose = purpose
	a.mode = modePicker
}

// openNotice opens a message the user only has to read.
func (a *app) openNotice(title, body string) {
	a.notice.title, a.notice.body = title, body
	a.mode = modeNotice
}

// openExitNodePicker lists the peers offering an exit node, and none.
func (a *app) openExitNodePicker() {
	options := []string{noExitNode}
	a.exitChoices = map[string]string{noExitNode: ""}
	current := noExitNode
	using, inUse := a.state.CurrentExitNode()
	for _, p := range a.state.ExitNodeOptions() {
		label := p.Name() + "  " + p.IPv4()
		if !p.Online {
			label += "  (offline)"
		}
		options = append(options, label)
		a.exitChoices[label] = p.IPv4()
		if inUse && p.ID == using.ID {
			current = label
		}
	}
	if len(options) == 1 && !a.state.Prefs.UsesExitNode() {
		a.exitChoices = nil
		a.setStatus(ui.StatusInfo, "no peer offers itself as an exit node")
		return
	}
	a.picker = ui.NewPicker("Use which exit node?", options, current)
	a.pickerPurpose = pickerExitNode
	a.mode = modePicker
}

// --- the join form (j) ------------------------------------------------------

// startJoin opens the first question: the login server.
func (a *app) startJoin() {
	a.join = joinDraft{}
	server := a.state.Prefs.ControlURL
	if tailscale.IsTailscaleControl(server) {
		// A fresh client carries Tailscale's server as its default; the
		// question is usually about a self-hosted one.
		server = ""
	}
	a.askJoinServer(server, "")
}

// askJoinServer asks for the login server, with the problem of the last
// answer when there was one.
func (a *app) askJoinServer(value, problem string) {
	help := "Join step 1 of 6 — the control server this node registers with: a Headscale's " +
		"server_url, or " + tailscale.DefaultControlURL + " for Tailscale's own."
	if problem != "" {
		help = "⚠ " + problem + "\n\n" + help
	}
	a.openInput(inputJoinServer, "Login server", "https://headscale.example.com", value, help)
}

// tookJoinServer validates the server and asks for the key.
func (a *app) tookJoinServer(value string) {
	value = strings.TrimRight(value, "/")
	if problem := tailscale.ServerURLProblem(value); problem != "" {
		a.askJoinServer(value, problem)
		return
	}
	a.join.server = value
	a.openInput(inputJoinKey, "Pre-auth key (optional)", "empty: log in with a browser", "",
		"Join step 2 of 6 — a pre-auth key (Headscale: `headscale preauthkeys create`), or "+
			"empty to log in with a browser instead. The key is not echoed, never shown "+
			"again, and reaches tailscale through a root-only file under /run, never a "+
			"command line.")
	a.input.Model.EchoMode = textinput.EchoPassword
}

// tookJoinKey records the key, or none, and asks for the hostname.
func (a *app) tookJoinKey(value string) {
	if value != "" && !tailscale.ValidAuthKey(value) {
		a.join = joinDraft{}
		a.setStatus(ui.StatusError, "not a valid pre-auth key (one word, no spaces) — j starts over")
		return
	}
	a.join.key = value
	current := a.state.Prefs.Hostname
	if current == "" {
		current = a.state.Node.HostName
	}
	a.openInput(inputJoinHostname, "Hostname (optional)", "empty: the machine's own name",
		current, "Join step 3 of 6 — the name this node registers with. Empty uses the "+
			"machine's hostname.")
}

// tookJoinHostname records the hostname and asks about accepting routes.
func (a *app) tookJoinHostname(value string) {
	if value != "" && !tailscale.ValidHostname(value) {
		a.join = joinDraft{}
		a.setStatusf(ui.StatusError, "not a valid hostname: %q — j starts over", value)
		return
	}
	a.join.hostname = value
	a.openPicker(pickerJoinAcceptRoutes,
		"Join step 4 of 6 — accept the subnet routes other nodes advertise?",
		a.state.Prefs.RouteAll)
}

// askJoinRoutes asks for the subnets to advertise.
func (a *app) askJoinRoutes() {
	a.openInput(inputJoinRoutes, "Advertise subnet routes (optional)", "192.168.1.0/24",
		strings.Join(a.state.Prefs.SubnetRoutes(), ", "),
		"Join step 5 of 6 — subnets other nodes can reach through this one, "+
			"comma-separated. Empty advertises none.")
}

// tookJoinRoutes validates the routes and asks about the exit node.
func (a *app) tookJoinRoutes(value string) {
	routes, err := tailscale.NormalizeRoutes(tailscale.SplitList(value))
	if err != nil {
		a.join = joinDraft{}
		a.setStatus(ui.StatusError, err.Error()+" — j starts over")
		return
	}
	a.join.routes = routes
	a.openPicker(pickerJoinExitNode,
		"Join step 6 of 6 — offer this node as an exit node?",
		a.state.Prefs.AdvertisesExitNode())
}

// confirmJoin builds the join and opens its confirm. The key leaves the draft
// here: from now on it exists only on the stdin of the command that writes it.
func (a *app) confirmJoin(exitNode bool) {
	d := a.join
	a.join = joinDraft{}
	a.openPlan(tailscale.BuildCommand(tailscale.Request{
		Action:            tailscale.ActionJoin,
		LoginServer:       d.server,
		AuthKey:           d.key,
		Hostname:          d.hostname,
		AcceptRoutes:      d.acceptRoutes,
		Routes:            d.routes,
		ExitNodeOffer:     exitNode,
		CurrentControlURL: a.state.Prefs.ControlURL,
		LoggedIn:          a.state.LoggedIn(),
	}))
}

// --- selection --------------------------------------------------------------

// setScreen switches tabs.
func (a *app) setScreen(s tailscale.Screen) {
	a.screen = s
	a.clampCursor()
}

// moveCursor moves the selection on the current screen.
func (a *app) moveCursor(delta int) {
	a.cursor[a.screen] += delta
	a.clampCursor()
}

// clampCursor keeps the cursor and the scroll offset within range.
func (a *app) clampCursor() {
	n := a.rowCount()
	s := a.screen
	if n == 0 {
		a.cursor[s], a.offset[s] = 0, 0
		return
	}
	a.cursor[s] = min(max(a.cursor[s], 0), n-1)
	height := a.listHeight()
	if a.cursor[s] < a.offset[s] {
		a.offset[s] = a.cursor[s]
	}
	if a.cursor[s] >= a.offset[s]+height {
		a.offset[s] = a.cursor[s] - height + 1
	}
	a.offset[s] = max(min(a.offset[s], max(n-height, 0)), 0)
}

// rowCount is the number of selectable rows on the current screen. The node
// screen is a panel, not a list.
func (a *app) rowCount() int {
	if a.screen == tailscale.ScreenPeers {
		return len(a.state.Peers)
	}
	return 0
}
