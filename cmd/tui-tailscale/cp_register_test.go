package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// The nodes screen shows the waiting node with its register URL on a line of
// its own, and R registers it as a user after a confirm.
func TestRegisterAWaitingNode(t *testing.T) {
	a, fake := fixtureApp(t, "")
	a.setScreen(screenNodes)
	url := headscale.DemoServerURL + "/register/" + headscale.DemoAuthID
	found := false
	for _, line := range strings.Split(ansi.Strip(a.View()), "\n") {
		if line == url {
			found = true
		}
	}
	if !found {
		t.Errorf("the register URL is not on a line of its own:\n%s", ansi.Strip(a.View()))
	}
	if !strings.Contains(a.View(), "waiting to register: 1 node") {
		t.Error("the waiting node is not announced")
	}
	model, _ := a.Update(key("R"))
	a = model.(*app)
	a = pick(t, a, "ops@example.com")
	if a.confirm.Command != "sudo -n headscale nodes register --key "+headscale.DemoAuthID+
		" --user ops@example.com" {
		t.Fatalf("preview = %q", a.confirm.Command)
	}
	// The demo control plane has OIDC: the confirm is a danger one that
	// names the bypass (issue #26).
	if !a.confirm.Danger || !strings.Contains(a.confirm.Body,
		"without the identity provider's policy") {
		t.Errorf("confirm = %+v, want a danger confirm naming the bypass", a.confirm)
	}
	a = confirmAndRun(t, a)
	state, _ := fake.Load(t.Context())
	// Only the registration the identity provider refused is left.
	if len(state.Registrations) != 1 || state.Registrations[0].AuthID != headscale.DemoRefusedAuthID ||
		len(state.Nodes) != 4 {
		t.Errorf("after: %d waiting, %d nodes", len(state.Registrations), len(state.Nodes))
	}
	if !a.registered[headscale.DemoAuthID] {
		t.Error("the registered node is not recorded")
	}
	if regs, _ := a.registrables(); len(regs) != 0 {
		t.Errorf("still registrable: %+v", regs)
	}
}

// On 0.29 and later, R uses `headscale auth register`.
func TestRegisterUsesAuthRegisterOnNewHeadscale(t *testing.T) {
	a, _ := fixtureApp(t, "")
	a.hsCompat.Version = "0.29.3"
	a.setScreen(screenNodes)
	model, _ := a.Update(key("R"))
	a = pick(t, model.(*app), "user@example.com")
	if !strings.Contains(a.confirm.Command, "headscale auth register --auth-id "+headscale.DemoAuthID) {
		t.Errorf("preview = %q", a.confirm.Command)
	}
}

// The hint bar leads with the key of the readiness line's next step: S on the
// users screen of the demo, whose unit is disabled.
func TestHintBarLeadsWithTheNextStep(t *testing.T) {
	a := newCPApp(t)
	a.setScreen(screenUsers)
	hints := a.shortHelpKeys()
	if hints[1].Key != "S" || !strings.Contains(hints[1].Desc, "next") {
		t.Errorf("hints = %+v, want S first and marked", hints)
	}
	// On the node screen, j leads when the next step is the first node.
	a.hsState.Nodes, a.hsState.Registrations = nil, nil
	a.hsState.ControlPlane.ServiceEnabled = "enabled"
	a.setScreen(screenNode)
	hints = a.shortHelpKeys()
	if hints[1].Key != "j" || !strings.Contains(hints[1].Desc, "next") {
		t.Errorf("node hints = %+v, want j first and marked", hints)
	}
}

// A registration the identity provider's policy refused is shown as refused,
// and R refuses it: it never reaches a user picker or a confirm (issue #26).
func TestRegisterRefusesARegistrationTheIdPRefused(t *testing.T) {
	a, fake := fixtureApp(t, "")
	a.hsState.Registrations = []headscale.Registration{{AuthID: headscale.DemoRefusedAuthID,
		Seen: time.Now().Add(-37 * time.Second), AtIdP: true, Refused: true,
		RefusedBy: "allowed_groups"}}
	a.setScreen(screenNodes)
	view := ansi.Strip(a.View())
	if !strings.Contains(view, "refused by the identity provider's policy (not in allowed_groups): 1 node") {
		t.Errorf("the refusal is not shown:\n%s", view)
	}
	if strings.Contains(view, "waiting to register") {
		t.Errorf("the refused registration is shown as waiting:\n%s", view)
	}
	if a.nextKey() == "R" {
		t.Error("R is the next step for a refused registration")
	}
	model, _ := a.Update(key("R"))
	a = model.(*app)
	if a.mode != modeBrowse || !strings.Contains(a.status, "refused by the identity provider") {
		t.Errorf("mode %v, status %q: R did not refuse", a.mode, a.status)
	}
	for _, cmd := range fake.Commands() {
		if strings.Contains(cmd.String(), "register") {
			t.Errorf("ran %q", cmd.String())
		}
	}
}

// A registration whose browser login is at the identity provider is not R's
// to finish either: the browser finishes it.
func TestRegisterRefusesALoginAtTheIdP(t *testing.T) {
	a, _ := fixtureApp(t, "")
	a.hsState.Registrations = []headscale.Registration{{AuthID: headscale.DemoAuthID, AtIdP: true}}
	a.setScreen(screenNodes)
	if !strings.Contains(ansi.Strip(a.View()), "logging in at the identity provider") {
		t.Errorf("the login at the provider is not said:\n%s", ansi.Strip(a.View()))
	}
	model, _ := a.Update(key("R"))
	a = model.(*app)
	if a.mode != modeBrowse || !strings.Contains(a.status, "logging in at the identity provider") {
		t.Errorf("mode %v, status %q: R did not refuse", a.mode, a.status)
	}
}

// Without OIDC, R keeps its plain confirm: there is no policy to bypass.
func TestRegisterWithoutOIDCIsAPlainConfirm(t *testing.T) {
	a, _ := fixtureApp(t, "")
	a.hsState.ControlPlane.OIDC = headscale.OIDCConfig{}
	a.hsState.OIDCInferred = false
	a.setScreen(screenNodes)
	if !strings.Contains(ansi.Strip(a.View()), "R registers it as a user") {
		t.Errorf("view:\n%s", ansi.Strip(a.View()))
	}
	model, _ := a.Update(key("R"))
	a = pick(t, model.(*app), "ops@example.com")
	if a.confirm.Danger || strings.Contains(a.confirm.Body, "identity provider") {
		t.Errorf("confirm = %+v", a.confirm)
	}
}

// The users panel says where the relays come from, and the ready line adds
// the note about Tailscale's public DERP servers (issue #27).
func TestRelaysOnThePanelAndTheReadyLine(t *testing.T) {
	a, _ := fixtureApp(t, "")
	if !strings.Contains(ansi.Strip(a.View()), "relays      Tailscale's public DERP servers") {
		t.Errorf("no relays line:\n%s", ansi.Strip(a.View()))
	}
	a.hsState.Registrations = nil
	for i := range a.hsState.Nodes {
		a.hsState.Nodes[i].ApprovedRoutes = a.hsState.Nodes[i].AvailableRoutes
	}
	a.width = 400
	if line := ansi.Strip(a.readinessLine()); !strings.Contains(line, "readiness") ||
		!strings.Contains(line, "Tailscale's public DERP servers") {
		t.Errorf("ready line = %q", line)
	}
}
