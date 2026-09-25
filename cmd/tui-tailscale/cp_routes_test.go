package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// selectNode moves the nodes screen's cursor onto a node by id.
func selectNode(t *testing.T, a *app, id string) {
	t.Helper()
	a.setScreen(screenNodes)
	for i, n := range a.hsState.Nodes {
		if n.ID == id {
			a.cursor[screenNodes] = i
			return
		}
	}
	t.Fatalf("no node %s", id)
}

// TestApproveRoutes drives r on the demo's subnet router, office-router: the
// dialog is prefilled with everything it advertises, the confirm says what
// changes and previews approve-routes, and the node then serves both subnets.
// Taking the list back to empty revokes everything, in a danger dialog.
func TestApproveRoutes(t *testing.T) {
	a := newCPApp(t)
	selectNode(t, a, "3")
	if view := a.View(); !strings.Contains(view, "ROUTES") ||
		!strings.Contains(view, "1/2 approved") ||
		!strings.Contains(view, "routes of office-router: 192.0.2.0/24 ✓ · 198.51.100.0/24 pending") {
		t.Errorf("the nodes screen does not show the routes:\n%s", view)
	}
	if got := headscale.RoutesText(a.hsState.Nodes[2]); !strings.Contains(got, "pending") {
		t.Errorf("the demo router should have routes pending: %q", got)
	}
	model, _ := a.Update(key("r"))
	a = model.(*app)
	if a.mode != modeInput || a.inputPurpose != inputApproveRoutes {
		t.Fatalf("r did not open the routes dialog (mode %d, loading %v)", a.mode, a.loading)
	}
	if got := a.input.Model.Value(); got != "192.0.2.0/24, 198.51.100.0/24" {
		t.Errorf("prefill = %q, want every advertised route", got)
	}
	a = enter(t, a)
	if a.mode != modeConfirm {
		t.Fatalf("no confirm (status %q)", a.status)
	}
	if a.confirm.Command != "sudo -n headscale nodes approve-routes --identifier 3 --routes "+
		"192.0.2.0/24,198.51.100.0/24" {
		t.Errorf("preview = %q", a.confirm.Command)
	}
	if !strings.Contains(a.confirm.Body, "Approves: 198.51.100.0/24") || a.confirm.Danger {
		t.Errorf("body (danger %v):\n%s", a.confirm.Danger, a.confirm.Body)
	}
	a = confirmAndRun(t, a)
	state, _ := a.hs.Load(t.Context())
	a.hsState = state
	if got := headscale.RoutesText(state.Nodes[2]); got != "192.0.2.0/24 ✓ · 198.51.100.0/24 ✓" {
		t.Errorf("after approving: %q", got)
	}

	// The exit node's two routes read as one word, "exit".
	selectNode(t, a, "2")
	model, _ = a.Update(key("r"))
	a = model.(*app)
	if got := a.input.Model.Value(); got != "exit" {
		t.Errorf("exit-gateway prefill = %q, want exit", got)
	}
	a = enter(t, a)
	if a.mode == modeConfirm || !strings.Contains(a.status, "already has exactly these routes") {
		t.Errorf("an unchanged list: mode %d, status %q", a.mode, a.status)
	}

	// Revoking all: an empty line is an answer, and a dangerous one.
	selectNode(t, a, "3")
	model, _ = a.Update(key("r"))
	a = model.(*app)
	a.input.Model.SetValue("")
	a = enter(t, a)
	if !strings.HasSuffix(a.confirm.Command, "--routes=") || !a.confirm.Danger ||
		!strings.Contains(a.confirm.Body, "Revokes:") {
		t.Errorf("revoke all: %q (danger %v)\n%s", a.confirm.Command, a.confirm.Danger, a.confirm.Body)
	}
}

// TestRoutesOnANodeWithoutAny: r says there is nothing to approve, and does
// not reload; ctrl+r still reloads on the nodes screen.
func TestRoutesOnANodeWithoutAny(t *testing.T) {
	a := newCPApp(t)
	selectNode(t, a, "1")
	model, _ := a.Update(key("r"))
	a = model.(*app)
	if a.mode != modeBrowse || !strings.Contains(a.status, "advertises no routes") || a.loading {
		t.Errorf("mode %d, status %q, loading %v", a.mode, a.status, a.loading)
	}
	model, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	a = model.(*app)
	if cmd == nil || !a.loading {
		t.Error("ctrl+r did not reload on the nodes screen")
	}
}

// TestCheckCountsRoutes: --check reports each node's routes as counts, never
// the networks.
func TestCheckCountsRoutes(t *testing.T) {
	var out strings.Builder
	if err := runCheck(context.Background(), tailscale.NewFake(), headscale.NewFake(), nil, &out); err != nil {
		t.Fatal(err)
	}
	var report checkReport
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatal(err)
	}
	routes := report.Headscale.NodeRoutes
	if len(routes) != 2 || routes[0].ID != "2" || routes[0].ExitNode != "approved" ||
		routes[1].ID != "3" || routes[1].Advertised != 2 || routes[1].Approved != 1 ||
		routes[1].Pending != 1 || routes[1].ExitNode != "" {
		t.Errorf("nodeRoutes = %+v", routes)
	}
	if r := report.Headscale.Readiness; r.RoutesPending != 1 || r.RoutesApproved {
		t.Errorf("readiness = %+v", r)
	}
	if strings.Contains(out.String(), "198.51.100.0") || strings.Contains(out.String(), "0.0.0.0/0") {
		t.Error("--check printed a route")
	}
}
