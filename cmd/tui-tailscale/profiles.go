package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// This file is the app side of join profiles (issue #4): where they are read
// from and saved to, the picker `j` opens with when there are any, and the
// offer to save the answers after a join. A join profile is the tool's preset
// for j; tailscale's own login profiles are something else, switched with p.

// profileStore is the join profiles the tool knows and the file new ones are
// saved to.
type profileStore struct {
	// path is the config.toml a save writes: /etc/tui-tailscale/config.toml
	// for root, the per-user file under ~/.config otherwise.
	path string
	// text is that file as it is now, so a save rewrites it with every other
	// line kept.
	text string
	// list is every valid profile, the per-user file's overriding the
	// machine-wide one's of the same name.
	list []tailscale.JoinProfile
	// problems are the profiles that were dropped, and why.
	problems []string
}

// names lists the profiles by name, for --check.
func (s profileStore) names() []string {
	names := make([]string, 0, len(s.list))
	for _, p := range s.list {
		names = append(names, p.Name)
	}
	return names
}

// byName finds a profile.
func (s profileStore) byName(name string) (tailscale.JoinProfile, bool) {
	for _, p := range s.list {
		if p.Name == name {
			return p, true
		}
	}
	return tailscale.JoinProfile{}, false
}

// matching is the profile whose answers are the node's current settings.
func (s profileStore) matching(prefs tailscale.Prefs) (tailscale.JoinProfile, bool) {
	for _, p := range s.list {
		if p.Matches(prefs) {
			return p, true
		}
	}
	return tailscale.JoinProfile{}, false
}

// saved records a profile written to the store's file, so the next picker has
// it without re-reading the disk.
func (s *profileStore) saved(text string) {
	s.text = text
	list, _ := tailscale.ParseJoinProfiles(text)
	merged := append([]tailscale.JoinProfile(nil), list...)
	// Profiles from the other file stay, unless this one now names them.
	for _, p := range s.list {
		if !containsProfile(merged, p.Name) {
			merged = append(merged, p)
		}
	}
	s.list = merged
}

// containsProfile reports whether a list has a profile of that name.
func containsProfile(list []tailscale.JoinProfile, name string) bool {
	for _, p := range list {
		if p.Name == name {
			return true
		}
	}
	return false
}

// expandHome turns a leading "~" into the home directory.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

// loadProfileStore reads the join profiles from the two config files the kit
// reads its keys from: the machine-wide one, then the per-user one, whose
// profiles override the machine-wide ones of the same name. Reading them is a
// plain file read, not a command: nothing here is started.
func loadProfileStore(systemPath, userPath string, root bool) profileStore {
	userPath = expandHome(userPath)
	store := profileStore{path: userPath}
	if root {
		// root's own home is not where an operator looks for a machine's
		// configuration: a root session saves machine-wide.
		store.path = systemPath
	}
	for _, path := range []string{systemPath, userPath} {
		data, err := os.ReadFile(path) //nolint:gosec // the tool's own config locations
		if err != nil {
			continue
		}
		if path == store.path {
			store.text = string(data)
		}
		list, problems := tailscale.ParseJoinProfiles(string(data))
		store.problems = append(store.problems, problems...)
		for _, p := range list {
			replaced := false
			for i := range store.list {
				if store.list[i].Name == p.Name {
					store.list[i], replaced = p, true
				}
			}
			if !replaced {
				store.list = append(store.list, p)
			}
		}
	}
	return store
}

// demoProfileText is the demo's config.toml: two join profiles for the two
// roles a machine usually has, a laptop and a subnet router.
const demoProfileText = `# tui-tailscale configuration (demo)
sudo = "sudo -n"

[[profile]]
name = "laptop"
login_server = "https://headscale.example.com"
hostname = ""
accept_routes = true
advertise_routes = []
advertise_exit_node = false

[[profile]]
name = "subnet-router"
login_server = "https://headscale.example.com"
hostname = "office-router"
accept_routes = false
advertise_routes = ["192.0.2.0/24"]
advertise_exit_node = true
`

// demoProfileStore is the store --demo runs with. It is never read from or
// written to disk: a save in the demo goes through the fake backend.
func demoProfileStore() profileStore {
	list, _ := tailscale.ParseJoinProfiles(demoProfileText)
	return profileStore{path: config.SystemPathFor(toolName), text: demoProfileText, list: list}
}

// newJoinProfile is the picker's way out of the saved profiles.
const newJoinProfile = "new — answer every question"

// openJoinProfilePicker is j's first step when there are join profiles: one
// of them, pre-filling every step, or a fresh set of questions. The profile
// that matches the node's settings is the one offered.
func (a *app) openJoinProfilePicker() {
	options := []string{newJoinProfile}
	a.profileChoices = map[string]string{}
	current := newJoinProfile
	match, matched := a.profiles.matching(a.state.Prefs)
	for _, p := range a.profiles.list {
		label := p.Name + "  " + tailscale.URLHost(p.LoginServer)
		if routes := listOrDash(p.AdvertiseRoutes); routes != "-" {
			label += "  routes " + routes
		}
		if p.AdvertiseExitNode {
			label += "  exit node"
		}
		options = append(options, label)
		a.profileChoices[label] = p.Name
		if matched && p.Name == match.Name {
			current = label
		}
	}
	a.picker = ui.NewPicker("Join — start from a join profile?", options, current)
	a.pickerPurpose = pickerJoinProfile
	a.mode = modePicker
}

// tookJoinProfile starts the questions, pre-filled from the chosen profile.
func (a *app) tookJoinProfile(choice string) {
	name := a.profileChoices[choice]
	a.profileChoices = nil
	if p, ok := a.profiles.byName(name); ok {
		a.join.from = &p
		a.askJoinServer(p.LoginServer, "")
		return
	}
	a.startJoinQuestions()
}

// offerSaveProfile asks for a name to save the answers of the join that just
// went through under. A join started from a profile, answered exactly as that
// profile says, has nothing new to save.
func (a *app) offerSaveProfile(answers tailscale.JoinProfile) {
	if answers.Name != "" {
		if p, ok := a.profiles.byName(answers.Name); ok && p.Same(answers) {
			return
		}
	}
	name := answers.Name
	if name == "" {
		name = answers.Hostname
	}
	if name == "" || !tailscale.ValidProfileName(name) {
		name = "default"
	}
	a.saveDraft = answers
	a.openInput(inputSaveProfile, "Save these answers as a join profile? (optional)", "laptop",
		name, "A join profile keeps this join's answers — login server, hostname, routes, "+
			"exit-node offer — under a name, so the next j on this machine starts from them "+
			"and is confirm-and-go. The pre-auth key is not part of it: no secret is ever "+
			"saved. Empty skips. Saved to "+a.profiles.path+".")
}

// tookSaveProfileName builds the previewed write of the profile.
func (a *app) tookSaveProfileName(name string) {
	answers := a.saveDraft
	a.saveDraft = tailscale.JoinProfile{}
	if name == "" {
		a.setStatus(ui.StatusInfo, "not saved as a join profile")
		return
	}
	answers.Name = name
	if err := answers.Check(); err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return
	}
	text, removed, added := tailscale.UpsertJoinProfile(a.profiles.text, answers)
	plan, err := tailscale.BuildSaveJoinProfile(a.profiles.path, text, answers, removed, added)
	if err == nil {
		a.savingText = text
	}
	a.openPlan(plan, err)
}

// profileLines are the node screen's two profile lines: which join profile
// matches the node's settings, and tailscale's own login profiles.
func (a *app) profileLines() []string {
	t := a.theme
	var lines []string
	switch p, ok := a.profiles.matching(a.state.Prefs); {
	case ok:
		lines = append(lines, a.factHint("join profile", p.Name+" (matches these settings)",
			t.Base, "j starts from it"))
	case len(a.profiles.list) > 0:
		lines = append(lines, a.factHint("join profile", "none matches · "+
			pluralCount(len(a.profiles.list), "saved profile"), t.Base, "j picks one"))
	}
	if cur, ok := a.state.CurrentLoginProfile(); ok {
		value := cur.Account
		if cur.Tailnet != "" {
			value += " on " + cur.Tailnet
		}
		hint := ""
		if n := len(a.state.LoginProfiles); n > 1 {
			value += " · " + pluralCount(n, "login profile")
			hint = "p switches"
		}
		lines = append(lines, a.factHint("login profile", value, t.Base, hint))
	}
	return lines
}

// pluralCount renders "1 thing" or "n things".
func pluralCount(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// openLoginProfilePicker lists tailscale's login profiles, when there is more
// than one to switch between.
func (a *app) openLoginProfilePicker() {
	profiles := a.state.LoginProfiles
	if len(profiles) < 2 {
		a.setStatus(ui.StatusInfo, "tailscale remembers "+pluralCount(len(profiles),
			"login profile")+": nothing to switch to (a join to another login server adds one)")
		return
	}
	options := make([]string, 0, len(profiles))
	a.profileChoices = map[string]string{}
	current := ""
	for _, p := range profiles {
		label := p.Account + " on " + orDash(p.Tailnet) + "  (" + p.ID + ")"
		options = append(options, label)
		a.profileChoices[label] = p.ID
		if p.Current {
			current = label
		}
	}
	a.picker = ui.NewPicker("Switch to which login profile?", options, current)
	a.pickerPurpose = pickerLoginProfile
	a.mode = modePicker
}

// tookLoginProfile opens the switch, unless the pick is the current profile.
func (a *app) tookLoginProfile(choice string) {
	id := a.profileChoices[choice]
	a.profileChoices = nil
	if cur, ok := a.state.CurrentLoginProfile(); ok && cur.ID == id {
		a.setStatus(ui.StatusInfo, "that is the login profile in use")
		return
	}
	a.openPlan(tailscale.BuildCommand(tailscale.Request{
		Action: tailscale.ActionSwitchProfile, LoginProfile: id}))
}
