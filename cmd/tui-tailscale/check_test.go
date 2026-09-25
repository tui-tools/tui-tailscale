package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

func TestRunCheckDemo(t *testing.T) {
	var out strings.Builder
	err := runCheck(context.Background(), tailscale.NewFake(), headscale.NewFake(),
		[]compat.Result{{Backend: backendName, Version: "1.98.4"}}, &out)
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
	// The control plane's block prints two names on purpose — the issuer's
	// host and dns.base_domain, see cp_check.go — so the node's half is
	// checked on its own, and the whole report for URLs and addresses.
	node, err := json.Marshal(report.Tailscale)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"://", "example-node", "100.64.", "fd7a:", "example.com",
		"192.0.2."} {
		if strings.Contains(string(node), leak) {
			t.Errorf("--check's node block carries %q:\n%s", leak, node)
		}
	}
	for _, leak := range []string{"://", "example-node", "office-router", "100.64.", "fd7a:",
		"192.0.2.", "198.51.100.", "user@example.com"} {
		if strings.Contains(out.String(), leak) {
			t.Errorf("--check carries %q:\n%s", leak, out.String())
		}
	}
}

func TestRunCheckNotInstalled(t *testing.T) {
	var out strings.Builder
	err := runCheck(context.Background(), notInstalled{tailscale.NewFake()}, headscale.NewFake(),
		nil, &out)
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
		"sudo env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y tailscale") {
		t.Errorf("commands = %s", got)
	}
	if report.Compat == nil {
		t.Error("compat must be a list, empty when nothing was probed")
	}
}

// A pending login's URL is the one URL --check prints, at the top level, so
// `--check | jq -r .loginUrl` is a copyable fallback.
func TestRunCheckPendingLogin(t *testing.T) {
	fake := tailscale.NewFake()
	ctx := context.Background()
	for _, req := range []tailscale.Request{{Action: tailscale.ActionLogout},
		{Action: tailscale.ActionJoin, LoginServer: tailscale.DemoLoginServer}} {
		plan, err := tailscale.BuildCommand(req)
		if err != nil {
			t.Fatalf("BuildCommand: %v", err)
		}
		for _, cmd := range plan.Steps {
			_, _ = fake.Run(ctx, cmd)
		}
	}
	var out strings.Builder
	if err := runCheck(ctx, fake, headscale.NewFake(), nil, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatalf("the output is not JSON: %v", err)
	}
	want := tailscale.DemoLoginServer + tailscale.DemoRegisterPath
	if report["loginUrl"] != want {
		t.Errorf("loginUrl = %v, want %q", report["loginUrl"], want)
	}
	if !strings.Contains(out.String(), `"loginPending": true`) {
		t.Error("loginPending should be true")
	}
}
