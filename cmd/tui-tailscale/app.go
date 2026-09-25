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
	"github.com/tui-tools/tui-tailscale/internal/headscale"
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
	// modeFilePicker is the kit's file picker: a certificate, a key or a CA.
	modeFilePicker
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
	// The name a join's answers are saved under as a join profile.
	inputSaveProfile
	// The CA certificate to trust when the login server's does not verify.
	inputJoinCA
	// The one-setting edits.
	inputRoutes
	inputHostname

	// The control plane's inputs, on the users, nodes and keys screens.
	inputCreateUser
	inputCreatePreAuthKey
	inputRenameNode
	// The routes a node is approved to serve.
	inputApproveRoutes
	// The server-settings form, in the order the fields are asked for; the
	// ACME and certificate steps only for the transports that need them.
	inputServerURL
	inputListenAddr
	inputACMEEmail
	inputTLSCertPath
	inputTLSKeyPath
	inputBaseDomain
	// The OIDC form, in the order the fields are asked for.
	inputOIDCIssuer
	inputOIDCClientID
	inputOIDCSecret
	inputOIDCDomains
	inputOIDCGroups
	inputOIDCUsers
	inputOIDCScope
	// The dns screen's inputs.
	inputDNSBase
	inputDNSGlobal
	inputDNSSearch
	inputDNSSplitDomain
	inputDNSSplitServers
	inputDNSRecord
)

// pickerPurpose records what an open picker is choosing.
type pickerPurpose int

const (
	pickerNone pickerPurpose = iota
	pickerJoinAcceptRoutes
	pickerJoinExitNode
	pickerExitNode
	// The join profile j starts from, and tailscale's login profile p
	// switches to.
	pickerJoinProfile
	pickerLoginProfile

	// The control plane's pickers. The two OIDC switches are pickers rather
	// than typed words so there is nothing to spell wrong.
	pickerOIDCOnlyStart
	pickerOIDCPKCE
	// The server-settings form's two choices.
	pickerTransport
	pickerACMEChallenge
	// The OIDC form's first step: which identity provider.
	pickerOIDCProvider
	// The dns screen's switches, and what n adds there.
	pickerDNSMagic
	pickerDNSOverride
	pickerDNSNew
	// R's two questions: which waiting registration, as which user.
	pickerRegistration
	pickerRegisterUser
	// The pairs and CAs tui-cert reports, offered before the file picker.
	pickerIssuedPair
	pickerJoinCA
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
	// from is the join profile the answers were pre-filled from, if any.
	from *tailscale.JoinProfile
}

// app is the Bubble Tea model. It drives both ends of a self-hosted tailnet:
// this host as a node (backend, internal/tailscale) and the headscale control
// plane on it (hs, internal/headscale). Each change goes through the backend
// that owns its binary, so each preview carries that backend's own prefix.
type app struct {
	backend tailscale.Backend
	hs      headscale.Backend
	theme   theme.Theme
	// backendCompat and hsCompat are the two version probes: the tailscale
	// client and headscale, each empty when it was not probed (--demo).
	backendCompat compat.Result
	hsCompat      compat.Result

	state   tailscale.State
	hsState headscale.State

	width, height int
	screen        screen
	// cursor and offset are per screen, so switching tabs keeps each one's
	// position.
	cursor [screenCount]int
	offset [screenCount]int

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
	notice      notice
	// filePicker is the open file picker; its purpose is inputPurpose, since
	// it answers the steps a typed path used to. files is the filesystem it
	// lists: nil is this machine's, --demo a made-up tree.
	filePicker ui.FilePicker
	files      ui.FileSystem
	// pki is tui-cert's last report; pairChoices and caChoices map a pick
	// list's option to the pair or CA certificate it stands for.
	pki         headscale.LocalPKI
	pairChoices map[string]headscale.IssuedPair
	caChoices   map[string]string
	// joinTLSDetail is why the login server's certificate did not verify,
	// kept for the CA step's help.
	joinTLSDetail string

	// profiles are the join profiles (issue #4); profileChoices maps a
	// profile picker's option to the profile name or login profile id it
	// stands for.
	profiles       profileStore
	profileChoices map[string]string
	// pendingJoin is the answers of the join being run, without its key;
	// lastJoin keeps them while a browser login is pending, and offerSave
	// asks for the save offer once the screen is free. saveDraft is the
	// answers the save input is naming, and savingText the config file the
	// confirmed save writes.
	pendingJoin *tailscale.JoinProfile
	lastJoin    *tailscale.JoinProfile
	offerSave   *tailscale.JoinProfile
	saveDraft   tailscale.JoinProfile
	savingText  string

	// cpDraft collects the control-plane forms' answers across their steps.
	cpDraft controlPlaneDraft
	// dnsDraft is the dns section as the open dns step would leave it.
	dnsDraft dnsDraft
	// registerDraft is the registration R is registering; registered the
	// ones this session registered, which the journal still logs as started.
	registerDraft string
	registered    map[string]bool
	// after, when set, runs once on the next successful control-plane
	// command. It is how the control plane's multi-step flows chain — the
	// secret, then config.yaml, then the restart — each step its own confirm.
	// Cancelling a confirm clears it.
	after func(output string) tea.Cmd

	status     string
	statusKind ui.StatusKind
	// running names the plan that is running, and runningSince when it
	// started: an install downloads for minutes, and the status line counts
	// the time so the screen is not mistaken for frozen.
	running      string
	runningSince time.Time
	loading      bool
	loadFailed   bool
	busy         bool

	// probe re-reads the backends' versions for the header; nil keeps the
	// ones the app started with (tests).
	probe func() []compat.Result
	// settling is the moment right after a change — an install, a start or
	// restart, down, up, logout, a join — when a daemon that is still coming
	// up refuses its socket. A read that fails then is shown as "not running
	// yet", and is retried once after settleDelay; settleRetried records that
	// the retry was spent.
	settling      bool
	settleRetried bool

	// loginURLShown is the pending login URL the status line carries, so a
	// later read can replace it once the login completes or expires instead
	// of leaving a dead link there (issue #10).
	loginURLShown string
	// loginPolling reports that a re-read is scheduled because a login is
	// pending; loginPolls counts the re-reads spent on the current URL,
	// loginPollURL, so the polling stops after loginPollLimit.
	loginPolling bool
	loginPolls   int
	loginPollURL string
	// loginWaiting reports that the login URL the status line carried is
	// gone and the node is not running yet: the browser confirmed, and
	// tailscaled is still on its way from NeedsLogin through Starting to
	// Running (issue #20). loginWaitPolls counts the re-reads spent waiting,
	// bounded by loginGracePolls while the node still says it needs a login
	// and by loginWaitLimit otherwise.
	loginWaiting   bool
	loginWaitPolls int
	// daemonStarted reports that u just started tailscaled: the first read
	// that finds it answering replaces the status line with where the node
	// stands (issue #18).
	daemonStarted bool
}

// settleDelay is how long the one automatic re-read after a change waits.
var settleDelay = time.Second

// settleMsg asks for the automatic re-read after a change.
type settleMsg struct{}

// loginPollDelay is how long the re-read waits while a login is pending.
// headscale takes up to half a minute after the browser confirms to finish
// /machine/register, so the node flips from "logged out" to running a while
// after the user is done; re-reading every few seconds shows it flip without
// anyone pressing r.
var loginPollDelay = 3 * time.Second

// loginPollLimit bounds the re-reads spent on one pending login URL: about
// three minutes at loginPollDelay, after which r still re-reads by hand.
const loginPollLimit = 60

// loginGracePolls is how many re-reads a vanished login URL gets while the
// node still reports NeedsLogin (or NoState) before the login is called
// expired: headscale answers /register/confirm before tailscaled has picked
// the login up, so for a few seconds after a successful browser login the
// node looks exactly like one whose login was cancelled.
const loginGracePolls = 5

// loginWaitLimit bounds the re-reads spent on a node that left NeedsLogin
// (Starting, typically) without reaching Running yet: about two minutes at
// loginPollDelay, after which r still re-reads by hand.
const loginWaitLimit = 40

// loginExpiredLine is the status line for a login that vanished without the
// node logging in. A later read that finds the node running replaces it.
const loginExpiredLine = "the pending login expired or was cancelled — j starts a new one"

// loginPollMsg asks for the re-read while a login is pending.
type loginPollMsg struct{}

// loadedMsg carries the result of a read: the node and the control plane,
// read together so the two halves of the screen never disagree about when.
type loadedMsg struct {
	state   tailscale.State
	hsState headscale.State
	err     error
	// probed carries the re-read versions when the read followed a change
	// and reprobed says it did.
	probed   []compat.Result
	reprobed bool
}

// ranMsg carries the result of one confirmed control-plane command.
type ranMsg struct {
	cmd    runner.Command
	output string
	err    error
}

// hsPlanRanMsg carries the result of the companion headscale install.
type hsPlanRanMsg struct {
	plan   headscale.Plan
	output string
	err    error
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

// newApp builds the model around the two backends and the version probes.
func newApp(backend tailscale.Backend, hs headscale.Backend, th theme.Theme,
	probed []compat.Result) *app {
	a := &app{backend: backend, hs: hs, theme: th,
		backendCompat: compatFor(probed, backendName),
		hsCompat:      compatFor(probed, backendHeadscale),
		registered:    map[string]bool{},
		width:         80, height: 24, loading: true}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first read.
func (a *app) Init() tea.Cmd { return a.load() }

// load reads the current state in the background: the node, then the control
// plane.
func (a *app) load() tea.Cmd {
	backend, hs := a.backend, a.hs
	return func() tea.Msg { return readBoth(backend, hs) }
}

// loadNode re-reads the node alone, keeping the control plane as it was read
// last: the pending-login poll only waits for the node to flip, and has no
// reason to re-read headscale's lists, journal and firewall every few seconds.
func (a *app) loadNode() tea.Cmd {
	backend, hsState := a.backend, a.hsState
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		state, err := backend.Load(ctx)
		return loadedMsg{state: state, hsState: hsState, err: err}
	}
}

// readBoth is one read of the node and the control plane.
func readBoth(backend tailscale.Backend, hs headscale.Backend) loadedMsg {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := backend.Load(ctx)
	if err != nil {
		return loadedMsg{err: err}
	}
	hsState, err := hs.Load(ctx)
	return loadedMsg{state: state, hsState: hsState, err: err}
}

// reloadAfterChange is the read that follows every confirmed change. The
// backends forget the binaries they found — an install may have just put one
// there — the versions are probed again for the header, and the read that
// follows is a settling one: a daemon still coming up is "not running yet",
// re-read once automatically.
func (a *app) reloadAfterChange() tea.Cmd {
	return a.reprobeRead(true)
}

// reprobeRead is a read after the backends forget the binaries they found,
// with the versions probed again: after a confirmed change (settling), after
// r, and when tui-firewall hands the terminal back (issue #25), since
// anything may have been installed in between.
func (a *app) reprobeRead(settle bool) tea.Cmd {
	a.loading = true
	a.settling, a.settleRetried = settle, false
	backend, hs, probe := a.backend, a.hs, a.probe
	return func() tea.Msg {
		backend.Reprobe()
		hs.Reprobe()
		msg := readBoth(backend, hs)
		if probe != nil {
			msg.probed, msg.reprobed = probe(), true
		}
		return msg
	}
}

// settleRead handles a read taken while settling: when a daemon that should
// answer does not, its state says "not running yet" instead of the error the
// socket gave, and one more read is scheduled; once both answer, or after the
// retry, the moment is over.
func (a *app) settleRead() tea.Cmd {
	unsettled := false
	s := &a.state
	if s.Installed && !s.DaemonRunning {
		unsettled = true
		s.Error = "tailscaled is not running yet · r re-reads"
		s.NotRunning, s.PermissionDenied = true, false
	}
	h := &a.hsState
	if h.Present && h.Error != "" && !h.NotRunning {
		unsettled = true
		h.Error = "headscale is not running yet · r re-reads"
		h.NotRunning = true
		h.Users, h.Nodes, h.PreAuthKeys = nil, nil, nil
	}
	if !unsettled || a.settleRetried {
		a.settling = false
		return nil
	}
	a.settleRetried = true
	return tea.Tick(settleDelay, func(time.Time) tea.Msg { return settleMsg{} })
}

// run executes one confirmed control-plane command in the background.
func (a *app) run(cmd runner.Command) tea.Cmd {
	hs := a.hs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, err := hs.Run(ctx, cmd)
		return ranMsg{cmd: cmd, output: out, err: err}
	}
}

// runHSPlan executes the confirmed headscale install: its steps in order,
// stopping at the first failure — or at a repository key that is not the
// pinned one.
func (a *app) runHSPlan(plan headscale.Plan) tea.Cmd {
	hs := a.hs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), planTimeout)
		defer cancel()
		var out strings.Builder
		for i, cmd := range plan.Steps {
			text, err := hs.Run(ctx, cmd)
			if text != "" {
				out.WriteString(text)
				out.WriteString("\n")
			}
			if err == nil {
				err = plan.CheckStep(i, text)
			}
			if err != nil {
				return hsPlanRanMsg{plan: plan, output: out.String(), err: err}
			}
		}
		return hsPlanRanMsg{plan: plan, output: out.String()}
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
	if message != a.loginURLShown {
		// Another message took the line: there is no URL left to follow.
		a.loginURLShown = ""
	}
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
		a.hsState = msg.hsState
		if msg.reprobed {
			a.backendCompat = compatFor(msg.probed, backendName)
			a.hsCompat = compatFor(msg.probed, backendHeadscale)
		}
		var retry tea.Cmd
		if a.settling {
			retry = a.settleRead()
		}
		a.clampCursor()
		a.followLogin()
		a.replaceStaleLogin()
		a.followDaemonStart()
		if a.offerSave != nil && a.mode == modeBrowse {
			answers := *a.offerSave
			a.offerSave = nil
			a.offerSaveProfile(answers)
		}
		return a, tea.Batch(retry, a.pollLogin())

	case settleMsg:
		if !a.settling {
			return a, nil
		}
		a.loading = true
		return a, a.load()

	case loginPollMsg:
		a.loginPolling = false
		switch {
		case a.state.Node.AuthURL != "":
			a.loginPolls++
		case a.loginWaiting:
			a.loginWaitPolls++
		default:
			return a, nil
		}
		return a, a.loadNode()

	case firewallDoneMsg:
		a.busy = false
		if msg.err != nil {
			a.setStatus(ui.StatusError, "tui-firewall: "+runner.FirstLine(msg.err.Error()))
		} else {
			a.setStatus(ui.StatusInfo, "back from tui-firewall · the ports are read again")
		}
		return a, a.reprobeRead(false)

	case ui.RunningTickMsg:
		if a.running == "" {
			// The plan returned; the tick stops here.
			return a, nil
		}
		a.setStatus(ui.StatusInfo, ui.RunningMessage(a.running, time.Since(a.runningSince)))
		return a, runningTick()

	case planRanMsg:
		a.busy, a.running = false, ""
		a.planResult(msg)
		if msg.plan.Action == tailscale.ActionTrustCA && msg.err == nil && a.join.server != "" {
			// The CA is trusted now: check again, and go on with the join.
			return a, tea.Batch(a.reloadAfterChange(), a.checkJoinTLS(a.join.server))
		}
		return a, a.reloadAfterChange()

	case hsPlanRanMsg:
		a.busy, a.running = false, ""
		a.loading = true
		switch {
		case msg.err != nil:
			a.setStatus(ui.StatusError, runner.FirstLine(msg.err.Error()))
		case msg.plan.Done != "":
			a.setStatus(ui.StatusOK, msg.plan.Done)
		case msg.plan.Reinstall:
			a.setStatus(ui.StatusOK, "headscale reinstalled · "+headscale.HeadscaleConfigPath+
				" is back (the package's example) · S configures it and starts the unit")
		default:
			a.setStatus(ui.StatusOK, "headscale installed · S configures it and starts the unit")
		}
		return a, a.reloadAfterChange()

	case ranMsg:
		return a, a.ranResult(msg)

	case discoveredMsg:
		return a, a.confirmOIDCChain(msg)

	case tlsCheckedMsg:
		return a, a.tookTLSCheck(msg)

	case joinTLSMsg:
		return a, a.tookJoinTLS(msg)

	case localPKIMsg:
		return a, a.tookLocalPKI(msg)

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	switch a.mode {
	case modeInput:
		cmd, _ := a.input.Update(msg)
		return a, cmd
	case modeFilePicker:
		return a.handleFilePicker(msg)
	}
	return a, nil
}

// planResult turns a plan's outcome into the status line — and, for a join
// that printed a login URL, into the dialog that says to open it.
func (a *app) planResult(msg planRanMsg) {
	if msg.plan.Action == tailscale.ActionJoin {
		// The answers are offered as a join profile once the join went
		// through: now, or when a pending browser login completes.
		answers := a.pendingJoin
		a.pendingJoin = nil
		switch {
		case answers == nil:
		case msg.err == nil && msg.cleanupErr == nil:
			a.offerSave = answers
		case tailscale.ParseLoginURL(msg.output+"\n"+errText(msg.err)) != "":
			a.lastJoin = answers
		}
	}
	if msg.plan.Action == tailscale.ActionSaveProfile && msg.err == nil {
		a.profiles.saved(a.savingText)
		a.savingText = ""
		a.setStatus(ui.StatusOK, "join profile saved in "+a.profiles.path+
			" · j starts from it next time")
		return
	}
	if msg.plan.Action == tailscale.ActionJoin {
		text := msg.output
		if msg.err != nil {
			text += "\n" + msg.err.Error()
		}
		if url := tailscale.ParseLoginURL(text); url != "" {
			// The client stopped waiting (--timeout), which it reports as a
			// failure; the login itself is pending in tailscaled, and this URL
			// is how it finishes.
			//
			// The status line carries the URL alone, so nothing but the URL is
			// there to select; the notice prints it outside its frame.
			a.setStatus(ui.StatusWarn, url)
			a.loginURLShown = url
			a.loginWaiting, a.loginWaitPolls = false, 0
			a.openNotice("Log in to finish joining",
				"tailscale is waiting for a login. Open the URL below in a browser — on "+
					"any machine — and log in (with a self-hosted control plane, this is "+
					"the OIDC login of your identity provider). The node joins as soon as "+
					"the login completes; r re-reads it. The URL also stays on the node "+
					"screen and the status line until then, and `"+toolName+
					" --check | jq -r .loginUrl` prints it too.", url)
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
	case msg.plan.Action == tailscale.ActionInstall:
		// The package manager's first line of output ("Get:1 …") says
		// nothing about the result.
		a.setStatus(ui.StatusOK, "tailscale installed · j joins a tailnet")
	case msg.plan.Action == tailscale.ActionStartDaemon:
		// The read that follows says where the node stands; until then,
		// what was done.
		a.setStatus(ui.StatusOK, "tailscaled started and enabled at boot · reading the node…")
		a.daemonStarted = true
	default:
		if summary := routingSummary(msg.plan); summary != "" {
			a.setStatus(ui.StatusOK, summary)
			return
		}
		summary := strings.TrimSpace(msg.output)
		if summary == "" {
			summary = "done"
		}
		a.setStatusf(ui.StatusOK, "%s: %s", msg.plan.Title, runner.FirstLine(summary))
	}
}

// routingSummary is the status line after an exit-node or subnet-route offer.
// Their plans turn on IP forwarding first, and the first line of their output
// is sysctl's echo of it ("net.ipv4.ip_forward = 1"), which says nothing about
// the result; what is left to do, the approval on the control plane, does.
func routingSummary(plan tailscale.Plan) string {
	// An offer carries the forwarding steps; a withdrawal is the flag alone.
	offered := len(plan.Steps) > 1
	switch plan.Action {
	case tailscale.ActionAdvertiseExitNode:
		if offered {
			return "exit node offered · approve it on the control plane"
		}
		return "no longer offered as an exit node"
	case tailscale.ActionAdvertiseRoutes:
		if offered {
			return "routes advertised · approve them on the control plane"
		}
		return "no subnet routes advertised"
	}
	return ""
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
		a.cpDraft.forgetSecret()
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
	case modeFilePicker:
		return a.handleFilePicker(msg)
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
	payload := a.confirm.Payload
	a.confirm = ui.Confirm{}
	if !confirmed || payload == nil {
		// The plan — and a pre-auth key or a client secret on its stdin —
		// goes with the dialog. Cancelling also abandons whatever step a
		// chained control-plane flow would take next.
		a.after = nil
		a.cpDraft.forgetSecret()
		a.pendingJoin, a.savingText = nil, ""
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	a.busy = true
	switch p := payload.(type) {
	case tailscale.Plan:
		return a, tea.Batch(a.runPlan(p), a.startRunning(p.Title))
	case headscale.Plan:
		return a, tea.Batch(a.runHSPlan(p), a.startRunning(p.Title))
	case runner.Command:
		a.setStatusf(ui.StatusInfo, "running %s…", a.hs.Preview(p))
		return a, a.run(p)
	}
	a.busy = false
	return a, nil
}

// startRunning puts a plan's name and its elapsed time on the status line,
// refreshed every second until the plan returns.
func (a *app) startRunning(title string) tea.Cmd {
	a.running, a.runningSince = title, time.Now()
	a.setStatus(ui.StatusInfo, ui.RunningMessage(title, 0))
	return runningTick()
}

// runningTick schedules the next refresh of the running message. Tests swap
// it for one that schedules nothing, since the real one sleeps a second.
var runningTick = ui.RunningTick

// handleInput resolves an open text input.
func (a *app) handleInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		return a, cmd
	}
	value := strings.TrimSpace(a.input.Value())
	accepted := a.input.Accepted
	purpose := a.inputPurpose
	payload := a.input.Payload
	a.input = ui.Input{}
	a.inputPurpose = inputNone
	a.mode = modeBrowse
	if !accepted || (value == "" && !acceptsEmpty(purpose)) {
		a.cancelled()
		return a, nil
	}
	switch purpose {
	case inputCreateUser:
		return a, a.openConfirm(headscale.BuildCreateUser(value))
	case inputCreatePreAuthKey:
		return a, a.openConfirmPreAuthKey(value)
	case inputRenameNode:
		id, _ := payload.(string)
		return a, a.openConfirm(headscale.BuildRenameNode(id, value))
	case inputApproveRoutes:
		id, _ := payload.(string)
		return a, a.confirmApproveRoutes(id, value)
	case inputJoinServer:
		return a, a.tookJoinServer(value)
	case inputJoinCA:
		return a, a.tookJoinCA(value)
	case inputJoinKey:
		a.tookJoinKey(value)
	case inputJoinHostname:
		a.tookJoinHostname(value)
	case inputJoinRoutes:
		a.tookJoinRoutes(value)
	case inputSaveProfile:
		a.tookSaveProfileName(value)
	case inputRoutes:
		a.openPlan(tailscale.BuildCommand(tailscale.Request{
			Action: tailscale.ActionAdvertiseRoutes, Routes: tailscale.SplitList(value)}))
	case inputHostname:
		a.openPlan(tailscale.BuildCommand(tailscale.Request{
			Action: tailscale.ActionHostname, Hostname: value}))
	default:
		return a, a.handleControlPlaneInput(purpose, value)
	}
	return a, nil
}

// acceptsEmpty reports whether an empty answer is a real answer rather than a
// cancel: no pre-auth key (log in with a browser), the OS hostname, no routes
// — and on the control plane, "no domains" in an allow list, "keep the secret
// that is already set", no ACME email, no base domain, and an empty routes
// list, which revokes every approval (previewed as a danger dialog).
func acceptsEmpty(purpose inputPurpose) bool {
	switch purpose {
	case inputJoinKey, inputJoinHostname, inputJoinRoutes, inputRoutes, inputSaveProfile,
		inputOIDCDomains, inputOIDCGroups, inputOIDCUsers, inputOIDCSecret,
		inputACMEEmail, inputBaseDomain, inputApproveRoutes,
		inputDNSBase, inputDNSGlobal, inputDNSSearch, inputDNSSplitServers, inputDNSRecord:
		return true
	}
	return false
}

// cancelled abandons whatever form was open, forgetting a typed key or client
// secret with it.
func (a *app) cancelled() {
	a.join = joinDraft{}
	a.exitChoices = nil
	a.profileChoices = nil
	a.saveDraft = tailscale.JoinProfile{}
	a.registerDraft = ""
	a.pairChoices, a.caChoices = nil, nil
	a.cpDraft.forgetSecret()
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
	case pickerJoinProfile:
		a.tookJoinProfile(choice)
	case pickerLoginProfile:
		a.tookLoginProfile(choice)
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
	case pickerOIDCOnlyStart:
		a.cpDraft.onlyStart = choice == pickerYes
		return a, a.askOIDCPKCE()
	case pickerOIDCPKCE:
		a.cpDraft.pkce = choice == pickerYes
		return a, a.discoverIssuer()
	case pickerTransport:
		return a, a.tookTransport(choice)
	case pickerACMEChallenge:
		return a, a.tookChallenge(choice)
	case pickerOIDCProvider:
		return a, a.tookOIDCProvider(choice)
	case pickerDNSMagic:
		return a, a.tookDNSMagic(choice == pickerYes)
	case pickerDNSOverride:
		return a, a.tookDNSOverride(choice == pickerYes)
	case pickerDNSNew:
		return a, a.tookDNSNew(choice)
	case pickerRegistration:
		return a, a.tookRegistration(choice)
	case pickerRegisterUser:
		return a, a.tookRegisterUser(choice)
	case pickerIssuedPair:
		return a, a.tookIssuedPair(choice)
	case pickerJoinCA:
		return a, a.tookLocalCA(choice)
	}
	return a, nil
}

// handleBrowseKey handles the tabbed browse view. j and h are actions here —
// join and hostname — so the selection moves with the arrows. The action keys
// belong to the screen they are pressed on: the node's on the node and peers
// screens, the control plane's on the users, nodes and keys screens.
func (a *app) handleBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.mode = modeHelp
		return a, nil
	case "tab", "right":
		a.setScreen((a.screen + 1) % screenCount)
		return a, nil
	case "shift+tab", "left":
		a.setScreen((a.screen + screenCount - 1) % screenCount)
		return a, nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// A digit jumps to a screen, so a new tab needs no new key binding.
		if n := screen(key[0] - '1'); n < screenCount {
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
		// On the control plane's nodes screen r is the routes of the selected
		// node; ctrl+r still reloads there, as everywhere.
		if key == "r" && a.screen == screenNodes {
			break
		}
		// A reload by hand shows what the socket says, whatever it is, and
		// looks for the binaries again: one installed in another terminal
		// (tui-firewall, say) shows up without a restart (issue #25).
		return a, a.reprobeRead(false)
	}
	if a.screen.controlPlane() {
		return a, a.handleControlPlaneKey(key)
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
	if action == tailscale.ActionUp && a.state.DaemonStartable() {
		// u on a stopped tailscaled starts it (issue #18): the fix the
		// screen names, previewed, instead of a command to type elsewhere.
		a.openPlan(tailscale.BuildCommand(tailscale.Request{
			Action: tailscale.ActionStartDaemon, DaemonEnabled: a.state.DaemonEnabled}))
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
		plan, err := tailscale.BuildCommand(tailscale.Request{Action: action})
		if p, ok := a.profiles.matching(s.Prefs); ok {
			plan.Body += "\n\nThe join profile " + p.Name + " matches this node's settings: " +
				"j restores them from it (with a new login or pre-auth key)."
		} else {
			plan.Body += "\n\nNo join profile matches this node's settings: the next j " +
				"offers to save its answers as one."
		}
		a.openPlan(plan, err)
	case tailscale.ActionSwitchProfile:
		a.openLoginProfilePicker()
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

// notice is a message the user only has to read, with at most one value to
// copy out of it.
type notice struct {
	title, body string
	// copyable is printed on a line of its own, outside the frame and never
	// wrapped: a terminal selection across a frame picks up the border
	// characters, and a wrapped URL is two half URLs.
	copyable string
}

// openNotice opens a message the user only has to read.
func (a *app) openNotice(title, body, copyable string) {
	a.notice = notice{title: title, body: body, copyable: copyable}
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

// startJoin opens the first question: the login server. When headscale is set
// up on this very host, its server_url is the answer offered: joining this
// host to its own control plane is the usual first node.
func (a *app) startJoin() {
	a.join = joinDraft{}
	if len(a.profiles.list) > 0 {
		a.openJoinProfilePicker()
		return
	}
	a.startJoinQuestions()
}

// startJoinQuestions opens the join form without a profile.
func (a *app) startJoinQuestions() {
	a.join = joinDraft{}
	server := a.state.Prefs.ControlURL
	if tailscale.IsTailscaleControl(server) {
		// A fresh client carries Tailscale's server as its default; the
		// question is usually about a self-hosted one.
		server = ""
	}
	if local := a.localServerURL(); local != "" {
		server = local
	}
	a.askJoinServer(server, "")
}

// localServerURL is the server_url of the headscale on this host, when one
// is configured for clients: the "this control plane" answer to the join's
// first question.
func (a *app) localServerURL() string {
	cp := a.hsState.ControlPlane
	if !a.hsState.Present || !headscale.ControlPlaneConfigured(cp) ||
		tailscale.ServerURLProblem(cp.ServerURL) != "" {
		return ""
	}
	return strings.TrimRight(cp.ServerURL, "/")
}

// askJoinServer asks for the login server, with the problem of the last
// answer when there was one.
func (a *app) askJoinServer(value, problem string) {
	help := "Join step 1 of 6 — the control server this node registers with: a Headscale's " +
		"server_url, or " + tailscale.DefaultControlURL + " for Tailscale's own."
	if p := a.join.from; p != nil {
		help = "Join profile " + p.Name + " — every step is pre-filled from it and still " +
			"editable.\n\n" + help
	} else if local := a.localServerURL(); local != "" {
		help += "\n\nThis control plane: headscale on this host serves " + local +
			", so that is the answer offered."
		if current := strings.TrimRight(a.state.Prefs.ControlURL, "/"); current != "" &&
			!strings.EqualFold(current, local) && !tailscale.IsTailscaleControl(current) {
			help += " The node is joined to " + current + " now."
		}
	}
	if problem != "" {
		help = "⚠ " + problem + "\n\n" + help
	}
	a.openInput(inputJoinServer, "Login server", "https://headscale.example.com", value, help)
}

// tookJoinServer validates the server and, for an https one, checks its
// certificate from this machine before asking for the key.
func (a *app) tookJoinServer(value string) tea.Cmd {
	value = strings.TrimRight(value, "/")
	if problem := tailscale.ServerURLProblem(value); problem != "" {
		a.askJoinServer(value, problem)
		return nil
	}
	a.join.server = value
	if tailscale.IsHTTPS(value) {
		return a.checkJoinTLS(value)
	}
	a.askJoinKey()
	return nil
}

// askJoinKey asks for the pre-auth key, masked.
func (a *app) askJoinKey() {
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
	if p := a.join.from; p != nil {
		current = p.Hostname
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
	accept := a.state.Prefs.RouteAll
	if p := a.join.from; p != nil {
		accept = p.AcceptRoutes
	}
	a.openPicker(pickerJoinAcceptRoutes,
		"Join step 4 of 6 — accept the subnet routes other nodes advertise?", accept)
}

// askJoinRoutes asks for the subnets to advertise.
func (a *app) askJoinRoutes() {
	routes := a.state.Prefs.SubnetRoutes()
	if p := a.join.from; p != nil {
		routes = p.AdvertiseRoutes
	}
	a.openInput(inputJoinRoutes, "Advertise subnet routes (optional)", "192.168.1.0/24",
		strings.Join(routes, ", "),
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
	offer := a.state.Prefs.AdvertisesExitNode()
	if p := a.join.from; p != nil {
		offer = p.AdvertiseExitNode
	}
	a.openPicker(pickerJoinExitNode,
		"Join step 6 of 6 — offer this node as an exit node?", offer)
}

// confirmJoin builds the join and opens its confirm. The key leaves the draft
// here: from now on it exists only on the stdin of the command that writes it.
func (a *app) confirmJoin(exitNode bool) {
	d := a.join
	a.join = joinDraft{}
	// The answers without the key: what a join profile would keep.
	answers := tailscale.JoinProfile{LoginServer: d.server, Hostname: d.hostname,
		AcceptRoutes: d.acceptRoutes, AdvertiseRoutes: d.routes, AdvertiseExitNode: exitNode}
	if d.from != nil {
		answers.Name = d.from.Name
	}
	a.pendingJoin = &answers
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
func (a *app) setScreen(s screen) {
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
	switch a.screen {
	case screenPeers:
		return len(a.state.Peers)
	case screenUsers:
		return len(a.hsState.Users)
	case screenNodes:
		return len(a.hsState.Nodes)
	case screenKeys:
		return len(a.hsState.PreAuthKeys)
	case screenDNS:
		return len(a.dnsRows())
	}
	return 0
}

// --- the pending login (issue #10) --------------------------------------------

// followLogin keeps the status line honest about a login it announced. While
// the status line carries a pending login URL, each read either finds the same
// URL (nothing to say), a new one (the line shows the new one), the node
// logged in (the dead link is replaced by who joined), or no URL any more.
//
// A URL that is gone is not yet an expired login (issue #20): headscale
// confirms the registration before tailscaled has taken it, and the node goes
// NeedsLogin, then NoState or Starting, then Running, all without a URL. So a
// vanished URL starts a wait: the node is re-read until it runs (who joined),
// until it has said NeedsLogin for loginGracePolls reads in a row (expired),
// or until loginWaitLimit reads (r re-reads by hand). When the login
// completes while its notice is still open, the notice closes: there is
// nothing left to open.
func (a *app) followLogin() {
	shown := a.loginURLShown
	if shown == "" && !a.loginWaiting {
		return
	}
	n := a.state.Node
	switch {
	case n.AuthURL != "" && n.AuthURL == shown:
		return
	case n.AuthURL != "":
		// A new URL (or the old one back after a wait): follow it.
		a.loginWaiting, a.loginWaitPolls = false, 0
		previous := shown
		a.setStatus(ui.StatusWarn, n.AuthURL)
		a.loginURLShown = n.AuthURL
		if a.mode == modeNotice && previous != "" && a.notice.copyable == previous {
			a.notice.copyable = n.AuthURL
		}
		return
	}
	if shown != "" && a.mode == modeNotice && a.notice.copyable == shown {
		a.mode = modeBrowse
	}
	if a.state.LoggedIn() && n.BackendState == tailscale.StateRunning {
		a.loginWaiting, a.loginWaitPolls = false, 0
		a.setStatus(ui.StatusOK, joinedLine(n))
		a.loginCompleted()
		return
	}
	if !a.loginWaiting {
		// The URL just went away: wait for the node before calling it.
		a.loginWaiting, a.loginWaitPolls = true, 0
	}
	switch loginVerdict(n.BackendState, a.loginWaitPolls) {
	case loginExpired:
		a.loginWaiting, a.loginWaitPolls = false, 0
		a.setStatus(ui.StatusWarn, loginExpiredLine)
	case loginGaveUp:
		a.loginWaiting, a.loginWaitPolls = false, 0
		a.setStatusf(ui.StatusWarn, "the login went through but the node is still %s · "+
			"r re-reads", stateWord(n.BackendState))
	default:
		a.setStatusf(ui.StatusInfo, "the login link is gone · waiting for the node to come up (%s) · "+
			"r re-reads", stateWord(n.BackendState))
	}
}

// The verdicts on a login whose URL is gone and whose node is not running.
const (
	loginStillWaiting = iota
	loginExpired
	loginGaveUp
)

// loginVerdict decides what a vanished login URL means, given the node's
// state and the re-reads already spent waiting: a node that still needs a
// login after the grace period lost it; a node on its way up is waited for,
// up to loginWaitLimit.
func loginVerdict(state string, polls int) int {
	switch state {
	case tailscale.StateNeedsLogin, tailscale.StateNoState, "":
		if polls >= loginGracePolls {
			return loginExpired
		}
	default:
		if polls >= loginWaitLimit {
			return loginGaveUp
		}
	}
	return loginStillWaiting
}

// replaceStaleLogin swaps a login line that a read has made untrue for the
// truth: a node found running and logged in under an "expired" line (the
// login went through after all, as a re-read by hand shows) says who joined.
func (a *app) replaceStaleLogin() {
	n := a.state.Node
	if a.status != loginExpiredLine || !a.state.LoggedIn() ||
		n.BackendState != tailscale.StateRunning {
		return
	}
	a.setStatus(ui.StatusOK, joinedLine(n))
	a.loginCompleted()
}

// followDaemonStart says where the node stands once the tailscaled u started
// answers: logged out (j joins), or the state it came back in. While the
// settling re-read still finds it down, the line is left to the settle logic.
func (a *app) followDaemonStart() {
	if !a.daemonStarted || !a.state.DaemonRunning {
		if a.daemonStarted && !a.settling {
			// The settle retry was spent and it still does not answer.
			a.daemonStarted = false
		}
		return
	}
	a.daemonStarted = false
	n := a.state.Node
	if !a.state.LoggedIn() && n.AuthURL == "" {
		a.setStatus(ui.StatusOK, "tailscaled is running · logged out · j joins a tailnet")
		return
	}
	a.setStatusf(ui.StatusOK, "tailscaled is running · %s", stateLine(n))
}

// joinedLine says who joined which tailnet, for the status line that replaces
// a login URL once the login went through.
func joinedLine(n tailscale.Node) string {
	line := "joined"
	if n.TailnetName != "" {
		line += " " + n.TailnetName
	}
	if n.User != "" {
		line += " as " + n.User
	}
	if len(n.IPs) > 0 {
		line += " · " + n.IPs[0]
	}
	return line
}

// pollLogin schedules the next re-read while a login is pending, bounded per
// URL: a new URL starts a new count, and no pending login resets it. After the
// URL is gone, it keeps re-reading while followLogin waits for the node.
func (a *app) pollLogin() tea.Cmd {
	url := a.state.Node.AuthURL
	if url == "" {
		a.loginPolls, a.loginPollURL = 0, ""
		if !a.loginWaiting || a.loginPolling {
			return nil
		}
		// The URL is gone and the node is on its way up: keep re-reading
		// until followLogin reaches a verdict.
		a.loginPolling = true
		return tea.Tick(loginPollDelay, func(time.Time) tea.Msg { return loginPollMsg{} })
	}
	if url != a.loginPollURL {
		a.loginPolls, a.loginPollURL = 0, url
	}
	if a.loginPolling || a.loginPolls >= loginPollLimit {
		return nil
	}
	a.loginPolling = true
	return tea.Tick(loginPollDelay, func(time.Time) tea.Msg { return loginPollMsg{} })
}

// loginCompleted is the hook a completed browser login runs: the join form's
// answers, when this session collected them, are offered as a join profile.
func (a *app) loginCompleted() {
	if a.lastJoin != nil {
		a.offerSave, a.lastJoin = a.lastJoin, nil
	}
}

// errText is an error's text, empty for none.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
