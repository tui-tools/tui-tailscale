package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// The shown-once pre-auth key (issue #30). headscale prints a created key
// once and this tool shows it once: on a notice of its own, on a line of its
// own — flush left, unframed, never wrapped and never cut, like the login
// URL. A terminal narrower than the key gets no partial key (a cut key is one
// headscale rejects, and nothing marks the cut): the notice says how wide the
// window has to be, and the key appears whole as soon as it is. The one other
// way out is explicit: w writes the key, once, to a root-only file under
// /run, previewed and confirmed. The key is never put anywhere else: not the
// status line, not --check, not the list, which keeps headscale's prefix.

// openShownOnceKey shows a key headscale just created.
func (a *app) openShownOnceKey(key string) {
	a.notice = notice{
		title: "Pre-auth key, shown once",
		body: "Copy the key below now: headscale shows it once and this tool does " +
			"not store it, so closing this notice forgets it. The keys list keeps its " +
			"prefix only.",
		copyable: key,
		secret:   true,
	}
	a.mode = modeNotice
	a.setStatus(ui.StatusWarn, "pre-auth key created · shown once in the dialog, never stored")
}

// openWriteShownOnceKey previews the write of the shown key to a root-only
// file, the operator's explicit alternative to copying it off the screen.
func (a *app) openWriteShownOnceKey() tea.Cmd {
	path := headscale.ShownOnceKeyPath(time.Now())
	cmd, err := headscale.BuildWriteShownOnceKey(a.notice.copyable, path)
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	a.keyNotice = a.notice
	a.notice = notice{}
	body := "The key is written once to " + path + ", owned by root with mode 600, " +
		"through the command's standard input: it is not on the command line, in the " +
		"process list or in this preview. " + headscale.ShownOnceKeyDir + " is in /run, " +
		"a tmpfs, so the file is gone at the next boot at the latest. Read it with " +
		"`sudo cat " + path + "`, then delete it (`sudo rm " + path + "`). The key is " +
		"not shown again once it is written."
	return a.openConfirmWith(body, cmd, nil)
}

// isShownOnceKeyWrite reports whether cmd is the write of a shown-once key.
func isShownOnceKeyWrite(cmd runner.Command) bool {
	n := len(cmd.Argv)
	return n >= 2 && cmd.Argv[0] == "install" && cmd.Argv[n-2] == "/dev/stdin" &&
		len(cmd.Argv[n-1]) > len(headscale.ShownOnceKeyDir) &&
		cmd.Argv[n-1][:len(headscale.ShownOnceKeyDir)+1] == headscale.ShownOnceKeyDir+"/"
}

// wroteShownOnceKey takes the write's result: the key is forgotten once the
// file holds it, and its notice comes back when the write failed.
func (a *app) wroteShownOnceKey(msg ranMsg) tea.Cmd {
	if msg.err != nil {
		a.notice, a.keyNotice = a.keyNotice, notice{}
		a.mode = modeNotice
		a.setStatus(ui.StatusError, "not written: "+runner.FirstLine(msg.err.Error()))
		return nil
	}
	a.keyNotice = notice{}
	path := msg.cmd.Argv[len(msg.cmd.Argv)-1]
	a.setStatus(ui.StatusOK, "pre-auth key written to "+path+
		" (root only, mode 600) · sudo cat it, then delete it")
	return nil
}
