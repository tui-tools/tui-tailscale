package tailscale

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/runner"
)

// Fake is the in-memory backend behind --demo and the tests: a node joined to
// a self-hosted control plane at headscale.example.com (the one
// internal/headscale's demo serves, where this node is registered under
// user@example.com), with two peers: one offering itself as an exit node,
// one serving a subnet. It builds and
// previews exactly the commands the real backend would, then applies each
// confirmed one to its own state the way tailscale would, so every key in the
// demo does something visible and nothing reaches the machine.
//
// Every value here is documentation space: 100.64.0.0/10 addresses from the
// CGNAT range tailnets use, example.com names, and a 192.0.2.0/24 subnet.
type Fake struct {
	mu    sync.Mutex
	state State
	run   *runner.Fake
	// absent is whether the client is on the fake machine; detected whether
	// the backend has found it — which, like the real backend's runner cache,
	// only changes on Reprobe. refuse counts the reads tailscaled refuses
	// while it is still coming up after an install.
	absent, detected bool
	refuse           int
	// completeAfter is how many reads a pending browser login lasts before
	// the demo "logs in" on its own, 0 for never; pendingReads counts them
	// down for the login pending now.
	completeAfter, pendingReads int
	// confirmPhases are the states a login the browser confirmed still
	// reads as, one per read, before Running: the real client goes
	// NeedsLogin (the URL gone) → NoState or Starting → Running, and the
	// node screen must not call that an expired login (issue #20). phases is
	// what is left of them for the login being confirmed now, and
	// confirming says one is.
	confirmPhases, phases []string
	confirming            bool
	// caTrusted records that the trust-CA step ran: from then on the demo's
	// private login servers verify.
	caTrusted bool
}

// DemoLoginServer is the control plane the demo node is joined to.
const DemoLoginServer = "https://headscale.example.com"

// DemoRegisterPath is the path of the login URL the demo prints, the shape
// Headscale's own register page has.
const DemoRegisterPath = "/register/demo-registration-key"

// NewFake returns a Fake preloaded with the demo tailnet.
func NewFake() *Fake {
	f := &Fake{state: demoState(), detected: true}
	f.run = &runner.Fake{Prefix: "sudo -n", Hook: f.apply}
	return f
}

// Name identifies the backend.
func (f *Fake) Name() string { return "demo" }

// Describe is the one-line summary shown in the header.
func (f *Fake) Describe() string { return "tailscale via sudo -n  ·  demo (no changes are applied)" }

// Preview renders the command the way the real backend would: escalated,
// except the commands the real backend runs as the invoking user.
func (f *Fake) Preview(cmd runner.Command) string {
	if !escalates(cmd) {
		return cmd.String()
	}
	return f.run.Preview(cmd)
}

// Run applies a confirmed command to the in-memory state.
func (f *Fake) Run(ctx context.Context, cmd runner.Command) (string, error) {
	return f.run.Run(ctx, cmd)
}

// Commands returns every command the fake was asked to run, for the tests.
func (f *Fake) Commands() []runner.Command { return f.run.Ran }

// SetAbsent takes the client off the fake machine, the way a host without
// tailscale starts: `i` installs it, and the next Reprobe finds it.
func (f *Fake) SetAbsent() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.absent, f.detected = true, false
}

// Reprobe finds the client again, when it is there.
func (f *Fake) Reprobe() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detected = !f.absent
}

// CompleteLoginAfter makes a pending browser login complete by itself after n
// reads, the way it does once somebody opens the URL: --demo uses it so the
// node screen shows the login flip from pending to running (issue #10).
func (f *Fake) CompleteLoginAfter(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeAfter = n
}

// SetConfirmPhases sets the states a confirmed browser login reads as, one per
// read, before the node runs — e.g. NeedsLogin, NoState, Starting — the way
// tailscaled takes a moment to pick up a registration headscale already
// confirmed. None (the default) goes straight to Running.
func (f *Fake) SetConfirmPhases(states ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmPhases = append([]string(nil), states...)
}

// CompleteLogin finishes a pending browser login now, as the browser would,
// straight to Running.
func (f *Fake) CompleteLogin() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Node.AuthURL == "" && !f.confirming {
		return
	}
	f.completeLogin()
}

// ConfirmLogin is the browser confirming a pending login: the URL goes away
// at once, and the node reads as the confirm phases before it runs.
func (f *Fake) ConfirmLogin() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmLogin()
}

// confirmLogin starts the confirm phases, or completes the login when there
// are none.
func (f *Fake) confirmLogin() {
	if f.state.Node.AuthURL == "" {
		return
	}
	if len(f.confirmPhases) == 0 {
		f.completeLogin()
		return
	}
	f.state.Node = Node{BackendState: f.confirmPhases[0], Version: f.state.Node.Version}
	f.phases = append([]string(nil), f.confirmPhases[1:]...)
	f.confirming = true
}

// completeLogin logs the pending node in with the settings it joined with.
func (f *Fake) completeLogin() {
	f.confirming, f.phases = false, nil
	node := demoState().Node
	if h := f.state.Prefs.Hostname; h != "" {
		node.HostName = h
		node.DNSName = h + "." + node.MagicDNSSuffix
	}
	f.state.Node = node
	f.state.Peers = demoState().Peers
	f.state.Prefs.LoggedOut = false
}

// ExpireLogin drops a pending browser login without logging in, the way a
// registration the control plane forgot looks from the node.
func (f *Fake) ExpireLogin() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Node.AuthURL = ""
}

// Load returns a copy of the demo state.
func (f *Fake) Load(_ context.Context) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.confirming && len(f.phases) == 0:
		f.completeLogin()
	case f.confirming:
		f.state.Node.BackendState, f.phases = f.phases[0], f.phases[1:]
	case f.state.Node.AuthURL != "" && f.completeAfter > 0:
		f.pendingReads--
		if f.pendingReads <= 0 {
			f.confirmLogin()
		}
	}
	if !f.detected {
		return State{Distro: f.state.Distro}, nil
	}
	if f.refuse > 0 {
		// The socket is not up yet: what the real backend reports when
		// tailscaled refuses a read, as the first read after an install did.
		f.refuse--
		return State{Installed: true, Distro: f.state.Distro, PermissionDenied: true,
			Error: "tailscaled refused this user — run with sudo, or make this user " +
				"the operator (`sudo tailscale set --operator=$USER`)"}, nil
	}
	s := f.state
	s.Peers = append([]Peer(nil), f.state.Peers...)
	s.Node.IPs = append([]string(nil), f.state.Node.IPs...)
	s.Prefs.AdvertiseRoutes = append([]string(nil), f.state.Prefs.AdvertiseRoutes...)
	s.LoginProfiles = append([]LoginProfile(nil), f.state.LoginProfiles...)
	for i := range s.Peers {
		s.Peers[i].ExitNode = f.usesExitNode(s.Peers[i])
	}
	return s, nil
}

// usesExitNode reports whether the demo node routes through a peer.
func (f *Fake) usesExitNode(p Peer) bool {
	return f.state.Prefs.ExitNodeIP != "" && contains(p.IPs, f.state.Prefs.ExitNodeIP)
}

// apply mutates the demo state the way the real command would. It is the
// runner.Fake hook, so it runs only for a command that was previewed and
// confirmed — the same path the real backend takes.
func (f *Fake) apply(cmd runner.Command) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(cmd.Argv) == 0 {
		return "", fmt.Errorf("malformed command %q", cmd)
	}
	switch cmd.Argv[0] {
	case "apt-get", "dnf", "pacman":
		// The package install puts the client on the machine; tailscaled
		// takes a moment to answer after it.
		if contains(cmd.Argv, "tailscale") && f.absent {
			f.absent, f.refuse = false, 1
		}
		return "", nil
	case "curl":
		if hasPair(cmd.Argv, "-o", "/dev/null") {
			return f.tlsCheck(cmd.Argv[len(cmd.Argv)-1])
		}
		return "", nil
	case "update-ca-certificates", "update-ca-trust", "trust":
		f.caTrusted = true
		return "", nil
	case "install", "rm", "sysctl", "systemctl":
		// The helpers change files the demo does not model.
		return "", nil
	case "tailscale":
	default:
		return "", fmt.Errorf("%s: command not found", cmd.Argv[0])
	}
	if len(cmd.Argv) < 2 {
		return "", fmt.Errorf("malformed command %q", cmd)
	}
	flags := parseFlags(cmd.Argv[2:])
	switch cmd.Argv[1] {
	case "set":
		return f.applySet(flags)
	case "up":
		if _, ok := flags["login-server"]; ok {
			return f.applyJoin(flags)
		}
		if f.state.Node.BackendState == StateNeedsLogin {
			return "", fmt.Errorf("this node is logged out: join it again with j")
		}
		f.state.Prefs.WantRunning = true
		f.state.Node.BackendState = StateRunning
		f.state.Node.Online = true
		return "", nil
	case "down":
		f.state.Prefs.WantRunning = false
		f.state.Node.BackendState = StateStopped
		f.state.Node.Online = false
		return "", nil
	case "switch":
		return f.applySwitch(cmd.Argv[2:])
	case "logout":
		f.state.Prefs.LoggedOut = true
		f.state.Prefs.WantRunning = false
		f.state.Node = Node{BackendState: StateNeedsLogin, Version: f.state.Node.Version}
		f.state.Peers = nil
		return "", nil
	}
	return "", fmt.Errorf("tailscale: unknown subcommand %q", cmd.Argv[1])
}

// applySwitch applies `tailscale switch <id>`: the demo's two login profiles
// are the same account on the same control plane, so only the mark moves.
func (f *Fake) applySwitch(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: tailscale switch <id>")
	}
	found := false
	for i := range f.state.LoginProfiles {
		p := &f.state.LoginProfiles[i]
		p.Current = p.ID == args[0]
		found = found || p.Current
	}
	if !found {
		return "", fmt.Errorf("profile %q not found", args[0])
	}
	return "Switching to profile " + args[0], nil
}

// applySet applies `tailscale set --flag=value`.
func (f *Fake) applySet(flags map[string]string) (string, error) {
	for flag, value := range flags {
		switch flag {
		case "accept-routes":
			f.state.Prefs.RouteAll = value == "true"
		case "hostname":
			f.state.Prefs.Hostname = value
			f.state.Node.HostName = value
			f.state.Node.DNSName = value + "." + f.state.Node.MagicDNSSuffix
		case "exit-node":
			if value != "" && !f.hasExitNodeOption(value) {
				return "", fmt.Errorf("invalid value %q for --exit-node; must be IP or "+
					"unique node name", value)
			}
			f.state.Prefs.ExitNodeIP = value
		case "advertise-routes":
			routes := SplitList(value)
			if f.state.Prefs.AdvertisesExitNode() {
				routes = append(routes, "0.0.0.0/0", "::/0")
			}
			f.state.Prefs.AdvertiseRoutes = routes
		case "advertise-exit-node":
			routes := f.state.Prefs.SubnetRoutes()
			if value == "true" {
				routes = append(routes, "0.0.0.0/0", "::/0")
			}
			f.state.Prefs.AdvertiseRoutes = routes
		default:
			return "", fmt.Errorf("flag provided but not defined: -%s", flag)
		}
	}
	return "", nil
}

// hasExitNodeOption reports whether an address belongs to a peer offering
// itself as an exit node.
func (f *Fake) hasExitNodeOption(ip string) bool {
	for _, p := range f.state.Peers {
		if p.ExitNodeOption && contains(p.IPs, ip) {
			return true
		}
	}
	return false
}

// applyJoin applies `tailscale up --login-server=… --reset`. A node that is
// already logged in to that server only takes the new settings, as the real
// one does. Otherwise, without a key it behaves the way the real client does
// under --timeout: it prints the login URL, fails on the timeout, and leaves
// the node waiting for the browser.
func (f *Fake) applyJoin(flags map[string]string) (string, error) {
	server := flags["login-server"]
	_, reauth := flags["force-reauth"]
	_, withKey := flags["authkey"]
	stayLoggedIn := f.state.LoggedIn() && !reauth &&
		strings.EqualFold(strings.TrimRight(f.state.Prefs.ControlURL, "/"), server)
	prefs := Prefs{ControlURL: server, CorpDNS: true, WantRunning: true}
	if _, ok := flags["accept-routes"]; ok {
		prefs.RouteAll = true
	}
	prefs.AdvertiseRoutes = SplitList(flags["advertise-routes"])
	if _, ok := flags["advertise-exit-node"]; ok {
		prefs.AdvertiseRoutes = append(prefs.AdvertiseRoutes, "0.0.0.0/0", "::/0")
	}
	prefs.Hostname = flags["hostname"]
	f.state.Prefs = prefs

	if !withKey && !stayLoggedIn {
		url := strings.TrimRight(server, "/") + DemoRegisterPath
		f.confirming, f.phases = false, nil
		f.state.Node = Node{BackendState: StateNeedsLogin, AuthURL: url,
			Version: f.state.Node.Version}
		f.state.Peers = nil
		f.pendingReads = f.completeAfter
		return "\nTo authenticate, visit:\n\n\t" + url + "\n\n",
			fmt.Errorf("timeout waiting for Tailscale service to enter a Running state; " +
				"check health with \"tailscale status\"")
	}
	demo := demoState()
	node := demo.Node
	if prefs.Hostname != "" {
		node.HostName = prefs.Hostname
		node.DNSName = prefs.Hostname + "." + node.MagicDNSSuffix
	}
	f.state.Node = node
	f.state.Peers = demo.Peers
	return "", nil
}

// DemoPrivateServer is a login server the demo serves with a certificate from
// a private CA: the certificate check fails until the trust step ran.
const DemoPrivateServer = "https://headscale.lab.internal"

// tlsCheck answers the certificate check the way curl would: the demo's
// public server verifies, a server under .internal does not until its CA was
// trusted, and anything else does not answer.
func (f *Fake) tlsCheck(url string) (string, error) {
	host := URLHost(url)
	switch {
	case host == URLHost(DemoLoginServer) || IsTailscaleControl(url):
		return "", nil
	case strings.HasSuffix(host, ".internal") && f.caTrusted:
		return "", nil
	case strings.HasSuffix(host, ".internal"):
		return "curl: (60) SSL certificate problem: unable to get local issuer certificate",
			fmt.Errorf("exit status 60")
	}
	return "curl: (6) Could not resolve host: " + host, fmt.Errorf("exit status 6")
}

// hasPair reports whether argv carries a flag followed by a value.
func hasPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// parseFlags reads `--name=value` and `--name` arguments into a map.
func parseFlags(args []string) map[string]string {
	flags := map[string]string{}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, value, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		flags[name] = value
	}
	return flags
}

// demoState is the sample tailnet.
func demoState() State {
	now := time.Now()
	return State{
		Installed:     true,
		Distro:        Distro{Distro: pkgmgr.Distro{ID: "ubuntu", PrettyName: "Ubuntu 24.04 LTS", VersionID: "24.04"}, Codename: "noble"},
		DaemonRunning: true,
		Node: Node{
			BackendState:   StateRunning,
			Version:        "1.98.4",
			ID:             "1",
			HostName:       "example-node",
			DNSName:        "example-node.tailnet.example.com",
			TailnetName:    "headscale.example.com",
			MagicDNSSuffix: "tailnet.example.com",
			MagicDNS:       true,
			User:           "user@example.com",
			IPs:            []string{"100.64.0.1", "fd7a:115c:a1e0::1"},
			Online:         true,
		},
		Prefs: Prefs{
			ControlURL:  DemoLoginServer,
			RouteAll:    true,
			CorpDNS:     true,
			WantRunning: true,
		},
		PrefsRead: true,
		// Two login profiles: the tailnet the demo node is on, and a second
		// login it remembers from an earlier control plane.
		LoginProfiles: []LoginProfile{
			{ID: "a1b2", Tailnet: "headscale.example.com", Account: "user@example.com",
				Current: true},
			{ID: "c3d4", Tailnet: "lab.example.net", Account: "ops@example.net"},
		},
		Peers: []Peer{
			{
				ID: "2", HostName: "exit-gateway", DNSName: "exit-gateway.tailnet.example.com",
				OS: "linux", User: "user@example.com",
				IPs:    []string{"100.64.0.2", "fd7a:115c:a1e0::2"},
				Online: true, Active: true, ExitNodeOption: true,
				LastSeen: now.Add(-2 * time.Minute),
			},
			{
				ID: "3", HostName: "office-router", DNSName: "office-router.tailnet.example.com",
				OS: "linux", User: "ops@example.com",
				IPs:    []string{"100.64.0.3", "fd7a:115c:a1e0::3"},
				Online: true, Routes: []string{"192.0.2.0/24"},
				LastSeen: now.Add(-40 * time.Minute),
			},
		},
	}
}
