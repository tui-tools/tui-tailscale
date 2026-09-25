package main

import (
	"strings"
	"testing"

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
	a = confirmAndRun(t, a)
	state, _ := fake.Load(t.Context())
	if len(state.Registrations) != 0 || len(state.Nodes) != 4 {
		t.Errorf("after: %d waiting, %d nodes", len(state.Registrations), len(state.Nodes))
	}
	if !a.registered[headscale.DemoAuthID] || len(a.pendingRegistrations()) != 0 {
		t.Error("the registered node is still shown as waiting")
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
