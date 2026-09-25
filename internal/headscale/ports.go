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
// the rule set is read from `nft -j list ruleset` or `iptables -S`. Every
// source is reduced to the same few facts per rule, and a rule this reader
// cannot judge (a match on something it does not model) never decides the
// answer by itself.
//
// A jump or a goto is followed into the chain it names (issue #24): ufw on
// iptables-nft, the default firewall of an Ubuntu server, is an input chain
// with a drop policy whose rules are nothing but jumps, and the accept for a
// port sits two chains down. When a jump cannot be followed (its chain is not
// in what was read, or the chains nest deeper than the kernel allows), the
// answer is unknown: the policy after it cannot be trusted either.

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
	// verdict is accept, reject (reject or drop), jump or goto (to target),
	// return, or skip for anything that does not end the walk (a log, a
	// counter, a mark).
	verdict string
	// target is the chain a jump or a goto continues in.
	target string
	// addrtype marks iptables-nft's address-type match, whose type the JSON
	// does not carry. On the input path it is ufw's "not local" guard, whose
	// first rule returns for a packet addressed to this host: a return under
	// it is taken, anything else under it says nothing about clients.
	addrtype bool
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

// fwChain is one chain: its rules in order and, for an input base chain, its
// policy.
type fwChain struct {
	rules []fwRule
	// policy is accept, reject, or empty when unknown.
	policy string
	// table names the table the chain belongs to, which is where its jumps
	// are looked up.
	table string
}

// maxJumpDepth is how deep chains may nest: the kernel's own limit for
// nftables, well past anything ufw or firewalld builds.
const maxJumpDepth = 16

// Firewall is what the read found: where from, and the IPv4 input chains.
type Firewall struct {
	// Source is tui-firewall, nftables or iptables; empty when nothing
	// could be read.
	Source string `json:"source,omitempty"`
	// Error is why nothing could be read.
	Error string `json:"error,omitempty"`
	// Launchable reports that tui-firewall is installed, so f can hand over.
	Launchable bool `json:"-"`
	// chains are the input base chains, where a packet starts; named are
	// every other chain a jump can reach, by table and name.
	chains []fwChain
	named  map[string]fwChain
}

// chainName keys a regular chain by its table and name.
func chainName(table, name string) string { return table + "\x00" + name }

// Check judges one port: closed when any input chain would refuse a new
// connection, open when every chain accepts it, unknown otherwise.
func (f Firewall) Check(proto string, port int) PortState {
	if f.Source == "" || len(f.chains) == 0 {
		return PortUnknown
	}
	state := PortOpen
	for _, chain := range f.chains {
		switch f.check(chain, proto, port) {
		case PortClosed:
			return PortClosed
		case PortUnknown:
			state = PortUnknown
		}
	}
	return state
}

// check walks one base chain the way the kernel would for a new connection
// from anywhere, and falls back on its policy when no rule decided.
func (f Firewall) check(c fwChain, proto string, port int) PortState {
	if state, decided := f.walk(c, proto, port, 0); decided {
		return state
	}
	switch c.policy {
	case "accept":
		return PortOpen
	case "reject":
		return PortClosed
	}
	return PortUnknown
}

// walk goes through one chain's rules, into the chains they jump to, and
// reports the verdict when one was reached; not decided means the packet
// fell off the end of the chain (or returned) to whoever called it.
func (f Firewall) walk(c fwChain, proto string, port, depth int) (PortState, bool) {
	for _, r := range c.rules {
		if !r.applies(proto, port) {
			continue
		}
		switch r.verdict {
		case "accept":
			return PortOpen, true
		case "reject":
			return PortClosed, true
		case "return":
			return "", false
		case "jump", "goto":
			target, ok := f.named[chainName(c.table, r.target)]
			if !ok || depth >= maxJumpDepth {
				// A jump that cannot be followed may end anywhere.
				return PortUnknown, true
			}
			if state, decided := f.walk(target, proto, port, depth+1); decided {
				return state, true
			}
			if r.verdict == "goto" {
				// A goto does not come back: its chain's end is this one's.
				return "", false
			}
		}
	}
	return "", false
}

// applies reports whether a rule takes a new connection from anywhere to
// this host on the port: it ends somewhere, and matches nothing narrower
// than the protocol and the port.
func (r fwRule) applies(proto string, port int) bool {
	if r.verdict == "skip" || r.narrow {
		return false
	}
	if r.addrtype && r.verdict != "return" {
		return false
	}
	if r.proto != "" && r.proto != proto {
		return false
	}
	return len(r.ports) == 0 || inRanges(r.ports, port)
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

// --- iptables -S ---------------------------------------------------------------

// iptablesTable is the one table `iptables -S` lists: filter.
const iptablesTable = "filter"

// ParseIptablesInput reads `iptables -S`: the INPUT chain, and every chain
// its jumps reach. Output limited to `iptables -S INPUT` still reads; a jump
// there names a chain that is not in it, which makes the answer unknown.
func ParseIptablesInput(out string) (Firewall, bool) {
	lines := strings.Split(out, "\n")
	// Chains are declared with -N, before or after the rules that jump to
	// them; the built-in ones are always there.
	chains := map[string]*fwChain{}
	for _, name := range []string{"INPUT", "FORWARD", "OUTPUT"} {
		chains[name] = &fwChain{table: iptablesTable}
	}
	for _, line := range lines {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "-N" {
			chains[f[1]] = &fwChain{table: iptablesTable}
		}
	}
	seen := false
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		c, ok := chains[f[1]]
		if !ok {
			continue
		}
		switch f[0] {
		case "-P":
			seen = seen || f[1] == "INPUT"
			if len(f) >= 3 {
				c.policy = policyWord(f[2])
			}
		case "-A":
			seen = seen || f[1] == "INPUT"
			c.rules = append(c.rules, iptablesRule(f[2:]))
		}
	}
	if !seen {
		return Firewall{}, false
	}
	fw := Firewall{Source: SourceIptables, chains: []fwChain{*chains["INPUT"]},
		named: map[string]fwChain{}}
	for name, c := range chains {
		if name != "INPUT" {
			fw.named[chainName(iptablesTable, name)] = *c
		}
	}
	return fw, true
}

// nonTerminal are the iptables targets that let the packet go on to the next
// rule: a log, a mark, a counter of some kind.
var nonTerminal = map[string]bool{
	"LOG": true, "NFLOG": true, "ULOG": true, "MARK": true, "CONNMARK": true,
	"TRACE": true, "AUDIT": true, "CT": true, "NOTRACK": true, "TCPMSS": true,
	"SET": true, "CLASSIFY": true, "DSCP": true, "TOS": true, "TTL": true,
	"HL": true, "SECMARK": true, "CONNSECMARK": true, "IDLETIMER": true, "LED": true,
}

// iptablesRule reduces one `-A <chain> …` rule.
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
		case "-s", "--source", "-i", "--in-interface", "--src-range", "--src-type":
			if next != "0.0.0.0/0" {
				r.narrow = true
			}
			i++
		case "--dst-type":
			// A packet a client sends to this host is addressed to one of
			// its own addresses: LOCAL matches it, any other type does not.
			if !strings.EqualFold(next, "LOCAL") {
				r.narrow = true
			}
			i++
		case "--state", "--ctstate":
			if !strings.Contains(strings.ToUpper(next), "NEW") {
				r.narrow = true
			}
			i++
		case "-j", "--jump":
			switch target := strings.ToUpper(next); {
			case target == "ACCEPT":
				r.verdict = "accept"
			case target == "DROP" || target == "REJECT":
				r.verdict = "reject"
			case target == "RETURN":
				r.verdict = "return"
			case nonTerminal[target]:
			default:
				r.verdict, r.target = "jump", next
			}
			i++
		case "-g", "--goto":
			r.verdict, r.target = "goto", next
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
// hooked to input, its policy and its rules, and every other chain of those
// families, for the jumps to follow.
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
			Family, Table, Name, Hook, Type, Policy string
		}
		if json.Unmarshal(raw, &c) != nil || (c.Family != "ip" && c.Family != "inet") {
			continue
		}
		k := chainKey{c.Family, c.Table, c.Name}
		switch {
		case c.Hook == "input" && (c.Type == "" || c.Type == "filter"):
			chains[k] = &fwChain{policy: policyWord(c.Policy), table: c.Family + " " + c.Table}
			order = append(order, k)
		case c.Hook == "":
			// A regular chain: reached only by a jump or a goto.
			chains[k] = &fwChain{table: c.Family + " " + c.Table}
		}
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
	fw := Firewall{Source: SourceNftables, named: map[string]fwChain{}}
	base := map[chainKey]bool{}
	for _, k := range order {
		fw.chains = append(fw.chains, *chains[k])
		base[k] = true
	}
	for k, c := range chains {
		if !base[k] {
			fw.named[chainName(c.table, k.name)] = *c
		}
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
			case "return":
				r.verdict = "return"
			case "jump", "goto":
				var to struct{ Target string }
				if json.Unmarshal(body, &to) != nil || to.Target == "" {
					r.narrow = true
					continue
				}
				r.verdict, r.target = kind, to.Target
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
				case xt.Type == "match" && xt.Name == "addrtype":
					r.addrtype = true
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
		Fib     *struct {
			Result string   `json:"result"`
			Flags  []string `json:"flags"`
		} `json:"fib"`
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
	case left.Fib != nil && left.Fib.Result == "type" && hasArg(left.Fib.Flags, "daddr") &&
		!negated && strings.Contains(string(m.Right), `"local"`):
		// fib daddr type local: a packet to this host's own address, which
		// is what a client's is.
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
	// ControlPort is the port the host firewall has to let clients in on,
	// and Control what the firewall does to new connections on it over TCP.
	// It is listen_addr's port when headscale binds a public address:
	// server_url's can differ, when a NAT or a port forward in front of the
	// host maps another port onto it. Behind a reverse proxy (a loopback
	// bind) it is server_url's, the port the proxy answers on.
	ControlPort int       `json:"controlPort"`
	Control     PortState `json:"control"`
	// NodePort is tailscale's 41641/udp, and Node what the firewall does to
	// it. A closed node port is not fatal: peers relay through DERP.
	NodePort int       `json:"nodePort"`
	Node     PortState `json:"node"`
}

// PortsFor judges the two ports against a read firewall.
func PortsFor(fw Firewall, serverURL, listenAddr string) PortsReadiness {
	p := PortsReadiness{Source: fw.Source, NodePort: NodePort,
		Control: PortUnknown, Node: PortUnknown}
	switch {
	case listenAddr != "" && !IsLoopbackHost(ListenHost(listenAddr)) && ListenPort(listenAddr) > 0:
		p.ControlPort = ListenPort(listenAddr)
	case serverURL != "":
		p.ControlPort = URLPort(serverURL)
	}
	if p.ControlPort > 0 {
		p.Control = fw.Check("tcp", p.ControlPort)
	}
	p.Node = fw.Check("udp", NodePort)
	return p
}
