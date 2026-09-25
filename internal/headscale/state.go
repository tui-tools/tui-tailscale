// Package headscale is the control-plane half of tui-tailscale: a self-hosted
// Headscale on this host — its users, nodes and pre-authentication keys, its
// own configuration (/etc/headscale/config.yaml: how clients reach it and
// which identity provider it federates to), the unit that runs it and the
// ownership of its files. The node half, this host as a member of a tailnet,
// is the sibling package internal/tailscale.
//
// This package is the tool's second exec site. A process is started from
// exactly two places, internal/tailscale and here, both through the kit
// runner, so the command a confirm dialog showed is provably the command that
// ran. It drives `headscale` for every read and change of the control plane's
// lists, `cat`, `sh` and `install` to read and write its configuration and
// the OIDC client secret, `systemctl` for the unit, `stat` and `chown` for the
// ownership of its files, `curl` for the one read that leaves the machine (an
// IdP's discovery document), and the package manager and the tui-tools
// repository files for the companion install.
//
// PRIVACY: the OIDC client secret is typed masked, travels to this package on
// stdin, and is written to its own root-only file that config.yaml references;
// it is never on an argv, in a preview or read back. A pre-auth key is shown
// once, the moment headscale prints it, and the model keeps only its prefix.
package headscale

import (
	"context"
	"time"

	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/runner"
)

// State is the control plane, when one is present on this host.
type State struct {
	// Present reports that the headscale binary was found.
	Present bool `json:"present"`
	// Error carries why a present control plane could not be read: the CLI's
	// own error, or — when the unit is known to be stopped and the CLI was
	// not asked at all — what to do about it (see NotRunningMessage).
	Error string `json:"error,omitempty"`
	// NotRunning reports that the lists were not read because the headscale
	// unit is not running; Error then says how to start it.
	NotRunning bool `json:"notRunning,omitempty"`
	// OIDCInferred reports that user identity looks like it comes from an
	// external OpenID Connect provider — guessed from a user carrying a
	// provider, or a node that registered through OIDC. It is the fallback
	// answer, kept for the host whose config.yaml cannot be read; the honest
	// answer comes from ControlPlane, and OIDCEnabled prefers it.
	OIDCInferred bool `json:"oidcInferred"`
	// ControlPlane is what /etc/headscale/config.yaml says: the URL clients
	// reach this server on, and the IdP it federates identity to. Login
	// happens in the client's browser against the IdP, and the server exposes
	// no web admin of its own, so this is also the one thing the tool has to
	// be able to configure.
	ControlPlane ControlPlane `json:"controlPlane"`
	Users        []User       `json:"users"`
	Nodes        []Node       `json:"nodes"`
	PreAuthKeys  []PreAuthKey `json:"preAuthKeys"`
	// Registrations are the nodes waiting for their login to be confirmed,
	// read from headscale's journal (see registrations.go).
	Registrations []Registration `json:"-"`
	// Firewall is the host firewall's input chain as read for the readiness
	// ports step (see ports.go).
	Firewall Firewall `json:"-"`

	// Distro and Repo are what the companion install needs when headscale is
	// absent: the distribution, and whether the tui-tools repository — the
	// family's mirror of headscale — is already set up on it.
	Distro pkgmgr.Distro `json:"-"`
	Repo   RepoState     `json:"-"`
}

// OIDCEnabled is whether identity really is federated: read from the
// configuration when it could be read, and only otherwise guessed from who has
// logged in so far.
func (h State) OIDCEnabled() bool {
	if h.ControlPlane.Readable {
		return h.ControlPlane.OIDC.Configured()
	}
	return h.OIDCInferred
}

// User is a Headscale user. Its identity, when OIDC is configured, is owned by
// the IdP; headscale only mirrors it.
type User struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName,omitempty"`
	Email       string    `json:"email,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	ProviderID  string    `json:"providerId,omitempty"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
}

// Node is a machine registered with Headscale, owned by one user.
type Node struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	GivenName      string    `json:"givenName,omitempty"`
	User           string    `json:"user"`
	IPAddresses    []string  `json:"ipAddresses"`
	LastSeen       time.Time `json:"lastSeen,omitempty"`
	Expiry         time.Time `json:"expiry,omitempty"`
	Online         bool      `json:"online"`
	RegisterMethod string    `json:"registerMethod,omitempty"`
	// AvailableRoutes are the routes the node advertises (subnet routes, and
	// 0.0.0.0/0 with ::/0 for an exit node); ApprovedRoutes the ones an admin
	// approved; SubnetRoutes the ones actually served, advertised and
	// approved. See routes.go.
	AvailableRoutes []string `json:"availableRoutes,omitempty"`
	ApprovedRoutes  []string `json:"approvedRoutes,omitempty"`
	SubnetRoutes    []string `json:"subnetRoutes,omitempty"`
}

// PreAuthKey is a key that lets a machine register itself for a user without a
// browser login. Headscale only ever shows its prefix on a list, never the
// whole key, and neither do we.
type PreAuthKey struct {
	ID         string    `json:"id"`
	User       string    `json:"user"`
	KeyPrefix  string    `json:"keyPrefix,omitempty"`
	Reusable   bool      `json:"reusable"`
	Ephemeral  bool      `json:"ephemeral"`
	Used       bool      `json:"used"`
	Expiration time.Time `json:"expiration,omitempty"`
	CreatedAt  time.Time `json:"createdAt,omitempty"`
	ACLTags    []string  `json:"aclTags,omitempty"`
}

// Usable reports whether a pre-auth key can still register a machine: not
// expired, and either reusable or not used yet.
func (k PreAuthKey) Usable(now time.Time) bool {
	if !k.Expiration.IsZero() && !k.Expiration.After(now) {
		return false
	}
	return k.Reusable || !k.Used
}

// Backend is the boundary between the UI and the control plane. Load reads the
// model; Preview renders the exact command a confirmed action would run; Run
// executes it. Nothing else in this package's callers may start a process.
type Backend interface {
	// Name identifies the backend ("headscale", "demo").
	Name() string
	// Load reads the current state. It never fails as a whole: a missing
	// binary or a stopped unit is a fact in the State, not an error.
	Load(ctx context.Context) (State, error)
	// Preview renders the exact command line Run will execute, routing to the
	// right binary so the privilege prefix shown is the one that will apply.
	Preview(cmd runner.Command) string
	// Run executes a previously previewed command.
	Run(ctx context.Context, cmd runner.Command) (string, error)
	// Reprobe forgets which binaries were found and how to run them, so the
	// next read detects them again: after an install, a binary that was
	// missing at start-up is there.
	Reprobe()
	// Stat reads owner, group and mode of the given paths — a read, like
	// Load's, with no confirm. A path that does not exist is absent from the
	// answer. The server-settings form uses it to check that the service
	// account can read a certificate before it writes the path down.
	Stat(ctx context.Context, paths []string) map[string]FileStat
	// LaunchFirewall prepares the hand-over of the terminal to tui-firewall,
	// the family's tool for opening the ports the readiness line reports
	// closed. It is not a change and is not previewed as one: tui-firewall
	// previews and confirms whatever it changes.
	LaunchFirewall() (Process, error)
}
