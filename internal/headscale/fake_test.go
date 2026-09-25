package headscale

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/runner"
)

// --- the commands -------------------------------------------------------------

func TestBuildCreatePreAuthKey(t *testing.T) {
	cmd, err := BuildCreatePreAuthKey("2", true, true, "7d")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []string{"headscale", "preauthkeys", "create", "--user", "2",
		"--reusable", "--ephemeral", "--expiration", "7d"}
	if !reflect.DeepEqual(cmd.Argv, want) {
		t.Errorf("argv = %q, want %q", cmd.Argv, want)
	}

	plain, err := BuildCreatePreAuthKey("2", false, false, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want = []string{"headscale", "preauthkeys", "create", "--user", "2", "--expiration", "24h"}
	if !reflect.DeepEqual(plain.Argv, want) {
		t.Errorf("default argv = %q, want the 24h default", plain.Argv)
	}
}

func TestBuildCreatePreAuthKeyRejectsBadInput(t *testing.T) {
	if _, err := BuildCreatePreAuthKey("ana", false, false, "24h"); err == nil {
		t.Error("accepted a non-numeric user id")
	}
	if _, err := BuildCreatePreAuthKey("2; reboot", false, false, "24h"); err == nil {
		t.Error("accepted an injected user id")
	}
	for _, bad := range []string{"24", "h", "24h; rm -rf /", "-24h", "24 h", "1y1d"} {
		if _, err := BuildCreatePreAuthKey("2", false, false, bad); err == nil {
			t.Errorf("accepted a bad expiration: %q", bad)
		}
	}
}

func TestBuildDeleteNode(t *testing.T) {
	cmd, err := BuildDeleteNode("3")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []string{"headscale", "nodes", "delete", "--identifier", "3", "--force"}
	if !reflect.DeepEqual(cmd.Argv, want) {
		t.Errorf("argv = %q, want %q", cmd.Argv, want)
	}
	if !cmd.Destructive {
		t.Error("deleting a node must be marked destructive")
	}
	for _, bad := range []string{"", "abc", "3; reboot", "-3"} {
		if _, err := BuildDeleteNode(bad); err == nil {
			t.Errorf("accepted a bad node id: %q", bad)
		}
	}
}

func TestBuildRenameNode(t *testing.T) {
	cmd, err := BuildRenameNode("2", "build-box")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []string{"headscale", "nodes", "rename", "--identifier", "2", "build-box"}
	if !reflect.DeepEqual(cmd.Argv, want) {
		t.Errorf("argv = %q, want %q", cmd.Argv, want)
	}
	for _, bad := range []string{"", "-box", "a b", "a;b", "a_b", "box-", strings.Repeat("a", 64)} {
		if _, err := BuildRenameNode("2", bad); err == nil {
			t.Errorf("accepted a bad node name: %q", bad)
		}
	}
}

func TestBuildCreateUserAndExpire(t *testing.T) {
	cmd, err := BuildCreateUser("dana")
	if err != nil || !reflect.DeepEqual(cmd.Argv, []string{"headscale", "users", "create", "dana"}) {
		t.Errorf("create user = %q, %v", cmd.Argv, err)
	}
	for _, bad := range []string{"", "-x", "a b", "a/b"} {
		if _, err := BuildCreateUser(bad); err == nil {
			t.Errorf("accepted a bad user name: %q", bad)
		}
	}
	expire, err := BuildExpireNode("1")
	if err != nil || !expire.Destructive ||
		!reflect.DeepEqual(expire.Argv, []string{"headscale", "nodes", "expire", "--identifier", "1"}) {
		t.Errorf("expire = %+v, %v", expire, err)
	}
}

func TestManageValidators(t *testing.T) {
	if !ValidExpiration("24h") || !ValidExpiration("30m") || !ValidExpiration("7d") {
		t.Error("a plain duration should validate")
	}
	if ValidExpiration("") || ValidExpiration("24h1m") || ValidExpiration("h") {
		t.Error("a non-duration should not validate")
	}
	if !ValidNodeName("box") || !ValidNodeName("build-box-2") {
		t.Error("a DNS-label name should validate")
	}
	if ValidNodeName("") || ValidNodeName("-box") || ValidNodeName("a b") {
		t.Error("a non-label should not validate")
	}
}

// --- the fake -----------------------------------------------------------------

// TestFakePreviewMatchesWhatRuns: the fake previews through the same prefix
// the real backend would, and records what it ran.
func TestFakePreviewMatchesWhatRuns(t *testing.T) {
	f := NewFake()
	cmd, _ := BuildExpireNode("1")
	preview := f.Preview(cmd)
	if preview != "sudo -n headscale nodes expire --identifier 1" {
		t.Fatalf("preview = %q", preview)
	}
	if _, err := f.Run(t.Context(), cmd); err != nil {
		t.Fatal(err)
	}
	if ran := f.Commands(); len(ran) != 1 || f.Preview(ran[0]) != preview {
		t.Errorf("ran %q, the preview promised %q", ran, preview)
	}
}

// TestDiscoveryIsNeverEscalated: the IdP's public document is read as the
// invoking user, in the preview as on the machine.
func TestDiscoveryIsNeverEscalated(t *testing.T) {
	cmd, err := BuildDiscoverIssuer("https://idp.example.com/realms/demo")
	if err != nil {
		t.Fatal(err)
	}
	if escalates(cmd) {
		t.Error("the discovery read escalates")
	}
	if got := NewFake().Preview(cmd); strings.HasPrefix(got, "sudo") {
		t.Errorf("preview = %q, want no prefix", got)
	}
	if !escalates(runner.Command{Argv: []string{"curl", "-fsSL", "-o", "/etc/x", "https://x"}}) {
		t.Error("a curl that writes under /etc must escalate")
	}
}

func TestFakeAppliesExpireNode(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	cmd, _ := BuildExpireNode("1")
	if _, err := f.Run(ctx, cmd); err != nil {
		t.Fatalf("run: %v", err)
	}
	state, _ := f.Load(ctx)
	for _, n := range state.Nodes {
		if n.ID == "1" && (n.Expiry.IsZero() || n.Online) {
			t.Errorf("node 1 should be expired and offline: %+v", n)
		}
	}
}

func TestFakeAppliesCreateUser(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	cmd, _ := BuildCreateUser("dana")
	if _, err := f.Run(ctx, cmd); err != nil {
		t.Fatalf("run: %v", err)
	}
	state, _ := f.Load(ctx)
	found := false
	for _, u := range state.Users {
		if u.Name == "dana" {
			found = true
		}
	}
	if !found {
		t.Error("the new user is missing after create")
	}
}

// TestFakeCreatePreAuthKeyShowsKeyOnce: the run answers the full key exactly
// once, and the state keeps only a prefix — never the full key.
func TestFakeCreatePreAuthKeyShowsKeyOnce(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	cmd, err := BuildCreatePreAuthKey("1", true, false, "24h")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	key, err := f.Run(ctx, cmd)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(key) < 20 {
		t.Fatalf("create answered %q, want a full key", key)
	}
	state, _ := f.Load(ctx)
	keys := state.PreAuthKeys
	last := keys[len(keys)-1]
	if !last.Reusable || last.Ephemeral {
		t.Errorf("flags not applied: %+v", last)
	}
	if last.KeyPrefix == key {
		t.Error("the state stores the full key; it must keep only the prefix")
	}
	if !strings.HasPrefix(key, last.KeyPrefix) {
		t.Errorf("prefix %q does not match the key", last.KeyPrefix)
	}
}

func TestFakeAppliesDeleteNode(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	cmd, _ := BuildDeleteNode("3")
	if _, err := f.Run(ctx, cmd); err != nil {
		t.Fatalf("run: %v", err)
	}
	state, _ := f.Load(ctx)
	for _, n := range state.Nodes {
		if n.ID == "3" {
			t.Error("node 3 is still present after delete")
		}
	}
}

func TestFakeAppliesRenameNode(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	cmd, _ := BuildRenameNode("2", "build-box")
	if _, err := f.Run(ctx, cmd); err != nil {
		t.Fatalf("run: %v", err)
	}
	state, _ := f.Load(ctx)
	found := false
	for _, n := range state.Nodes {
		if n.ID == "2" && n.GivenName == "build-box" {
			found = true
		}
	}
	if !found {
		t.Error("node 2 was not renamed")
	}
}

// TestDemoIsOneTailnet: the demo control plane serves the tailnet the demo
// node of internal/tailscale belongs to — its login server, the demo node
// under a demo user, and one subnet still waiting for `r`.
func TestDemoIsOneTailnet(t *testing.T) {
	state, _ := NewFake().Load(t.Context())
	if state.ControlPlane.ServerURL != DemoServerURL {
		t.Errorf("server_url = %q, want %q", state.ControlPlane.ServerURL, DemoServerURL)
	}
	names := []string{}
	for _, n := range state.Nodes {
		names = append(names, n.GivenName)
	}
	if strings.Join(names, " ") != "example-node exit-gateway office-router" {
		t.Errorf("nodes = %q", names)
	}
	if state.Nodes[0].User != "user@example.com" {
		t.Errorf("the demo node belongs to %q", state.Nodes[0].User)
	}
	if got := pendingRoutes(state.Nodes[2]); got != 1 {
		t.Errorf("office-router has %d pending routes, want 1", got)
	}
}

// --- readiness ----------------------------------------------------------------

// TestReadinessWalksTheSteps: each step is only named once the one before it
// is done.
func TestReadinessWalksTheSteps(t *testing.T) {
	now := time.Now()
	f := NewFake()
	state, _ := f.Load(t.Context())

	// The demo unit runs but is disabled: that is the first thing missing.
	if r := ReadinessFor(state, now); r.Next != NextUnit || !r.ServerConfigured ||
		!r.UnitRunning || r.UnitEnabled {
		t.Errorf("demo readiness = %+v", r)
	}

	f.SetService("active", "enabled")
	state, _ = f.Load(t.Context())
	r := ReadinessFor(state, now)
	if r.Next != NextRoutes || r.RoutesPending != 1 || r.RoutesApproved {
		t.Errorf("enabled demo = %+v", r)
	}
	if !strings.Contains(r.NextStep, "routes pending approval") {
		t.Errorf("next step = %q", r.NextStep)
	}

	approve, _ := BuildApproveRoutes("3", []string{"192.0.2.0/24", "198.51.100.0/24"}, false)
	if _, err := f.Run(t.Context(), approve); err != nil {
		t.Fatal(err)
	}
	state, _ = f.Load(t.Context())
	if r := ReadinessFor(state, now); r.Next != NextReady || !r.RoutesApproved {
		t.Errorf("after approving = %+v", r)
	}

	state.Nodes = nil
	if r := ReadinessFor(state, now); r.Next != NextFirstNode {
		t.Errorf("no node = %+v", r)
	}
	state.ControlPlane.OIDC = OIDCConfig{}
	state.PreAuthKeys = []PreAuthKey{{Used: true, Expiration: now.Add(time.Hour)}}
	if r := ReadinessFor(state, now); r.Next != NextIdentity || r.PreAuthKey {
		t.Errorf("no identity = %+v", r)
	}
	state.PreAuthKeys = []PreAuthKey{{Reusable: true, Used: true, Expiration: now.Add(time.Hour)}}
	if r := ReadinessFor(state, now); r.Next != NextFirstNode || !r.PreAuthKey {
		t.Errorf("a reusable key is a way in: %+v", r)
	}

	f.SetConfig("server_url: http://127.0.0.1:8080\nlisten_addr: 127.0.0.1:8080\n")
	state, _ = f.Load(t.Context())
	if r := ReadinessFor(state, now); r.Next != NextServer {
		t.Errorf("stock config = %+v", r)
	}
	if r := ReadinessFor(State{}, now); r.Next != NextInstall || r.UnitRunning || r.UnitEnabled {
		t.Errorf("no headscale = %+v", r)
	}
}

// --- the companion install ----------------------------------------------------

func ubuntu() pkgmgr.Distro {
	return pkgmgr.Distro{ID: "ubuntu", VersionID: "24.04", Like: []string{"debian"}}
}

// TestInstallAddsThePinnedRepositoryFirst: without the repository, the plan is
// the kit's own setup — the key read back and checked — then the package.
func TestInstallAddsThePinnedRepositoryFirst(t *testing.T) {
	plan, err := BuildInstall(ubuntu(), RepoState{})
	if err != nil {
		t.Fatal(err)
	}
	last := plan.Steps[len(plan.Steps)-1]
	if last.String() != "apt-get install -y headscale" {
		t.Errorf("last step = %q", last)
	}
	if plan.Fingerprint != RepoFingerprint || plan.Steps[plan.Verify].Argv[0] != "gpg" {
		t.Errorf("verify = %d (%q), fingerprint %q", plan.Verify, plan.Steps[plan.Verify],
			plan.Fingerprint)
	}
	if err := plan.CheckStep(plan.Verify, "fpr:::::::::0000000000000000000000000000000000000000:\n"); err == nil {
		t.Error("a different key passed the check")
	}
	fake := NewFake()
	out, _ := fake.Run(t.Context(), plan.Steps[plan.Verify])
	if err := plan.CheckStep(plan.Verify, out); err != nil {
		t.Errorf("the pinned key failed the check: %v", err)
	}
	lines := InstallInstructions(ubuntu(), RepoState{})
	if !strings.HasPrefix(lines[1], "sudo curl") || strings.HasPrefix(lines[2], "sudo") {
		t.Errorf("instructions = %q", lines)
	}
}

func TestInstallWithTheRepositoryConfigured(t *testing.T) {
	cases := map[string]struct {
		distro pkgmgr.Distro
		want   []string
	}{
		"apt": {ubuntu(), []string{"apt-get update", "apt-get install -y headscale"}},
		"dnf": {pkgmgr.Distro{ID: "fedora", VersionID: "42"},
			[]string{"dnf install -y headscale"}},
		"omarchy": {pkgmgr.Distro{ID: "omarchy-server", Like: []string{"omarchy", "arch"}},
			[]string{"pacman -S --needed --noconfirm tui-tools/headscale"}},
		"arch": {pkgmgr.Distro{ID: "arch"},
			[]string{"pacman -Syu --needed --noconfirm tui-tools/headscale"}},
	}
	for name, tc := range cases {
		plan, err := BuildInstall(tc.distro, RepoState{Configured: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got []string
		for _, s := range plan.Steps {
			got = append(got, s.String())
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: steps = %q, want %q", name, got, tc.want)
		}
	}
	if _, err := BuildInstall(pkgmgr.Distro{ID: "gentoo"}, RepoState{}); err == nil ||
		!strings.Contains(err.Error(), ManualInstallURL) {
		t.Errorf("an unknown distribution: %v", err)
	}
}

// Omarchy's pacman hook refuses a direct -Syu: a fresh install there adds the
// repository (whose setup refreshes the databases) and installs, and never
// upgrades the machine.
func TestInstallOnOmarchyNeverUpgrades(t *testing.T) {
	plan, err := BuildInstall(pkgmgr.Distro{ID: "omarchy-server", Like: []string{"omarchy", "arch"}},
		RepoState{})
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, s := range plan.Steps {
		steps = append(steps, s.String())
		if strings.Contains(s.String(), "-Syu") {
			t.Errorf("an Omarchy install upgrades the machine: %q", s.String())
		}
	}
	n := len(steps)
	if n < 2 || steps[n-2] != "pacman -Sy" ||
		steps[n-1] != "pacman -S --needed --noconfirm tui-tools/headscale" {
		t.Errorf("steps = %q", steps)
	}
	if !strings.Contains(plan.Body, "omarchy update") {
		t.Errorf("the body does not say how Omarchy upgrades: %q", plan.Body)
	}
}
