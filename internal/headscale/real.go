package headscale

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// searchPaths gives each binary the absolute fallbacks to try when it is not on
// PATH.
var searchPaths = map[string][]string{
	"headscale": {"/usr/bin/headscale", "/usr/local/bin/headscale"},
	// sh and install serve the control-plane configuration flow: config.yaml
	// arrives on the shell's stdin, the OIDC client secret on install's.
	// install also writes the tui-tools apt source list for the companion
	// install.
	"sh":      {"/bin/sh", "/usr/bin/sh"},
	"install": {"/usr/bin/install", "/bin/install"},
	// cat is the escalated read of /etc/headscale/config.yaml, which is
	// root-only on every distribution that packages headscale.
	"cat": {"/usr/bin/cat", "/bin/cat"},
	// systemctl reads the headscale unit's state and restarts it after a
	// configuration change.
	"systemctl": {"/usr/bin/systemctl", "/bin/systemctl"},
	// curl makes the one request this package sends off the machine on its
	// own: the IdP's discovery document, fetched from this host because it is
	// what will have to reach the IdP. It also downloads the tui-tools
	// repository's key and file for the companion install, on demand and
	// previewed.
	"curl": {"/usr/bin/curl", "/bin/curl"},
	// stat reads who owns headscale's state files and the files this tool
	// writes for it; chown is the previewed fix when the answer is wrong.
	"stat":  {"/usr/bin/stat", "/bin/stat"},
	"chown": {"/usr/bin/chown", "/bin/chown"},
	// The companion install: the package manager, and the kit's steps that
	// add the tui-tools repository — gpg reads the downloaded key back and
	// dearmours it, chmod and tee write the keyring and the repository file,
	// rpm and pacman-key import the key.
	"apt-get":    {"/usr/bin/apt-get", "/bin/apt-get"},
	"dnf":        {"/usr/bin/dnf", "/bin/dnf"},
	"rpm":        {"/usr/bin/rpm", "/bin/rpm"},
	"pacman":     {"/usr/bin/pacman", "/bin/pacman"},
	"pacman-key": {"/usr/bin/pacman-key", "/bin/pacman-key"},
	"gpg":        {"/usr/bin/gpg", "/bin/gpg"},
	"chmod":      {"/usr/bin/chmod", "/bin/chmod"},
	"tee":        {"/usr/bin/tee", "/bin/tee"},
}

// privilegedRead marks the binaries whose reads need root. The Headscale CLI
// talks to a socket only root (or the service account) can open.
var privilegedRead = map[string]bool{
	"headscale": true,
	// config.yaml is root-only, so its read escalates.
	"cat": true,
	// systemctl's reads are unprivileged; only its verbs are not.
	"systemctl": false,
	// curl reads the public internet, which needs no privilege at all.
	"curl": false,
	// The state directory is mode 750 and owned by the service account, so
	// only root can see inside it.
	"stat": true,
}

// escalates reports whether a command runs through the escalation prefix.
// Every command does but two reads: `curl` fetching an IdP's public discovery
// document, which needs no privilege at all — a preview reading
// `sudo -n curl …` would be asking for one the command has no business
// having — and `gpg --show-keys` reading back the repository key the install
// downloaded. It is decided per command rather than per binary because the
// companion install's curl writes a file under /etc and its gpg writes the
// keyring, and both of those have to escalate.
func escalates(cmd runner.Command) bool {
	if len(cmd.Argv) == 0 {
		return true
	}
	switch cmd.Argv[0] {
	case "curl":
		return hasArg(cmd.Argv, "-o")
	case "gpg":
		return !hasArg(cmd.Argv, "--show-keys")
	}
	return true
}

// hasArg reports whether argv carries a literal token.
func hasArg(argv []string, token string) bool {
	for _, a := range argv {
		if a == token {
			return true
		}
	}
	return false
}

// timeouts bounds each binary's runs. A package manager downloads, so it gets
// minutes.
var timeouts = map[string]time.Duration{
	"curl":    2 * time.Minute,
	"apt-get": 10 * time.Minute,
	"dnf":     10 * time.Minute,
	"rpm":     2 * time.Minute,
	"pacman":  10 * time.Minute,
}

// installHints tell a user what to install when a binary is missing.
var installHints = map[string]string{
	"headscale": "press i on a control-plane screen to install it",
	"curl":      "install curl to validate an OIDC issuer",
}

// Real is the backend that drives the control plane on this machine. Every
// process it starts goes through a kit runner, one per binary and privilege,
// resolved on first use. Preview and Run pick the runner by the command's own
// argv[0], so the preview the user confirmed carries the exact privilege
// prefix that binary will really run with.
type Real struct {
	sudo []string

	mu      sync.Mutex
	runners map[string]*runner.Runner
	missing map[string]error
}

// New builds the real backend. It deliberately cannot fail: a host without
// headscale is still a host the tool has something to say about — how to
// install it.
func New(sudoPrefix []string) *Real {
	return &Real{
		sudo:    sudoPrefix,
		runners: map[string]*runner.Runner{},
		missing: map[string]error{},
	}
}

// Name identifies the backend.
func (r *Real) Name() string { return "headscale" }

// runnerFor resolves a binary's runner on first use and caches it. A binary
// that cannot be resolved is remembered as missing so it is not probed again.
// escalate false is the unprivileged runner a curl of a public document uses.
func (r *Real) runnerFor(bin string, escalate bool) (*runner.Runner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cacheKey := bin
	if !escalate {
		cacheKey += "\x00user"
	}
	if run, ok := r.runners[cacheKey]; ok {
		return run, nil
	}
	if err, ok := r.missing[cacheKey]; ok {
		return nil, err
	}
	paths, known := searchPaths[bin]
	if !known {
		// Nothing outside the table may be started: a command naming another
		// binary is a bug in a builder, not something to resolve on PATH.
		err := fmt.Errorf("%w: %s is not a binary this tool drives", runner.ErrNotAvailable, bin)
		r.missing[cacheKey] = err
		return nil, err
	}
	priv := privilegedRead[bin]
	sudo := r.sudo
	if !escalate {
		sudo = nil
	}
	run, err := runner.New(runner.Options{
		Bin:             bin,
		SearchPaths:     paths,
		SudoPrefix:      sudo,
		PrivilegedReads: &priv,
		Timeout:         timeouts[bin],
		InstallHint:     installHints[bin],
	})
	if err != nil {
		r.missing[cacheKey] = err
		return nil, err
	}
	r.runners[cacheKey] = run
	return run, nil
}

// Reprobe drops every resolved runner and every remembered miss, so the next
// read resolves the binaries again. It is what makes an install take effect
// without restarting the tool: runners are resolved on first use and cached,
// and a binary that was missing then stays missing in the cache.
func (r *Real) Reprobe() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runners = map[string]*runner.Runner{}
	r.missing = map[string]error{}
}

// Preview renders the command the way its binary's runner would, so the
// privilege prefix in the dialog is the real one.
func (r *Real) Preview(cmd runner.Command) string {
	if len(cmd.Argv) == 0 {
		return ""
	}
	run, err := r.runnerFor(cmd.Argv[0], escalates(cmd))
	if err != nil {
		// The binary is missing (headscale before its install, say); show the
		// honest argv with the prefix it would get.
		if len(r.sudo) > 0 && escalates(cmd) {
			return strings.Join(r.sudo, " ") + " " + cmd.String()
		}
		return cmd.String()
	}
	return run.Preview(cmd)
}

// Run executes a previewed command through its binary's runner.
func (r *Real) Run(ctx context.Context, cmd runner.Command) (string, error) {
	if len(cmd.Argv) == 0 {
		return "", fmt.Errorf("nothing to run")
	}
	run, err := r.runnerFor(cmd.Argv[0], escalates(cmd))
	if err != nil {
		return "", err
	}
	return run.Run(ctx, cmd)
}

// Load reads the control plane, when its binary is present. None of it fails
// the load: each missing piece becomes a fact the UI shows.
func (r *Real) Load(ctx context.Context) (State, error) {
	state := State{Distro: DetectDistro(), Repo: DetectRepo()}
	run, err := r.runnerFor("headscale", true)
	if err != nil {
		if runner.Available("headscale", searchPaths["headscale"]...) {
			// The binary is there, the escalation prefix is not.
			state.Present = true
			state.Error = runner.FirstLine(err.Error())
		}
		return state, nil
	}
	state.Present = true
	// The configuration is read first: it is the one part of the control plane
	// that still has an answer when headscale's own socket does not.
	state.ControlPlane = r.loadControlPlane(ctx)

	// With the unit known to be stopped, every CLI read would fail on the
	// socket; the screens say so instead of showing that failure.
	if msg := NotRunningMessage(state.ControlPlane); msg != "" {
		state.Error, state.NotRunning = msg, true
		return state, nil
	}

	usersOut, err := run.Read(ctx, "headscale", "users", "list", "--output", "json")
	if err != nil {
		state.Error = CLIErrorMessage(usersOut, err)
		return state, nil
	}
	if users, err := ParseUsers([]byte(usersOut)); err == nil {
		state.Users = users
	}
	if nodesOut, err := run.Read(ctx, "headscale", "nodes", "list", "--output", "json"); err == nil {
		if nodes, err := ParseNodes([]byte(nodesOut)); err == nil {
			state.Nodes = nodes
		}
	}
	if keysOut, err := run.Read(ctx, "headscale", "preauthkeys", "list", "--output", "json"); err == nil {
		if keys, err := ParsePreAuthKeys([]byte(keysOut)); err == nil {
			state.PreAuthKeys = keys
		}
	}
	state.OIDCInferred = InferOIDC(state.Users, state.Nodes)
	return state, nil
}

// loadControlPlane reads headscale's own configuration and the state of its
// unit. Neither is fatal: a host where config.yaml cannot be read still shows
// its users and nodes, and says why the control-plane panel is empty.
func (r *Real) loadControlPlane(ctx context.Context) ControlPlane {
	cp := r.readControlPlane(ctx)
	// The unit's state is read whether or not the file could be: "not
	// running" is the answer the list screens need even when config.yaml is
	// unreadable.
	cp.ServiceState = r.serviceState(ctx)
	cp.ServiceEnabled = r.serviceEnabled(ctx)
	if cp.Readable {
		cp.Ownership = r.checkOwnership(ctx, cp)
	}
	return cp
}

// readControlPlane reads and parses config.yaml, with the account the unit
// runs as, which the ownership check compares against.
func (r *Real) readControlPlane(ctx context.Context) ControlPlane {
	cp := ControlPlane{ConfigPath: HeadscaleConfigPath}

	run, err := r.runnerFor("cat", true)
	if err != nil {
		cp.Error = runner.FirstLine(err.Error())
		return cp
	}
	out, err := run.Read(ctx, "cat", HeadscaleConfigPath)
	if err != nil {
		cp.Error = runner.FirstLine(err.Error())
		return cp
	}
	parsed, err := ParseHeadscaleConfig([]byte(out))
	if err != nil {
		cp.Error = runner.FirstLine(err.Error())
		return cp
	}
	cp = parsed
	cp.ServiceUser, cp.ServiceGroup = r.serviceAccount(ctx)
	return cp
}

// checkOwnership stats headscale's state paths and the tool's own files, and
// compares their owners with the account the unit runs as. `stat` exits
// non-zero when any path is missing — the database of a server that never
// started, a backup never taken — and still prints every path it found, so
// its output is parsed whatever the exit status. Only a read that printed
// nothing at all leaves the ownership unchecked.
func (r *Real) checkOwnership(ctx context.Context, cp ControlPlane) Ownership {
	stats := r.Stat(ctx, OwnershipPaths(cp))
	if len(stats) == 0 {
		return Ownership{}
	}
	return CheckOwnership(cp, stats)
}

// Stat reads owner, group and mode of each path, escalated. `stat` exits
// non-zero when any path is missing and still prints every one it found, so
// the output is parsed whatever the exit status.
func (r *Real) Stat(ctx context.Context, paths []string) map[string]FileStat {
	run, err := r.runnerFor("stat", true)
	if err != nil || len(paths) == 0 {
		return map[string]FileStat{}
	}
	out, _ := run.Read(ctx, StatArgv(paths)...)
	return ParseStat(out)
}

// serviceAccount asks systemd which account the headscale unit runs as. It is
// what the client secret file must be owned by: headscale's own .deb and the
// Arch package run it as a dedicated user, older or hand-written units run it
// as root, and a file the service cannot read is a service that will not come
// back from the restart.
func (r *Real) serviceAccount(ctx context.Context) (user, group string) {
	run, err := r.runnerFor("systemctl", true)
	if err != nil {
		return DefaultServiceUser, DefaultServiceUser
	}
	out, _ := run.Read(ctx, ServiceAccountProperties()...)
	return ParseServiceAccount(out)
}

// serviceState asks systemd what the headscale unit is doing. `is-active`
// exits non-zero for every answer but "active", which is a state, not a
// failure: the word it printed is the answer either way.
func (r *Real) serviceState(ctx context.Context) string {
	run, err := r.runnerFor("systemctl", true)
	if err != nil {
		return "unknown"
	}
	out, _ := run.Read(ctx, "systemctl", "is-active", HeadscaleService)
	if state := strings.TrimSpace(runner.FirstLine(out)); state != "" {
		return state
	}
	return "unknown"
}

// serviceEnabled asks systemd whether the headscale unit starts at boot. Like
// is-active, is-enabled exits non-zero for most of its answers ("disabled"
// among them), and the word it printed is the answer either way.
func (r *Real) serviceEnabled(ctx context.Context) string {
	run, err := r.runnerFor("systemctl", true)
	if err != nil {
		return "unknown"
	}
	out, _ := run.Read(ctx, "systemctl", "is-enabled", HeadscaleService)
	if state := strings.TrimSpace(runner.FirstLine(out)); state != "" && !strings.Contains(state, " ") {
		return state
	}
	return "unknown"
}

// HostFact is what --report says about the control plane without privilege:
// whether the binary is there and what systemd says about its unit. It reads
// no configuration, user, node or key.
type HostFact struct {
	Present bool
	// Service is `systemctl is-active headscale`, empty when not asked.
	Service string
}

// HostFacts probes the control plane without privilege, for the bug-report
// block.
func HostFacts(ctx context.Context) HostFact {
	fact := HostFact{Present: runner.Available("headscale", searchPaths["headscale"]...)}
	if !fact.Present {
		return fact
	}
	priv := false
	run, err := runner.New(runner.Options{
		Bin: "systemctl", SearchPaths: searchPaths["systemctl"],
		PrivilegedReads: &priv, Timeout: 5 * time.Second,
	})
	if err != nil {
		return fact
	}
	out, _ := run.Read(ctx, "systemctl", "is-active", HeadscaleService)
	if state := strings.TrimSpace(runner.FirstLine(out)); state != "" && !strings.Contains(state, " ") {
		fact.Service = state
	}
	return fact
}
