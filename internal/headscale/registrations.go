package headscale

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// This file is the pending registrations the guided setup needs to see (issue
// #12). A client that runs `tailscale up --login-server …` without a key
// starts a registration headscale keeps in a cache for a while and nowhere
// else: `headscale nodes list` does not show it, and the client itself may
// print nothing (a stale identity from another control plane, say). headscale
// logs it, though — "starting node registration using auth id: hskey-authreq-…"
// — and logs the browser's confirm as a POST to /register/confirm/<id>. So the
// journal of the last RegistrationWindow is read, and every id started there
// and not confirmed is a node waiting: its /register/<id> URL finishes it in
// a browser, and `headscale auth register` (0.29+) or `headscale nodes
// register` finishes it from here, as a user the operator picks.

// RegistrationWindow is how far back the journal is read: headscale keeps a
// pending registration for 15 minutes (tuning.register_cache_expiration).
const RegistrationWindow = 15 * time.Minute

// Registration is one node waiting for its login to be confirmed.
type Registration struct {
	// AuthID is headscale's registration id, hskey-authreq-….
	AuthID string `json:"-"`
	// Seen is when headscale logged it, zero when the log line had no time.
	Seen time.Time `json:"-"`
}

// authIDPattern is a registration id as headscale prints it.
const authIDPattern = `hskey-authreq-[A-Za-z0-9_-]{8,64}`

var (
	startedPattern   = regexp.MustCompile(`registration using auth id: (` + authIDPattern + `)`)
	confirmedPattern = regexp.MustCompile(`/register/confirm/(` + authIDPattern + `)`)
	validAuthID      = regexp.MustCompile(`^` + authIDPattern + `$`)
)

// ValidAuthID reports whether s is a registration id.
func ValidAuthID(s string) bool { return validAuthID.MatchString(s) }

// JournalArgv is the read of headscale's recent log.
func JournalArgv() []string {
	return []string{"journalctl", "-u", HeadscaleService, "-o", "short-unix", "--no-pager",
		"--since=-" + strconv.Itoa(int(RegistrationWindow.Seconds())) + "s"}
}

// ParseRegistrations reads the journal (short-unix: an epoch timestamp first)
// into the registrations still pending, newest first. A registration seen
// more than RegistrationWindow before now has expired from headscale's cache.
func ParseRegistrations(out string, now time.Time) []Registration {
	started := map[string]time.Time{}
	var order []string
	confirmed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if m := confirmedPattern.FindStringSubmatch(line); m != nil {
			confirmed[m[1]] = true
			continue
		}
		m := startedPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if _, dup := started[m[1]]; !dup {
			order = append(order, m[1])
		}
		started[m[1]] = journalTime(line)
	}
	var pending []Registration
	for i := len(order) - 1; i >= 0; i-- {
		id := order[i]
		seen := started[id]
		if confirmed[id] || (!seen.IsZero() && now.Sub(seen) > RegistrationWindow) {
			continue
		}
		pending = append(pending, Registration{AuthID: id, Seen: seen})
	}
	return pending
}

// journalTime reads a short-unix line's leading epoch timestamp.
func journalTime(line string) time.Time {
	field, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	secs, err := strconv.ParseFloat(field, 64)
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	whole := int64(secs)
	return time.Unix(whole, int64((secs-float64(whole))*1e9)).UTC()
}

// RegisterURL is the browser page that finishes a registration.
func RegisterURL(serverURL, authID string) string {
	if serverURL == "" {
		return ""
	}
	return strings.TrimRight(serverURL, "/") + "/register/" + authID
}

// BuildRegisterNode finishes a pending registration from the CLI, as a user:
// `headscale auth register` on 0.29 and later, `headscale nodes register` (its
// deprecated name, the only one before 0.29) otherwise.
func BuildRegisterNode(authID, user string, authCommand bool) (runner.Command, error) {
	if !ValidAuthID(authID) {
		return runner.Command{}, fmt.Errorf("not a registration id: %q", authID)
	}
	if !ValidUserName(user) {
		return runner.Command{}, fmt.Errorf("not a valid user name: %q", user)
	}
	argv := []string{"headscale", "nodes", "register", "--key", authID, "--user", user}
	if authCommand {
		argv = []string{"headscale", "auth", "register", "--auth-id", authID, "--user", user}
	}
	return runner.Command{
		Argv:        argv,
		Description: "Register the waiting node as " + user,
	}, nil
}

// AuthRegisterSince is the first headscale release with `auth register`.
const AuthRegisterSince = "0.29"

// HasAuthRegister reports whether a headscale version has `auth register`.
func HasAuthRegister(version string) bool {
	major, minor, ok := majorMinor(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	if !ok {
		return false
	}
	return major > 0 || minor >= 29
}

// majorMinor reads the first two numbers of a version.
func majorMinor(v string) (int, int, bool) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(strings.TrimRightFunc(parts[1], func(r rune) bool {
		return r < '0' || r > '9'
	}))
	return major, minor, err1 == nil && err2 == nil
}

// ValidUserName reports whether s can name a headscale user on an argv: the
// rule BuildCreateUser applies to a new one.
func ValidUserName(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, " \t\n/")
}
