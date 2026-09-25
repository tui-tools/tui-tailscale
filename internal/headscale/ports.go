package headscale

import (
	"encoding/json"
	"strconv"
	"strings"
)

// This file is the readiness "ports" step (issue #15, read side): whether the
// host firewall lets clients reach the control plane (its server_url port,
// 443/tcp for https) and lets peers reach this host's node directly
// (41641/udp, tailscale's default). A cloud image whose INPUT chain ends in
// REJECT is the usual reason a correctly configured headscale answers nobody.
//
// It is read, never changed: tui-firewall is the family's tool for opening a
// port, and `f` hands the terminal to it. The read prefers tui-firewall's own
// --check, which already understands ufw, firewalld and nftables; without it,
// the input chain is read from `nft -j list ruleset` or `iptables -S INPUT`.
// Every source is reduced to the same few facts per rule, and a rule this
// reader cannot judge (a jump to another chain, a match on something it does
// not model) never decides the answer by itself.

// NodePort is the UDP port tailscaled listens on for direct connections.
const NodePort = 41641

// PortState is what the firewall does to new connections on one port.
type PortState string

const (
	// PortOpen: a rule accepts new connections from anywhere, or nothing
	// refuses them.
	PortOpen PortState = "open"
	// PortClosed: they fall through to a reject or a drop.
	PortClosed PortState = "closed"
	// PortUnknown: the firewall could not be read, or its rules could not be
	// judged.
	PortUnknown PortState = "unknown"
)

// Firewall sources.
const (
	SourceTuiFirewall = "tui-firewall"
	SourceNftables    = "nftables"
	SourceIptables    = "iptables"
)

// fwRule is one input rule, reduced to what decides a port: its verdict, the
// protocol and ports it matches, and whether it matches anything else.
type fwRule struct {
	// verdict is accept, reject (reject or drop), or skip for anything that
	// ends somewhere this reader does not follow (a jump, a return, a log).
	verdict string
	// proto is tcp, udp, or empty for any.
	proto string
	// ports are the destination port ranges, none for any.
	ports [][2]int
	// narrow reports a match on something beyond protocol and port: an
	// interface, a source, established connections only, an ICMP type. Such
	// a rule says nothing about new connections from clients.
	narrow bool
	// xtState marks iptables-nft's state match, whose states the JSON does
	// not carry.
	xtState bool
}

// fwChain is one input chain: its rules in order and its policy.
type fwChain struct {
	rules []fwRule
	// policy is accept, reject, or empty when unknown.
	policy string
}

// Firewall is what the read found: where from, and the IPv4 input chains.
type Firewall struct {
	// Source is tui-firewall, nftables or iptables; empty when nothing
	// could be read.
	Source string `json:"source,omitempty"`
	// Error is why nothing could be read.
	Error string `json:"error,omitempty"`
	// Launchable reports that tui-firewall is installed, so f can hand over.
	Launchable bool `json:"-"`
	chains     []fwChain
}

// Check judges one port: closed when any input chain would refuse a new
// connection, open when every chain accepts it, unknown otherwise.
func (f Firewall) Check(proto string, port int) PortState {
	if f.Source == "" || len(f.chains) == 0 {
		return PortUnknown
	}
	state := PortOpen
	for _, chain := range f.chains {
		switch chain.check(proto, port) {
		case PortClosed:
			return PortClosed
		case PortUnknown:
			state = PortUnknown
		}
	}
	return state
}

// check walks one chain the way the kernel would for a new connection from
// anywhere, skipping what it cannot judge.
func (c fwChain) check(proto string, port int) PortState {
	for _, r := range c.rules {
		if r.verdict == "skip" || r.narrow {
			continue
		}
		if r.proto != "" && r.proto != proto {
			continue
		}
		if len(r.ports) > 0 && !inRanges(r.ports, port) {
			continue
		}
		if r.verdict == "accept" {
			return PortOpen
		}
		return PortClosed
	}
	switch c.policy {
	case "accept":
		return PortOpen
	case "reject":
		return PortClosed
	}
	return PortUnknown
}

// inRanges reports whether a port is in one of the ranges.
func inRanges(ranges [][2]int, port int) bool {
	for _, r := range ranges {
		if port >= r[0] && port <= r[1] {
			return true
		}
	}
	return false
}

// parsePorts reads "80,443", "1000:2000" or "1000-2000" lists.
func parsePorts(s string) [][2]int {
	var out [][2]int
	for _, item := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		lo, hi, isRange := strings.Cut(item, ":")
		if !isRange {
			lo, hi, isRange = strings.Cut(item, "-")
		}
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			continue
		}
		b := a
		if isRange {
			if v, err := strconv.Atoi(strings.TrimSpace(hi)); err == nil {
				b = v
			}
		}
		out = append(out, [2]int{a, b})
	}
	return out
}

// policyWord folds the words firewalls use for a default into accept or
// reject.
func policyWord(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "accept", "allow":
		return "accept"
	case "drop", "deny", "reject", "limit-deny":
		return "reject"
	}
	return ""
}

// --- tui-firewall --check ---------------------------------------------------

// ParseTuiFirewallCheck reads tui-firewall's --check JSON: whether the
// firewall is on, and each input group's default and rules, the way its model
// prints them for ufw, firewalld and nftables alike.
func ParseTuiFirewallCheck(out string) (Firewall, bool) {
	start := strings.IndexByte(out, '{')
	if start < 0 {
		return Firewall{}, false
	}
	var report struct {
		Enabled bool `json:"enabled"`
		Model   struct {
			Groups []struct {
				Name    string `json:"Name"`
				Default struct {
					Incoming string `json:"Incoming"`
				} `json:"Default"`
				Rules []struct {
					Action    string `json:"Action"`
					Direction string `json:"Direction"`
					Proto     string `json:"Proto"`
					Ports     string `json:"Ports"`
					From      string `json:"From"`
					Service   string `json:"Service"`
					Raw       string `json:"Raw"`
				} `json:"Rules"`
			} `json:"Groups"`
		} `json:"model"`
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&report); err != nil {
		return Firewall{}, false
	}
	fw := Firewall{Source: SourceTuiFirewall}
	if !report.Enabled {
		// A firewall that is off refuses nothing.
		fw.chains = []fwChain{{policy: "accept"}}
		return fw, true
	}
	for _, g := range report.Model.Groups {
		name := strings.ToLower(g.Name)
		if g.Default.Incoming == "" || strings.HasPrefix(name, "ip6 ") {
			continue
		}
		chain := fwChain{policy: policyWord(g.Default.Incoming)}
		for _, r := range g.Rules {
			if dir := strings.ToUpper(r.Direction); dir != "" && dir != "IN" {
				continue
			}
			rule := fwRule{proto: strings.ToLower(r.Proto), ports: parsePorts(r.Ports)}
			unknownService := false
			if len(rule.ports) == 0 && r.Service != "" {
				rule.ports, unknownService = servicePorts(r.Service)
				if len(rule.ports) > 0 {
					rule.proto = "tcp"
				}
			}
			raw := strings.ToLower(r.Raw)
			switch strings.ToUpper(r.Action) {
			case "ALLOW", "ACCEPT", "LIMIT":
				rule.verdict = "accept"
			case "DENY", "REJECT", "DROP":
				rule.verdict = "reject"
			case "":
				// nftables' model leaves an xt REJECT target without an
				// action; its raw text still names it.
				if strings.Contains(raw, "reject") || strings.Contains(raw, " drop") {
					rule.verdict = "reject"
				} else {
					rule.verdict = "skip"
				}
			default:
				rule.verdict = "skip"
			}
			from := strings.ToLower(strings.TrimSpace(r.From))
			if from != "" && from != "anywhere" && from != "any" && from != "0.0.0.0/0" {
				rule.narrow = true
			}
			if strings.Contains(raw, "iifname") || strings.Contains(raw, "saddr") ||
				(rule.proto == "" && len(rule.ports) == 0 &&
					(strings.Contains(raw, "conntrack") || strings.Contains(raw, "ct state") ||
						strings.Contains(raw, "icmp"))) {
				rule.narrow = true
			}
			if rule.proto == "icmp" || rule.proto == "ipv6-icmp" {
				rule.narrow = true
			}
			if unknownService {
				// A service this reader does not know stands for ports it
				// cannot name: the rule cannot decide anything.
				rule.verdict = "skip"
			}
			chain.rules = append(chain.rules, rule)
		}
		fw.chains = append(fw.chains, chain)
	}
	return fw, true
}

// servicePorts is the TCP port of the two services a control plane is
// reached through, when a rule names the service rather than its port; the
// second value reports a service this reader does not know.
func servicePorts(service string) ([][2]int, bool) {
	switch strings.ToLower(service) {
	case "https", "nginx https", "apache secure":
		return [][2]int{{443, 443}}, false
	case "http", "nginx http", "www":
		return [][2]int{{80, 80}}, false
	case "nginx full", "apache full", "www full":
		return [][2]int{{80, 80}, {443, 443}}, false
	}
	return nil, true
}

// --- iptables -S INPUT --------------------------------------------------------

// ParseIptablesInput reads `iptables -S INPUT`.
func ParseIptablesInput(out string) (Firewall, bool) {
	chain := fwChain{}
	seen := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[1] != "INPUT" {
			continue
		}
		switch f[0] {
		case "-P":
			seen = true
			if len(f) >= 3 {
				chain.policy = policyWord(f[2])
			}
		case "-A":
			seen = true
			chain.rules = append(chain.rules, iptablesRule(f[2:]))
		}
	}
	if !seen {
		return Firewall{}, false
	}
	return Firewall{Source: SourceIptables, chains: []fwChain{chain}}, true
}

// iptablesRule reduces one `-A INPUT …` rule.
func iptablesRule(args []string) fwRule {
	r := fwRule{verdict: "skip"}
	for i := 0; i < len(args); i++ {
		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		switch args[i] {
		case "-p", "--protocol":
			r.proto = strings.ToLower(next)
			if r.proto == "icmp" || r.proto == "ipv6-icmp" {
				r.narrow = true
			}
			i++
		case "--dport", "--dports", "--destination-port":
			r.ports = parsePorts(next)
			i++
		case "-s", "--source", "-i", "--in-interface", "--src-range":
			if next != "0.0.0.0/0" {
				r.narrow = true
			}
			i++
		case "--state", "--ctstate":
			if !strings.Contains(strings.ToUpper(next), "NEW") {
				r.narrow = true
			}
			i++
		case "-j", "--jump":
			switch strings.ToUpper(next) {
			case "ACCEPT":
				r.verdict = "accept"
			case "DROP", "REJECT":
				r.verdict = "reject"
			}
			i++
		case "!":
			// A negated match is beyond this reader.
			r.narrow = true
		}
	}
	return r
}

// --- nft -j list ruleset --------------------------------------------------------

// ParseNftRuleset reads `nft -j list ruleset`: every IPv4-capable base chain
// hooked to input, its policy and its rules.
func ParseNftRuleset(out string) (Firewall, bool) {
	start := strings.IndexByte(out, '{')
	if start < 0 {
		return Firewall{}, false
	}
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &doc); err != nil {
		return Firewall{}, false
	}
	type chainKey struct{ family, table, name string }
	chains := map[chainKey]*fwChain{}
	var order []chainKey
	for _, obj := range doc.Nftables {
		raw, ok := obj["chain"]
		if !ok {
			continue
		}
		var c struct {
			Family, Table, Name, Hook, Policy string
		}
		if json.Unmarshal(raw, &c) != nil || c.Hook != "input" ||
			(c.Family != "ip" && c.Family != "inet") {
			continue
		}
		k := chainKey{c.Family, c.Table, c.Name}
		chains[k] = &fwChain{policy: policyWord(c.Policy)}
		order = append(order, k)
	}
	for _, obj := range doc.Nftables {
		raw, ok := obj["rule"]
		if !ok {
			continue
		}
		var r struct {
			Family, Table, Chain string
			Expr                 []map[string]json.RawMessage
		}
		if json.Unmarshal(raw, &r) != nil {
			continue
		}
		c, ok := chains[chainKey{r.Family, r.Table, r.Chain}]
		if !ok {
			continue
		}
		c.rules = append(c.rules, nftRule(r.Expr))
	}
	if len(order) == 0 {
		// No input base chain at all: nothing filters input.
		return Firewall{Source: SourceNftables, chains: []fwChain{{policy: "accept"}}}, true
	}
	fw := Firewall{Source: SourceNftables}
	for _, k := range order {
		fw.chains = append(fw.chains, *chains[k])
	}
	return fw, true
}

// nftRule reduces one rule's expression list.
func nftRule(exprs []map[string]json.RawMessage) fwRule {
	r := fwRule{verdict: "skip"}
	for _, e := range exprs {
		for kind, body := range e {
			switch kind {
			case "accept":
				r.verdict = "accept"
			case "drop", "reject":
				r.verdict = "reject"
			case "xt":
				var xt struct{ Type, Name string }
				_ = json.Unmarshal(body, &xt)
				switch {
				case xt.Type == "target" && (xt.Name == "REJECT" || xt.Name == "DROP"):
					r.verdict = "reject"
				case xt.Type == "match" && (xt.Name == "conntrack" || xt.Name == "state"):
					// iptables-nft's state match: its states are not in the
					// JSON. With a port it is the NEW-connections rule of a
					// port; without one it is the established catch-all.
					r.xtState = true
				case xt.Type == "match" && (xt.Name == "tcp" || xt.Name == "udp" ||
					xt.Name == "multiport"):
				default:
					r.narrow = true
				}
			case "match":
				nftMatch(body, &r)
			}
		}
	}
	if r.xtState && len(r.ports) == 0 {
		r.narrow = true
	}
	return r
}

// nftMatch reduces one match expression.
func nftMatch(body json.RawMessage, r *fwRule) {
	var m struct {
		Op    string          `json:"op"`
		Left  json.RawMessage `json:"left"`
		Right json.RawMessage `json:"right"`
	}
	if json.Unmarshal(body, &m) != nil {
		r.narrow = true
		return
	}
	var left struct {
		Payload *struct{ Protocol, Field string } `json:"payload"`
		Meta    *struct{ Key string }             `json:"meta"`
		Ct      *struct{ Key string }             `json:"ct"`
	}
	_ = json.Unmarshal(m.Left, &left)
	negated := m.Op == "!="
	switch {
	case left.Payload != nil && left.Payload.Field == "dport" && !negated:
		r.proto = left.Payload.Protocol
		r.ports = nftPorts(m.Right)
	case left.Payload != nil && left.Payload.Protocol == "ip" && left.Payload.Field == "protocol":
		var proto string
		if json.Unmarshal(m.Right, &proto) == nil && (proto == "tcp" || proto == "udp") {
			r.proto = proto
		} else {
			r.narrow = true
		}
	case left.Meta != nil && left.Meta.Key == "l4proto":
		var proto string
		if json.Unmarshal(m.Right, &proto) == nil && (proto == "tcp" || proto == "udp") {
			r.proto = proto
		} else {
			r.narrow = true
		}
	case left.Ct != nil && left.Ct.Key == "state":
		if !strings.Contains(string(m.Right), "new") || negated {
			r.narrow = true
		}
	default:
		// An interface, an address, a mark: not a rule about everyone.
		r.narrow = true
	}
}

// nftPorts reads a port, a set of ports or a range from a match's right side.
func nftPorts(raw json.RawMessage) [][2]int {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return [][2]int{{n, n}}
	}
	var rng struct {
		Range [2]int `json:"range"`
	}
	if json.Unmarshal(raw, &rng) == nil && rng.Range[1] > 0 {
		return [][2]int{rng.Range}
	}
	var set struct {
		Set []json.RawMessage `json:"set"`
	}
	if json.Unmarshal(raw, &set) == nil {
		var out [][2]int
		for _, item := range set.Set {
			out = append(out, nftPorts(item)...)
		}
		return out
	}
	return nil
}

// PortsReadiness is the ports step of the readiness block.
type PortsReadiness struct {
	// Source is where the answer was read from: tui-firewall, nftables,
	// iptables, or empty when the firewall could not be read.
	Source string `json:"source,omitempty"`
	// ControlPort is the server_url port (443 for https), and Control what
	// the firewall does to new connections on it over TCP.
	ControlPort int       `json:"controlPort"`
	Control     PortState `json:"control"`
	// NodePort is tailscale's 41641/udp, and Node what the firewall does to
	// it. A closed node port is not fatal: peers relay through DERP.
	NodePort int       `json:"nodePort"`
	Node     PortState `json:"node"`
}

// PortsFor judges the two ports against a read firewall.
func PortsFor(fw Firewall, serverURL string) PortsReadiness {
	p := PortsReadiness{Source: fw.Source, NodePort: NodePort,
		Control: PortUnknown, Node: PortUnknown}
	if serverURL != "" {
		p.ControlPort = URLPort(serverURL)
	}
	if p.ControlPort > 0 {
		p.Control = fw.Check("tcp", p.ControlPort)
	}
	p.Node = fw.Check("udp", NodePort)
	return p
}
