package headscale

import (
	"strings"
	"testing"
	"time"
)

// journal is the shape headscale 0.29 logs a browser registration in, under
// journalctl -o short-unix: one confirmed, one still waiting, one expired.
const journal = `1790300000.100000 host headscale[1]: 2026-09-24T21:03:58Z INF starting node registration using auth id: hskey-authreq-AAAAAAAAAAAAAAAAAAAAAAAA
1790300004.000000 host headscale[1]: 2026-09-24T21:04:02Z INF http request method=GET path=/register/hskey-authreq-AAAAAAAAAAAAAAAAAAAAAAAA status=302
1790300025.000000 host headscale[1]: 2026-09-24T21:04:23Z INF http request method=POST path=/register/confirm/hskey-authreq-AAAAAAAAAAAAAAAAAAAAAAAA status=200
1790300100.000000 host headscale[1]: 2026-09-24T21:05:38Z INF new followup node registration using auth id: hskey-authreq-BBBBBBBBBBBB_BBBB-BBBBBB
1790299000.000000 host headscale[1]: 2026-09-24T20:47:18Z INF starting node registration using auth id: hskey-authreq-CCCCCCCCCCCCCCCCCCCCCCCC
`

func TestParseRegistrations(t *testing.T) {
	now := time.Unix(1790300200, 0)
	got := ParseRegistrations(journal, now)
	if len(got) != 1 || got[0].AuthID != "hskey-authreq-BBBBBBBBBBBB_BBBB-BBBBBB" {
		t.Fatalf("pending = %+v: want only the waiting one (confirmed and expired dropped)", got)
	}
	if got[0].Seen.Unix() != 1790300100 {
		t.Errorf("seen = %v", got[0].Seen)
	}
	if got := ParseRegistrations("", now); got != nil {
		t.Errorf("empty journal: %+v", got)
	}
}

func TestBuildRegisterNode(t *testing.T) {
	id := "hskey-authreq-BBBBBBBBBBBB_BBBB-BBBBBB"
	cmd, err := BuildRegisterNode(id, "user@example.com", true)
	if err != nil || strings.Join(cmd.Argv, " ") !=
		"headscale auth register --auth-id "+id+" --user user@example.com" {
		t.Fatalf("0.29: %q %v", cmd.Argv, err)
	}
	cmd, _ = BuildRegisterNode(id, "user@example.com", false)
	if strings.Join(cmd.Argv, " ") != "headscale nodes register --key "+id+" --user user@example.com" {
		t.Errorf("older: %q", cmd.Argv)
	}
	for _, bad := range [][2]string{{"hskey-authreq-x", "u"}, {id, "-u"}, {id, "a b"}, {"--x", "u"}} {
		if _, err := BuildRegisterNode(bad[0], bad[1], true); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for v, want := range map[string]bool{"0.29.3": true, "v0.29.0": true, "0.28.0": false,
		"1.0.0": true, "": false, "0.30.0-beta.1": true} {
		if HasAuthRegister(v) != want {
			t.Errorf("HasAuthRegister(%q) = %v", v, !want)
		}
	}
	if RegisterURL("https://headscale.example.com/", id) !=
		"https://headscale.example.com/register/"+id {
		t.Error("RegisterURL")
	}
}

// With no node yet, a waiting registration is the next step.
func TestReadinessPointsAtAWaitingRegistration(t *testing.T) {
	f := NewFake()
	f.SetService("active", "enabled")
	state, _ := f.Load(t.Context())
	state.Nodes = nil
	r := ReadinessFor(state, time.Now())
	if r.Next != NextFirstNode || r.PendingRegistrations != 2 || r.RefusedRegistrations != 1 ||
		!strings.Contains(r.NextStep, "R on the nodes screen") {
		t.Errorf("readiness = %+v", r)
	}
	if strings.Contains(r.NextStep, "hskey-") || strings.Contains(r.NextStep, "://") {
		t.Errorf("the readiness line names the registration: %q", r.NextStep)
	}
}

// oidcJournal is a browser join refused by allowed_groups, in the lines
// headscale 0.29.3 logged for it on a lab control plane with Keycloak (issue
// #26), plus one login that is still at the identity provider and one plain
// CLI registration nobody opened in a browser. The refusal comes as the
// "unauthorised group" line and the 401 of the callback, which does not name
// the registration.
const oidcJournal = `1790300300.000000 host headscale[1]: 2026-09-24T21:08:20Z INF starting node registration using auth id: hskey-authreq-AQ8ZrefusedAAAAAAAAAAAA
1790300301.000000 host headscale[1]: 2026-09-24T21:08:21Z INF http request bytes=0 elapsed=0.412 method=GET path=/register/hskey-authreq-AQ8ZrefusedAAAAAAAAAAAA proto=HTTP/1.1 remote=192.0.2.10:51234 status=302
1790300330.000000 host headscale[1]: 2026-09-24T21:08:50Z ERR user msg: unauthorised group error="authenticated principal is not in any allowed group" code=401
1790300330.100000 host headscale[1]: 2026-09-24T21:08:50Z INF http request bytes=213 elapsed=38.9 method=GET path=/oidc/callback proto=HTTP/1.1 remote=192.0.2.10:51240 status=401
1790300400.000000 host headscale[1]: 2026-09-24T21:10:00Z INF starting node registration using auth id: hskey-authreq-ATIDPloggingInAAAAAAAAAA
1790300401.000000 host headscale[1]: 2026-09-24T21:10:01Z INF http request bytes=0 elapsed=0.3 method=GET path=/register/hskey-authreq-ATIDPloggingInAAAAAAAAAA proto=HTTP/1.1 remote=192.0.2.11:40000 status=302
1790300450.000000 host headscale[1]: 2026-09-24T21:10:50Z INF starting node registration using auth id: hskey-authreq-CLIonlyNoBrowserAAAAAAAA
`

func TestParseRegistrationsSeesTheIdPRefusal(t *testing.T) {
	got := ParseRegistrations(oidcJournal, time.Unix(1790300500, 0))
	if len(got) != 3 {
		t.Fatalf("pending = %+v", got)
	}
	byID := map[string]Registration{}
	for _, r := range got {
		byID[r.AuthID] = r
	}
	refused := byID["hskey-authreq-AQ8ZrefusedAAAAAAAAAAAA"]
	if !refused.AtIdP || !refused.Refused || refused.RefusedBy != "allowed_groups" ||
		refused.RefusedReason() != "not in allowed_groups" {
		t.Errorf("refused = %+v", refused)
	}
	if ok, why := refused.Registrable(); ok || !strings.Contains(why, "refused by the identity provider") {
		t.Errorf("refused registrable = %v %q", ok, why)
	}
	atIdP := byID["hskey-authreq-ATIDPloggingInAAAAAAAAAA"]
	if !atIdP.AtIdP || atIdP.Refused {
		t.Errorf("at the provider = %+v", atIdP)
	}
	if ok, _ := atIdP.Registrable(); ok {
		t.Error("a login at the provider is registrable")
	}
	cli := byID["hskey-authreq-CLIonlyNoBrowserAAAAAAAA"]
	if cli.AtIdP || cli.Refused {
		t.Errorf("cli = %+v", cli)
	}
	if ok, _ := cli.Registrable(); !ok {
		t.Error("a plain CLI registration is not registrable")
	}
}

// The 401 of the callback alone still refuses, and the reason line may come
// after it; other callback errors (a stale state, 403) are not a refusal.
func TestParseRegistrationsRefusalOrder(t *testing.T) {
	const id = "hskey-authreq-OrderTestAAAAAAAAAAAAAAA"
	start := "1790300300.0 host headscale[1]: INF starting node registration using auth id: " + id + "\n" +
		"1790300301.0 host headscale[1]: INF http request method=GET path=/register/" + id + " status=302\n"
	now := time.Unix(1790300400, 0)
	got := ParseRegistrations(start+
		"1790300330.1 host headscale[1]: INF http request method=GET path=/oidc/callback status=401\n"+
		"1790300330.2 host headscale[1]: ERR user msg: unauthorised domain error=\"x\" code=401\n", now)
	if len(got) != 1 || !got[0].Refused || got[0].RefusedBy != "allowed_domains" {
		t.Errorf("callback first = %+v", got)
	}
	got = ParseRegistrations(start+
		"1790300330.1 host headscale[1]: INF http request method=GET path=/oidc/callback status=401\n", now)
	if len(got) != 1 || !got[0].Refused || got[0].RefusedReason() != "the login callback answered 401" {
		t.Errorf("401 alone = %+v", got)
	}
	got = ParseRegistrations(start+
		"1790300330.1 host headscale[1]: INF http request method=GET path=/oidc/callback status=403\n", now)
	if len(got) != 1 || got[0].Refused || !got[0].AtIdP {
		t.Errorf("403 = %+v", got)
	}
}

// The readiness line says a refused login for what it is, and does not send
// the operator to R.
func TestReadinessSaysTheRefusedLogin(t *testing.T) {
	f := NewFake()
	f.SetService("active", "enabled")
	f.SetRegistrations([]Registration{{AuthID: DemoRefusedAuthID, AtIdP: true, Refused: true,
		RefusedBy: "allowed_groups"}})
	state, _ := f.Load(t.Context())
	state.Nodes = nil
	r := ReadinessFor(state, time.Now())
	if r.Next != NextFirstNode || r.RefusedRegistrations != 1 ||
		!strings.Contains(r.NextStep, "refused by the identity provider's policy (not in allowed_groups)") ||
		strings.Contains(r.NextStep, "R on the nodes screen") {
		t.Errorf("readiness = %+v", r)
	}
}
