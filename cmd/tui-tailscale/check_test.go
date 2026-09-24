package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

func TestRunCheckDemo(t *testing.T) {
	var out strings.Builder
	err := runCheck(context.Background(), tailscale.NewFake(),
		compat.Result{Backend: backendName, Version: "1.98.4"}, &out)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var report checkReport
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatalf("the output is not JSON: %v\n%s", err, out.String())
	}
	n := report.Tailscale
	if report.Backend != "demo" || !n.Installed || !n.DaemonRunning || n.BackendState != "Running" {
		t.Errorf("report = %+v", report)
	}
	if !n.LoginServer.Set || !n.LoginServer.HTTPS || n.LoginServer.TailscaleControl {
		t.Errorf("login server = %+v, want a self-hosted https one", n.LoginServer)
	}
	if !n.Prefs.AcceptRoutes || n.Prefs.AdvertisesExitNode || n.Prefs.UsesExitNode {
		t.Errorf("prefs = %+v", n.Prefs)
	}
	if n.Peers.Total != 2 || n.Peers.Online != 2 || n.Peers.ExitNodeOptions != 1 || n.Peers.WithRoutes != 1 {
		t.Errorf("peers = %+v", n.Peers)
	}
	if !n.TailnetIPv4 || !n.TailnetIPv6 || !n.LoggedIn || n.LoginPending {
		t.Errorf("node = %+v", n)
	}
	if report.Install != nil {
		t.Error("an installed client needs no install block")
	}
	if len(report.Compat) != 1 || report.Compat[0].Version != "1.98.4" {
		t.Errorf("compat = %+v", report.Compat)
	}
	// The privacy promise: no address, name or URL of the node or its tailnet.
	for _, leak := range []string{"://", "example-node", "100.64.", "fd7a:", "example.com",
		"192.0.2."} {
		if strings.Contains(out.String(), leak) {
			t.Errorf("--check carries %q:\n%s", leak, out.String())
		}
	}
}

func TestRunCheckNotInstalled(t *testing.T) {
	var out strings.Builder
	err := runCheck(context.Background(), notInstalled{tailscale.NewFake()}, compat.Result{}, &out)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var report checkReport
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatalf("the output is not JSON: %v", err)
	}
	if report.Tailscale.Installed || report.Install == nil {
		t.Fatalf("report = %+v", report)
	}
	if report.Install.Distro != "ubuntu-24.04" || report.Install.Manager != "apt" {
		t.Errorf("install = %+v", report.Install)
	}
	if got := strings.Join(report.Install.Commands, "\n"); !strings.Contains(got,
		"sudo apt-get install -y tailscale") {
		t.Errorf("commands = %s", got)
	}
	if report.Compat == nil {
		t.Error("compat must be a list, empty when nothing was probed")
	}
}
