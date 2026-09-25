package headscale

import (
	"strconv"
	"time"
)

// This file is the guided half of the control-plane screens: one line that
// says what is missing next, in the order a control plane comes up. Each
// step only makes sense once the one before it is done — a login needs a
// server clients can reach, a node needs a way to log in, a route needs a
// node — so the line names the first missing step and the key that does it,
// never a list of everything at once.

// The steps, in order. NextReady means nothing is missing.
const (
	NextInstall   = "install"
	NextServer    = "server"
	NextUnit      = "unit"
	NextPorts     = "ports"
	NextIdentity  = "identity"
	NextFirstNode = "first-node"
	NextRoutes    = "routes"
	NextReady     = "ready"
)

// Readiness is where the control plane stands, step by step. It names nothing
// of the host: --check prints it as it is.
type Readiness struct {
	// Installed reports that the headscale binary is present.
	Installed bool `json:"installed"`
	// ServerConfigured is the first step, S: a server_url clients can reach
	// (set, valid, not loopback), and a MagicDNS base_domain headscale will
	// start with (present while magic_dns is on, not containing the
	// server_url host).
	ServerConfigured bool `json:"serverConfigured"`
	// UnitRunning and UnitEnabled are the second: the unit is active, and
	// starts at boot.
	UnitRunning bool `json:"unitRunning"`
	UnitEnabled bool `json:"unitEnabled"`
	// OIDCConfigured and PreAuthKey are the two ways a machine can log in;
	// either one makes the third step.
	OIDCConfigured bool `json:"oidcConfigured"`
	PreAuthKey     bool `json:"preAuthKey"`
	// FirstNode is the fourth: at least one node is registered.
	FirstNode bool `json:"firstNode"`
	// PendingRegistrations counts the nodes waiting for their login to be
	// confirmed (read from headscale's journal): the first node usually
	// shows up here before it shows up in the node list.
	PendingRegistrations int `json:"pendingRegistrations"`
	// Ports is the host firewall's answer for the control plane's port and
	// the node's, present when the firewall could be read. A closed control
	// port is the step after the unit: headscale runs, and nobody reaches it.
	Ports *PortsReadiness `json:"ports,omitempty"`
	// RoutesPending counts the advertised routes nobody approved yet, an exit
	// node counting as one; RoutesApproved is the last step, none pending.
	RoutesPending  int  `json:"routesPending"`
	RoutesApproved bool `json:"routesApproved"`
	// Next is the first missing step: install, server, unit, identity,
	// first-node, routes, or ready.
	Next string `json:"next"`
	// NextStep says it in words, with the key that does it.
	NextStep string `json:"nextStep"`
}

// ReadinessFor works out the readiness of a control plane at a moment.
func ReadinessFor(h State, now time.Time) Readiness {
	r := Readiness{Installed: h.Present}
	cp := h.ControlPlane
	r.ServerConfigured = serverReady(cp)
	// Without headscale there is no unit to be running or enabled.
	r.UnitRunning = h.Present && unitRunning(h)
	r.UnitEnabled = h.Present && cp.ServiceEnabled != "disabled" && cp.ServiceEnabled != "masked"
	r.OIDCConfigured = h.OIDCEnabled()
	for _, k := range h.PreAuthKeys {
		if k.Usable(now) {
			r.PreAuthKey = true
		}
	}
	if h.Present && h.Firewall.Source != "" && r.ServerConfigured {
		ports := PortsFor(h.Firewall, cp.ServerURL)
		r.Ports = &ports
	}
	r.FirstNode = len(h.Nodes) > 0
	r.PendingRegistrations = len(h.Registrations)
	for _, n := range h.Nodes {
		r.RoutesPending += pendingRoutes(n)
	}
	r.RoutesApproved = r.RoutesPending == 0

	switch {
	case !r.Installed:
		r.Next = NextInstall
		r.NextStep = "headscale is not installed · i installs it from the tui-tools repository"
	case !r.ServerConfigured:
		r.Next = NextServer
		r.NextStep = serverStep(cp)
	case !r.UnitRunning:
		r.Next = NextUnit
		r.NextStep = unitStep(h)
	case !r.UnitEnabled:
		r.Next = NextUnit
		r.NextStep = "headscale runs but won't start at boot · S or O end with the " +
			"enable (or: systemctl enable " + HeadscaleService + ")"
	case r.Ports != nil && r.Ports.Control == PortClosed:
		r.Next = NextPorts
		r.NextStep = strconv.Itoa(r.Ports.ControlPort) + "/tcp is closed in the host " +
			"firewall (read from " + r.Ports.Source + "): clients cannot reach headscale · " +
			firewallHint(h.Firewall)
	case !r.OIDCConfigured && !r.PreAuthKey:
		r.Next = NextIdentity
		r.NextStep = "no way to log in yet · O sets up an identity provider, or n on " +
			"the keys screen creates a pre-auth key"
	case !r.FirstNode && r.PendingRegistrations > 0:
		r.Next = NextFirstNode
		r.NextStep = "a node is waiting to register · R on the nodes screen registers it " +
			"as a user, or open its /register URL in a browser"
	case !r.FirstNode:
		r.Next = NextFirstNode
		r.NextStep = "no node yet · j on the node screen joins this host (or " +
			"tailscale up --login-server=<server_url> on another machine)"
	case !r.RoutesApproved:
		r.Next = NextRoutes
		r.NextStep = "routes pending approval (" + strconv.Itoa(r.RoutesPending) +
			") · r on the nodes screen approves them"
	default:
		r.Next = NextReady
		r.NextStep = "ready · clients can log in and every advertised route is approved"
		if r.Ports != nil && r.Ports.Node == PortClosed {
			r.NextStep += " · " + strconv.Itoa(NodePort) + "/udp is closed here, so peers " +
				"relay through DERP instead of connecting directly · " + firewallHint(h.Firewall)
		}
	}
	return r
}

// firewallHint is the way to open a port: f when tui-firewall is here, the
// package that brings it otherwise.
func firewallHint(fw Firewall) string {
	if fw.Launchable {
		return "f opens tui-firewall"
	}
	return "open it with the host's firewall tool (tui-firewall, from pkgs.tui.tools, does it)"
}

// serverReady reports whether config.yaml is set up for clients.
func serverReady(cp ControlPlane) bool {
	if !ControlPlaneConfigured(cp) || ServerURLProblem(cp.ServerURL) != "" {
		return false
	}
	if cp.MagicDNS && cp.BaseDomain == "" {
		return false
	}
	return BaseDomainConflict(cp.ServerURL, cp.BaseDomain) == ""
}

// serverStep says what S has to fix.
func serverStep(cp ControlPlane) string {
	switch {
	case !cp.Readable:
		reason := cp.Error
		if reason == "" {
			reason = "it could not be read"
		}
		return HeadscaleConfigPath + " is not readable (" + reason + ") · run with sudo"
	case !ControlPlaneConfigured(cp):
		return "server not set up for clients · S sets the transport, server_url and base_domain"
	case ServerURLProblem(cp.ServerURL) != "":
		return "server_url is not valid · S fixes it"
	}
	return "base_domain will stop headscale from starting · S fixes it"
}

// unitRunning reports whether the server answers: the unit is active, or —
// on a host whose unit systemd cannot describe — the lists were read.
func unitRunning(h State) bool {
	switch h.ControlPlane.ServiceState {
	case "active":
		return true
	case "", "unknown":
		return !h.NotRunning && h.Error == ""
	}
	return false
}

// unitStep says what to do about a unit that is not running.
func unitStep(h State) string {
	if msg := NotRunningMessage(h.ControlPlane); msg != "" {
		return msg
	}
	if h.Error != "" {
		return "headscale does not answer: " + h.Error
	}
	return "headscale is not running · systemctl start " + HeadscaleService
}

// pendingRoutes counts a node's advertised routes that are not approved, the
// exit node counting as one.
func pendingRoutes(n Node) int {
	pending := 0
	for _, r := range NodeRoutes(n) {
		if IsExitRoute(r.Route) || !r.Advertised || r.Approved {
			continue
		}
		pending++
	}
	if ExitNodeState(n) == "pending" {
		pending++
	}
	return pending
}
