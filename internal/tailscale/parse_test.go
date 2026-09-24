package tailscale

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fixture reads a file from testdata.
func fixture(t testing.TB, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // testdata is in the repository
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(raw)
}

func TestParseStatusRunning(t *testing.T) {
	st, err := ParseStatus(fixture(t, "status-running.json"))
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	n := st.Node
	if n.BackendState != StateRunning || n.Version != "1.98.4" || !n.Online {
		t.Errorf("node = %+v", n)
	}
	if n.HostName != "example-node" || n.DNSName != "example-node.tailnet.example.com" {
		t.Errorf("names = %q / %q", n.HostName, n.DNSName)
	}
	if n.User != "user@example.com" || !n.MagicDNS || n.TailnetName != "headscale.example.com" {
		t.Errorf("user/tailnet = %q %v %q", n.User, n.MagicDNS, n.TailnetName)
	}
	if !reflect.DeepEqual(n.IPs, []string{"100.64.0.1", "fd7a:115c:a1e0::1"}) {
		t.Errorf("IPs = %q", n.IPs)
	}

	if len(st.Peers) != 3 {
		t.Fatalf("got %d peers, want 3", len(st.Peers))
	}
	// Online first, then by name; the offline laptop sorts last.
	names := []string{st.Peers[0].Name(), st.Peers[1].Name(), st.Peers[2].Name()}
	if !reflect.DeepEqual(names, []string{"exit-gateway", "office-router", "laptop"}) {
		t.Errorf("order = %q", names)
	}
	exit, router, laptop := st.Peers[0], st.Peers[1], st.Peers[2]
	if !exit.ExitNodeOption || len(exit.Routes) != 0 {
		t.Errorf("exit peer = %+v (default routes are not subnet routes)", exit)
	}
	if !reflect.DeepEqual(router.Routes, []string{"192.0.2.0/24"}) || router.User != "ops@example.com" {
		t.Errorf("router peer = %+v", router)
	}
	if laptop.Online || laptop.OS != "macOS" || len(laptop.Routes) != 0 {
		t.Errorf("laptop peer = %+v", laptop)
	}
}

func TestParseStatusNeedsLogin(t *testing.T) {
	st, err := ParseStatus(fixture(t, "status-needs-login.json"))
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if st.Node.BackendState != StateNeedsLogin {
		t.Errorf("state = %q", st.Node.BackendState)
	}
	if st.Node.AuthURL == "" {
		t.Error("the pending login URL must be kept: it is how the login finishes")
	}
	if len(st.Peers) != 0 || len(st.Node.IPs) != 0 {
		t.Errorf("a logged-out node has no peers and no address: %+v", st)
	}
}

func TestParseStatusSkipsAWarning(t *testing.T) {
	st, err := ParseStatus(fixture(t, "status-with-warning.txt"))
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if st.Node.BackendState != StateStopped || st.Node.User != "user@example.com" {
		t.Errorf("node = %+v", st.Node)
	}
}

func TestParseStatusRefuses(t *testing.T) {
	for _, out := range []string{"", fixture(t, "status-not-running.txt"), "{}", "{", "[1,2]"} {
		if _, err := ParseStatus(out); err == nil {
			t.Errorf("%q parsed", out)
		}
	}
}

func TestParsePrefs(t *testing.T) {
	p, err := ParsePrefs(fixture(t, "debug-prefs.json"))
	if err != nil {
		t.Fatalf("ParsePrefs: %v", err)
	}
	if p.ControlURL != "https://headscale.example.com" || !p.RouteAll || !p.CorpDNS || !p.WantRunning {
		t.Errorf("prefs = %+v", p)
	}
	if !p.AdvertisesExitNode() || !reflect.DeepEqual(p.SubnetRoutes(), []string{"192.0.2.0/24"}) {
		t.Errorf("routes = %q", p.AdvertiseRoutes)
	}
}

func TestParseLoginURL(t *testing.T) {
	want := "https://headscale.example.com/register/0000000000000000000000000000"
	if got := ParseLoginURL(fixture(t, "up-interactive.txt")); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// A link in a warning is not a login.
	if got := ParseLoginURL("Warning: see https://tailscale.com/s/foo for details"); got != "" {
		t.Errorf("a warning's link was taken for a login: %q", got)
	}
	if got := ParseLoginURL(""); got != "" {
		t.Errorf("got %q from nothing", got)
	}
}

func TestClassifyReadError(t *testing.T) {
	tests := map[string]ReadProblem{
		fixture(t, "status-not-running.txt"):                                       ProblemNotRunning,
		"dial unix /var/run/tailscale/tailscaled.sock: connect: permission denied": ProblemPermission,
		"Access denied: status access denied":                                      ProblemPermission,
		"something else entirely":                                                  ProblemOther,
	}
	for text, want := range tests {
		if got := ClassifyReadError(text); got != want {
			t.Errorf("%q: got %v, want %v", text, got, want)
		}
	}
}

func TestParseDistro(t *testing.T) {
	d := ParseDistro("NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\n" +
		"VERSION_CODENAME=noble\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n")
	if d.ID != "ubuntu" || d.Codename != "noble" || d.PrettyName != "Ubuntu 24.04.1 LTS" {
		t.Errorf("distro = %+v", d)
	}
	if d.Manager() != "apt" {
		t.Errorf("manager = %q", d.Manager())
	}
}
