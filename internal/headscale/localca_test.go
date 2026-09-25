package headscale

import (
	"os"
	"strings"
	"testing"
	"time"
)

// tui-cert's --check yields its CAs and the pairs they issued, each key
// beside its chain; certificates no local CA signed are not offered.
func TestParseTuiCertCheck(t *testing.T) {
	data, err := os.ReadFile("testdata/tui-cert-check.json")
	if err != nil {
		t.Fatal(err)
	}
	pki, err := ParseTuiCertCheck(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if !pki.Installed || len(pki.CAs) != 1 || len(pki.Pairs) != 2 {
		t.Fatalf("pki = %+v", pki)
	}
	ca := pki.CAs[0]
	if ca.Name != "homelab-ca" || ca.CertPath != "/etc/tui-cert/ca/homelab-ca/ca.crt" ||
		ca.Trusted || !ca.CanIssue || ca.NotAfter.IsZero() {
		t.Errorf("ca = %+v", ca)
	}
	var withIP IssuedPair
	for _, p := range pki.Pairs {
		if p.CA != "homelab-ca" || !strings.HasSuffix(p.CertPath, "/"+IssuedChainFile) ||
			!strings.HasSuffix(p.KeyPath, "/"+IssuedKeyFile) {
			t.Errorf("pair = %+v", p)
		}
		if p.Subject == "headscale.example.internal" {
			withIP = p
		}
	}
	if withIP.KeyPath != "/etc/tui-cert/issued/headscale.example.internal/privkey.pem" {
		t.Errorf("pair = %+v", withIP)
	}
	if label := withIP.Label(); !strings.HasPrefix(label,
		"issued by homelab-ca · headscale.example.internal (192.0.2.10) · expires ") {
		t.Errorf("label = %q", label)
	}
}

// A report that is not tui-cert's, or not JSON, is an error the pickers show
// as a hint rather than a list.
func TestParseTuiCertCheckRefuses(t *testing.T) {
	for _, out := range []string{"", "not json", `{"tool":"tui-firewall"}`} {
		if _, err := ParseTuiCertCheck(out); err == nil {
			t.Errorf("%q was accepted", out)
		}
	}
	// A row that could not be read, or whose key is not beside it under
	// tui-cert's names, is left out.
	pki, err := ParseTuiCertCheck(`{"tool":"tui-cert","cas":[{"name":"x","certPath":"",` +
		`"unreadable":"permission denied"}],"certs":[{"path":"/srv/tls/site.crt","localCA":"x"}]}`)
	if err != nil || len(pki.CAs) != 0 || len(pki.Pairs) != 0 {
		t.Errorf("pki = %+v, err %v", pki, err)
	}
}

func TestLocalCALabel(t *testing.T) {
	ca := LocalCA{Name: "homelab-ca", Fingerprint: "AA:BB:CC:DD:EE:FF",
		NotAfter: time.Date(2035, 9, 25, 0, 0, 0, 0, time.UTC), Trusted: true}
	if got := ca.Label(); got != "homelab-ca · SHA-256 AA:BB:CC:DD · expires 2035-09-25 · "+
		"already trusted here" {
		t.Errorf("label = %q", got)
	}
	expired := IssuedPair{CA: "old", Subject: "a.example.internal",
		NotAfter: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)}
	if got := expired.Label(); got != "issued by old · a.example.internal · expired 2020-01-02" {
		t.Errorf("label = %q", got)
	}
}
