// Package tailscale is the part of tui-tailscale that is about its own
// subject: this host as a node of a tailnet, driven through the `tailscale`
// client — whichever control plane it answers to, Tailscale's own or a
// self-hosted Headscale. Everything generic (palette, widgets, configuration,
// running commands) comes from tui-kit and is not repeated here.
//
// This package is also the tool's single exec site: the only place a process
// is started (through the kit runner) is internal/tailscale, so the command
// the confirm dialog showed is provably the command that ran. It drives
// `tailscale` for every read and every change to the node, plus a handful of
// helpers for the steps around it: `install` and `rm` for the pre-auth key
// file, `sysctl` for IP forwarding, and `curl`, the package manager and
// `systemctl` for the companion install.
//
// PRIVACY: a pre-auth key never reaches an argv. It is typed masked, travels
// on the standard input of `install`, which writes it to a root-only file
// under /run, and `tailscale up` is handed the file's PATH (`--authkey
// file:…`), never the value. The file is removed after the join, whether the
// join worked or not. The model never holds the key after the command that
// writes it has been built.
package tailscale

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// The backend states `tailscale status --json` reports, as ipn.State names
// them. Only the ones the UI treats differently are named here.
const (
	StateRunning    = "Running"
	StateStopped    = "Stopped"
	StateNeedsLogin = "NeedsLogin"
	// StateNeedsMachineAuth is a login the control plane has not approved
	// yet: the node is registered and waits for an administrator.
	StateNeedsMachineAuth = "NeedsMachineAuth"
	StateNoState          = "NoState"
	StateStarting         = "Starting"
)

// DefaultControlURL is the login server a client uses when none is given:
// Tailscale's own coordination server.
const DefaultControlURL = "https://controlplane.tailscale.com"

// Prefs are the node's settings, as `tailscale debug prefs` prints them. Only
// the fields the tool shows or changes are kept; the rest of that output —
// including its Config block, which names keys — is never parsed into
// anything.
type Prefs struct {
	// ControlURL is the login server this node registers with.
	ControlURL string `json:"ControlURL"`
	// RouteAll is --accept-routes: use the subnet routes other nodes offer.
	RouteAll bool `json:"RouteAll"`
	// ExitNodeID is the stable id of the exit node in use, empty for none.
	ExitNodeID string `json:"ExitNodeID"`
	// ExitNodeIP is the exit node picked by address, before it resolves.
	ExitNodeIP string `json:"ExitNodeIP"`
	// CorpDNS is --accept-dns, which is how MagicDNS reaches this host.
	CorpDNS bool `json:"CorpDNS"`
	// WantRunning is false after `tailscale down`.
	WantRunning bool `json:"WantRunning"`
	// LoggedOut is true after `tailscale logout`.
	LoggedOut bool `json:"LoggedOut"`
	// Hostname is the name set with --hostname; empty uses the OS hostname.
	Hostname string `json:"Hostname"`
	// AdvertiseRoutes is every prefix this node offers, the two default
	// routes of an exit node included.
	AdvertiseRoutes []string `json:"AdvertiseRoutes"`
}

// AdvertisesExitNode reports whether the node offers itself as an exit node,
// which tailscale records as advertising both default routes.
func (p Prefs) AdvertisesExitNode() bool {
	v4, v6 := false, false
	for _, route := range p.AdvertiseRoutes {
		switch route {
		case "0.0.0.0/0":
			v4 = true
		case "::/0":
			v6 = true
		}
	}
	return v4 && v6
}

// SubnetRoutes is AdvertiseRoutes without the exit node's default routes,
// which are shown as their own switch.
func (p Prefs) SubnetRoutes() []string {
	var routes []string
	for _, route := range p.AdvertiseRoutes {
		if isDefaultRoute(route) {
			continue
		}
		routes = append(routes, route)
	}
	return routes
}

// UsesExitNode reports whether traffic is sent through an exit node.
func (p Prefs) UsesExitNode() bool { return p.ExitNodeID != "" || p.ExitNodeIP != "" }

// isDefaultRoute reports whether a prefix is one of the two default routes.
func isDefaultRoute(route string) bool { return route == "0.0.0.0/0" || route == "::/0" }

// Node is this host as the tailnet sees it.
type Node struct {
	// BackendState is ipn's state: Running, Stopped, NeedsLogin…
	BackendState string `json:"backendState"`
	// AuthURL is the pending interactive login, when there is one. It is
	// shown on the node screen because it is how an OIDC login finishes.
	AuthURL string `json:"-"`
	// Version is the client's version.
	Version string `json:"version"`
	// ID is the node's stable id.
	ID string `json:"-"`
	// HostName is the machine's name; DNSName its MagicDNS name.
	HostName string `json:"-"`
	DNSName  string `json:"-"`
	// TailnetName and MagicDNSSuffix describe the tailnet it belongs to.
	TailnetName    string `json:"-"`
	MagicDNSSuffix string `json:"-"`
	MagicDNS       bool   `json:"magicDns"`
	// User is the login name of the node's owner.
	User string `json:"-"`
	// IPs are the node's tailnet addresses.
	IPs []string `json:"-"`
	// Online is the control plane's view of this node.
	Online bool `json:"online"`
	// Health carries the client's own warnings.
	Health []string `json:"health,omitempty"`
}

// Peer is another node of the tailnet.
type Peer struct {
	ID       string   `json:"id"`
	HostName string   `json:"hostName"`
	DNSName  string   `json:"dnsName"`
	OS       string   `json:"os"`
	User     string   `json:"user"`
	IPs      []string `json:"ips"`
	Online   bool     `json:"online"`
	Active   bool     `json:"active"`
	// ExitNodeOption reports that the peer offers itself as an exit node;
	// ExitNode that this node is using it as one.
	ExitNodeOption bool `json:"exitNodeOption"`
	ExitNode       bool `json:"exitNode"`
	// Routes are the subnets the peer serves: its primary routes and any
	// allowed prefix beyond its own addresses, default routes excluded.
	Routes   []string  `json:"routes"`
	LastSeen time.Time `json:"lastSeen"`
	Tags     []string  `json:"tags,omitempty"`
}

// Name is the peer's short name: the first label of its MagicDNS name, or
// its host name when it has none.
func (p Peer) Name() string {
	if label, _, ok := strings.Cut(strings.TrimSuffix(p.DNSName, "."), "."); ok && label != "" {
		return label
	}
	if p.DNSName != "" {
		return strings.TrimSuffix(p.DNSName, ".")
	}
	return p.HostName
}

// IPv4 is the peer's first IPv4 tailnet address, which is what `--exit-node`
// is given: an address resolves on every control plane, a name does not.
func (p Peer) IPv4() string {
	for _, ip := range p.IPs {
		if addr, err := netip.ParseAddr(ip); err == nil && addr.Is4() {
			return ip
		}
	}
	if len(p.IPs) > 0 {
		return p.IPs[0]
	}
	return ""
}

// State is everything one read produces. Each part can be empty without the
// others failing: an uninstalled client still has a distribution to install
// on, and a stopped daemon still has a version.
type State struct {
	// Installed reports that the tailscale binary was found.
	Installed bool
	// Distro is what /etc/os-release says, for the install instructions.
	Distro Distro

	// DaemonRunning reports that tailscaled answered.
	DaemonRunning bool
	// NotRunning reports that the client is installed but tailscaled did not
	// answer: the unit is stopped or disabled.
	NotRunning bool
	// PermissionDenied reports that the socket refused this user, and
	// escalating did not help (no sudo, or a password it could not ask for).
	PermissionDenied bool
	// Error is why the status could not be read, one line.
	Error string

	Node  Node
	Prefs Prefs
	// PrefsRead reports that Prefs came from the machine; PrefsError says
	// why it did not.
	PrefsRead  bool
	PrefsError string

	Peers []Peer

	// LoginProfiles are tailscale's own login profiles (`tailscale switch
	// --list`), one per identity the client remembers. Not to be confused with
	// the tool's join profiles, which are presets for j.
	LoginProfiles []LoginProfile
}

// CurrentLoginProfile is the login profile in use, when the list was read.
func (s State) CurrentLoginProfile() (LoginProfile, bool) {
	for _, p := range s.LoginProfiles {
		if p.Current {
			return p, true
		}
	}
	return LoginProfile{}, false
}

// LoggedIn reports whether the node holds a login it would keep across a
// restart: anything but a node that was never logged in or logged out.
func (s State) LoggedIn() bool {
	if !s.DaemonRunning || s.Prefs.LoggedOut {
		return false
	}
	switch s.Node.BackendState {
	case StateRunning, StateStopped, StateNeedsMachineAuth, StateStarting:
		return true
	}
	return false
}

// ExitNodeOptions are the peers that offer themselves as an exit node.
func (s State) ExitNodeOptions() []Peer {
	var peers []Peer
	for _, p := range s.Peers {
		if p.ExitNodeOption {
			peers = append(peers, p)
		}
	}
	return peers
}

// CurrentExitNode is the peer in use as exit node, when there is one.
func (s State) CurrentExitNode() (Peer, bool) {
	for _, p := range s.Peers {
		if p.ExitNode {
			return p, true
		}
	}
	for _, p := range s.Peers {
		if s.Prefs.ExitNodeID != "" && p.ID == s.Prefs.ExitNodeID {
			return p, true
		}
		if s.Prefs.ExitNodeIP != "" && contains(p.IPs, s.Prefs.ExitNodeIP) {
			return p, true
		}
	}
	return Peer{}, false
}

// contains reports whether a list holds a value.
func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// Action is something the user can do. Each maps to one previewed plan.
type Action string

const (
	// ActionJoin runs `tailscale up --login-server=… --reset`.
	ActionJoin Action = "join"
	// ActionAcceptRoutes toggles --accept-routes.
	ActionAcceptRoutes Action = "accept-routes"
	// ActionAdvertiseRoutes replaces the advertised subnet routes.
	ActionAdvertiseRoutes Action = "advertise-routes"
	// ActionExitNode picks the exit node to use, or none.
	ActionExitNode Action = "exit-node"
	// ActionAdvertiseExitNode toggles offering this node as an exit node.
	ActionAdvertiseExitNode Action = "advertise-exit-node"
	// ActionHostname sets the name the node registers with.
	ActionHostname Action = "hostname"
	// ActionLogout logs the node out of the tailnet.
	ActionLogout Action = "logout"
	// ActionDown disconnects without logging out.
	ActionDown Action = "down"
	// ActionUp reconnects after a down.
	ActionUp Action = "up"
	// ActionInstall installs the client from the distribution's package
	// manager and Tailscale's own repository.
	ActionInstall Action = "install"
	// ActionSwitchProfile switches to another of tailscale's login profiles.
	ActionSwitchProfile Action = "switch-profile"
	// ActionSaveProfile writes a join profile to the tool's config file. It
	// has no key of its own: it is offered after a join.
	ActionSaveProfile Action = "save-profile"
)

// ActionSpec describes one action for the key map and the help screen, so the
// two cannot drift apart. The confirm dialog's text comes from the Plan the
// action builds, because most of it depends on the answers typed.
type ActionSpec struct {
	Action Action
	// Key is the key that triggers it.
	Key string
	// Label is the short name in the help bar.
	Label string
	// Help is the line on the help screen.
	Help string
}

// Actions is the action table, in help-screen order.
var Actions = []ActionSpec{
	{Action: ActionJoin, Key: "j", Label: "join",
		Help: "join a tailnet: login server, optional pre-auth key, hostname, routes"},
	{Action: ActionAcceptRoutes, Key: "a", Label: "accept routes",
		Help: "toggle accepting the subnet routes other nodes offer"},
	{Action: ActionAdvertiseRoutes, Key: "A", Label: "adv. routes",
		Help: "edit the subnet routes this node advertises"},
	{Action: ActionExitNode, Key: "x", Label: "exit node",
		Help: "pick the exit node to use, from the peers offering one, or none"},
	{Action: ActionAdvertiseExitNode, Key: "E", Label: "adv. exit",
		Help: "toggle offering this node as an exit node"},
	{Action: ActionHostname, Key: "h", Label: "hostname",
		Help: "set the name this node registers with"},
	{Action: ActionDown, Key: "d", Label: "down",
		Help: "disconnect from the tailnet, keeping the login"},
	{Action: ActionUp, Key: "u", Label: "up",
		Help: "reconnect after a down"},
	{Action: ActionLogout, Key: "L", Label: "logout",
		Help: "log this node out of the tailnet"},
	{Action: ActionSwitchProfile, Key: "p", Label: "login profile",
		Help: "switch between tailscale's own login profiles (when it remembers more than one)"},
	{Action: ActionInstall, Key: "i", Label: "install",
		Help: "install tailscale from the package manager (when it is absent)"},
}

// ActionFor returns the spec bound to a key.
func ActionFor(key string) (ActionSpec, bool) {
	for _, spec := range Actions {
		if spec.Key == key {
			return spec, true
		}
	}
	return ActionSpec{}, false
}

// Request is everything an action needs to become a Plan. Each action reads
// only its own fields.
type Request struct {
	Action Action

	// LoginServer, AuthKey, Hostname, AcceptRoutes, Routes and ExitNodeOffer
	// are the join form's answers. AuthKey is a secret: it leaves this struct
	// only as the stdin of the command that writes it to a root-only file.
	LoginServer   string
	AuthKey       string
	Hostname      string
	AcceptRoutes  bool
	Routes        []string
	ExitNodeOffer bool
	// CurrentControlURL and LoggedIn describe the node before the join: a
	// logged-in node moving to another login server needs --force-reauth.
	CurrentControlURL string
	LoggedIn          bool

	// Enable is the new value of a toggle (accept routes, advertise exit
	// node).
	Enable bool
	// ExitNode is the address of the exit node to use, empty for none.
	ExitNode string

	// Distro is the machine the install plan is for.
	Distro Distro

	// LoginProfile is the id of the login profile to switch to.
	LoginProfile string
}

// Plan is what one confirm dialog shows and one confirmation runs: a few
// commands in order, stopping at the first failure, then the cleanup
// commands whatever happened. Most plans are one command; a join with a
// pre-auth key is three, because the key has to reach tailscale through a
// file rather than an argv, and that file must not outlive the join.
type Plan struct {
	Action Action
	// Title is the dialog title; Body explains, above the command preview.
	Title string
	Body  string
	// Steps run in order; the first failure stops them.
	Steps []runner.Command
	// Cleanup runs after the steps, whether they succeeded or not.
	Cleanup []runner.Command
	// Destructive paints the dialog in the danger color.
	Destructive bool
}

// Commands is every command of the plan, in the order it runs.
func (p Plan) Commands() []runner.Command {
	return append(append([]runner.Command{}, p.Steps...), p.Cleanup...)
}

// Fixed paths and values the plans use.
const (
	// AuthKeyPath is where a pre-auth key lives for the length of a join:
	// /run is a root-owned tmpfs, so the file never reaches a disk, and it is
	// written mode 600 by root.
	AuthKeyPath = "/run/tui-tailscale.authkey"
	// SysctlPath is the drop-in that keeps IP forwarding on across reboots.
	SysctlPath = "/etc/sysctl.d/99-tailscale.conf"
	// SysctlContent is that drop-in, the settings tailscale's own
	// documentation asks a subnet router or an exit node for.
	SysctlContent = "net.ipv4.ip_forward = 1\nnet.ipv6.conf.all.forwarding = 1\n"
	// JoinTimeout bounds how long `tailscale up` waits for the node to reach
	// Running. Without it, a browser login blocks the command until the user
	// has logged in; with it, the command prints the login URL and returns,
	// and the login goes on in tailscaled.
	JoinTimeout = "20s"
)

// BuildCommand turns a request into the plan the confirm dialog previews and
// the runner executes. It is shared by the real and the fake backend, so
// --demo previews exactly what the real thing would run, and it is the only
// place in the tool that builds a command line.
func BuildCommand(req Request) (Plan, error) {
	switch req.Action {
	case ActionJoin:
		return buildJoin(req)
	case ActionAcceptRoutes:
		return Plan{
			Action: req.Action,
			Title:  "Accept routes: " + onOff(req.Enable),
			Body: acceptRoutesBody(req.Enable) +
				"\n\n`tailscale set` changes this one setting and leaves every other one as it is.",
			Steps: []runner.Command{setFlag("accept-routes", boolFlag(req.Enable),
				"Accept routes "+onOff(req.Enable))},
		}, nil
	case ActionAdvertiseRoutes:
		return buildAdvertiseRoutes(req)
	case ActionExitNode:
		return buildExitNode(req)
	case ActionAdvertiseExitNode:
		return buildAdvertiseExitNode(req)
	case ActionHostname:
		if !ValidHostname(req.Hostname) {
			return Plan{}, fmt.Errorf("not a valid hostname (letters, digits and dashes, "+
				"up to 63): %q", req.Hostname)
		}
		return Plan{
			Action: req.Action,
			Title:  "Set the hostname to " + req.Hostname,
			Body: "The node registers under this name, and its MagicDNS name follows it " +
				"(the control plane may add a suffix when the name is taken).",
			Steps: []runner.Command{setFlag("hostname", req.Hostname,
				"Set hostname "+req.Hostname)},
		}, nil
	case ActionLogout:
		return Plan{
			Action: req.Action,
			Title:  "Log out of the tailnet",
			Body: "The node disconnects and forgets its login; joining again needs a new " +
				"login or pre-auth key. If you are connected to this machine over the " +
				"tailnet, this ends that session.",
			Steps: []runner.Command{{Argv: []string{"tailscale", "logout"},
				Description: "Log out", Destructive: true}},
			Destructive: true,
		}, nil
	case ActionDown:
		return Plan{
			Action: req.Action,
			Title:  "Disconnect from the tailnet",
			Body: "The node goes offline but keeps its login, so `u` brings it back " +
				"without logging in again. If you are connected to this machine over the " +
				"tailnet, this ends that session.",
			Steps: []runner.Command{{Argv: []string{"tailscale", "down"},
				Description: "Disconnect", Destructive: true}},
			Destructive: true,
		}, nil
	case ActionUp:
		return Plan{
			Action: req.Action,
			Title:  "Reconnect to the tailnet",
			Body: "A plain `tailscale up` brings back the connection `down` stopped, with " +
				"the settings the node already has.",
			Steps: []runner.Command{{Argv: []string{"tailscale", "up"},
				Description: "Reconnect"}},
		}, nil
	case ActionInstall:
		return buildInstall(req.Distro)
	case ActionSwitchProfile:
		return buildSwitchProfile(req)
	case "":
		return Plan{}, fmt.Errorf("no action given")
	}
	return Plan{}, fmt.Errorf("unknown action %q", req.Action)
}

// buildJoin assembles the join: the key file when a pre-auth key was typed,
// IP forwarding when the node will route for others, `tailscale up`, and the
// removal of the key file.
func buildJoin(req Request) (Plan, error) {
	server := strings.TrimRight(strings.TrimSpace(req.LoginServer), "/")
	if problem := ServerURLProblem(server); problem != "" {
		return Plan{}, fmt.Errorf("login server: %s", problem)
	}
	if req.AuthKey != "" && !ValidAuthKey(req.AuthKey) {
		// The value is deliberately not quoted: it is a secret.
		return Plan{}, fmt.Errorf("not a valid pre-auth key (one word, no spaces)")
	}
	if req.Hostname != "" && !ValidHostname(req.Hostname) {
		return Plan{}, fmt.Errorf("not a valid hostname: %q", req.Hostname)
	}
	routes, err := NormalizeRoutes(req.Routes)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{Action: ActionJoin, Title: "Join the tailnet at " + server}
	body := []string{}

	if req.AuthKey != "" {
		plan.Steps = append(plan.Steps, runner.Command{
			Argv:        []string{"install", "-m", "600", "/dev/stdin", AuthKeyPath},
			Description: "Write the pre-auth key to " + AuthKeyPath,
			Stdin:       req.AuthKey,
		})
		plan.Cleanup = append(plan.Cleanup, runner.Command{
			Argv:        []string{"rm", "-f", AuthKeyPath},
			Description: "Remove " + AuthKeyPath,
		})
		body = append(body, "The pre-auth key travels on the standard input of `install`, "+
			"which writes it to "+AuthKeyPath+" (root, mode 600, on a tmpfs), and tailscale "+
			"reads it from there: it is on no command line, not even in this dialog. The "+
			"file is removed after the join, whether it worked or not.")
	} else {
		body = append(body, "No pre-auth key: tailscale prints a login URL. It is shown "+
			"here when the command returns — open it in a browser to log in (this is how an "+
			"OIDC login happens). The node stays waiting for that login in the background.")
	}

	if len(routes) > 0 || req.ExitNodeOffer {
		plan.Steps = append(plan.Steps, forwardingSteps()...)
		body = append(body, forwardingNote())
	}

	argv := []string{"tailscale", "up", "--login-server=" + server}
	if req.AuthKey != "" {
		argv = append(argv, "--authkey=file:"+AuthKeyPath)
	}
	if req.Hostname != "" {
		argv = append(argv, "--hostname="+req.Hostname)
	}
	if req.AcceptRoutes {
		argv = append(argv, "--accept-routes")
	}
	if len(routes) > 0 {
		argv = append(argv, "--advertise-routes="+strings.Join(routes, ","))
	}
	if req.ExitNodeOffer {
		argv = append(argv, "--advertise-exit-node")
	}
	if needsForceReauth(req, server) {
		argv = append(argv, "--force-reauth")
		body = append(body, "The node is logged in to "+
			strings.TrimRight(req.CurrentControlURL, "/")+"; tailscale refuses to change "+
			"the login server of a logged-in node without --force-reauth, which logs in again.")
	}
	argv = append(argv, "--timeout="+JoinTimeout, "--reset")
	body = append(body, "--reset returns every setting not on this line to its default "+
		"(MagicDNS on, no exit node, no operator), so tailscale never refuses the change "+
		"for not mentioning a flag set earlier. --timeout stops waiting after "+JoinTimeout+
		"; the join itself goes on.")

	plan.Steps = append(plan.Steps, runner.Command{Argv: argv, Description: plan.Title})
	plan.Body = strings.Join(body, "\n\n")
	return plan, nil
}

// needsForceReauth reports whether the join moves a logged-in node to
// another login server.
func needsForceReauth(req Request, server string) bool {
	if !req.LoggedIn {
		return false
	}
	current := strings.TrimRight(strings.TrimSpace(req.CurrentControlURL), "/")
	if current == "" {
		current = DefaultControlURL
	}
	return !strings.EqualFold(current, server)
}

// buildAdvertiseRoutes replaces the subnet routes the node offers.
func buildAdvertiseRoutes(req Request) (Plan, error) {
	routes, err := NormalizeRoutes(req.Routes)
	if err != nil {
		return Plan{}, err
	}
	joined := strings.Join(routes, ",")
	plan := Plan{Action: ActionAdvertiseRoutes}
	if len(routes) == 0 {
		plan.Title = "Stop advertising subnet routes"
		plan.Body = "The node stops offering any subnet. Offering itself as an exit node " +
			"is a separate switch (E) and is left as it is."
	} else {
		plan.Title = "Advertise " + joined
		plan.Body = "Other nodes can reach these subnets through this one once the control " +
			"plane approves the routes (on Headscale: `headscale nodes approve-routes`). " +
			"Offering itself as an exit node is a separate switch (E) and is left as it " +
			"is.\n\n" + forwardingNote()
		plan.Steps = append(plan.Steps, forwardingSteps()...)
	}
	plan.Steps = append(plan.Steps, setFlag("advertise-routes", joined, plan.Title))
	return plan, nil
}

// buildExitNode picks the exit node to use, or none.
func buildExitNode(req Request) (Plan, error) {
	if req.ExitNode == "" {
		return Plan{
			Action: ActionExitNode,
			Title:  "Stop using an exit node",
			Body:   "Internet traffic leaves this machine directly again.",
			Steps:  []runner.Command{setFlag("exit-node", "", "Stop using an exit node")},
		}, nil
	}
	if _, err := netip.ParseAddr(req.ExitNode); err != nil {
		return Plan{}, fmt.Errorf("not an exit node address: %q", req.ExitNode)
	}
	return Plan{
		Action: ActionExitNode,
		Title:  "Use " + req.ExitNode + " as exit node",
		Body: "All internet traffic from this machine goes out through that node. " +
			"If you are connected to this machine from outside the tailnet, the " +
			"session may drop while the route changes.",
		Steps: []runner.Command{setFlag("exit-node", req.ExitNode,
			"Use exit node "+req.ExitNode)},
	}, nil
}

// buildAdvertiseExitNode toggles offering this node as an exit node.
func buildAdvertiseExitNode(req Request) (Plan, error) {
	plan := Plan{Action: ActionAdvertiseExitNode}
	if !req.Enable {
		plan.Title = "Stop offering this node as an exit node"
		plan.Body = "Other nodes can no longer send their internet traffic through this one. " +
			"Advertised subnet routes are left as they are."
	} else {
		plan.Title = "Offer this node as an exit node"
		plan.Body = "Other nodes can send all their internet traffic through this one once " +
			"the control plane approves it. Advertised subnet routes are left as they " +
			"are.\n\n" + forwardingNote()
		plan.Steps = append(plan.Steps, forwardingSteps()...)
	}
	plan.Steps = append(plan.Steps, setFlag("advertise-exit-node", boolFlag(req.Enable),
		plan.Title))
	return plan, nil
}

// forwardingSteps turn IP forwarding on now and at every boot. tailscale warns
// when a subnet router or an exit node lacks it, but does not set it.
func forwardingSteps() []runner.Command {
	return []runner.Command{
		{
			Argv:        []string{"install", "-m", "644", "/dev/stdin", SysctlPath},
			Description: "Write " + SysctlPath,
			Stdin:       SysctlContent,
		},
		{
			Argv: []string{"sysctl", "-w", "net.ipv4.ip_forward=1",
				"net.ipv6.conf.all.forwarding=1"},
			Description: "Turn on IP forwarding",
		},
	}
}

// forwardingNote explains the forwarding steps in a dialog body.
func forwardingNote() string {
	return "Routing for other nodes needs IP forwarding, which tailscale only warns " +
		"about: " + SysctlPath + " is written with\n" +
		"  " + strings.ReplaceAll(strings.TrimSpace(SysctlContent), "\n", "\n  ") +
		"\nso it survives a reboot, and sysctl applies it now."
}

// acceptRoutesBody explains the accept-routes switch.
func acceptRoutesBody(enable bool) string {
	if enable {
		return "This node starts using the subnet routes other nodes advertise, so it can " +
			"reach the networks behind them."
	}
	return "This node stops using the subnet routes other nodes advertise; the tailnet " +
		"addresses themselves stay reachable."
}

// setFlag builds `tailscale set --<flag>=<value>`: one setting per command, so
// the preview says exactly what changes.
func setFlag(flag, value, description string) runner.Command {
	return runner.Command{
		Argv:        []string{"tailscale", "set", "--" + flag + "=" + value},
		Description: description,
	}
}

// boolFlag renders a boolean the way tailscale's flags parse it.
func boolFlag(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// onOff renders a boolean as a switch.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// --- validation -------------------------------------------------------------

// hostnamePattern is one DNS label, which is what a node name becomes.
var hostnamePattern = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidHostname reports whether s can be a node's hostname.
func ValidHostname(s string) bool { return hostnamePattern.MatchString(s) }

// authKeyPattern is a pre-auth key: Tailscale's are tskey-auth-…, Headscale's
// are hex, and both are one printable word. It is checked so a pasted key
// with a trailing newline or a second word is refused before it is written.
var authKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_+=/.:-]{8,512}$`)

// ValidAuthKey reports whether s is shaped like a pre-auth key.
func ValidAuthKey(s string) bool { return authKeyPattern.MatchString(s) }

// NormalizeRoutes validates a list of subnet routes, dropping blanks. Each has
// to be a prefix whose address has no bits beyond its length (tailscale
// refuses 192.168.1.1/24), and the default routes are refused: offering them
// is what the exit-node switch is for.
func NormalizeRoutes(routes []string) ([]string, error) {
	out := make([]string, 0, len(routes))
	seen := map[string]bool{}
	for _, route := range routes {
		route = strings.TrimSpace(route)
		if route == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(route)
		if err != nil {
			return nil, fmt.Errorf("not a subnet (try 192.168.1.0/24): %q", route)
		}
		if prefix != prefix.Masked() {
			return nil, fmt.Errorf("%s has host bits set: did you mean %s?", route, prefix.Masked())
		}
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf("%s is a default route: use the exit-node switch (E) instead", route)
		}
		if !seen[prefix.String()] {
			seen[prefix.String()] = true
			out = append(out, prefix.String())
		}
	}
	return out, nil
}

// SplitList splits a comma- or space-separated answer into its items.
func SplitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
}

// Backend is the boundary between the UI and the machine. Load reads the model;
// Preview renders the exact command a confirmed action would run; Run executes
// it. Nothing else may start a process.
type Backend interface {
	// Name identifies the backend ("tailscale", "demo").
	Name() string
	// Describe is the one-line summary shown in the header.
	Describe() string
	// Load reads the current state. It never fails as a whole: a missing
	// client or a stopped daemon is a fact in the State, not an error.
	Load(ctx context.Context) (State, error)
	// Preview renders the exact command line Run will execute, with the
	// privilege prefix its binary will really run with.
	Preview(cmd runner.Command) string
	// Run executes a previously previewed command.
	Run(ctx context.Context, cmd runner.Command) (string, error)
	// Reprobe forgets which binaries were found and how to run them, so the
	// next read detects them again: after an install, a binary that was
	// missing at start-up is there.
	Reprobe()
}
