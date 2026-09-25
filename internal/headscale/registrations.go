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
//
// With OIDC configured, the browser half matters too (issue #26): the
// /register/<id> URL answers with a redirect to the identity provider, and
// the provider's answer comes back on /oidc/callback, where headscale applies
// allowed_groups, allowed_domains and allowed_users. A login they turn away
// is logged as "user msg: unauthorised group" (or domain, or user) and a 401
// on the callback, and the registration stays in the cache, looking like any
// other waiting node. Registering it from the CLI would admit the machine the
// policy just refused, so each registration also carries whether its browser
// login went to the identity provider, and whether that login was refused.
// The callback does not name the registration (its state parameter maps to a
// cookie), so a refusal is attributed to the newest registration whose
// browser login is still open; the tool refuses R for every registration that
// went to the provider anyway, so the attribution only chooses the words.

// RegistrationWindow is how far back the journal is read: headscale keeps a
// pending registration for 15 minutes (tuning.register_cache_expiration).
const RegistrationWindow = 15 * time.Minute

// Registration is one node waiting for its login to be confirmed.
type Registration struct {
	// AuthID is headscale's registration id, hskey-authreq-….
	AuthID string `json:"-"`
	// Seen is when headscale logged it, zero when the log line had no time.
	Seen time.Time `json:"-"`
	// AtIdP reports that its /register URL was opened and redirected to the
	// identity provider: the login is the provider's to finish, and the CLI
	// must not finish it around the provider's policy.
	AtIdP bool `json:"-"`
	// Refused reports that the identity provider's policy turned the login
	// away, and RefusedBy names the list that did it (allowed_groups,
	// allowed_domains or allowed_users), empty when the log only had the 401.
	Refused   bool   `json:"-"`
	RefusedBy string `json:"-"`
}

// RefusedReason says why the identity provider refused a registration.
func (r Registration) RefusedReason() string {
	if r.RefusedBy == "" {
		return "the login callback answered 401"
	}
	return "not in " + r.RefusedBy
}

// Registrable reports whether R may finish a registration from the CLI, and
// says why not when it may not: a login the identity provider refused, or
// one that is at the provider now, is the provider's to decide.
func (r Registration) Registrable() (bool, string) {
	switch {
	case r.Refused:
		return false, r.AuthID + " was refused by the identity provider's policy (" +
			r.RefusedReason() + "): registering it with R would admit the machine the " +
			"policy turned away"
	case r.AtIdP:
		return false, r.AuthID + " is logging in at the identity provider: its browser " +
			"finishes it, and R would register it without the provider's policy"
	}
	return true, ""
}

// authIDPattern is a registration id as headscale prints it.
const authIDPattern = `hskey-authreq-[A-Za-z0-9_-]{8,64}`

var (
	startedPattern   = regexp.MustCompile(`registration using auth id: (` + authIDPattern + `)`)
	confirmedPattern = regexp.MustCompile(`/register/confirm/(` + authIDPattern + `)`)
	validAuthID      = regexp.MustCompile(`^` + authIDPattern + `$`)
	// browserPattern is the GET of a /register/<id> page; with OIDC it
	// answers with a redirect (3xx) to the identity provider.
	browserPattern = regexp.MustCompile(`method=GET\b.*path=/register/(` + authIDPattern +
		`)\b.*status=(3\d\d)\b`)
	// callbackPattern is the identity provider's answer coming back, and the
	// status headscale gave it.
	callbackPattern = regexp.MustCompile(`path=/oidc/callback\b.*status=(\d{3})\b`)
	// unauthorisedPattern is the policy refusal headscale logs for the
	// callback: which allow list turned the login away.
	unauthorisedPattern = regexp.MustCompile(`user msg: unauthori[sz]ed (group|domain|user)\b`)
)

// allowList names the config key an "unauthorised …" refusal comes from.
var allowList = map[string]string{
	"group": "allowed_groups", "domain": "allowed_domains", "user": "allowed_users",
}

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
// Each one also says whether its browser login went to the identity provider,
// and whether the provider's policy refused it (see the top of this file).
func ParseRegistrations(out string, now time.Time) []Registration {
	started := map[string]time.Time{}
	var order []string
	confirmed := map[string]bool{}
	atIdP := map[string]bool{}
	refusedBy := map[string]string{}
	refused := map[string]bool{}
	// browsers are the registrations whose login went to the provider and
	// has no outcome yet, oldest first; a refusal belongs to the newest.
	var browsers []string
	// A refusal is logged twice, the "unauthorised" line and the 401 of the
	// callback, in either order: the first one marks the registration and
	// leaves it here for the second to complete.
	awaitingCallback, awaitingReason := "", ""
	newestOpen := func() string {
		for i := len(browsers) - 1; i >= 0; i-- {
			if id := browsers[i]; !refused[id] && !confirmed[id] {
				return id
			}
		}
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if m := confirmedPattern.FindStringSubmatch(line); m != nil {
			confirmed[m[1]] = true
			continue
		}
		if m := browserPattern.FindStringSubmatch(line); m != nil {
			if !atIdP[m[1]] {
				browsers = append(browsers, m[1])
			}
			atIdP[m[1]] = true
			continue
		}
		if m := unauthorisedPattern.FindStringSubmatch(line); m != nil {
			if awaitingReason != "" {
				refusedBy[awaitingReason] = allowList[m[1]]
				awaitingReason = ""
				continue
			}
			if id := newestOpen(); id != "" {
				refused[id], refusedBy[id] = true, allowList[m[1]]
				awaitingCallback = id
			}
			continue
		}
		if m := callbackPattern.FindStringSubmatch(line); m != nil {
			if m[1] != "401" {
				continue
			}
			if awaitingCallback != "" {
				awaitingCallback = ""
				continue
			}
			if id := newestOpen(); id != "" {
				refused[id] = true
				awaitingReason = id
			}
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
		pending = append(pending, Registration{AuthID: id, Seen: seen, AtIdP: atIdP[id],
			Refused: refused[id], RefusedBy: refusedBy[id]})
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
