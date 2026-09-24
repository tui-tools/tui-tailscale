package main

import (
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// checkTimeout bounds the whole read.
const checkTimeout = 30 * time.Second

// checkReport is what --check prints: one read of the node, reduced to the
// facts a script or a support answer needs.
//
// PRIVACY: this block is meant to be pasted into scripts and issues, so it
// carries no address, no name and no URL of this host or its tailnet. The
// login server is answered as the questions that matter — is one set, is it
// https, is it Tailscale's own or self-hosted — rather than printed, because
// the URL names somebody's infrastructure. Addresses are counted and peers
// are counted. The one URL it does print is a pending login's, and only while
// it is pending: it is a one-time registration link rather than an address of
// this host, and `--check | jq -r .loginUrl` is the copyable fallback for a
// terminal too narrow to show it whole.
type checkReport struct {
	Tool     string `json:"tool"`
	Version  string `json:"version"`
	Backend  string `json:"backend"`
	Describe string `json:"describe"`

	// LoginURL is a pending login's URL, present only while it is pending
	// and at the top level, so `--check | jq -r .loginUrl` prints it whole.
	LoginURL string `json:"loginUrl,omitempty"`

	Tailscale nodeSummary `json:"tailscale"`
	// Install is how the client would be installed here, present only when it
	// is not: the commands `i` would run, for the detected distribution.
	Install *installSummary `json:"install,omitempty"`

	// Compat is what the version probe found. It is a list, like every tool's
	// in the family, so the lab's harvest reads one shape.
	Compat []compat.Result `json:"compat"`
}

// nodeSummary is the node, reduced.
type nodeSummary struct {
	Installed        bool   `json:"installed"`
	DaemonRunning    bool   `json:"daemonRunning"`
	NotRunning       bool   `json:"notRunning,omitempty"`
	PermissionDenied bool   `json:"permissionDenied,omitempty"`
	Error            string `json:"error,omitempty"`

	BackendState  string `json:"backendState,omitempty"`
	ClientVersion string `json:"clientVersion,omitempty"`
	// LoginPending reports that an interactive login is waiting for a
	// browser; the report's top-level loginUrl carries its URL.
	LoginPending bool `json:"loginPending"`
	LoggedIn     bool `json:"loggedIn"`
	Online       bool `json:"online"`

	LoginServer loginServerSummary `json:"loginServer"`

	TailnetIPv4 bool `json:"tailnetIpv4"`
	TailnetIPv6 bool `json:"tailnetIpv6"`
	MagicDNS    bool `json:"magicDns"`
	// HealthWarnings counts the client's own warnings; their text can name
	// hosts, so it stays on the node screen.
	HealthWarnings int `json:"healthWarnings"`

	PrefsRead  bool         `json:"prefsRead"`
	PrefsError string       `json:"prefsError,omitempty"`
	Prefs      prefsSummary `json:"prefs"`
	Peers      peersSummary `json:"peers"`
}

// loginServerSummary answers what a support question needs about the login
// server without naming it.
type loginServerSummary struct {
	Set              bool `json:"set"`
	HTTPS            bool `json:"https"`
	TailscaleControl bool `json:"tailscaleControl"`
}

// prefsSummary is the node's settings, reduced.
type prefsSummary struct {
	AcceptRoutes       bool `json:"acceptRoutes"`
	AdvertisedRoutes   int  `json:"advertisedRoutes"`
	AdvertisesExitNode bool `json:"advertisesExitNode"`
	UsesExitNode       bool `json:"usesExitNode"`
	AcceptDNS          bool `json:"acceptDns"`
	WantRunning        bool `json:"wantRunning"`
	LoggedOut          bool `json:"loggedOut"`
	HostnameSet        bool `json:"hostnameSet"`
}

// peersSummary counts the peers.
type peersSummary struct {
	Total           int `json:"total"`
	Online          int `json:"online"`
	ExitNodeOptions int `json:"exitNodeOptions"`
	WithRoutes      int `json:"withRoutes"`
}

// installSummary is the install plan for this distribution.
type installSummary struct {
	Distro   string   `json:"distro"`
	Manager  string   `json:"manager,omitempty"`
	Commands []string `json:"commands"`
}

// runCheck reads the state once and prints the reduced summary as JSON.
func runCheck(ctx context.Context, backend tailscale.Backend,
	probed compat.Result, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	state, err := backend.Load(ctx)
	if err != nil {
		return err
	}

	report := checkReport{
		Tool:      toolName,
		Version:   version,
		Backend:   backend.Name(),
		Describe:  backend.Describe(),
		LoginURL:  state.Node.AuthURL,
		Tailscale: summariseNode(state),
		Compat:    compatList(probed),
	}
	if !state.Installed {
		report.Install = &installSummary{
			Distro:   distroLabel(state.Distro),
			Manager:  string(state.Distro.Manager()),
			Commands: tailscale.InstallInstructions(state.Distro),
		}
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// compatList is the probe as the one-entry list --check carries, empty when
// nothing was probed (--demo).
func compatList(r compat.Result) []compat.Result {
	if r.Backend == "" {
		return []compat.Result{}
	}
	return []compat.Result{r}
}

// summariseNode reduces the state to counts and booleans.
func summariseNode(s tailscale.State) nodeSummary {
	n := nodeSummary{
		Installed:        s.Installed,
		DaemonRunning:    s.DaemonRunning,
		NotRunning:       s.NotRunning,
		PermissionDenied: s.PermissionDenied,
		Error:            s.Error,
		BackendState:     s.Node.BackendState,
		ClientVersion:    s.Node.Version,
		LoginPending:     s.Node.AuthURL != "",
		LoggedIn:         s.LoggedIn(),
		Online:           s.Node.Online,
		MagicDNS:         s.Node.MagicDNS,
		HealthWarnings:   len(s.Node.Health),
		PrefsRead:        s.PrefsRead,
		PrefsError:       s.PrefsError,
	}
	for _, ip := range s.Node.IPs {
		if addr, err := netip.ParseAddr(ip); err == nil {
			if addr.Is4() {
				n.TailnetIPv4 = true
			} else {
				n.TailnetIPv6 = true
			}
		}
	}
	if s.PrefsRead {
		p := s.Prefs
		n.LoginServer = loginServerSummary{
			Set:              p.ControlURL != "",
			HTTPS:            tailscale.IsHTTPS(p.ControlURL),
			TailscaleControl: p.ControlURL != "" && tailscale.IsTailscaleControl(p.ControlURL),
		}
		n.Prefs = prefsSummary{
			AcceptRoutes:       p.RouteAll,
			AdvertisedRoutes:   len(p.SubnetRoutes()),
			AdvertisesExitNode: p.AdvertisesExitNode(),
			UsesExitNode:       p.UsesExitNode(),
			AcceptDNS:          p.CorpDNS,
			WantRunning:        p.WantRunning,
			LoggedOut:          p.LoggedOut,
			HostnameSet:        p.Hostname != "",
		}
	}
	for _, peer := range s.Peers {
		n.Peers.Total++
		if peer.Online {
			n.Peers.Online++
		}
		if peer.ExitNodeOption {
			n.Peers.ExitNodeOptions++
		}
		if len(peer.Routes) > 0 {
			n.Peers.WithRoutes++
		}
	}
	return n
}

// distroLabel is ID-VERSION_ID, the form the compatibility evidence uses.
func distroLabel(d tailscale.Distro) string {
	if d.ID == "" {
		return "unknown"
	}
	if d.VersionID == "" {
		return d.ID + "-rolling"
	}
	return d.ID + "-" + d.VersionID
}
