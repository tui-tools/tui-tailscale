package headscale

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file is headscale's `dns:` section: MagicDNS and its base domain, the
// nameservers nodes use (global, and split per domain), the search domains,
// and the extra records headscale serves itself. Before it, everything but
// the base domain was a hand edit of config.yaml (issue #14).
//
// Every change goes through the same minimal splice writer as S and O. Lists
// and maps are written in YAML flow style on the key's own line, so a change
// is one line of diff, and each change is checked by reading the edited file
// back: what headscale would read has to be exactly what the form asked for.

// SplitDNS is one split-DNS entry: the nameservers that answer for a domain.
type SplitDNS struct {
	Domain      string   `json:"domain"`
	Nameservers []string `json:"nameservers"`
}

// DNSRecord is one of headscale's extra records.
type DNSRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// String renders a record the way the form takes it: name, type, value.
func (r DNSRecord) String() string { return r.Name + " " + r.Type + " " + r.Value }

// DNSConfig is the `dns:` section.
type DNSConfig struct {
	MagicDNS   bool   `json:"magicDns"`
	BaseDomain string `json:"baseDomain,omitempty"`
	// OverrideLocalDNS makes nodes use headscale's DNS settings instead of
	// their own; headscale's default is true.
	OverrideLocalDNS bool        `json:"overrideLocalDns"`
	Global           []string    `json:"global,omitempty"`
	Split            []SplitDNS  `json:"split,omitempty"`
	SearchDomains    []string    `json:"searchDomains,omitempty"`
	ExtraRecords     []DNSRecord `json:"extraRecords,omitempty"`
	// ExtraRecordsPath is the JSON file headscale loads records from instead;
	// while it is set, extra_records is not this tool's to edit.
	ExtraRecordsPath string `json:"extraRecordsPath,omitempty"`
}

// dnsDoc is the part of the configuration the DNS section is read from.
type dnsDoc struct {
	DNS struct {
		MagicDNS         *bool  `yaml:"magic_dns"`
		BaseDomain       string `yaml:"base_domain"`
		OverrideLocalDNS *bool  `yaml:"override_local_dns"`
		Nameservers      struct {
			Global flexList   `yaml:"global"`
			Split  orderedMap `yaml:"split"`
		} `yaml:"nameservers"`
		SearchDomains    flexList    `yaml:"search_domains"`
		ExtraRecords     []DNSRecord `yaml:"extra_records"`
		ExtraRecordsPath string      `yaml:"extra_records_path"`
	} `yaml:"dns"`
}

// orderedMap reads the split map keeping the order the file lists it in, so
// the screen lists the domains the way the file does.
type orderedMap []SplitDNS

// UnmarshalYAML reads a mapping of domain to nameserver list.
func (m *orderedMap) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		if node.Kind == yaml.ScalarNode && (node.Value == "" || node.Tag == "!!null") {
			return nil
		}
		return fmt.Errorf("dns.nameservers.split: expected a mapping")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		var servers flexList
		if err := node.Content[i+1].Decode(&servers); err != nil {
			return err
		}
		*m = append(*m, SplitDNS{Domain: node.Content[i].Value, Nameservers: servers})
	}
	return nil
}

// ParseDNS reads the `dns:` section of a configuration.
func ParseDNS(data []byte) (DNSConfig, error) {
	var doc dnsDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return DNSConfig{}, fmt.Errorf("config.yaml: %w", err)
	}
	d := doc.DNS
	cfg := DNSConfig{
		// headscale's defaults for both switches are true.
		MagicDNS:         d.MagicDNS == nil || *d.MagicDNS,
		OverrideLocalDNS: d.OverrideLocalDNS == nil || *d.OverrideLocalDNS,
		BaseDomain:       strings.TrimSpace(d.BaseDomain),
		Global:           d.Nameservers.Global,
		Split:            d.Nameservers.Split,
		SearchDomains:    d.SearchDomains,
		ExtraRecords:     d.ExtraRecords,
		ExtraRecordsPath: strings.TrimSpace(d.ExtraRecordsPath),
	}
	return cfg, nil
}

// --- validation ---------------------------------------------------------------

// NameserverProblem says why s cannot be a nameserver, or "" when it can:
// headscale takes an IP address, or a DNS-over-HTTPS URL (NextDNS's shape).
func NameserverProblem(s string) string {
	if addr, err := netip.ParseAddr(s); err == nil && addr.Zone() == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(s), "https://") {
		if problem := ServerURLProblem(s); problem != "" {
			return "not a DNS-over-HTTPS URL: " + problem
		}
		return ""
	}
	return fmt.Sprintf("%q is not a nameserver: an IP address, or an https:// "+
		"DNS-over-HTTPS URL", s)
}

// DomainProblem says why s cannot be a DNS domain (a split-DNS domain, a
// search domain, a record name), or "" when it can: one or more labels, the
// last not all digits.
func DomainProblem(s string) string {
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return fmt.Sprintf("%q is not a DNS name", s)
	}
	labels := strings.Split(s, ".")
	for _, label := range labels {
		if !dnsLabel.MatchString(label) {
			return fmt.Sprintf("%q is not a DNS name", s)
		}
	}
	if last := labels[len(labels)-1]; strings.Trim(last, "0123456789") == "" {
		return fmt.Sprintf("%q is an address, not a DNS name", s)
	}
	return ""
}

// RecordTypes are the extra record types a Tailscale client resolves.
// headscale's own configuration notes that only A and AAAA reach the client,
// so a CNAME would be written and never answered: it is refused here.
var RecordTypes = []string{"A", "AAAA"}

// ParseRecord reads "name type value", the one line the form takes a record
// as, and validates it: a DNS name, A or AAAA, and an address of that family.
func ParseRecord(line string) (DNSRecord, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return DNSRecord{}, fmt.Errorf("a record is three words: name type value " +
			"(app-1.tailnet.example.com A 100.64.0.10)")
	}
	r := DNSRecord{Name: strings.ToLower(strings.TrimSuffix(fields[0], ".")),
		Type: strings.ToUpper(fields[1]), Value: fields[2]}
	return r, r.Check()
}

// Check validates a record.
func (r DNSRecord) Check() error {
	if problem := DomainProblem(r.Name); problem != "" {
		return errors.New(problem)
	}
	if !slices.Contains(RecordTypes, r.Type) {
		return fmt.Errorf("record type %q: only A and AAAA reach a Tailscale client", r.Type)
	}
	addr, err := netip.ParseAddr(r.Value)
	if err != nil || addr.Zone() != "" {
		return fmt.Errorf("%q is not an IP address", r.Value)
	}
	if r.Type == "A" && !addr.Is4() {
		return fmt.Errorf("an A record needs an IPv4 address, not %s", r.Value)
	}
	if r.Type == "AAAA" && (!addr.Is6() || addr.Is4In6()) {
		return fmt.Errorf("an AAAA record needs an IPv6 address, not %s", r.Value)
	}
	return nil
}

// Check validates the whole section the way headscale does at startup, plus
// the shapes of every entry. serverURL is what base_domain is checked
// against.
func (d DNSConfig) Check(serverURL string) error {
	if d.MagicDNS && d.BaseDomain == "" {
		return fmt.Errorf("dns.base_domain is required while MagicDNS is on " +
			"(headscale refuses to start without it)")
	}
	if d.BaseDomain != "" {
		if !ValidBaseDomain(d.BaseDomain) {
			return fmt.Errorf("not a valid DNS name for dns.base_domain (a name with a dot, "+
				"such as tailnet.internal): %q", d.BaseDomain)
		}
		if conflict := BaseDomainConflict(serverURL, d.BaseDomain); conflict != "" {
			return errors.New(conflict)
		}
	}
	for _, ns := range d.Global {
		if p := NameserverProblem(ns); p != "" {
			return errors.New("global nameservers: " + p)
		}
	}
	seen := map[string]bool{}
	for _, s := range d.Split {
		if p := DomainProblem(s.Domain); p != "" {
			return errors.New("split DNS: " + p)
		}
		if seen[s.Domain] {
			return fmt.Errorf("split DNS: %s is listed twice", s.Domain)
		}
		seen[s.Domain] = true
		if len(s.Nameservers) == 0 {
			return fmt.Errorf("split DNS: %s has no nameserver", s.Domain)
		}
		for _, ns := range s.Nameservers {
			if p := NameserverProblem(ns); p != "" {
				return errors.New("split DNS " + s.Domain + ": " + p)
			}
		}
	}
	for _, domain := range d.SearchDomains {
		if p := DomainProblem(domain); p != "" {
			return errors.New("search domains: " + p)
		}
	}
	for _, r := range d.ExtraRecords {
		if err := r.Check(); err != nil {
			return fmt.Errorf("extra record %s: %w", r.Name, err)
		}
	}
	return nil
}

// RecordOutsideBase reports a record whose name is not under base_domain:
// allowed, but only nodes that resolve through the tailnet ever see it, and
// the name reads as a public one. The form warns rather than refuses.
func RecordOutsideBase(r DNSRecord, baseDomain string) bool {
	base := strings.ToLower(strings.Trim(baseDomain, "."))
	name := strings.ToLower(strings.TrimSuffix(r.Name, "."))
	return base != "" && name != base && !strings.HasSuffix(name, "."+base)
}

// --- the edits --------------------------------------------------------------------

// DNSEdits turns a change of the section into the keys to set: only the keys
// whose value differs, so the diff is the change and nothing else.
func DNSEdits(cur, want DNSConfig) []ConfigEdit {
	var edits []ConfigEdit
	add := func(value string, path ...string) {
		edits = append(edits, ConfigEdit{Path: append([]string{"dns"}, path...), Value: value})
	}
	if cur.MagicDNS != want.MagicDNS {
		add(YAMLBool(want.MagicDNS), "magic_dns")
	}
	if cur.BaseDomain != want.BaseDomain {
		add(YAMLString(want.BaseDomain), "base_domain")
	}
	if cur.OverrideLocalDNS != want.OverrideLocalDNS {
		add(YAMLBool(want.OverrideLocalDNS), "override_local_dns")
	}
	if !slices.Equal(cur.Global, want.Global) {
		add(YAMLList(want.Global), "nameservers", "global")
	}
	if !sameSplit(cur.Split, want.Split) {
		add(YAMLSplit(want.Split), "nameservers", "split")
	}
	if !slices.Equal(cur.SearchDomains, want.SearchDomains) {
		add(YAMLList(want.SearchDomains), "search_domains")
	}
	if !slices.Equal(cur.ExtraRecords, want.ExtraRecords) {
		add(YAMLRecords(want.ExtraRecords), "extra_records")
	}
	return edits
}

// sameSplit compares two split maps entry by entry, in order.
func sameSplit(a, b []SplitDNS) bool {
	return slices.EqualFunc(a, b, func(x, y SplitDNS) bool {
		return x.Domain == y.Domain && slices.Equal(x.Nameservers, y.Nameservers)
	})
}

// YAMLSplit renders the split map as one flow mapping.
func YAMLSplit(split []SplitDNS) string {
	if len(split) == 0 {
		return "{}"
	}
	entries := make([]string, 0, len(split))
	for _, s := range split {
		entries = append(entries, strconv.Quote(s.Domain)+": "+YAMLList(s.Nameservers))
	}
	return "{" + strings.Join(entries, ", ") + "}"
}

// YAMLRecords renders the extra records as one flow sequence of mappings,
// the one-line shape headscale's own sample file shows.
func YAMLRecords(records []DNSRecord) string {
	if len(records) == 0 {
		return "[]"
	}
	entries := make([]string, 0, len(records))
	for _, r := range records {
		entries = append(entries, "{name: "+strconv.Quote(r.Name)+", type: "+
			strconv.Quote(r.Type)+", value: "+strconv.Quote(r.Value)+"}")
	}
	return "[" + strings.Join(entries, ", ") + "]"
}

// PlanDNSChange validates the wanted section, works out its edits and proves
// them: the edited file is read back, and what headscale would read has to be
// exactly the section that was asked for. A file whose layout the splice
// cannot edit safely is refused rather than written.
func PlanDNSChange(raw, serverURL string, cur, want DNSConfig) ([]ConfigEdit, error) {
	if err := want.Check(serverURL); err != nil {
		return nil, err
	}
	edits := DNSEdits(cur, want)
	if len(edits) == 0 {
		return nil, nil
	}
	updated, _, err := EditConfig(raw, edits)
	if err != nil {
		return nil, err
	}
	got, err := ParseDNS([]byte(updated))
	if err != nil {
		return nil, fmt.Errorf("the edited file would not parse: %w", err)
	}
	if !sameDNS(got, want) {
		return nil, fmt.Errorf("config.yaml's dns section is laid out in a way this " +
			"tool cannot edit safely; edit it by hand")
	}
	return edits, nil
}

// sameDNS compares the parts of the section the form edits.
func sameDNS(a, b DNSConfig) bool {
	return a.MagicDNS == b.MagicDNS && a.BaseDomain == b.BaseDomain &&
		a.OverrideLocalDNS == b.OverrideLocalDNS &&
		slices.Equal(nilIfEmpty(a.Global), nilIfEmpty(b.Global)) &&
		sameSplit(a.Split, b.Split) &&
		slices.Equal(nilIfEmpty(a.SearchDomains), nilIfEmpty(b.SearchDomains)) &&
		slices.Equal(a.ExtraRecords, b.ExtraRecords)
}

// nilIfEmpty folds an empty list and no list together.
func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
