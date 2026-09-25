// Command tui-tailscale manages both ends of a self-hosted Tailscale from the
// terminal. The node end is this host as a member of a tailnet, through the
// `tailscale` client, whichever control plane it answers to — Tailscale's own
// or a self-hosted Headscale: the node (state, login server, addresses, the
// settings that decide what it routes) and the peers it sees, joined,
// reconfigured, disconnected and logged out. The control-plane end is a
// Headscale on this host: its users, nodes and pre-auth keys, the server and
// identity-provider settings in its config.yaml, the unit that runs it and
// the ownership of its files.
//
// It manages as well as reads. Every change is shown as the exact command
// line first and applied only after it is confirmed. A process is started
// from exactly two places, internal/tailscale and internal/headscale — one
// per backend — so the command the dialog showed is the command that runs.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// toolName is the binary name, which is also the configuration directory:
// /etc/tui-tailscale/config.toml and ~/.config/tui-tailscale/config.toml.
const toolName = "tui-tailscale"

// version is stamped by the release build (-ldflags "-X main.version=…").
var version = "dev"

// defaults declares the configuration keys the tool understands. Only these
// are read from the environment (TUI_TAILSCALE_*), so an unrelated variable
// can never leak into the configuration.
func defaults() map[string]string {
	return map[string]string{
		config.KeySudo:  "sudo -n",
		config.KeyTheme: "",
	}
}

// The --demo cases beyond the default sample tailnet, each a real situation
// the screens have to handle, reachable without the machine that has it.
const (
	// demoNodeOnly is a machine that is only a node: no headscale here, the
	// node joined to a control plane elsewhere (issue #20).
	demoNodeOnly = "node-only"
	// demoPartialReset is a host after a partial reset: tailscale installed
	// with tailscaled stopped and disabled, headscale installed with its
	// config.yaml and state directory deleted (issue #18).
	demoPartialReset = "partial-reset"
)

// demoCases lists the accepted --demo values, for the usage and the error.
var demoCases = []string{demoNodeOnly, demoPartialReset}

// demoFlag is --demo: bare, it runs the sample tailnet; --demo=<case> runs
// one of demoCases instead.
type demoFlag struct {
	on   *bool
	name *string
}

// String renders the flag's current value for the usage text.
func (d demoFlag) String() string {
	if d.name == nil {
		return ""
	}
	return *d.name
}

// Set accepts the bare form ("true", from `--demo`) and a case name.
func (d demoFlag) Set(value string) error {
	switch value {
	case "", "true":
		*d.on, *d.name = true, ""
		return nil
	case "false":
		*d.on, *d.name = false, ""
		return nil
	}
	for _, c := range demoCases {
		if value == c {
			*d.on, *d.name = true, value
			return nil
		}
	}
	return fmt.Errorf("unknown demo case %q: use --demo, or --demo=%s", value,
		strings.Join(demoCases, ", --demo="))
}

// IsBoolFlag lets `--demo` stand alone.
func (d demoFlag) IsBoolFlag() bool { return true }

// options holds the parsed command line.
type options struct {
	demo bool
	// demoCase is the --demo=<case> picked, empty for the sample tailnet.
	demoCase string
	check    bool
	report   bool
	// probeIssuer adds the OIDC issuer's reachability to --check, the one
	// network request --check can make.
	probeIssuer bool
	themePath   string
	sudo        string
	showVersion bool
	// sudoSet records whether -sudo was passed, so `--sudo ""` can disable
	// escalation instead of reading as "not given".
	sudoSet bool
}

// parseFlags defines and reads the command line.
func parseFlags(args []string, out *os.File) (options, error) {
	var opts options
	fs := flag.NewFlagSet(toolName, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Var(demoFlag{on: &opts.demo, name: &opts.demoCase}, "demo",
		"run against a fake node and control plane on a sample tailnet, without reading this "+
			"host; --demo="+demoNodeOnly+" is a machine that is only a node of a "+
			"control plane elsewhere, --demo="+demoPartialReset+" one with tailscaled "+
			"stopped and headscale's configuration deleted")
	fs.BoolVar(&opts.check, "check", false,
		"read the node and the control plane once, print the summary as JSON and exit "+
			"(no UI, nothing is changed, no address, name or URL of this host)")
	fs.BoolVar(&opts.probeIssuer, "probe-issuer", false,
		"with --check: fetch the OIDC issuer's discovery document from this machine and "+
			"report whether it answered (the only network request --check makes)")
	fs.BoolVar(&opts.report, "report", false, reportUsage)
	fs.StringVar(&opts.themePath, "theme", "",
		"path to an Omarchy-style colors.toml (overrides the config file)")
	fs.StringVar(&opts.sudo, "sudo", "",
		"privilege escalation prefix, e.g. \"sudo -n\" or \"\" to disable")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "tui-tailscale — self-hosted Tailscale from the terminal: "+
			"the control plane and this node\n\n"+
			"Usage:\n  tui-tailscale [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(out, "\nConfiguration is read from %s, then %s, "+
			"then TUI_TAILSCALE_* in the environment.\n",
			config.SystemPathFor(toolName), config.UserPathFor(toolName))
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "sudo" {
			opts.sudoSet = true
		}
	})
	return opts, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, toolName+":", err)
		os.Exit(1)
	}
}

// run wires the configuration, the backend and the Bubble Tea program. Every
// tool in the family has this function, and it is worth keeping it recognisable.
func run(args []string) error {
	opts, err := parseFlags(args, os.Stdout)
	if err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if opts.showVersion {
		fmt.Println(toolName, version)
		return nil
	}

	cfg, err := config.Load(config.Options{Tool: toolName, Defaults: defaults()})
	if err != nil {
		return err
	}
	applyOverrides(&cfg, opts)

	// The configured theme is handed to the kit through the same variable the
	// user could set by hand, so precedence stays in one place. It is set
	// before the backend is built so --report can name the theme the UI would
	// have used even on a machine where no backend can be.
	if path := cfg.Theme(); path != "" {
		if err := os.Setenv("TUI_THEME", path); err != nil {
			return err
		}
	}

	// --report is the non-interactive path that must work everywhere. It reads
	// nothing privileged and comes before the backend is required: a machine
	// without tailscale still has to be able to file a usable bug report.
	if opts.report {
		return runReport(cfg, opts, os.Stdout)
	}

	// The client's version is probed once, at startup, and shown in the
	// header. A missing binary is an empty result rather than an error.
	backendCompat := probeCompat(context.Background(), opts.demo)

	backend, err := pickBackend(cfg, opts)
	if err != nil {
		return err
	}
	hs := pickControlPlane(cfg, opts)
	profiles := pickProfiles(opts)

	// --check is the other non-interactive path: it reads once and prints, and
	// never starts a terminal program.
	if opts.check {
		return runCheckWith(context.Background(), backend, hs, backendCompat, os.Stdout,
			checkOptions{probeIssuer: opts.probeIssuer, joinProfiles: profiles.names()})
	}

	model := newApp(backend, hs, theme.New(), backendCompat)
	model.profiles = profiles
	if opts.demo {
		// The file picker lists a made-up tree under --demo.
		model.files = demoFiles()
	}
	// After a change the versions are probed again, so an install shows its
	// version in the header without a restart.
	model.probe = func() []compat.Result { return probeCompat(context.Background(), opts.demo) }
	program := tea.NewProgram(model, tea.WithAltScreen())
	_, err = program.Run()
	return err
}

// applyOverrides folds the command line into the configuration, which is the
// last and highest-precedence layer.
func applyOverrides(cfg *config.Config, opts options) {
	if opts.themePath != "" {
		cfg.Set(config.KeyTheme, opts.themePath)
	}
	// An explicitly empty -sudo disables escalation, so the flag is applied
	// whenever it was passed, empty value included.
	if opts.sudoSet {
		cfg.Set(config.KeySudo, opts.sudo)
	}
}

// pickBackend returns the demo node backend or the real one.
func pickBackend(cfg config.Config, opts options) (tailscale.Backend, error) {
	if opts.demo {
		fake := tailscale.NewFake()
		// A browser login in the demo completes by itself a few reads after
		// it starts, so the node screen shows it flip without a browser.
		fake.CompleteLoginAfter(4)
		// Once "confirmed", it takes tailscaled a few reads to come up, as it
		// does on a real machine: the node screen waits instead of calling
		// the login expired (issue #20).
		fake.SetConfirmPhases(tailscale.StateNeedsLogin, tailscale.StateNoState,
			tailscale.StateStarting)
		if opts.demoCase == demoPartialReset {
			fake.SetDaemonStopped("disabled")
		}
		return fake, nil
	}
	return tailscale.New(cfg.SudoPrefix())
}

// pickControlPlane returns the demo control plane or the real one. The real
// one cannot fail: a host without headscale is a host the control-plane
// screens explain how to install it on.
func pickControlPlane(cfg config.Config, opts options) headscale.Backend {
	if opts.demo {
		fake := headscale.NewFake()
		switch opts.demoCase {
		case demoNodeOnly:
			fake.SetAbsent()
		case demoPartialReset:
			fake.SetConfigMissing()
		}
		return fake
	}
	return headscale.New(cfg.SudoPrefix())
}

// pickProfiles returns the demo's join profiles or the ones saved on this
// machine: the machine-wide config file's and this user's.
func pickProfiles(opts options) profileStore {
	if opts.demo {
		return demoProfileStore()
	}
	return loadProfileStore(config.SystemPathFor(toolName), config.UserPathFor(toolName),
		os.Geteuid() == 0)
}
