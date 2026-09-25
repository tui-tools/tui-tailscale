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
	if r.Next != NextFirstNode || r.PendingRegistrations != 1 ||
		!strings.Contains(r.NextStep, "R on the nodes screen") {
		t.Errorf("readiness = %+v", r)
	}
	if strings.Contains(r.NextStep, "hskey-") || strings.Contains(r.NextStep, "://") {
		t.Errorf("the readiness line names the registration: %q", r.NextStep)
	}
}
