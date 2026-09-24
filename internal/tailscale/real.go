package tailscale

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// searchPaths gives each binary the absolute fallbacks to try when it is not
// on PATH. sysctl lives in /usr/sbin on the distributions that still split it.
var searchPaths = map[string][]string{
	"tailscale": {"/usr/bin/tailscale", "/usr/local/bin/tailscale"},
	// install and rm carry the pre-auth key file in and out; install also
	// writes the sysctl drop-in.
	"install": {"/usr/bin/install", "/bin/install"},
	"rm":      {"/usr/bin/rm", "/bin/rm"},
	"sysctl":  {"/usr/sbin/sysctl", "/sbin/sysctl", "/usr/bin/sysctl"},
	// The companion install: curl fetches Tailscale's repository files, the
	// package manager installs, systemctl starts the daemon.
	"curl":      {"/usr/bin/curl", "/bin/curl"},
	"apt-get":   {"/usr/bin/apt-get", "/bin/apt-get"},
	"dnf":       {"/usr/bin/dnf", "/bin/dnf"},
	"pacman":    {"/usr/bin/pacman", "/bin/pacman"},
	"systemctl": {"/usr/bin/systemctl", "/bin/systemctl"},
}

// timeouts bounds each binary's runs. A package manager downloads, so it gets
// minutes; `tailscale up` waits JoinTimeout for the node on its own, and
// gets headroom above that.
var timeouts = map[string]time.Duration{
	"tailscale": 45 * time.Second,
	"curl":      2 * time.Minute,
	"apt-get":   10 * time.Minute,
	"dnf":       10 * time.Minute,
	"pacman":    10 * time.Minute,
	"systemctl": time.Minute,
}

// installHints tell a user what to install when a binary is missing.
var installHints = map[string]string{
	"tailscale": "press i on the node screen to install it",
	"curl":      "install curl first",
}

// unprivileged is the address-of-false the runner options need: every read
// this tool makes is first tried as the invoking user.
var unprivileged = false

// Real is the backend that drives the machine. It is the tool's only exec
// site: every process it starts goes through a kit runner, one per binary,
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
// tailscale is still a host the tool has something to say about — how to
// install it.
func New(sudoPrefix []string) (*Real, error) {
	return &Real{
		sudo:    sudoPrefix,
		runners: map[string]*runner.Runner{},
		missing: map[string]error{},
	}, nil
}

// Name identifies the backend.
func (r *Real) Name() string { return "tailscale" }

// Describe is the one-line summary shown in the header.
func (r *Real) Describe() string {
	run, err := r.runnerFor("tailscale")
	if err != nil {
		if runner.Available("tailscale", searchPaths["tailscale"]...) {
			return "tailscale (cannot run: " + runner.FirstLine(err.Error()) + ")"
		}
		return "tailscale is not installed — i installs it, or run --demo"
	}
	return run.Describe()
}

// runnerFor resolves a binary's runner on first use and caches it. A binary
// that cannot be resolved is remembered as missing so it is not probed again.
func (r *Real) runnerFor(bin string) (*runner.Runner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runners[bin]; ok {
		return run, nil
	}
	if err, ok := r.missing[bin]; ok {
		return nil, err
	}
	paths, known := searchPaths[bin]
	if !known {
		// Nothing outside the table may be started: a command naming another
		// binary is a bug in BuildCommand, not something to resolve on PATH.
		err := fmt.Errorf("%w: %s is not a binary this tool drives", runner.ErrNotAvailable, bin)
		r.missing[bin] = err
		return nil, err
	}
	run, err := runner.New(runner.Options{
		Bin:             bin,
		SearchPaths:     paths,
		SudoPrefix:      r.sudo,
		PrivilegedReads: &unprivileged,
		Timeout:         timeouts[bin],
		InstallHint:     installHints[bin],
	})
	if err != nil {
		r.missing[bin] = err
		return nil, err
	}
	r.runners[bin] = run
	return run, nil
}

// Preview renders the command the way its binary's runner would, so the
// privilege prefix in the dialog is the real one.
func (r *Real) Preview(cmd runner.Command) string {
	if len(cmd.Argv) == 0 {
		return ""
	}
	run, err := r.runnerFor(cmd.Argv[0])
	if err != nil {
		// The binary is missing (curl before an install, say); show the
		// honest argv with the prefix it would get.
		if len(r.sudo) > 0 {
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
	run, err := r.runnerFor(cmd.Argv[0])
	if err != nil {
		return "", err
	}
	return run.Run(ctx, cmd)
}

// Load reads the node: whether the client is installed, what tailscaled says
// about this node and its peers, and the node's settings. None of it fails
// the load — each missing piece becomes a fact the UI shows.
func (r *Real) Load(ctx context.Context) (State, error) {
	state := State{Distro: DetectDistro()}

	run, err := r.runnerFor("tailscale")
	if err != nil {
		// The binary can be there while its runner cannot be built: an
		// escalation prefix that is not installed fails the runner, not the
		// client. That is a different message from "not installed".
		if runner.Available("tailscale", searchPaths["tailscale"]...) {
			state.Installed = true
			state.Error = runner.FirstLine(err.Error())
		}
		return state, nil
	}
	state.Installed = true

	out, err := r.read(ctx, run, "tailscale", "status", "--json")
	if err != nil {
		describeReadFailure(&state, out, err)
		return state, nil
	}
	status, err := ParseStatus(out)
	if err != nil {
		state.Error = runner.FirstLine(err.Error())
		return state, nil
	}
	state.DaemonRunning = true
	state.Node, state.Peers = status.Node, status.Peers

	prefsOut, err := r.read(ctx, run, "tailscale", "debug", "prefs")
	if err != nil {
		state.PrefsError = runner.FirstLine(err.Error())
		return state, nil
	}
	prefs, err := ParsePrefs(prefsOut)
	if err != nil {
		state.PrefsError = runner.FirstLine(err.Error())
		return state, nil
	}
	state.Prefs, state.PrefsRead = prefs, true
	return state, nil
}

// read runs a read as the invoking user first, which is enough on most
// machines: tailscaled lets any local user read the node's status. Only when
// the socket refuses (a hardened daemon, or a platform that checks) is the
// same read run again through the escalation prefix.
func (r *Real) read(ctx context.Context, run *runner.Runner, argv ...string) (string, error) {
	out, err := run.Read(ctx, argv...)
	if err == nil {
		return out, nil
	}
	if ClassifyReadError(out+" "+err.Error()) != ProblemPermission || !run.Privileged() {
		return out, err
	}
	return run.Run(ctx, runner.Command{Argv: argv})
}

// describeReadFailure turns a failed status read into what the node screen
// says: a stopped daemon is told how to start, a refused socket how to read
// it, anything else is quoted.
func describeReadFailure(state *State, out string, err error) {
	switch ClassifyReadError(out + " " + err.Error()) {
	case ProblemNotRunning:
		state.NotRunning = true
		state.Error = "tailscaled is not running — start it with " +
			"`sudo systemctl enable --now tailscaled`"
	case ProblemPermission:
		state.PermissionDenied = true
		state.Error = "tailscaled refused this user — run with sudo, or make this user " +
			"the operator (`sudo tailscale set --operator=$USER`)"
	default:
		state.Error = runner.FirstLine(err.Error())
	}
}

// HostFact is what --report says about the client without privilege: whether
// the binary is there and whether tailscaled answers. It reads no address,
// name or login server.
type HostFact struct {
	Installed bool
	// Daemon is "running", "not running", "refused" or "unknown".
	Daemon string
	// BackendState is ipn's state, when the daemon answered.
	BackendState string
}

// HostFacts probes the client without privilege, for the bug-report block.
func HostFacts(ctx context.Context) HostFact {
	fact := HostFact{Daemon: "unknown"}
	run, err := runner.New(runner.Options{
		Bin: "tailscale", SearchPaths: searchPaths["tailscale"],
		PrivilegedReads: &unprivileged, Timeout: 5 * time.Second,
	})
	if err != nil {
		return fact
	}
	fact.Installed = true
	out, err := run.Read(ctx, "tailscale", "status", "--json")
	if err != nil {
		switch ClassifyReadError(out + " " + err.Error()) {
		case ProblemNotRunning:
			fact.Daemon = "not running"
		case ProblemPermission:
			fact.Daemon = "refused this user"
		}
		return fact
	}
	status, err := ParseStatus(out)
	if err != nil {
		return fact
	}
	fact.Daemon = "running"
	fact.BackendState = status.Node.BackendState
	return fact
}
