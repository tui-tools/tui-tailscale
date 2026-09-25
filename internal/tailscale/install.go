package tailscale

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/runner"
)

// The companion install. When `tailscale` is absent the node screen says how
// to install it on this distribution, and `i` previews the package manager
// commands that do it. Those are the steps Tailscale documents for each
// family — its apt repository and signing key, its dnf repository file, or
// Arch's own package — rather than its `curl | sh` script: the family's rule
// is that every command is shown before it runs, and a script piped into a
// shell is exactly the command nobody sees.

// Distro is the machine as /etc/os-release describes it: the kit's parse, plus
// the release codename apt repositories are keyed by.
type Distro struct {
	pkgmgr.Distro
	// Codename is VERSION_CODENAME ("noble", "bookworm"), or UBUNTU_CODENAME
	// on a derivative that carries Ubuntu's.
	Codename string
}

// osReleasePath is where the distribution identifies itself.
const osReleasePath = "/etc/os-release"

// DetectDistro reads /etc/os-release. An unreadable file is an unknown
// distribution, not an error: the install instructions then say so.
func DetectDistro() Distro {
	raw, err := os.ReadFile(osReleasePath)
	if err != nil {
		return Distro{}
	}
	return ParseDistro(string(raw))
}

// ParseDistro reads an os-release file: ID, ID_LIKE, PRETTY_NAME and
// VERSION_ID through the kit's parser, and the codename beside it.
func ParseDistro(text string) Distro {
	d := Distro{Distro: pkgmgr.ParseOSRelease(text)}
	var ubuntuCodename string
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "VERSION_CODENAME":
			d.Codename = value
		case "UBUNTU_CODENAME":
			ubuntuCodename = value
		}
	}
	// A derivative (Mint, Pop!_OS) names its own codename in VERSION_CODENAME
	// and Ubuntu's in UBUNTU_CODENAME; the repository is Ubuntu's.
	if d.ID != "ubuntu" && ubuntuCodename != "" {
		d.Codename = ubuntuCodename
	}
	return d
}

// Manager is the package manager an install on this distribution goes
// through, empty when the distribution is not one this tool knows.
func (d Distro) Manager() pkgmgr.Manager {
	for _, m := range []pkgmgr.Manager{pkgmgr.ManagerAPT, pkgmgr.ManagerDNF, pkgmgr.ManagerPacman} {
		if d.Matches(m) {
			return m
		}
	}
	return ""
}

// aptFamily is the path segment of Tailscale's apt repository: "ubuntu" or
// "debian".
func (d Distro) aptFamily() string {
	if d.ID == "debian" || d.ID == "raspbian" {
		return "debian"
	}
	for _, like := range append([]string{d.ID}, d.Like...) {
		if like == "ubuntu" {
			return "ubuntu"
		}
	}
	return "debian"
}

// rpmFamily is the path of Tailscale's rpm repository: fedora, or rhel/<major>
// for the RHEL rebuilds.
func (d Distro) rpmFamily() string {
	if d.ID == "fedora" {
		return "fedora"
	}
	major, _, _ := strings.Cut(d.VersionID, ".")
	if major == "" || !allDigits(major) {
		return "fedora"
	}
	return "rhel/" + major
}

// PkgsBase is Tailscale's package repository.
const PkgsBase = "https://pkgs.tailscale.com/stable"

// codenamePattern is a release codename: a plain lowercase word, which is what
// ends up in a URL path.
var codenamePattern = regexp.MustCompile(`^[a-z]{2,32}$`)

// ManualInstallURL is where the instructions live for a distribution this
// tool has no plan for.
const ManualInstallURL = "https://tailscale.com/download/linux"

// buildInstall assembles the install plan for a distribution.
func buildInstall(d Distro) (Plan, error) {
	enable := runner.Command{
		Argv:        []string{"systemctl", "enable", "--now", "tailscaled"},
		Description: "Start tailscaled now and at boot",
	}
	plan := Plan{Action: ActionInstall, Title: "Install tailscale"}
	switch d.Manager() {
	case pkgmgr.ManagerAPT:
		if !codenamePattern.MatchString(d.Codename) {
			return Plan{}, fmt.Errorf("cannot tell this release's codename from %s; "+
				"see %s", osReleasePath, ManualInstallURL)
		}
		base := PkgsBase + "/" + d.aptFamily() + "/" + d.Codename
		plan.Body = "Tailscale's apt repository is added — its signing key and its source " +
			"list, both from pkgs.tailscale.com, the way Tailscale documents it for " +
			d.aptFamily() + " " + d.Codename + " — then the package is installed and " +
			"tailscaled started. The repository keeps the client up to date with apt."
		plan.Steps = []runner.Command{
			{Argv: []string{"curl", "-fsSL", "-o", "/usr/share/keyrings/tailscale-archive-keyring.gpg",
				base + ".noarmor.gpg"}, Description: "Add Tailscale's signing key"},
			{Argv: []string{"curl", "-fsSL", "-o", "/etc/apt/sources.list.d/tailscale.list",
				base + ".tailscale-keyring.list"}, Description: "Add Tailscale's apt repository"},
			{Argv: []string{"apt-get", "update"}, Description: "Refresh the package lists"},
			{Argv: []string{"apt-get", "install", "-y", "tailscale"}, Description: "Install tailscale"},
			enable,
		}
	case pkgmgr.ManagerDNF:
		repo := PkgsBase + "/" + d.rpmFamily() + "/tailscale.repo"
		plan.Body = "Tailscale's dnf repository file is fetched from pkgs.tailscale.com into " +
			"/etc/yum.repos.d (the same file `dnf config-manager` would add, written the " +
			"way that works with both dnf4 and dnf5), then the package is installed — dnf " +
			"asks nothing, so it imports the repository's signing key itself — and " +
			"tailscaled started."
		plan.Steps = []runner.Command{
			{Argv: []string{"curl", "-fsSL", "-o", "/etc/yum.repos.d/tailscale.repo", repo},
				Description: "Add Tailscale's dnf repository"},
			{Argv: []string{"dnf", "install", "-y", "tailscale"}, Description: "Install tailscale"},
			enable,
		}
	case pkgmgr.ManagerPacman:
		// Arch has no supported way to install one package against a
		// refreshed database without upgrading the rest (a bare -Sy is a
		// partial upgrade), and a stale database on a fresh image points at
		// files the mirrors no longer carry. So the family's convention: -Syu,
		// and the dialog says the machine is upgraded with it.
		plan.Body = "tailscale is in Arch's own repositories. pacman refreshes the " +
			"package database and installs it — and, because Arch supports no partial " +
			"upgrade, upgrades the rest of the machine with it (-Syu). Then tailscaled " +
			"is started."
		plan.Steps = []runner.Command{
			{Argv: []string{"pacman", "-Syu", "--needed", "--noconfirm", "tailscale"},
				Description: "Upgrade the system and install tailscale"},
			enable,
		}
	default:
		name := d.String()
		if name == "" {
			name = "this distribution"
		}
		return Plan{}, fmt.Errorf("no install plan for %s; see %s", name, ManualInstallURL)
	}
	return plan, nil
}

// InstallInstructions is the plan for this distribution as lines a person can
// read or copy — the node screen's text when tailscale is absent, and the
// --check block's. They are the commands `i` runs, prefixed with sudo, so the
// two cannot say different things.
func InstallInstructions(d Distro) []string {
	plan, err := buildInstall(d)
	if err != nil {
		return []string{err.Error()}
	}
	lines := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		lines = append(lines, "sudo "+step.String())
	}
	return lines
}
