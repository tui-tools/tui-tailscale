package tailscale

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/pkgmgr"
)

// argvs renders every command of a plan as one line each, for comparison.
func argvs(p Plan) []string {
	out := make([]string, 0, len(p.Commands()))
	for _, c := range p.Commands() {
		out = append(out, c.String())
	}
	return out
}

func TestBuildCommand(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want []string
	}{
		{
			name: "browser join: just up, no key file",
			req:  Request{Action: ActionJoin, LoginServer: "https://headscale.example.com"},
			want: []string{
				"tailscale up --login-server=https://headscale.example.com --timeout=20s --reset",
			},
		},
		{
			name: "join with every option",
			req: Request{Action: ActionJoin, LoginServer: "https://headscale.example.com/",
				AuthKey: "example-preauth-key-000000", Hostname: "example-node",
				AcceptRoutes: true, Routes: []string{"192.0.2.0/24", " 198.51.100.0/24"},
				ExitNodeOffer: true},
			want: []string{
				"install -m 600 /dev/stdin /run/tui-tailscale.authkey",
				"install -m 644 /dev/stdin /etc/sysctl.d/99-tailscale.conf",
				"sysctl -w net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1",
				"tailscale up --login-server=https://headscale.example.com " +
					"--authkey=file:/run/tui-tailscale.authkey --hostname=example-node " +
					"--accept-routes --advertise-routes=192.0.2.0/24,198.51.100.0/24 " +
					"--advertise-exit-node --timeout=20s --reset",
				"rm -f /run/tui-tailscale.authkey",
			},
		},
		{
			name: "moving a logged-in node to another server re-authenticates",
			req: Request{Action: ActionJoin, LoginServer: "https://headscale.example.com",
				LoggedIn: true, CurrentControlURL: DefaultControlURL},
			want: []string{
				"tailscale up --login-server=https://headscale.example.com --force-reauth " +
					"--timeout=20s --reset",
			},
		},
		{
			name: "re-joining the same server does not",
			req: Request{Action: ActionJoin, LoginServer: "https://headscale.example.com",
				LoggedIn: true, CurrentControlURL: "https://headscale.example.com/"},
			want: []string{
				"tailscale up --login-server=https://headscale.example.com --timeout=20s --reset",
			},
		},
		{
			name: "accept routes on",
			req:  Request{Action: ActionAcceptRoutes, Enable: true},
			want: []string{"tailscale set --accept-routes=true"},
		},
		{
			name: "accept routes off",
			req:  Request{Action: ActionAcceptRoutes},
			want: []string{"tailscale set --accept-routes=false"},
		},
		{
			name: "advertise routes turns forwarding on first",
			req:  Request{Action: ActionAdvertiseRoutes, Routes: []string{"192.0.2.0/24"}},
			want: []string{
				"install -m 644 /dev/stdin /etc/sysctl.d/99-tailscale.conf",
				"sysctl -w net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1",
				"tailscale set --advertise-routes=192.0.2.0/24",
			},
		},
		{
			name: "clearing the routes needs no forwarding",
			req:  Request{Action: ActionAdvertiseRoutes},
			want: []string{"tailscale set --advertise-routes="},
		},
		{
			name: "exit node",
			req:  Request{Action: ActionExitNode, ExitNode: "100.64.0.2"},
			want: []string{"tailscale set --exit-node=100.64.0.2"},
		},
		{
			name: "no exit node",
			req:  Request{Action: ActionExitNode},
			want: []string{"tailscale set --exit-node="},
		},
		{
			name: "offer exit node",
			req:  Request{Action: ActionAdvertiseExitNode, Enable: true},
			want: []string{
				"install -m 644 /dev/stdin /etc/sysctl.d/99-tailscale.conf",
				"sysctl -w net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1",
				"tailscale set --advertise-exit-node=true",
			},
		},
		{
			name: "stop offering exit node",
			req:  Request{Action: ActionAdvertiseExitNode},
			want: []string{"tailscale set --advertise-exit-node=false"},
		},
		{
			name: "hostname",
			req:  Request{Action: ActionHostname, Hostname: "example-node"},
			want: []string{"tailscale set --hostname=example-node"},
		},
		{name: "logout", req: Request{Action: ActionLogout}, want: []string{"tailscale logout"}},
		{name: "down", req: Request{Action: ActionDown}, want: []string{"tailscale down"}},
		{name: "up", req: Request{Action: ActionUp}, want: []string{"tailscale up"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := BuildCommand(tc.req)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if got := argvs(plan); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("commands =\n  %s\nwant\n  %s", strings.Join(got, "\n  "),
					strings.Join(tc.want, "\n  "))
			}
			if plan.Title == "" || plan.Body == "" {
				t.Error("a plan needs a title and a body for the confirm dialog")
			}
			for _, c := range plan.Commands() {
				if c.Description == "" {
					t.Errorf("%q has no description", c)
				}
			}
		})
	}
}

func TestBuildCommandRefuses(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{"no action", Request{}},
		{"unknown action", Request{Action: "nope"}},
		{"not a URL", Request{Action: ActionJoin, LoginServer: "headscale.example.com"}},
		{"a flag as the server", Request{Action: ActionJoin, LoginServer: "--reset"}},
		{"user info in the URL", Request{Action: ActionJoin,
			LoginServer: "https://ana@headscale.example.com"}},
		{"mistyped address", Request{Action: ActionJoin, LoginServer: "http://203.0.113.1000"}},
		{"key with a space", Request{Action: ActionJoin,
			LoginServer: "https://headscale.example.com", AuthKey: "abc defghijk"}},
		{"key with a newline", Request{Action: ActionJoin,
			LoginServer: "https://headscale.example.com", AuthKey: "abcdefghijk\n--reset"}},
		{"bad hostname", Request{Action: ActionJoin,
			LoginServer: "https://headscale.example.com", Hostname: "-x"}},
		{"route with host bits", Request{Action: ActionAdvertiseRoutes,
			Routes: []string{"192.0.2.1/24"}}},
		{"route that is not one", Request{Action: ActionAdvertiseRoutes,
			Routes: []string{"lan"}}},
		{"default route as a subnet", Request{Action: ActionAdvertiseRoutes,
			Routes: []string{"0.0.0.0/0"}}},
		{"exit node by name", Request{Action: ActionExitNode, ExitNode: "exit-gateway"}},
		{"empty hostname", Request{Action: ActionHostname}},
		{"hostname with a dot", Request{Action: ActionHostname, Hostname: "a.b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := BuildCommand(tc.req)
			if err == nil {
				t.Fatalf("expected an error, got %q", argvs(plan))
			}
			if len(plan.Commands()) != 0 {
				t.Errorf("a refusal must carry nothing runnable, got %q", argvs(plan))
			}
		})
	}
}

// The whole point of the key file: the key is on the stdin of the command
// that writes it, and on no argv and in no preview anywhere in the plan.
func TestJoinKeepsTheKeyOffEveryArgv(t *testing.T) {
	const key = "tskey-auth-0123456789abcdef"
	plan, err := BuildCommand(Request{Action: ActionJoin,
		LoginServer: "https://headscale.example.com", AuthKey: key})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	stdin := 0
	for _, c := range plan.Commands() {
		if strings.Contains(c.String(), key) {
			t.Errorf("the key is on an argv: %q", c)
		}
		if strings.Contains(c.Description, key) {
			t.Errorf("the key is in a description: %q", c.Description)
		}
		if c.Stdin == key {
			stdin++
		}
	}
	if stdin != 1 {
		t.Errorf("the key reaches %d commands' stdin, want exactly 1", stdin)
	}
	if strings.Contains(plan.Body, key) || strings.Contains(plan.Title, key) {
		t.Error("the key is in the dialog text")
	}
	if len(plan.Cleanup) != 1 || plan.Cleanup[0].Argv[0] != "rm" {
		t.Errorf("the key file must be removed whatever happens, cleanup = %q", plan.Cleanup)
	}
}

func TestActionKeysAreUnique(t *testing.T) {
	seen := map[string]Action{}
	for _, spec := range Actions {
		if other, ok := seen[spec.Key]; ok {
			t.Errorf("key %q is bound to both %q and %q", spec.Key, other, spec.Action)
		}
		seen[spec.Key] = spec.Action
		if spec.Label == "" || spec.Help == "" {
			t.Errorf("%q needs a label and a help line", spec.Action)
		}
	}
}

func TestPrefsExitNodeRoutes(t *testing.T) {
	p := Prefs{AdvertiseRoutes: []string{"192.0.2.0/24", "0.0.0.0/0", "::/0"}}
	if !p.AdvertisesExitNode() {
		t.Error("both default routes mean an exit node")
	}
	if got := p.SubnetRoutes(); !reflect.DeepEqual(got, []string{"192.0.2.0/24"}) {
		t.Errorf("SubnetRoutes = %q", got)
	}
	if (Prefs{AdvertiseRoutes: []string{"0.0.0.0/0"}}).AdvertisesExitNode() {
		t.Error("one default route alone is not an exit node")
	}
}

func TestNormalizeRoutes(t *testing.T) {
	got, err := NormalizeRoutes(SplitList("192.0.2.0/24, 2001:db8::/48 192.0.2.0/24"))
	if err != nil {
		t.Fatalf("NormalizeRoutes: %v", err)
	}
	if want := []string{"192.0.2.0/24", "2001:db8::/48"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q (deduplicated, in order)", got, want)
	}
}

func TestServerURLProblem(t *testing.T) {
	for _, ok := range []string{
		"https://headscale.example.com", "http://100.64.0.1:8080",
		"https://[2001:db8::1]:443", "https://headscale.example.com/",
	} {
		if p := ServerURLProblem(ok); p != "" {
			t.Errorf("%q refused: %s", ok, p)
		}
	}
	for _, bad := range []string{
		"", "ftp://example.com", "https://", "https://exa mple.com", "https://example.com:0",
		"https://example.com:99999", "http://203.0.113.1000", "https://-bad.example.com",
	} {
		if ServerURLProblem(bad) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestInstallPlans(t *testing.T) {
	ubuntu := ParseDistro("ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\n" +
		"VERSION_CODENAME=noble\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n")
	fedora := ParseDistro("ID=fedora\nVERSION_ID=42\nPRETTY_NAME=\"Fedora Linux 42\"\n")
	arch := ParseDistro("ID=arch\nPRETTY_NAME=\"Arch Linux\"\n")
	omarchy := ParseDistro("ID=omarchy-server\nID_LIKE=\"omarchy arch\"\n")
	debian := ParseDistro("ID=debian\nVERSION_ID=12\nVERSION_CODENAME=bookworm\n")
	mint := ParseDistro("ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_CODENAME=wilma\n" +
		"UBUNTU_CODENAME=noble\n")
	rocky := ParseDistro("ID=rocky\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=9.4\n")

	tests := []struct {
		name   string
		distro Distro
		want   []string
	}{
		{"ubuntu", ubuntu, []string{
			"curl -fsSL -o /usr/share/keyrings/tailscale-archive-keyring.gpg " +
				"https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg",
			"curl -fsSL -o /etc/apt/sources.list.d/tailscale.list " +
				"https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list",
			"apt-get update",
			"apt-get install -y tailscale",
			"systemctl enable --now tailscaled",
		}},
		{"debian", debian, []string{
			"curl -fsSL -o /usr/share/keyrings/tailscale-archive-keyring.gpg " +
				"https://pkgs.tailscale.com/stable/debian/bookworm.noarmor.gpg",
			"curl -fsSL -o /etc/apt/sources.list.d/tailscale.list " +
				"https://pkgs.tailscale.com/stable/debian/bookworm.tailscale-keyring.list",
			"apt-get update",
			"apt-get install -y tailscale",
			"systemctl enable --now tailscaled",
		}},
		{"mint uses ubuntu's codename", mint, []string{
			"curl -fsSL -o /usr/share/keyrings/tailscale-archive-keyring.gpg " +
				"https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg",
			"curl -fsSL -o /etc/apt/sources.list.d/tailscale.list " +
				"https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list",
			"apt-get update",
			"apt-get install -y tailscale",
			"systemctl enable --now tailscaled",
		}},
		{"fedora", fedora, []string{
			"curl -fsSL -o /etc/yum.repos.d/tailscale.repo " +
				"https://pkgs.tailscale.com/stable/fedora/tailscale.repo",
			"dnf install -y tailscale",
			"systemctl enable --now tailscaled",
		}},
		{"rocky", rocky, []string{
			"curl -fsSL -o /etc/yum.repos.d/tailscale.repo " +
				"https://pkgs.tailscale.com/stable/rhel/9/tailscale.repo",
			"dnf install -y tailscale",
			"systemctl enable --now tailscaled",
		}},
		{"arch", arch, []string{
			"pacman -Syu --needed --noconfirm tailscale",
			"systemctl enable --now tailscaled",
		}},
		// Omarchy's pacman hook refuses a direct -Syu outside `omarchy update`.
		{"omarchy", omarchy, []string{
			"pacman -S --needed --noconfirm tailscale",
			"systemctl enable --now tailscaled",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := BuildCommand(Request{Action: ActionInstall, Distro: tc.distro})
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if got := argvs(plan); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("commands =\n  %s\nwant\n  %s", strings.Join(got, "\n  "),
					strings.Join(tc.want, "\n  "))
			}
			lines := InstallInstructions(tc.distro)
			if len(lines) != len(tc.want) || lines[0] != "sudo "+tc.want[0] {
				t.Errorf("the instructions must be the plan's commands, got %q", lines)
			}
		})
	}

	for _, d := range []Distro{{}, {Distro: pkgmgr.Distro{ID: "ubuntu"}}, ParseDistro("ID=nixos\n")} {
		if _, err := BuildCommand(Request{Action: ActionInstall, Distro: d}); err == nil {
			t.Errorf("%+v: expected no plan", d)
		}
	}
}

func TestStateHelpers(t *testing.T) {
	s := demoState()
	if !s.LoggedIn() {
		t.Error("the demo node is logged in")
	}
	options := s.ExitNodeOptions()
	if len(options) != 1 || options[0].Name() != "exit-gateway" {
		t.Errorf("exit node options = %+v", options)
	}
	s.Prefs.ExitNodeIP = "100.64.0.2"
	if p, ok := s.CurrentExitNode(); !ok || p.Name() != "exit-gateway" {
		t.Errorf("current exit node = %+v, %v", p, ok)
	}
	if got := (Peer{IPs: []string{"fd7a:115c:a1e0::2", "100.64.0.2"}}).IPv4(); got != "100.64.0.2" {
		t.Errorf("IPv4 = %q", got)
	}
	if got := (Peer{HostName: "h", DNSName: "name.tailnet.example.com"}).Name(); got != "name" {
		t.Errorf("Name = %q", got)
	}
}

// Issue #18: tailscaled in one word, for --check and for u.
func TestDaemonWord(t *testing.T) {
	cases := []struct {
		state State
		want  string
		start bool
	}{
		{State{}, "", false},
		{State{Installed: true, DaemonRunning: true}, DaemonRunning, false},
		{State{Installed: true, PermissionDenied: true}, DaemonRunning, false},
		{State{Installed: true, NotRunning: true, DaemonEnabled: "enabled"}, DaemonStopped, true},
		{State{Installed: true, NotRunning: true}, DaemonStopped, true},
		{State{Installed: true, NotRunning: true, DaemonEnabled: "disabled"}, DaemonDisabled, true},
		{State{Installed: true, NotRunning: true, DaemonEnabled: "masked"}, DaemonMasked, true},
		{State{Installed: true, Error: "boom"}, DaemonUnknown, false},
	}
	for _, c := range cases {
		if got := c.state.Daemon(); got != c.want || c.state.DaemonStartable() != c.start {
			t.Errorf("%+v: Daemon() = %q startable %v, want %q %v", c.state, got,
				c.state.DaemonStartable(), c.want, c.start)
		}
	}
}

// The start u previews: enable --now, after an unmask when the unit is masked.
func TestBuildStartDaemon(t *testing.T) {
	plan, err := BuildCommand(Request{Action: ActionStartDaemon, DaemonEnabled: "disabled"})
	if err != nil || len(plan.Steps) != 1 ||
		plan.Steps[0].String() != "systemctl enable --now tailscaled" {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	plan, _ = BuildCommand(Request{Action: ActionStartDaemon, DaemonEnabled: "masked"})
	if len(plan.Steps) != 2 || plan.Steps[0].String() != "systemctl unmask tailscaled" {
		t.Errorf("masked plan = %+v", plan.Steps)
	}
}

func TestParseIsEnabled(t *testing.T) {
	for out, want := range map[string]string{
		"enabled\n": "enabled",
		"disabled":  "disabled",
		"masked\n":  "masked",
		"":          "",
		"Failed to get unit file state for tailscaled.service: No such file or directory": "",
	} {
		if got := ParseIsEnabled(out); got != want {
			t.Errorf("ParseIsEnabled(%q) = %q, want %q", out, got, want)
		}
	}
}
