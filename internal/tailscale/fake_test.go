package tailscale

import (
	"context"
	"strings"
	"testing"
)

// run builds a request on the fake, runs every command of its plan the way
// the app does, and returns the outputs and the first error.
func runPlan(t *testing.T, f *Fake, req Request) (string, error) {
	t.Helper()
	plan, err := BuildCommand(req)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	var out strings.Builder
	var firstErr error
	for _, cmd := range plan.Steps {
		text, err := f.Run(context.Background(), cmd)
		out.WriteString(text)
		if err != nil {
			firstErr = err
			break
		}
	}
	for _, cmd := range plan.Cleanup {
		_, _ = f.Run(context.Background(), cmd)
	}
	return out.String(), firstErr
}

func load(t *testing.T, f *Fake) State {
	t.Helper()
	s, err := f.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestFakePreviewMatchesWhatRuns(t *testing.T) {
	f := NewFake()
	plan, err := BuildCommand(Request{Action: ActionAcceptRoutes})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	preview := f.Preview(plan.Steps[0])
	if preview != "sudo -n tailscale set --accept-routes=false" {
		t.Errorf("preview = %q", preview)
	}
	if _, err := f.Run(context.Background(), plan.Steps[0]); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.Commands()) != 1 {
		t.Fatalf("ran %d commands, want 1", len(f.Commands()))
	}
	// The command that ran must be the one the preview showed, character for
	// character. This is the guarantee the whole family is built around.
	if got := f.Preview(f.Commands()[0]); got != preview {
		t.Errorf("ran %q, but the preview promised %q", got, preview)
	}
}

func TestFakeAppliesEveryAction(t *testing.T) {
	f := NewFake()

	if _, err := runPlan(t, f, Request{Action: ActionAcceptRoutes}); err != nil {
		t.Fatal(err)
	}
	if load(t, f).Prefs.RouteAll {
		t.Error("accept routes off did not apply")
	}

	if _, err := runPlan(t, f, Request{Action: ActionAdvertiseRoutes,
		Routes: []string{"198.51.100.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runPlan(t, f, Request{Action: ActionAdvertiseExitNode, Enable: true}); err != nil {
		t.Fatal(err)
	}
	s := load(t, f)
	if !s.Prefs.AdvertisesExitNode() || strings.Join(s.Prefs.SubnetRoutes(), ",") != "198.51.100.0/24" {
		t.Errorf("routes = %q", s.Prefs.AdvertiseRoutes)
	}
	// Editing the subnets keeps the exit node, the way `tailscale set` does.
	if _, err := runPlan(t, f, Request{Action: ActionAdvertiseRoutes}); err != nil {
		t.Fatal(err)
	}
	if s := load(t, f); !s.Prefs.AdvertisesExitNode() || len(s.Prefs.SubnetRoutes()) != 0 {
		t.Errorf("routes = %q", s.Prefs.AdvertiseRoutes)
	}

	if _, err := runPlan(t, f, Request{Action: ActionExitNode, ExitNode: "100.64.0.2"}); err != nil {
		t.Fatal(err)
	}
	if p, ok := load(t, f).CurrentExitNode(); !ok || p.Name() != "exit-gateway" {
		t.Errorf("exit node = %+v %v", p, ok)
	}
	if _, err := runPlan(t, f, Request{Action: ActionExitNode, ExitNode: "100.64.0.3"}); err == nil {
		t.Error("a peer that offers no exit node must be refused, as tailscale does")
	}

	if _, err := runPlan(t, f, Request{Action: ActionHostname, Hostname: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if n := load(t, f).Node; n.HostName != "renamed" {
		t.Errorf("hostname = %q", n.HostName)
	}

	if _, err := runPlan(t, f, Request{Action: ActionDown}); err != nil {
		t.Fatal(err)
	}
	if n := load(t, f).Node; n.BackendState != StateStopped || n.Online {
		t.Errorf("after down: %+v", n)
	}
	if _, err := runPlan(t, f, Request{Action: ActionUp}); err != nil {
		t.Fatal(err)
	}
	if n := load(t, f).Node; n.BackendState != StateRunning {
		t.Errorf("after up: %+v", n)
	}

	if _, err := runPlan(t, f, Request{Action: ActionLogout}); err != nil {
		t.Fatal(err)
	}
	if s := load(t, f); s.Node.BackendState != StateNeedsLogin || len(s.Peers) != 0 || s.LoggedIn() {
		t.Errorf("after logout: %+v", s)
	}
}

// A join without a key behaves like the real client under --timeout: the
// login URL comes back in the output, next to a timeout error, and the node
// waits for the browser.
func TestFakeBrowserJoin(t *testing.T) {
	f := NewFake()
	_, _ = runPlan(t, f, Request{Action: ActionLogout})
	out, err := runPlan(t, f, Request{Action: ActionJoin, LoginServer: DemoLoginServer})
	if err == nil {
		t.Error("the real client fails on its timeout; the fake has to as well")
	}
	want := DemoLoginServer + DemoRegisterPath
	if got := ParseLoginURL(out); got != want {
		t.Errorf("login URL = %q, want %q", got, want)
	}
	if s := load(t, f); s.Node.BackendState != StateNeedsLogin || s.Node.AuthURL != want {
		t.Errorf("after a browser join: %+v", s.Node)
	}
}

func TestFakeKeyJoin(t *testing.T) {
	f := NewFake()
	_, _ = runPlan(t, f, Request{Action: ActionLogout})
	if _, err := runPlan(t, f, Request{Action: ActionJoin, LoginServer: DemoLoginServer,
		AuthKey: "example-preauth-key-000000", Hostname: "joined", AcceptRoutes: true}); err != nil {
		t.Fatalf("join: %v", err)
	}
	s := load(t, f)
	if s.Node.BackendState != StateRunning || s.Node.HostName != "joined" || len(s.Peers) != 2 {
		t.Errorf("after a key join: %+v", s)
	}
	// The key file was written and removed: the cleanup ran.
	last := f.Commands()[len(f.Commands())-1]
	if last.String() != "rm -f "+AuthKeyPath {
		t.Errorf("last command = %q, want the key file removed", last)
	}
}

func TestFakeUpWhileLoggedOutIsRefused(t *testing.T) {
	f := NewFake()
	_, _ = runPlan(t, f, Request{Action: ActionLogout})
	if _, err := runPlan(t, f, Request{Action: ActionUp}); err == nil {
		t.Error("a logged-out node cannot simply come up")
	}
}
