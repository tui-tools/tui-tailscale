package headscale

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// The local CA hand-off (issue #15). A private tailnet serves its control
// plane with a certificate from a local CA, and the family's tool for one is
// tui-cert. When it is installed, S's own-certificate step offers the pairs it
// issued before any file picker, and j's CA step the CAs it keeps: both read
// tui-cert's --check, which needs no privilege, changes nothing and opens no
// connection. tui-cert is started from this package only, like every other
// binary this tool runs.

// CertTool is the family tool that runs a local CA.
const CertTool = "tui-cert"

// CertToolURL is where tui-cert is documented, for the hint shown when it is
// not installed.
const CertToolURL = "https://tui.tools/tools/tui-cert/"

// IssuedChainFile and IssuedKeyFile are the names tui-cert gives an issued
// pair inside its directory: the chain the server presents, and its key.
const (
	IssuedChainFile = "fullchain.pem"
	IssuedKeyFile   = "privkey.pem"
)

// LocalCA is one certificate authority tui-cert keeps on this machine.
type LocalCA struct {
	Name        string
	CertPath    string
	Subject     string
	Fingerprint string
	NotAfter    time.Time
	// Trusted reports that this machine's trust store holds the CA.
	Trusted bool
	// CanIssue reports that the CA's key is on this machine.
	CanIssue bool
}

// IssuedPair is a certificate a local CA issued, with the key beside it.
type IssuedPair struct {
	// CA is the name of the local CA that signed the certificate.
	CA       string
	Subject  string
	SANs     []string
	CertPath string
	KeyPath  string
	NotAfter time.Time
}

// LocalPKI is what tui-cert reports about the local CAs on this machine.
type LocalPKI struct {
	// Installed reports that tui-cert is on this machine.
	Installed bool
	CAs       []LocalCA
	Pairs     []IssuedPair
	// Error is why tui-cert could not be read, when it could not.
	Error string
}

// tuiCertCheck is the part of tui-cert's --check JSON this tool reads.
type tuiCertCheck struct {
	Tool  string `json:"tool"`
	Certs []struct {
		Path       string   `json:"path"`
		Subject    string   `json:"subject"`
		SANs       []string `json:"sans"`
		NotAfter   string   `json:"notAfter"`
		LocalCA    string   `json:"localCA"`
		Unreadable string   `json:"unreadable"`
	} `json:"certs"`
	CAs []struct {
		Name        string `json:"name"`
		CertPath    string `json:"certPath"`
		Subject     string `json:"subject"`
		Fingerprint string `json:"fingerprint"`
		NotAfter    string `json:"notAfter"`
		CanIssue    bool   `json:"canIssue"`
		Trusted     bool   `json:"trusted"`
		Unreadable  string `json:"unreadable"`
	} `json:"cas"`
}

// ParseTuiCertCheck reads tui-cert's --check JSON: its local CAs, and the
// certificates they issued whose key sits beside them under tui-cert's names.
// A row that could not be read is left out, since there is nothing in it to
// point a server at.
func ParseTuiCertCheck(out string) (LocalPKI, error) {
	var check tuiCertCheck
	if err := json.Unmarshal([]byte(out), &check); err != nil {
		return LocalPKI{}, fmt.Errorf("tui-cert --check is not JSON: %w", err)
	}
	if check.Tool != "" && check.Tool != CertTool {
		return LocalPKI{}, fmt.Errorf("--check came from %q, not %s", check.Tool, CertTool)
	}
	pki := LocalPKI{Installed: true}
	for _, ca := range check.CAs {
		if ca.Unreadable != "" || ca.CertPath == "" {
			continue
		}
		pki.CAs = append(pki.CAs, LocalCA{Name: ca.Name, CertPath: ca.CertPath,
			Subject: ca.Subject, Fingerprint: ca.Fingerprint,
			NotAfter: parseTime(ca.NotAfter), Trusted: ca.Trusted, CanIssue: ca.CanIssue})
	}
	for _, c := range check.Certs {
		if c.LocalCA == "" || c.Unreadable != "" || path.Base(c.Path) != IssuedChainFile {
			continue
		}
		pki.Pairs = append(pki.Pairs, IssuedPair{CA: c.LocalCA, Subject: c.Subject,
			SANs: c.SANs, CertPath: c.Path,
			KeyPath:  path.Join(path.Dir(c.Path), IssuedKeyFile),
			NotAfter: parseTime(c.NotAfter)})
	}
	return pki, nil
}

// Label is how a picker lists the pair: who issued it, for which name, and
// until when.
func (p IssuedPair) Label() string {
	name := p.Subject
	if name == "" && len(p.SANs) > 0 {
		name = p.SANs[0]
	}
	if extra := otherNames(name, p.SANs); extra != "" {
		name += " (" + extra + ")"
	}
	return "issued by " + p.CA + " · " + name + " · " + expiryWord(p.NotAfter)
}

// Label is how a picker lists the CA: its name, a fingerprint prefix to
// compare with the machine it came from, and its expiry.
func (c LocalCA) Label() string {
	parts := []string{c.Name}
	if fp := shortFingerprint(c.Fingerprint); fp != "" {
		parts = append(parts, "SHA-256 "+fp)
	}
	parts = append(parts, expiryWord(c.NotAfter))
	if c.Trusted {
		parts = append(parts, "already trusted here")
	}
	return strings.Join(parts, " · ")
}

// otherNames lists the SANs besides the subject, which is how an IP SAN shows
// up in a picker row.
func otherNames(subject string, sans []string) string {
	var extra []string
	for _, san := range sans {
		if san != subject {
			extra = append(extra, san)
		}
	}
	return strings.Join(extra, ", ")
}

// expiryWord is "expires 2027-10-10", or "expired 2026-08-22".
func expiryWord(t time.Time) string {
	if t.IsZero() {
		return "expiry unknown"
	}
	if t.Before(time.Now()) {
		return "expired " + t.Format(time.DateOnly)
	}
	return "expires " + t.Format(time.DateOnly)
}

// shortFingerprint keeps the first four bytes of a colon-separated
// fingerprint: enough to tell two CAs apart on a screen.
func shortFingerprint(fp string) string {
	parts := strings.Split(fp, ":")
	if len(parts) > 4 {
		parts = parts[:4]
	}
	return strings.Join(parts, ":")
}

// ReadLocalPKI runs tui-cert's --check, unprivileged, when tui-cert is
// installed. Not being installed is an answer, not an error.
func (r *Real) ReadLocalPKI(ctx context.Context) LocalPKI {
	if !runner.Available(CertTool, searchPaths[CertTool]...) {
		return LocalPKI{}
	}
	run, err := r.runnerFor(CertTool, false)
	if err != nil {
		return LocalPKI{Installed: true, Error: runner.FirstLine(err.Error())}
	}
	out, err := run.Read(ctx, CertTool, "--check")
	if err != nil {
		return LocalPKI{Installed: true, Error: runner.FirstLine(err.Error())}
	}
	pki, err := ParseTuiCertCheck(out)
	if err != nil {
		return LocalPKI{Installed: true, Error: err.Error()}
	}
	return pki
}
