package tailscale

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tui-tools/tui-kit/runner"
)

// This file is about two different things that are both called a profile, and
// the UI never mixes their names up:
//
//   - a JOIN PROFILE is this tool's preset for `j`: the answers of the join
//     form (login server, hostname, routes, exit-node offer) saved under a
//     name in the tool's config.toml, so re-joining a machine of a known role
//     is confirm-and-go. It never holds a secret: the pre-auth key is asked
//     for every time and is not part of a profile.
//   - a LOGIN PROFILE is tailscale's own: one per login identity the client
//     remembers (`tailscale switch --list`), switched with `tailscale switch`.

// JoinProfile is one saved set of join answers.
type JoinProfile struct {
	Name              string
	LoginServer       string
	Hostname          string
	AcceptRoutes      bool
	AdvertiseRoutes   []string
	AdvertiseExitNode bool
}

// profileNamePattern is a join profile's name: short, one word, so it reads
// as a label in a picker and survives the TOML line it is written into.
var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

// ValidProfileName reports whether s can name a join profile.
func ValidProfileName(s string) bool { return profileNamePattern.MatchString(s) }

// Check validates a profile the way the join form validates its answers, so a
// hand-edited config.toml cannot pre-fill a join with something the form
// would have refused.
func (p JoinProfile) Check() error {
	if !ValidProfileName(p.Name) {
		return fmt.Errorf("not a valid profile name (letters, digits, . _ -, up to 40): %q", p.Name)
	}
	if problem := ServerURLProblem(p.LoginServer); problem != "" {
		return fmt.Errorf("profile %s: login server: %s", p.Name, problem)
	}
	if p.Hostname != "" && !ValidHostname(p.Hostname) {
		return fmt.Errorf("profile %s: not a valid hostname: %q", p.Name, p.Hostname)
	}
	if _, err := NormalizeRoutes(p.AdvertiseRoutes); err != nil {
		return fmt.Errorf("profile %s: %w", p.Name, err)
	}
	return nil
}

// Matches reports whether the node's settings are what this profile would
// set: the same login server, hostname, routes and switches.
func (p JoinProfile) Matches(prefs Prefs) bool {
	server := strings.TrimRight(prefs.ControlURL, "/")
	if !strings.EqualFold(server, strings.TrimRight(p.LoginServer, "/")) {
		return false
	}
	if p.Hostname != prefs.Hostname || p.AcceptRoutes != prefs.RouteAll ||
		p.AdvertiseExitNode != prefs.AdvertisesExitNode() {
		return false
	}
	want, _ := NormalizeRoutes(p.AdvertiseRoutes)
	have, _ := NormalizeRoutes(prefs.SubnetRoutes())
	slices.Sort(want)
	slices.Sort(have)
	return slices.Equal(want, have)
}

// Same reports whether two profiles carry the same answers, name included.
func (p JoinProfile) Same(q JoinProfile) bool {
	return p.Name == q.Name && strings.EqualFold(strings.TrimRight(p.LoginServer, "/"),
		strings.TrimRight(q.LoginServer, "/")) && p.Hostname == q.Hostname &&
		p.AcceptRoutes == q.AcceptRoutes && p.AdvertiseExitNode == q.AdvertiseExitNode &&
		slices.Equal(p.AdvertiseRoutes, q.AdvertiseRoutes)
}

// profileHeader is the TOML array-of-tables header a join profile starts with.
const profileHeader = "[[profile]]"

// ParseJoinProfiles reads the `[[profile]]` tables of a config.toml. It is a
// reader of exactly the shape RenderJoinProfile writes — `key = "string"`,
// `key = true`, `key = ["a", "b"]` — plus comments and blank lines, and it
// skips what it does not understand rather than failing: the file is shared
// with the kit's own keys, and a hand edit must not take the tool down. A
// profile that does not validate is dropped, with the reason in the second
// return value.
func ParseJoinProfiles(text string) ([]JoinProfile, []string) {
	var profiles []JoinProfile
	var problems []string
	var cur *JoinProfile
	flush := func() {
		if cur == nil {
			return
		}
		if err := cur.Check(); err != nil {
			problems = append(problems, err.Error())
		} else {
			profiles = append(profiles, *cur)
		}
		cur = nil
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case line == profileHeader:
			flush()
			cur = &JoinProfile{}
			continue
		case strings.HasPrefix(line, "["):
			flush()
			continue
		}
		if cur == nil {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "name":
			cur.Name = tomlString(value)
		case "login_server":
			cur.LoginServer = tomlString(value)
		case "hostname":
			cur.Hostname = tomlString(value)
		case "accept_routes":
			cur.AcceptRoutes = value == "true"
		case "advertise_exit_node":
			cur.AdvertiseExitNode = value == "true"
		case "advertise_routes":
			cur.AdvertiseRoutes = tomlStrings(value)
		}
	}
	flush()
	return profiles, problems
}

// tomlString reads a quoted TOML basic string, or a bare word.
func tomlString(v string) string {
	if unquoted, err := strconv.Unquote(v); err == nil {
		return unquoted
	}
	// A trailing comment after a quoted value.
	if strings.HasPrefix(v, `"`) {
		if end := strings.Index(v[1:], `"`); end >= 0 {
			return v[1 : end+1]
		}
	}
	return strings.Trim(v, `"' `)
}

// tomlStrings reads a one-line TOML array of strings.
func tomlStrings(v string) []string {
	v = strings.TrimSpace(v)
	if i := strings.LastIndexByte(v, ']'); strings.HasPrefix(v, "[") && i > 0 {
		v = v[1:i]
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = tomlString(strings.TrimSpace(item)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// RenderJoinProfile renders one profile as the `[[profile]]` table it is
// saved as. Every value is quoted, so nothing typed can end the line early.
func RenderJoinProfile(p JoinProfile) []string {
	routes := make([]string, 0, len(p.AdvertiseRoutes))
	for _, r := range p.AdvertiseRoutes {
		routes = append(routes, strconv.Quote(r))
	}
	return []string{
		profileHeader,
		"name = " + strconv.Quote(p.Name),
		"login_server = " + strconv.Quote(strings.TrimRight(p.LoginServer, "/")),
		"hostname = " + strconv.Quote(p.Hostname),
		"accept_routes = " + strconv.FormatBool(p.AcceptRoutes),
		"advertise_routes = [" + strings.Join(routes, ", ") + "]",
		"advertise_exit_node = " + strconv.FormatBool(p.AdvertiseExitNode),
	}
}

// UpsertJoinProfile returns the config.toml text with the profile saved: a
// profile of the same name is replaced in place, a new one is appended at the
// end. Every other line — the kit's keys, comments, other profiles — is kept
// as it was. The second value is the lines that were removed and the third
// the lines that were added, for the confirm dialog.
func UpsertJoinProfile(text string, p JoinProfile) (string, []string, []string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	added := RenderJoinProfile(p)
	start, end := -1, -1
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != profileHeader {
			continue
		}
		j := i + 1
		for j < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
			j++
		}
		block, _ := ParseJoinProfiles(strings.Join(lines[i:j], "\n"))
		named := blockName(lines[i:j])
		if (len(block) == 1 && block[0].Name == p.Name) || named == p.Name {
			start, end = i, j
			// Give back the blank lines at the tail of the block: they
			// separate it from whatever follows.
			for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
				end--
			}
			break
		}
		i = j - 1
	}
	var out []string
	var removed []string
	if start >= 0 {
		removed = append(removed, lines[start:end]...)
		out = append(append(append(out, lines[:start]...), added...), lines[end:]...)
	} else {
		out = append(out, lines...)
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, added...)
	}
	return strings.Join(out, "\n") + "\n", removed, added
}

// blockName is the name a profile block carries, even when the block does not
// validate: saving over a broken profile of the same name replaces it.
func blockName(block []string) string {
	for _, l := range block {
		key, value, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && strings.TrimSpace(key) == "name" {
			return tomlString(strings.TrimSpace(value))
		}
	}
	return ""
}

// BuildSaveJoinProfile is the previewed write of the config file that holds
// the profiles. The whole file travels on the standard input of `install`,
// which creates the directory when it is missing (-D); the preview names the
// path, and the dialog body shows the lines that change.
func BuildSaveJoinProfile(path, content string, p JoinProfile, removed, added []string) (Plan, error) {
	if err := p.Check(); err != nil {
		return Plan{}, err
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \n\t") {
		return Plan{}, fmt.Errorf("not an absolute config path: %q", path)
	}
	var diff []string
	for _, l := range removed {
		diff = append(diff, "- "+l)
	}
	for _, l := range added {
		diff = append(diff, "+ "+l)
	}
	verb := "Save"
	if len(removed) > 0 {
		verb = "Update"
	}
	return Plan{
		Action: ActionSaveProfile,
		Title:  verb + " the join profile " + p.Name,
		Body: "A join profile is this tool's preset for j: the answers below, under a name, so " +
			"the next join of this machine is confirm-and-go. It holds no secret: a pre-auth " +
			"key is asked for every time and is never saved.\n\n" +
			"--- " + path + "\n+++ " + path + " (after)\n" + strings.Join(diff, "\n"),
		Steps: []runner.Command{{
			Argv:        []string{"install", "-D", "-m", "644", "/dev/stdin", path},
			Description: verb + " join profile " + p.Name + " in " + path,
			Stdin:       content,
		}},
	}, nil
}

// --- tailscale's own login profiles ------------------------------------------

// LoginProfile is one login identity the client remembers.
type LoginProfile struct {
	ID      string
	Tailnet string
	Account string
	Current bool
}

// ParseLoginProfiles reads `tailscale switch --list`: a tab-aligned table
// whose header names the columns (ID, Tailnet, Account, and a Nickname column
// in some versions), the current profile marked with a trailing `*` on its
// account. The columns are cut where the header says they start, so a
// tailnet name with a space in it still lands in its column.
func ParseLoginProfiles(out string) []LoginProfile {
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	header := -1
	for i, l := range lines {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "ID" {
			header = i
			break
		}
	}
	if header < 0 {
		return nil
	}
	head := lines[header]
	columns := map[string]int{}
	for _, name := range []string{"ID", "Nickname", "Tailnet", "Account"} {
		if i := strings.Index(head, name); i >= 0 {
			columns[name] = i
		}
	}
	starts := []int{}
	for _, i := range columns {
		starts = append(starts, i)
	}
	slices.Sort(starts)
	cut := func(line, name string) string {
		start, ok := columns[name]
		if !ok || start >= len(line) {
			return ""
		}
		end := len(line)
		for _, s := range starts {
			if s > start && s < end {
				end = s
				break
			}
		}
		return strings.TrimSpace(line[start:min(end, len(line))])
	}
	var profiles []LoginProfile
	for _, l := range lines[header+1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		p := LoginProfile{ID: cut(l, "ID"), Tailnet: cut(l, "Tailnet"), Account: cut(l, "Account")}
		if strings.HasSuffix(p.Account, "*") {
			p.Account, p.Current = strings.TrimSuffix(p.Account, "*"), true
		}
		if p.ID == "" || strings.ContainsAny(p.ID, " \t") {
			continue
		}
		profiles = append(profiles, p)
	}
	return profiles
}

// loginProfileIDPattern is what `tailscale switch` is handed: the profile's
// short hex id, never a free-typed name.
var loginProfileIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

// buildSwitchProfile switches to another login profile.
func buildSwitchProfile(req Request) (Plan, error) {
	if !loginProfileIDPattern.MatchString(req.LoginProfile) {
		return Plan{}, fmt.Errorf("not a login profile id: %q", req.LoginProfile)
	}
	return Plan{
		Action: ActionSwitchProfile,
		Title:  "Switch to the login profile " + req.LoginProfile,
		Body: "tailscale keeps one login profile per identity it has logged in with. Switching " +
			"changes which tailnet and account this node is on, with that profile's own " +
			"settings; the one it leaves is kept, so switching back needs no new login. If you " +
			"are connected to this machine over the tailnet, this may end that session.",
		Steps: []runner.Command{{Argv: []string{"tailscale", "switch", req.LoginProfile},
			Description: "Switch login profile", Destructive: true}},
		Destructive: true,
	}, nil
}
