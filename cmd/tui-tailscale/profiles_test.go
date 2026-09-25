package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// newProfilesApp is the demo app with the demo's two join profiles.
func newProfilesApp(t *testing.T) (*app, *tailscale.Fake) {
	t.Helper()
	a, fake := newTestApp(t)
	a.profiles = demoProfileStore()
	return a, fake
}

// j with profiles opens with a picker; picking one pre-fills every step, so
// the join is enter all the way down.
func TestJoinFromAProfile(t *testing.T) {
	a, fake := newProfilesApp(t)
	press(t, a, "j")
	if a.mode != modePicker || a.pickerPurpose != pickerJoinProfile {
		t.Fatalf("j opened %v / %v, want the join profile picker", a.mode, a.pickerPurpose)
	}
	if a.picker.Options[0] != newJoinProfile || len(a.picker.Options) != 3 {
		t.Errorf("options = %q", a.picker.Options)
	}
	// The demo node matches the laptop profile, so that is the one offered.
	if !strings.HasPrefix(a.picker.Selected(), "laptop") {
		t.Errorf("offered %q, want the matching profile", a.picker.Selected())
	}
	press(t, a, "down") // subnet-router
	press(t, a, "enter")
	if a.inputPurpose != inputJoinServer || a.input.Value() != "https://headscale.example.com" ||
		!strings.Contains(a.input.Help, "subnet-router") {
		t.Fatalf("server step: %v %q %q", a.inputPurpose, a.input.Value(), a.input.Help)
	}
	press(t, a, "enter")
	typeText(a, "example-preauth-key-000000")
	press(t, a, "enter")
	if a.input.Value() != "office-router" {
		t.Errorf("hostname prefill = %q", a.input.Value())
	}
	for i := 0; i < 4; i++ {
		press(t, a, "enter")
	}
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v", a.mode)
	}
	for _, want := range []string{"--hostname=office-router", "--advertise-routes=192.0.2.0/24",
		"--advertise-exit-node"} {
		if !strings.Contains(a.confirm.Command, want) {
			t.Errorf("preview is missing %q:\n%s", want, a.confirm.Command)
		}
	}
	if strings.Contains(a.confirm.Command, "--accept-routes") {
		t.Error("the profile does not accept routes")
	}
	before := len(fake.Commands())
	press(t, a, "y")
	if len(fake.Commands()) == before {
		t.Fatal("the join did not run")
	}
	// The answers are the profile's, so there is nothing new to save.
	if a.mode == modeInput {
		t.Errorf("a join answered exactly as its profile offered to save it again")
	}
}

// A join with new answers offers to save them; the save is a previewed write
// of the config file with the diff, and the next j has the new profile.
func TestSaveAJoinProfileAfterAJoin(t *testing.T) {
	a, fake := newProfilesApp(t)
	press(t, a, "j")
	press(t, a, "enter") // the laptop profile
	press(t, a, "enter") // server
	typeText(a, "example-preauth-key-000000")
	press(t, a, "enter")
	typeText(a, "desk-1")
	press(t, a, "enter")
	for i := 0; i < 3; i++ {
		press(t, a, "enter")
	}
	press(t, a, "y")
	if a.mode != modeInput || a.inputPurpose != inputSaveProfile {
		t.Fatalf("mode = %v / %v, want the save offer", a.mode, a.inputPurpose)
	}
	if a.input.Value() != "laptop" {
		t.Errorf("name offered = %q, want the profile it started from", a.input.Value())
	}
	if strings.Contains(a.View(), "example-preauth-key") {
		t.Fatal("the key is on screen")
	}
	typeText(a, "desk")
	press(t, a, "enter")
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the previewed write", a.mode)
	}
	if a.confirm.Command != "sudo -n install -D -m 644 /dev/stdin /etc/tui-tailscale/config.toml" {
		t.Errorf("preview = %q", a.confirm.Command)
	}
	if !strings.Contains(a.confirm.Body, `+ hostname = "desk-1"`) {
		t.Errorf("body = %q", a.confirm.Body)
	}
	press(t, a, "y")
	ran := fake.Commands()
	last := ran[len(ran)-1]
	if last.Argv[0] != "install" || strings.Contains(last.Stdin, "preauth") {
		t.Fatalf("last command = %q (stdin must hold no key)", last.Argv)
	}
	if _, ok := a.profiles.byName("desk"); !ok || len(a.profiles.list) != 3 {
		t.Errorf("profiles after the save: %+v", a.profiles.names())
	}
	if !strings.Contains(a.status, "join profile saved") {
		t.Errorf("status = %q", a.status)
	}
}

// The logout dialog says which join profile restores the node.
func TestLogoutRemindsOfTheProfile(t *testing.T) {
	a, _ := newProfilesApp(t)
	press(t, a, "L")
	if !strings.Contains(a.confirm.Body, "join profile laptop matches") {
		t.Errorf("logout body = %q", a.confirm.Body)
	}
}

// The node screen names the matching join profile and the login profiles,
// with the words kept apart.
func TestNodeScreenShowsBothKindsOfProfile(t *testing.T) {
	a, _ := newProfilesApp(t)
	view := a.View()
	for _, want := range []string{"join profile", "laptop (matches these settings)",
		"login profile", "user@example.com on headscale.example.com · 2 login profiles",
		"p switches"} {
		if !strings.Contains(view, want) {
			t.Errorf("the node screen is missing %q", want)
		}
	}
}

// p switches between tailscale's own login profiles.
func TestSwitchLoginProfile(t *testing.T) {
	a, fake := newTestApp(t)
	press(t, a, "p")
	if a.mode != modePicker || len(a.picker.Options) != 2 {
		t.Fatalf("p: mode %v, options %q", a.mode, a.picker.Options)
	}
	press(t, a, "down")
	press(t, a, "enter")
	if a.confirm.Command != "sudo -n tailscale switch c3d4" {
		t.Fatalf("preview = %q", a.confirm.Command)
	}
	press(t, a, "y")
	if cur, _ := a.state.CurrentLoginProfile(); cur.ID != "c3d4" || len(fake.Commands()) != 1 {
		t.Errorf("current = %+v", cur)
	}
}

// Profiles are read from both files, the user's overriding the machine's.
func TestLoadProfileStore(t *testing.T) {
	dir := t.TempDir()
	system := filepath.Join(dir, "etc.toml")
	user := filepath.Join(dir, "user.toml")
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(system, demoProfileText)
	write(user, "[[profile]]\nname = \"laptop\"\nlogin_server = \"https://other.example.com\"\n")
	store := loadProfileStore(system, user, false)
	if store.path != user || len(store.list) != 2 {
		t.Fatalf("store = %+v", store)
	}
	if p, _ := store.byName("laptop"); p.LoginServer != "https://other.example.com" {
		t.Errorf("laptop = %+v, want the user's", p)
	}
	if root := loadProfileStore(system, user, true); root.path != system ||
		!strings.Contains(root.text, "subnet-router") {
		t.Errorf("root saves machine-wide: %+v", root.path)
	}
}

// --check lists the profile names, and nothing else about them.
func TestCheckListsProfileNames(t *testing.T) {
	var out bytes.Buffer
	err := runCheckWith(context.Background(), tailscale.NewFake(), headscale.NewFake(), nil,
		&out, checkOptions{joinProfiles: demoProfileStore().names()})
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		JoinProfiles []string `json:"joinProfiles"`
		Tailscale    struct {
			LoginProfiles int `json:"loginProfiles"`
		} `json:"tailscale"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if strings.Join(report.JoinProfiles, ",") != "laptop,subnet-router" ||
		report.Tailscale.LoginProfiles != 2 {
		t.Errorf("report = %+v", report)
	}
	if strings.Contains(out.String(), "office-router") {
		t.Error("--check printed a profile's contents")
	}
}
