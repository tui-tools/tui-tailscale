package tailscale

import (
	"strings"
	"testing"
)

const profileFile = `# tui-tailscale configuration
sudo = "sudo -n"

[[profile]]
name = "laptop"
login_server = "https://headscale.example.com/"
hostname = ""
accept_routes = true
advertise_routes = []
advertise_exit_node = false

[[profile]]
name = "router" # the VCN gateway
login_server = "https://headscale.example.com"
hostname = "gw-1"
accept_routes = false
advertise_routes = ["10.0.0.0/16", "192.0.2.0/24"]
advertise_exit_node = true

[[profile]]
name = "broken"
login_server = "not a url"
`

func TestParseJoinProfiles(t *testing.T) {
	list, problems := ParseJoinProfiles(profileFile)
	if len(list) != 2 {
		t.Fatalf("profiles = %+v", list)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "broken") {
		t.Errorf("problems = %q, want the broken profile named", problems)
	}
	r := list[1]
	if r.Name != "router" || r.Hostname != "gw-1" || r.AcceptRoutes || !r.AdvertiseExitNode ||
		strings.Join(r.AdvertiseRoutes, ",") != "10.0.0.0/16,192.0.2.0/24" {
		t.Errorf("router = %+v", r)
	}
	if !list[0].AcceptRoutes || len(list[0].AdvertiseRoutes) != 0 {
		t.Errorf("laptop = %+v", list[0])
	}
}

func TestUpsertJoinProfileKeepsEverythingElse(t *testing.T) {
	p := JoinProfile{Name: "router", LoginServer: "https://headscale.example.com",
		Hostname: "gw-2", AdvertiseRoutes: []string{"10.0.0.0/16"}}
	text, removed, added := UpsertJoinProfile(profileFile, p)
	if len(removed) != 7 || removed[1] != `name = "router" # the VCN gateway` {
		t.Errorf("removed = %q", removed)
	}
	if len(added) != 7 || added[2] != `login_server = "https://headscale.example.com"` {
		t.Errorf("added = %q", added)
	}
	for _, keep := range []string{`sudo = "sudo -n"`, `name = "laptop"`, `name = "broken"`,
		"# tui-tailscale configuration"} {
		if !strings.Contains(text, keep) {
			t.Errorf("the save lost %q", keep)
		}
	}
	list, _ := ParseJoinProfiles(text)
	if len(list) != 2 || !list[1].Same(p) {
		t.Errorf("after the save: %+v", list)
	}
	if strings.Count(text, "[[profile]]") != 3 {
		t.Errorf("the save duplicated or dropped a table:\n%s", text)
	}

	// A new name is appended, after one blank line.
	fresh := JoinProfile{Name: "new-one", LoginServer: "https://headscale.example.com"}
	text2, removed2, _ := UpsertJoinProfile(text, fresh)
	if len(removed2) != 0 || !strings.HasSuffix(text2, "advertise_exit_node = false\n") ||
		!strings.Contains(text2, "\n\n[[profile]]\nname = \"new-one\"") {
		t.Errorf("appended:\n%s", text2)
	}
	// An empty file becomes just the table.
	text3, _, _ := UpsertJoinProfile("", fresh)
	if !strings.HasPrefix(text3, "[[profile]]\n") {
		t.Errorf("from nothing:\n%s", text3)
	}
}

func TestJoinProfileMatches(t *testing.T) {
	p := JoinProfile{Name: "router", LoginServer: "https://headscale.example.com",
		Hostname: "gw-1", AdvertiseRoutes: []string{"192.0.2.0/24", "10.0.0.0/16"},
		AdvertiseExitNode: true}
	prefs := Prefs{ControlURL: "https://headscale.example.com/", Hostname: "gw-1",
		AdvertiseRoutes: []string{"10.0.0.0/16", "192.0.2.0/24", "0.0.0.0/0", "::/0"}}
	if !p.Matches(prefs) {
		t.Error("the same settings in another order should match")
	}
	prefs.RouteAll = true
	if p.Matches(prefs) {
		t.Error("a different accept-routes should not match")
	}
}

func TestSaveJoinProfilePlan(t *testing.T) {
	p := JoinProfile{Name: "laptop", LoginServer: "https://headscale.example.com"}
	text, removed, added := UpsertJoinProfile("", p)
	plan, err := BuildSaveJoinProfile("/etc/tui-tailscale/config.toml", text, p, removed, added)
	if err != nil {
		t.Fatal(err)
	}
	cmd := plan.Steps[0]
	if strings.Join(cmd.Argv, " ") != "install -D -m 644 /dev/stdin /etc/tui-tailscale/config.toml" ||
		cmd.Stdin != text {
		t.Errorf("command = %q", cmd.Argv)
	}
	if !strings.Contains(plan.Body, "+ name = \"laptop\"") || !strings.Contains(plan.Body, "no secret") {
		t.Errorf("body = %q", plan.Body)
	}
	if _, err := BuildSaveJoinProfile("relative.toml", text, p, nil, nil); err == nil {
		t.Error("a relative path was accepted")
	}
	if _, err := BuildSaveJoinProfile("/x", text, JoinProfile{Name: "bad name"}, nil, nil); err == nil {
		t.Error("an invalid profile was accepted")
	}
}

func TestParseLoginProfiles(t *testing.T) {
	out := "ID    Tailnet                Account\n" +
		"a1b2  headscale.example.com  user@example.com*\n" +
		"c3d4  lab example net        ops@example.net\n"
	got := ParseLoginProfiles(out)
	if len(got) != 2 || !got[0].Current || got[0].Account != "user@example.com" ||
		got[1].Tailnet != "lab example net" || got[1].Current {
		t.Errorf("profiles = %+v", got)
	}
	withNick := "ID    Nickname  Tailnet       Account\n" +
		"a1b2  home      example.com   user@example.com*\n"
	if got := ParseLoginProfiles(withNick); len(got) != 1 || got[0].Tailnet != "example.com" {
		t.Errorf("with a nickname column: %+v", got)
	}
	if got := ParseLoginProfiles("no profiles"); got != nil {
		t.Errorf("garbage: %+v", got)
	}
}

func TestSwitchProfilePlan(t *testing.T) {
	plan, err := BuildCommand(Request{Action: ActionSwitchProfile, LoginProfile: "a1b2"})
	if err != nil || strings.Join(plan.Steps[0].Argv, " ") != "tailscale switch a1b2" {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if _, err := BuildCommand(Request{Action: ActionSwitchProfile, LoginProfile: "a b"}); err == nil {
		t.Error("an id with a space was accepted")
	}
}
