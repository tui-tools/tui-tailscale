package headscale

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/tui-tools/tui-kit/runner"
)

// Process is a family tool, prepared but not started. Its method set is
// Bubble Tea's ExecCommand, so the UI hands it to tea.Exec — which suspends
// the program, gives the terminal to the child and restores the screen when
// it exits — without importing os/exec itself: the exec boundary stays in
// this package, the way the tui-tools launcher keeps it in its own.
type Process interface {
	Run() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
	// String is the command line, for the status line shown before the
	// screen is handed over.
	String() string
}

// FirewallTool is the family tool that opens ports.
const FirewallTool = "tui-firewall"

// LaunchFirewall prepares the hand-over to tui-firewall: the binary found at
// one of its known paths, with no argument at all. It is not previewed as a
// change, because it is not one: tui-firewall previews and confirms whatever
// it changes.
func (r *Real) LaunchFirewall() (Process, error) {
	for _, path := range searchPaths[FirewallTool] {
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			// G204: a fixed, absolute path from the table, no argument.
			return &process{cmd: exec.Command(path)}, nil //nolint:gosec // fixed path, no arguments
		}
	}
	return nil, fmt.Errorf("%s is not installed (it comes from pkgs.tui.tools)", FirewallTool)
}

// process adapts an exec.Cmd to Process.
type process struct{ cmd *exec.Cmd }

// Run starts the tool and waits for it.
func (p *process) Run() error { return p.cmd.Run() }

// SetStdin gives the child the terminal's input.
func (p *process) SetStdin(r io.Reader) {
	if p.cmd.Stdin == nil {
		p.cmd.Stdin = r
	}
}

// SetStdout gives the child the terminal's output.
func (p *process) SetStdout(w io.Writer) {
	if p.cmd.Stdout == nil {
		p.cmd.Stdout = w
	}
}

// SetStderr gives the child the terminal's error stream.
func (p *process) SetStderr(w io.Writer) {
	if p.cmd.Stderr == nil {
		p.cmd.Stderr = w
	}
}

// String is the command line.
func (p *process) String() string { return strings.Join(p.cmd.Args, " ") }

// readFirewall reads the host firewall's input chain for the readiness ports
// step: tui-firewall's own --check when it is installed, then nftables, then
// iptables. Every read escalates, since rule sets are root's to read, and
// none of them changes anything.
func (r *Real) readFirewall(ctx context.Context) Firewall {
	launchable := runner.Available(FirewallTool, searchPaths[FirewallTool]...)
	reads := []struct {
		bin   string
		argv  []string
		parse func(string) (Firewall, bool)
	}{
		{FirewallTool, []string{FirewallTool, "--check"}, ParseTuiFirewallCheck},
		{"nft", []string{"nft", "-j", "list", "ruleset"}, ParseNftRuleset},
		{"iptables", []string{"iptables", "-S", "INPUT"}, ParseIptablesInput},
	}
	var lastErr string
	for _, read := range reads {
		run, err := r.runnerFor(read.bin, true)
		if err != nil {
			continue
		}
		out, err := run.Read(ctx, read.argv...)
		if err != nil {
			lastErr = runner.FirstLine(err.Error())
			continue
		}
		if fw, ok := read.parse(out); ok {
			fw.Launchable = launchable
			return fw
		}
	}
	return Firewall{Error: lastErr, Launchable: launchable}
}
