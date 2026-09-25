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

// options holds the parsed command line.
type options struct {
	demo   bool
	check  bool
	report bool
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
	fs.BoolVar(&opts.demo, "demo", false,
		"run against a fake node and control plane on a sample tailnet, without reading this host")
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
		return fake, nil
	}
	return tailscale.New(cfg.SudoPrefix())
}

// pickControlPlane returns the demo control plane or the real one. The real
// one cannot fail: a host without headscale is a host the control-plane
// screens explain how to install it on.
func pickControlPlane(cfg config.Config, opts options) headscale.Backend {
	if opts.demo {
		return headscale.NewFake()
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
