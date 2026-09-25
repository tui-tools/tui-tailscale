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
	// RefusedRegistrations counts the pending registrations whose browser
	// login the identity provider's policy refused (allowed_groups,
	// allowed_domains, allowed_users): headscale keeps them in its cache
	// like any other, and R will not register them (issue #26). They are
	// part of PendingRegistrations too.
	RefusedRegistrations int `json:"refusedRegistrations"`
	// Ports is the host firewall's answer for the control plane's port and
	// the node's, present when the firewall could be read. A closed control
	// port is the step after the unit: headscale runs, and nobody reaches it.
	Ports *PortsReadiness `json:"ports,omitempty"`
	// RoutesPending counts the advertised routes nobody approved yet, an exit
	// node counting as one; RoutesApproved is the last step, none pending.
	RoutesPending  int  `json:"routesPending"`
	RoutesApproved bool `json:"routesApproved"`
	// CanJoinMore answers whether another machine could log in now: an
	// identity provider is configured, or a pre-auth key is still usable.
	// Once a node exists it is no longer a blocking step (issue #19): a
	// single-use key spent on the first node is the normal end of a first
	// join. CanJoinMoreReason says why not, when it cannot: "spent" (every
	// key was single-use and is used), "expired" (every key left expired)
	// or "none" (no key and no identity provider).
	CanJoinMore       bool   `json:"canJoinMore"`
	CanJoinMoreReason string `json:"canJoinMoreReason,omitempty"`
	// Hint is a non-blocking suggestion shown after the step, when there is
	// one: how to add another machine once the last key is spent.
	Hint string `json:"hint,omitempty"`
	// Relays says where the DERP relays come from: tailscale-public,
	// embedded, embedded+tailscale-public or custom; absent when config.yaml
	// could not be read. RelayHint is a non-blocking note when they are
	// Tailscale's public servers: a private tailnet still relays through a
	// third party unless headscale's embedded DERP is enabled (issue #27).
	Relays    string `json:"relays,omitempty"`
	RelayHint string `json:"relayHint,omitempty"`
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
		ports := PortsFor(h.Firewall, cp.ServerURL, cp.ListenAddr)
		if port := STUNPort(cp); port > 0 {
			ports.STUNPort, ports.STUN = port, h.Firewall.Check("udp", port)
		}
		r.Ports = &ports
	}
	r.FirstNode = len(h.Nodes) > 0
	r.PendingRegistrations = len(h.Registrations)
	registrable, atIdP := 0, 0
	var refused Registration
	for _, reg := range h.Registrations {
		switch ok, _ := reg.Registrable(); {
		case ok:
			registrable++
		case reg.Refused:
			if r.RefusedRegistrations == 0 {
				refused = reg
			}
			r.RefusedRegistrations++
		default:
			atIdP++
		}
	}
	for _, n := range h.Nodes {
		r.RoutesPending += pendingRoutes(n)
	}
	r.RoutesApproved = r.RoutesPending == 0
	r.CanJoinMore = r.OIDCConfigured || r.PreAuthKey
	r.Relays = Relays(cp)
	switch {
	case h.Present && r.Relays == RelaysPublic:
		r.RelayHint = "relays go through Tailscale's public DERP servers when nodes cannot " +
			"connect directly (derp.server.enabled is false) · S enables the embedded one"
	case h.Present && cp.DERPEmbedded && cp.ServerURL != "" && !ServerURLIsHTTPS(cp.ServerURL):
		// Clients reach the relay over TLS on server_url's port only.
		r.RelayHint = "the embedded DERP relay is on, but server_url is not https: " +
			"clients reach the relay over TLS only, so they cannot use it"
	}
	if !r.CanJoinMore {
		r.CanJoinMoreReason = joinMoreReason(h.PreAuthKeys, now)
		if r.FirstNode {
			r.Hint = anotherMachineHint(r.CanJoinMoreReason)
		}
	}

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
	case !r.CanJoinMore && !r.FirstNode:
		// Only a way in for the first node blocks: once one is there, a
		// spent key is the hint below, not a missing step.
		r.Next = NextIdentity
		r.NextStep = "no way to log in yet · O sets up an identity provider, or n on " +
			"the keys screen creates a pre-auth key"
	case !r.FirstNode && registrable > 0 && r.OIDCConfigured:
		r.Next = NextFirstNode
		r.NextStep = "a node is waiting to register · open its /register URL in a browser " +
			"to log in through the identity provider (R on the nodes screen registers it " +
			"without the provider's policy)"
	case !r.FirstNode && registrable > 0:
		r.Next = NextFirstNode
		r.NextStep = "a node is waiting to register · R on the nodes screen registers it " +
			"as a user, or open its /register URL in a browser"
	case !r.FirstNode && atIdP > 0:
		r.Next = NextFirstNode
		r.NextStep = "a node is logging in at the identity provider · its browser " +
			"finishes the registration"
	case !r.FirstNode && r.RefusedRegistrations > 0:
		r.Next = NextFirstNode
		r.NextStep = "the last browser login was refused by the identity provider's policy (" +
			refused.RefusedReason() + ") · O edits the allow lists, or j on the node screen " +
			"joins this host"
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
		switch {
		case r.Ports != nil && r.Ports.Node == PortClosed:
			r.NextStep += " · " + strconv.Itoa(NodePort) + "/udp is closed here, so peers " +
				"relay through DERP instead of connecting directly · " + firewallHint(h.Firewall)
		case r.Ports != nil && r.Ports.STUN == PortClosed:
			r.NextStep += " · " + strconv.Itoa(r.Ports.STUNPort) + "/udp (the embedded " +
				"DERP relay's STUN) is closed here · " + firewallHint(h.Firewall)
		}
	}
	return r
}

// The reasons no other machine can log in now, for CanJoinMoreReason.
const (
	JoinMoreSpent   = "spent"
	JoinMoreExpired = "expired"
	JoinMoreNone    = "none"
)

// joinMoreReason says why no key is usable: the keys there are were
// single-use and are spent, or they expired, or there is none at all. A
// spent key counts first, since it is the one the operator just used.
func joinMoreReason(keys []PreAuthKey, now time.Time) string {
	spent, expired := false, false
	for _, k := range keys {
		switch {
		case !k.Reusable && k.Used:
			spent = true
		case !k.Expiration.IsZero() && !k.Expiration.After(now):
			expired = true
		}
	}
	switch {
	case spent:
		return JoinMoreSpent
	case expired:
		return JoinMoreExpired
	}
	return JoinMoreNone
}

// anotherMachineHint is how to add the next machine once none can log in.
func anotherMachineHint(reason string) string {
	why := ""
	switch reason {
	case JoinMoreSpent:
		why = " (the last key was single-use and is spent)"
	case JoinMoreExpired:
		why = " (the last key expired)"
	}
	return "to add another machine: n on the keys screen" + why + ", or O for browser login"
}

// Spent reports a single-use key that already registered its machine: it
// stays on headscale's list and can never be used again.
func (k PreAuthKey) Spent() bool { return !k.Reusable && k.Used }

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
	case cp.ConfigMissing:
		return ConfigMissingMessage
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
