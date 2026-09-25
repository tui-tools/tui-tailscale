package tailscale

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/runner"
)

// This file is the private-CA half of `j` (issue #15): a control plane served
// with a certificate from a local certificate authority, which the client's
// system trust store does not know. `tailscale up` against it fails with a
// TLS error the node screen can do nothing with. So before the join, the
// login server's certificate is checked from this machine with the trust
// store tailscaled uses, and when it does not verify, the join offers a
// previewed "trust this CA" step: the CA certificate is installed where the
// distribution's trust store takes anchors, the store is rebuilt, and
// tailscaled is restarted, because a running daemon keeps the roots it
// loaded at start.

// TLSCheck is what the certificate check found.
type TLSCheck string

const (
	// TLSVerified: the certificate verifies against the system trust store.
	TLSVerified TLSCheck = "verified"
	// TLSUntrusted: it does not, because its issuer is not trusted here — the
	// case a CA certificate fixes.
	TLSUntrusted TLSCheck = "untrusted"
	// TLSMismatch: it is trusted but not for this name, which no CA fixes.
	TLSMismatch TLSCheck = "mismatch"
	// TLSUnreachable: the server did not answer at all.
	TLSUnreachable TLSCheck = "unreachable"
)

// BuildTLSCheck is the read that checks the login server's certificate: a
// request that only completes when the TLS handshake verifies against the
// system trust store, whose body is thrown away. It changes nothing and runs
// as the invoking user.
func BuildTLSCheck(server string) (runner.Command, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if problem := ServerURLProblem(server); problem != "" {
		return runner.Command{}, fmt.Errorf("login server: %s", problem)
	}
	return runner.Command{
		Argv:        []string{"curl", "-sS", "-o", "/dev/null", "--max-time", "8", server + "/health"},
		Description: "Check the certificate of " + URLHost(server),
	}, nil
}

// ClassifyTLSCheck reads the check's outcome from curl's error text.
func ClassifyTLSCheck(output string, err error) TLSCheck {
	if err == nil {
		return TLSVerified
	}
	text := strings.ToLower(output + " " + err.Error())
	switch {
	case strings.Contains(text, "ssl certificate problem") ||
		strings.Contains(text, "certificate verify failed") ||
		strings.Contains(text, "self-signed") || strings.Contains(text, "self signed") ||
		strings.Contains(text, "unable to get local issuer"):
		return TLSUntrusted
	case strings.Contains(text, "(60)") || strings.Contains(text, "subject name") ||
		strings.Contains(text, "does not match"):
		return TLSMismatch
	}
	return TLSUnreachable
}

// caPathPattern is a CA file's path: absolute, and plain enough to be one
// argument.
var caPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/@+-]{1,255}$`)

// ValidCAPath reports whether s can name the CA certificate to trust.
func ValidCAPath(s string) bool {
	return caPathPattern.MatchString(s) && !strings.Contains(s, "/../") &&
		!strings.HasSuffix(s, "/..") && !strings.HasSuffix(s, "/")
}

// CAName is the name a login server's CA is installed under in the trust
// store: tui-tailscale-<host>, so the file says who put it there and for
// which server.
func CAName(server string) string {
	host := strings.ToLower(URLHost(server))
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return "tui-tailscale-" + strings.Trim(b.String(), ".-")
}

// Trust store locations, per family.
const (
	// DebianAnchors is where update-ca-certificates takes local CAs from;
	// the file must end in .crt.
	DebianAnchors = "/usr/local/share/ca-certificates"
	// FedoraAnchors is where update-ca-trust takes anchors from.
	FedoraAnchors = "/etc/pki/ca-trust/source/anchors"
)

// buildTrustCA installs a CA certificate into the system trust store, the way
// the distribution documents it, and restarts tailscaled so it reads it.
func buildTrustCA(req Request) (Plan, error) {
	if !ValidCAPath(req.CAPath) {
		return Plan{}, fmt.Errorf("not an absolute, plain path to a CA certificate: %q", req.CAPath)
	}
	if !strings.HasPrefix(req.CAName, "tui-tailscale-") || strings.ContainsAny(req.CAName, "/ ") {
		return Plan{}, fmt.Errorf("not a trust store name: %q", req.CAName)
	}
	plan := Plan{Action: ActionTrustCA, Title: "Trust " + req.CAPath + " as a certificate authority"}
	var store string
	switch req.Distro.Manager() {
	case pkgmgr.ManagerAPT:
		dest := DebianAnchors + "/" + req.CAName + ".crt"
		store = dest + ", then update-ca-certificates rebuilds the system bundle"
		plan.Steps = []runner.Command{
			{Argv: []string{"install", "-m", "644", req.CAPath, dest},
				Description: "Add the CA to " + DebianAnchors},
			{Argv: []string{"update-ca-certificates"}, Description: "Rebuild the trust store"},
		}
	case pkgmgr.ManagerDNF:
		dest := FedoraAnchors + "/" + req.CAName + ".crt"
		store = dest + ", then update-ca-trust rebuilds the system bundle"
		plan.Steps = []runner.Command{
			{Argv: []string{"install", "-m", "644", req.CAPath, dest},
				Description: "Add the CA to " + FedoraAnchors},
			{Argv: []string{"update-ca-trust"}, Description: "Rebuild the trust store"},
		}
	case pkgmgr.ManagerPacman:
		store = "the p11-kit anchor store (trust anchor --store), which rebuilds the bundle itself"
		plan.Steps = []runner.Command{
			{Argv: []string{"trust", "anchor", "--store", req.CAPath},
				Description: "Add the CA to the trust store"},
		}
	default:
		name := req.Distro.String()
		if name == "" {
			name = "this distribution"
		}
		return Plan{}, fmt.Errorf("no trust store steps for %s: add %s to it by hand", name, req.CAPath)
	}
	plan.Steps = append(plan.Steps, runner.Command{
		Argv: []string{"systemctl", "restart", "tailscaled"}, Description: "Restart tailscaled"})
	plan.Body = "The login server's certificate does not verify against this machine's trust " +
		"store, so tailscale would refuse it. The CA certificate is copied to " + store + ".\n\n" +
		"Every program on this machine that uses the system trust store will trust " +
		"certificates this CA signs, not only tailscale: add a CA you run yourself (tui-cert's, " +
		"say), never one you were handed without knowing whose it is.\n\n" +
		"tailscaled is restarted because a running daemon keeps the roots it loaded when it " +
		"started. If you are connected to this machine over the tailnet, that drops the session " +
		"for a moment. The join continues after this."
	return plan, nil
}
