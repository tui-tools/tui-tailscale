package tailscale

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The two reads this tool makes are `tailscale status --json` and `tailscale
// debug prefs`. Both print one JSON object, but the kit runner hands back the
// combined output, and the client prints its warnings on stderr ("Warning:
// client version … != tailscaled server version …") ahead of it. So both
// parsers start at the first '{' and decode one value from there, ignoring
// whatever surrounds it.

// errNoJSON is the parse failure for output that carries no object at all:
// usually an error message the caller should have seen first.
var errNoJSON = errors.New("no JSON object in the output")

// decodeFirstObject decodes the first JSON object in out into v.
func decodeFirstObject(out string, v any) error {
	start := strings.IndexByte(out, '{')
	if start < 0 {
		return errNoJSON
	}
	dec := json.NewDecoder(strings.NewReader(out[start:]))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("cannot parse the JSON output: %w", err)
	}
	return nil
}

// rawStatus is the part of ipnstate.Status this tool reads.
type rawStatus struct {
	Version        string              `json:"Version"`
	BackendState   string              `json:"BackendState"`
	AuthURL        string              `json:"AuthURL"`
	TailscaleIPs   []string            `json:"TailscaleIPs"`
	Self           *rawPeer            `json:"Self"`
	Health         []string            `json:"Health"`
	MagicDNSSuffix string              `json:"MagicDNSSuffix"`
	CurrentTailnet *rawTailnet         `json:"CurrentTailnet"`
	Peer           map[string]*rawPeer `json:"Peer"`
	User           map[string]rawUser  `json:"User"`
}

// rawTailnet is ipnstate.TailnetStatus.
type rawTailnet struct {
	Name            string `json:"Name"`
	MagicDNSSuffix  string `json:"MagicDNSSuffix"`
	MagicDNSEnabled bool   `json:"MagicDNSEnabled"`
}

// rawPeer is the part of ipnstate.PeerStatus this tool reads. The public key
// and the endpoints are deliberately absent: nothing shows them.
type rawPeer struct {
	ID             string    `json:"ID"`
	HostName       string    `json:"HostName"`
	DNSName        string    `json:"DNSName"`
	OS             string    `json:"OS"`
	UserID         int64     `json:"UserID"`
	TailscaleIPs   []string  `json:"TailscaleIPs"`
	AllowedIPs     []string  `json:"AllowedIPs"`
	PrimaryRoutes  []string  `json:"PrimaryRoutes"`
	Tags           []string  `json:"Tags"`
	Online         bool      `json:"Online"`
	Active         bool      `json:"Active"`
	ExitNode       bool      `json:"ExitNode"`
	ExitNodeOption bool      `json:"ExitNodeOption"`
	LastSeen       time.Time `json:"LastSeen"`
}

// rawUser is tailcfg.UserProfile, reduced to the login name.
type rawUser struct {
	LoginName   string `json:"LoginName"`
	DisplayName string `json:"DisplayName"`
}

// Status is one parsed `tailscale status --json`.
type Status struct {
	Node  Node
	Peers []Peer
}

// ParseStatus reads `tailscale status --json`.
func ParseStatus(out string) (Status, error) {
	var raw rawStatus
	if err := decodeFirstObject(out, &raw); err != nil {
		return Status{}, err
	}
	if raw.BackendState == "" && raw.Version == "" {
		return Status{}, fmt.Errorf("not a tailscale status: no BackendState")
	}

	userName := func(id int64) string {
		if u, ok := raw.User[strconv.FormatInt(id, 10)]; ok {
			return u.LoginName
		}
		return ""
	}

	node := Node{
		BackendState: raw.BackendState,
		AuthURL:      raw.AuthURL,
		Version:      shortVersion(raw.Version),
		IPs:          append([]string(nil), raw.TailscaleIPs...),
		Health:       raw.Health,
	}
	if raw.CurrentTailnet != nil {
		node.TailnetName = raw.CurrentTailnet.Name
		node.MagicDNSSuffix = raw.CurrentTailnet.MagicDNSSuffix
		node.MagicDNS = raw.CurrentTailnet.MagicDNSEnabled
	}
	if node.MagicDNSSuffix == "" {
		node.MagicDNSSuffix = raw.MagicDNSSuffix
	}
	if self := raw.Self; self != nil {
		node.ID = self.ID
		node.HostName = self.HostName
		node.DNSName = strings.TrimSuffix(self.DNSName, ".")
		node.Online = self.Online
		node.User = userName(self.UserID)
		if len(node.IPs) == 0 {
			node.IPs = append([]string(nil), self.TailscaleIPs...)
		}
	}

	peers := make([]Peer, 0, len(raw.Peer))
	for _, p := range raw.Peer {
		if p == nil {
			continue
		}
		peers = append(peers, Peer{
			ID:             p.ID,
			HostName:       p.HostName,
			DNSName:        strings.TrimSuffix(p.DNSName, "."),
			OS:             p.OS,
			User:           userName(p.UserID),
			IPs:            append([]string(nil), p.TailscaleIPs...),
			Online:         p.Online,
			Active:         p.Active,
			ExitNodeOption: p.ExitNodeOption,
			ExitNode:       p.ExitNode,
			Routes:         peerRoutes(p),
			LastSeen:       p.LastSeen,
			Tags:           p.Tags,
		})
	}
	SortPeers(peers)
	return Status{Node: node, Peers: peers}, nil
}

// peerRoutes is what a peer serves beyond itself: its primary routes, plus
// any allowed prefix that is not one of its own addresses. The default routes
// are left out, because they mean "exit node" and have their own column.
func peerRoutes(p *rawPeer) []string {
	own := map[string]bool{}
	for _, ip := range p.TailscaleIPs {
		if addr, err := netip.ParseAddr(ip); err == nil {
			own[netip.PrefixFrom(addr, addr.BitLen()).String()] = true
		}
	}
	seen := map[string]bool{}
	var routes []string
	add := func(route string) {
		prefix, err := netip.ParsePrefix(route)
		if err != nil {
			return
		}
		key := prefix.Masked().String()
		if isDefaultRoute(key) || own[key] || seen[key] {
			return
		}
		// A single-host prefix that is not the peer's own address is still a
		// route (a /32 served on its behalf), so only its own are dropped.
		seen[key] = true
		routes = append(routes, key)
	}
	for _, r := range p.PrimaryRoutes {
		add(r)
	}
	for _, r := range p.AllowedIPs {
		add(r)
	}
	sort.Strings(routes)
	return routes
}

// SortPeers puts the peers in reading order: online first, then by name.
func SortPeers(peers []Peer) {
	sort.SliceStable(peers, func(i, j int) bool {
		if peers[i].Online != peers[j].Online {
			return peers[i].Online
		}
		return peers[i].Name() < peers[j].Name()
	})
}

// ParsePrefs reads `tailscale debug prefs`. Only the fields of Prefs are
// decoded; the rest of the object — its Config block included — is skipped by
// the decoder and never held.
func ParsePrefs(out string) (Prefs, error) {
	var prefs Prefs
	if err := decodeFirstObject(out, &prefs); err != nil {
		return Prefs{}, err
	}
	return prefs, nil
}

// versionPattern is the first version-shaped token of a version string.
var versionPattern = regexp.MustCompile(`[0-9]+\.[0-9]+(\.[0-9]+)?`)

// shortVersion reduces a long version ("1.98.4-t9e69045b2-ged3a62f14") to its
// release number.
func shortVersion(v string) string {
	if m := versionPattern.FindString(v); m != "" {
		return m
	}
	return v
}

// loginURLPattern is a URL on a line of its own, the way `tailscale up`
// prints the interactive login.
var loginURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// ParseLoginURL finds the login URL `tailscale up` prints when it has no
// pre-auth key:
//
//	To authenticate, visit:
//
//		https://headscale.example.com/register/…
//
// It returns "" when there is none. Only a URL after that sentence counts, so
// a warning that happens to carry a link is never mistaken for a login.
func ParseLoginURL(out string) string {
	// The marker is found on the output itself, not on a lower-cased copy:
	// lower-casing invalid UTF-8 changes its length, and an index into the
	// copy would then point somewhere else in the original.
	loc := loginMarker.FindStringIndex(out)
	if loc == nil {
		return ""
	}
	return strings.TrimRight(loginURLPattern.FindString(out[loc[1]:]), ".,;")
}

// loginMarker is the sentence `tailscale up` prints above the login URL.
var loginMarker = regexp.MustCompile(`(?i)to authenticate, visit`)

// ReadProblem classifies a failed read of the client, so the UI can say what
// to do rather than quote a socket error.
type ReadProblem int

const (
	// ProblemOther is a failure with no better explanation than its text.
	ProblemOther ReadProblem = iota
	// ProblemNotRunning is tailscaled not answering: the unit is stopped.
	ProblemNotRunning
	// ProblemPermission is the socket refusing this user.
	ProblemPermission
)

// ClassifyReadError reads the client's own words for a failed read.
func ClassifyReadError(text string) ReadProblem {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "access denied"),
		strings.Contains(lower, "operation not permitted"):
		return ProblemPermission
	case strings.Contains(lower, "doesn't appear to be running"),
		strings.Contains(lower, "failed to connect to local tailscale"),
		strings.Contains(lower, "is tailscaled running"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no such file or directory"):
		return ProblemNotRunning
	}
	return ProblemOther
}

// ParseIsEnabled reads the answer of `systemctl is-enabled <unit>`: the first
// word of its output ("enabled", "disabled", "masked", "static"…), or empty
// when it printed nothing a unit state looks like.
func ParseIsEnabled(out string) string {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	word := fields[0]
	for _, r := range word {
		if (r < 'a' || r > 'z') && r != '-' {
			return ""
		}
	}
	return word
}
