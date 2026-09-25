package headscale

import (
	"strings"
	"testing"
)

// The stock file: global nameservers as a block list followed by commented-out
// NextDNS lines, `split: {}` followed by commented-out examples, and empty
// search domains and records.
func TestParseDNSStock(t *testing.T) {
	d, err := ParseDNS([]byte(readFixture(t, "headscale-config.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if !d.MagicDNS || !d.OverrideLocalDNS || d.BaseDomain != "example.com" ||
		len(d.Global) != 4 || len(d.Split) != 0 || len(d.ExtraRecords) != 0 {
		t.Errorf("dns = %+v", d)
	}
}

const dnsConfig = `server_url: https://headscale.example.com
dns:
  magic_dns: true
  base_domain: tailnet.example.com
  nameservers:
    global: [1.1.1.1, 1.0.0.1]
    split:
      corp.example.com: [10.0.0.2]
      lab.example.com:
        - 10.0.1.2
        - 10.0.1.3
  search_domains: [tailnet.example.com]
  extra_records:
    - {name: "grafana.tailnet.example.com", type: "A", value: "100.64.0.3"}
    - name: ntp.tailnet.example.com
      type: AAAA
      value: fd7a:115c:a1e0::3
`

func TestParseDNSKeepsTheFileOrder(t *testing.T) {
	d, err := ParseDNS([]byte(dnsConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Split) != 2 || d.Split[0].Domain != "corp.example.com" ||
		strings.Join(d.Split[1].Nameservers, ",") != "10.0.1.2,10.0.1.3" {
		t.Errorf("split = %+v", d.Split)
	}
	if len(d.ExtraRecords) != 2 || d.ExtraRecords[1].Type != "AAAA" {
		t.Errorf("records = %+v", d.ExtraRecords)
	}
}

// changeDNS applies a change to a file and returns the new file and the diff.
func changeDNS(t *testing.T, raw string, change func(*DNSConfig)) (string, []ConfigChange) {
	t.Helper()
	cur, err := ParseDNS([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := cur
	want.Global = append([]string(nil), cur.Global...)
	want.Split = append([]SplitDNS(nil), cur.Split...)
	want.ExtraRecords = append([]DNSRecord(nil), cur.ExtraRecords...)
	change(&want)
	edits, err := PlanDNSChange(raw, "https://headscale.example.net", cur, want)
	if err != nil {
		t.Fatal(err)
	}
	out, changes, err := EditConfig(raw, edits)
	if err != nil {
		t.Fatal(err)
	}
	return out, changes
}

// On the stock file, a split entry is one changed line and the commented-out
// examples under `split: {}` survive.
func TestAddSplitOnTheStockFileIsOneLine(t *testing.T) {
	raw := readFixture(t, "headscale-config.yaml")
	out, changes := changeDNS(t, raw, func(d *DNSConfig) {
		d.Split = append(d.Split, SplitDNS{Domain: "corp.example.com",
			Nameservers: []string{"10.0.0.2"}})
	})
	if len(changes) != 1 || len(changes[0].Old) != 1 || len(changes[0].New) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].New[0] != `    split: {"corp.example.com": ["10.0.0.2"]}` {
		t.Errorf("new line = %q", changes[0].New[0])
	}
	for _, keep := range []string{"      # foo.bar.com:", "      #   - 8.8.8.8"} {
		if !strings.Contains(out, keep) {
			t.Errorf("the edit lost %q", keep)
		}
	}
}

// Replacing the block list of global nameservers keeps the commented-out
// NextDNS lines that follow it.
func TestGlobalNameserversKeepTheCommentsBelow(t *testing.T) {
	raw := readFixture(t, "headscale-config.yaml")
	out, changes := changeDNS(t, raw, func(d *DNSConfig) {
		d.Global = []string{"9.9.9.9"}
	})
	if len(changes) != 1 || len(changes[0].Old) != 5 {
		t.Fatalf("changes = %+v", changes)
	}
	if !strings.Contains(out, "# - https://dns.nextdns.io/abc123") {
		t.Error("the edit lost the NextDNS comment")
	}
}

func TestRecordsAndSearchDomains(t *testing.T) {
	out, _ := changeDNS(t, dnsConfig, func(d *DNSConfig) {
		d.ExtraRecords = append(d.ExtraRecords, DNSRecord{Name: "app-1.tailnet.example.com",
			Type: "A", Value: "10.0.0.5"})
		d.SearchDomains = nil
	})
	d, _ := ParseDNS([]byte(out))
	if len(d.ExtraRecords) != 3 || len(d.SearchDomains) != 0 {
		t.Errorf("after: %+v", d)
	}
	if !strings.Contains(out, `  extra_records: [{name: "grafana.tailnet.example.com", type: "A", `) {
		t.Errorf("records not one flow line:\n%s", out)
	}
}

// A dns section without nameservers gets the subsection inside it, not a
// second dns: at the end of the file.
func TestMissingSubsectionGoesInsideTheSection(t *testing.T) {
	raw := "server_url: https://headscale.example.com\ndns:\n  magic_dns: true\n" +
		"  base_domain: tailnet.example.com\nlog:\n  level: info\n"
	out, changes := changeDNS(t, raw, func(d *DNSConfig) {
		d.Split = []SplitDNS{{Domain: "corp.example.com", Nameservers: []string{"10.0.0.2"}}}
	})
	if strings.Count(out, "\ndns:") != 1 {
		t.Fatalf("dns: written twice:\n%s", out)
	}
	if len(changes) != 1 || changes[0].New[0] != "  nameservers:" ||
		changes[0].New[1] != `    split: {"corp.example.com": ["10.0.0.2"]}` {
		t.Errorf("changes = %+v", changes)
	}
}

// A section in flow style cannot take a key: the edit is refused, never
// appended as a duplicate.
func TestFlowSectionIsRefused(t *testing.T) {
	raw := "dns: {magic_dns: true, base_domain: tailnet.example.com}\n"
	cur, _ := ParseDNS([]byte(raw))
	want := cur
	want.Global = []string{"1.1.1.1"}
	if _, err := PlanDNSChange(raw, "https://headscale.example.com", cur, want); err == nil {
		t.Error("a flow-style dns: section was edited")
	}
}

func TestDNSValidation(t *testing.T) {
	ok := DNSConfig{MagicDNS: true, BaseDomain: "tailnet.example.com",
		Global:        []string{"1.1.1.1", "2606:4700:4700::1111", "https://dns.nextdns.io/abc123"},
		Split:         []SplitDNS{{Domain: "oraclevcn.com", Nameservers: []string{"169.254.169.254"}}},
		SearchDomains: []string{"tailnet.example.com"}}
	if err := ok.Check("https://headscale.example.com"); err != nil {
		t.Errorf("valid section refused: %v", err)
	}
	for name, bad := range map[string]DNSConfig{
		"no base domain":   {MagicDNS: true},
		"conflict":         {MagicDNS: true, BaseDomain: "example.com"},
		"bad nameserver":   {Global: []string{"dns.example.com"}},
		"empty split":      {Split: []SplitDNS{{Domain: "corp.example.com"}}},
		"bad split domain": {Split: []SplitDNS{{Domain: "10.0.0.1", Nameservers: []string{"10.0.0.2"}}}},
		"bad search":       {SearchDomains: []string{"not a domain"}},
	} {
		if err := bad.Check("https://headscale.example.com"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseRecord(t *testing.T) {
	r, err := ParseRecord("App-1.tailnet.example.com. a 10.0.0.5")
	if err != nil || r.Name != "app-1.tailnet.example.com" || r.Type != "A" {
		t.Errorf("record = %+v, %v", r, err)
	}
	for _, bad := range []string{"x A", "app.example.com CNAME other.example.com",
		"app.example.com A fd7a::1", "app.example.com AAAA 10.0.0.1", "app A 10.0.0.1 extra"} {
		if _, err := ParseRecord(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !RecordOutsideBase(DNSRecord{Name: "app.other.com"}, "tailnet.example.com") ||
		RecordOutsideBase(DNSRecord{Name: "app.tailnet.example.com"}, "tailnet.example.com") {
		t.Error("RecordOutsideBase")
	}
}
