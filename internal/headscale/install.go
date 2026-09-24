package headscale

import (
	"fmt"
	"strings"

	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/runner"
)

// The companion install. When `headscale` is absent the control-plane screens
// say how to install it on this distribution, and `i` previews the commands
// that do it. headscale comes from the tui-tools package repository, where the
// family publishes its own source-built mirror of it (tui-tools/headscale):
// the same signed repository every tool in the family installs from, set up
// the way the family's installer does it — the key pinned by fingerprint,
// read back and compared before anything trusts it — through the kit's own
// repository steps, so the two cannot drift apart.
//
// The package installs the binary, a hardened unit and an example
// config.yaml, and deliberately does not enable or start the unit: the example
// has placeholders. S configures it and ends with the enable.

// RepoFingerprint is the tui-tools repository signing key, the fingerprint the
// family's launcher pins too. The downloaded key is read back with
// `gpg --show-keys` and compared with this before any file names it.
const RepoFingerprint = "767CFB337B01F32FFC073F3F389120B277E4FB44"

// PackageName is the mirror's package name in every format.
const PackageName = "headscale"

// ManualInstallURL is where the repository setup is documented for a
// distribution this tool has no plan for.
const ManualInstallURL = "https://tui.tools/install/"

// RepoState is whether the tui-tools repository is configured on this host.
type RepoState struct {
	// Configured reports that the package manager already fetches from the
	// tui-tools repository.
	Configured bool
	// Detail says how the answer was reached, for the screen.
	Detail string
}

// DetectDistro reads /etc/os-release. An unreadable file is an unknown
// distribution, not an error: the install instructions then say so.
func DetectDistro() pkgmgr.Distro { return pkgmgr.DetectDistro() }

// DetectRepo reads whether the tui-tools repository is configured for this
// machine's package manager. It reads files only — the repository files the
// kit's probe knows — and starts no process.
func DetectRepo() RepoState {
	mgr, err := pkgmgr.New(pkgmgr.Options{})
	if err != nil {
		return RepoState{Detail: runner.FirstLine(err.Error())}
	}
	status, err := mgr.RepoStatus()
	if err != nil {
		return RepoState{Detail: runner.FirstLine(err.Error())}
	}
	return RepoState{Configured: status.Configured, Detail: status.Detail}
}

// ManagerOf is the package manager an install on this distribution goes
// through, empty when the distribution is not one this tool knows.
func ManagerOf(d pkgmgr.Distro) pkgmgr.Manager {
	for _, m := range []pkgmgr.Manager{pkgmgr.ManagerAPT, pkgmgr.ManagerDNF, pkgmgr.ManagerPacman} {
		if d.Matches(m) {
			return m
		}
	}
	return ""
}

// Plan is what one confirm dialog shows and one confirmation runs: commands in
// order, stopping at the first failure. The install is the one multi-command
// plan of this package; every other change is a single command.
type Plan struct {
	Title string
	Body  string
	Steps []runner.Command
	// Verify is the index of the step whose output has to carry Fingerprint,
	// when Fingerprint is set: the `gpg --show-keys` of the downloaded
	// repository key. A mismatch stops the plan before anything trusts it.
	Verify      int
	Fingerprint string
}

// CheckStep is called with each step's output as the plan runs; it stops the
// plan when the verified step does not show the pinned key.
func (p Plan) CheckStep(i int, out string) error {
	if p.Fingerprint == "" || i != p.Verify {
		return nil
	}
	setup := pkgmgr.Setup{Fingerprint: p.Fingerprint}
	if !setup.Match(out) {
		return fmt.Errorf("the downloaded repository key is not %s: stopped before trusting it",
			p.Fingerprint)
	}
	return nil
}

// BuildInstall assembles the install plan for a distribution: the tui-tools
// repository when it is not configured yet, then the package.
func BuildInstall(d pkgmgr.Distro, repo RepoState) (Plan, error) {
	manager := ManagerOf(d)
	if manager == "" {
		name := d.String()
		if name == "" {
			name = "this distribution"
		}
		return Plan{}, fmt.Errorf("no install plan for %s; set up the tui-tools "+
			"repository by hand (%s), then install %s", name, ManualInstallURL, PackageName)
	}
	plan := Plan{Title: "Install headscale"}
	body := []string{}
	if !repo.Configured {
		setup, err := pkgmgr.BuildRepoSetup(manager, pkgmgr.RepoConfig{}, RepoFingerprint)
		if err != nil {
			return Plan{}, err
		}
		for _, step := range setup.Steps {
			plan.Steps = append(plan.Steps, fromPkgmgr(step))
		}
		plan.Verify, plan.Fingerprint = setup.Verify, setup.Fingerprint
		body = append(body, "The tui-tools package repository is added first, the way the "+
			"family's installer does it: its signing key is downloaded, read back and "+
			"compared with the pinned fingerprint "+RepoFingerprint+" before any "+
			"repository file names it, and the plan stops there if it differs.")
	} else {
		body = append(body, "The tui-tools package repository is already configured here.")
		if manager == pkgmgr.ManagerAPT {
			refresh, err := pkgmgr.BuildRefresh(manager)
			if err != nil {
				return Plan{}, err
			}
			plan.Steps = append(plan.Steps, fromPkgmgr(refresh))
		}
	}
	switch manager {
	case pkgmgr.ManagerAPT:
		plan.Steps = append(plan.Steps, runner.Command{
			Argv: []string{"apt-get", "install", "-y", PackageName}, Description: "Install headscale"})
	case pkgmgr.ManagerDNF:
		plan.Steps = append(plan.Steps, runner.Command{
			Argv: []string{"dnf", "install", "-y", PackageName}, Description: "Install headscale"})
	case pkgmgr.ManagerPacman:
		// Arch carries a headscale of its own; the repository-qualified name
		// installs the family's source-built mirror whatever the repository
		// order in pacman.conf. Arch supports no partial upgrade, so the
		// family's convention is -Syu: the machine is upgraded with it.
		plan.Steps = append(plan.Steps, runner.Command{
			Argv:        []string{"pacman", "-Syu", "--needed", "--noconfirm", "tui-tools/" + PackageName},
			Description: "Upgrade the system and install headscale"})
		body = append(body, "pacman refreshes the package databases and, because Arch "+
			"supports no partial upgrade, upgrades the rest of the machine along with it (-Syu).")
	}
	body = append(body, "headscale is the family's source-built mirror of the upstream "+
		"release, signed and attested like every tool. The package ships the binary, a "+
		"hardened unit and an example /etc/headscale/config.yaml, and does not start the "+
		"unit: S configures it and ends with the enable.")
	plan.Body = strings.Join(body, "\n\n")
	return plan, nil
}

// fromPkgmgr turns a kit package-manager command into the runner command this
// package runs.
func fromPkgmgr(c pkgmgr.Command) runner.Command {
	return runner.Command{Argv: c.Argv, Description: c.Explain, Stdin: c.Stdin}
}

// InstallInstructions is the plan for this distribution as lines a person can
// read or copy — the control-plane screens' text when headscale is absent, and
// the --check block's. They are the commands `i` runs, with sudo in front of
// the ones that escalate, so the two cannot say different things.
func InstallInstructions(d pkgmgr.Distro, repo RepoState) []string {
	plan, err := BuildInstall(d, repo)
	if err != nil {
		return []string{err.Error()}
	}
	lines := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		line := step.String()
		if escalates(step) {
			line = "sudo " + line
		}
		lines = append(lines, line)
	}
	return lines
}
