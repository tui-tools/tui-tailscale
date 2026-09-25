package main

import (
	"context"
	"fmt"
	"io"
	"regexp"

	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/report"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// runReport prints the block a bug report needs and exits. Every tool in the
// family has this function, and it is worth keeping it recognisable.
//
// Everything generic — the kit version, the distribution, the kernel, the
// terminal, where the binary came from — is collected by the kit, so the whole
// family answers --report in the same shape. What this tool adds is the part
// only it knows: the client's version, whether tailscaled answers, and the
// state it reports; headscale's version, and what systemd says about its unit.
//
// PRIVACY: it never prints the login server, an address, the node's name or
// the tailnet's, and nothing of headscale's configuration, users or nodes. It reads nothing privileged, and it runs before the backend
// is required, so a host with nothing installed still produces a usable block.
func runReport(cfg config.Config, opts options, out io.Writer) error {
	palette, _ := theme.ResolvePalette()

	// The same probe the header uses. There is one version probe in a tool and
	// this is it — a report that probed separately could disagree with the
	// header the user is looking at.
	probed := probeCompat(context.Background(), opts.demo)
	backendCompat := compatFor(probed, backendName)
	hsCompat := compatFor(probed, backendHeadscale)

	var backendError string
	if _, err := pickBackend(cfg, opts); err != nil {
		backendError = err.Error()
	}

	info := report.Info{
		Tool:           toolName,
		Version:        version,
		Backend:        backendName,
		BackendVersion: backendCompat.Version,
		BackendDetail:  backendCompat.Detail,
		Demo:           opts.demo,
		Sudo:           cfg.String(config.KeySudo, ""),
		Theme:          palette.Name,
	}
	if opts.demo {
		// The fake imitates the real client; the state on screen is not this
		// host's, so no host fact is probed under --demo.
		info.Backend = "demo"
		info.Extra = append(info.Extra,
			report.Field{Key: "demo backend", Value: backendName + " + " + backendHeadscale})
	} else {
		facts := tailscale.HostFacts(context.Background())
		info.Extra = append(info.Extra,
			report.Field{Key: "tailscale", Value: installedLine(facts.Installed, backendCompat.Version)},
			report.Field{Key: "tailscaled", Value: facts.Daemon},
		)
		if facts.BackendState != "" {
			info.Extra = append(info.Extra,
				report.Field{Key: "backend state", Value: facts.BackendState})
		}
		hsFacts := headscale.HostFacts(context.Background())
		info.Extra = append(info.Extra, report.Field{Key: "headscale",
			Value: installedLine(hsFacts.Present, hsCompat.Version)})
		if hsFacts.Service != "" {
			info.Extra = append(info.Extra,
				report.Field{Key: "headscale unit", Value: hsFacts.Service})
		}
	}
	if backendError != "" {
		info.Extra = append(info.Extra, report.Field{
			Key: "backend error", Value: scrubHome(backendError),
		})
	}

	_, err := io.WriteString(out, report.Render(info))
	return err
}

// installedLine renders the client's presence as one honest fact.
func installedLine(installed bool, version string) string {
	switch {
	case version != "":
		return version
	case installed:
		return "installed, version unknown"
	}
	return "not installed"
}

// homePath matches a path under a home directory, the one thing a
// backend-build error could otherwise carry that names its user.
var homePath = regexp.MustCompile(`(/home|/root)(/[^\s:]*)?`)

// scrubHome replaces such a path with the placeholder the kit uses for the
// same reason. A value a tool hands to report.Extra is its own
// responsibility: the kit scrubs what it collected itself.
func scrubHome(s string) string {
	return homePath.ReplaceAllString(s, "~elsewhere~")
}

// reportUsage is the flag's one-line help, kept here next to what it prints.
var reportUsage = fmt.Sprintf(
	"print the versions and machine facts a bug report needs, then exit "+
		"(no UI, no privileges, no address, name or login server: paste it into a %s issue)",
	toolName)
