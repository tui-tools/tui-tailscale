package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// This file is the pending registrations of the guided setup (issue #12): a
// node that ran `tailscale up --login-server …` without a key waits in
// headscale's registration cache, invisible to `headscale nodes list`. The
// nodes screen shows it with the URL that finishes it in a browser, and `R`
// finishes it from here as a user the operator picks.

// pendingRegistrations are the registrations still waiting, minus the ones
// this session already registered: the journal keeps logging a registration
// the CLI finished as started, and only a browser confirm is logged as done.
func (a *app) pendingRegistrations() []headscale.Registration {
	var out []headscale.Registration
	for _, r := range a.hsState.Registrations {
		if !a.registered[r.AuthID] {
			out = append(out, r)
		}
	}
	return out
}

// registrationLines are the nodes screen's lines about the waiting nodes: how
// many, the newest with its age, and its register URL on a line of its own,
// flush left and unstyled, so a terminal selection copies only the URL.
func (a *app) registrationLines() []string {
	regs := a.pendingRegistrations()
	if len(regs) == 0 {
		return nil
	}
	newest := regs[0]
	line := "waiting to register: " + pluralCount(len(regs), "node") + " · newest " +
		newest.AuthID
	if !newest.Seen.IsZero() {
		line += " (" + durationUnit(time.Since(newest.Seen)) + " ago)"
	}
	line += " · R registers it as a user, or open this URL in a browser:"
	lines := []string{a.theme.Warn.Render(ui.Truncate(line, a.width))}
	if url := headscale.RegisterURL(a.hsState.ControlPlane.ServerURL, newest.AuthID); url != "" {
		lines = append(lines, url)
	}
	return lines
}

// startRegister is R: pick the registration when there is more than one,
// then the user it registers as.
func (a *app) startRegister() tea.Cmd {
	if !a.headscaleAnswers() {
		return nil
	}
	regs := a.pendingRegistrations()
	if len(regs) == 0 {
		a.setStatus(ui.StatusInfo, "no node is waiting to register (one shows up here when a "+
			"client runs tailscale up --login-server without a key)")
		return nil
	}
	if len(a.hsState.Users) == 0 {
		a.setStatus(ui.StatusWarn, "no user to register it as · n on the users screen creates "+
			"one; with OIDC, opening its /register URL creates the user at the login")
		return nil
	}
	if len(regs) == 1 {
		a.registerDraft = regs[0].AuthID
		a.openRegisterUserPicker()
		return nil
	}
	options := make([]string, 0, len(regs))
	for _, r := range regs {
		label := r.AuthID
		if !r.Seen.IsZero() {
			label += "  " + durationUnit(time.Since(r.Seen)) + " ago"
		}
		options = append(options, label)
	}
	a.picker = ui.NewPicker("Register which waiting node?", options, options[0])
	a.pickerPurpose = pickerRegistration
	a.mode = modePicker
	return nil
}

// tookRegistration records the registration and asks for the user.
func (a *app) tookRegistration(choice string) tea.Cmd {
	for _, r := range a.pendingRegistrations() {
		if len(choice) >= len(r.AuthID) && choice[:len(r.AuthID)] == r.AuthID {
			a.registerDraft = r.AuthID
		}
	}
	if a.registerDraft == "" {
		a.setStatus(ui.StatusError, "not a waiting registration: "+choice)
		return nil
	}
	a.openRegisterUserPicker()
	return nil
}

// openRegisterUserPicker asks which user the node belongs to.
func (a *app) openRegisterUserPicker() {
	options := make([]string, 0, len(a.hsState.Users))
	for _, u := range a.hsState.Users {
		options = append(options, u.Name)
	}
	a.picker = ui.NewPicker("Register "+a.registerDraft+" as which user?", options, options[0])
	a.pickerPurpose = pickerRegisterUser
	a.mode = modePicker
}

// tookRegisterUser opens the confirm of the registration.
func (a *app) tookRegisterUser(user string) tea.Cmd {
	id := a.registerDraft
	a.registerDraft = ""
	cmd, err := headscale.BuildRegisterNode(id, user, headscale.HasAuthRegister(a.hsCompat.Version))
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	open := a.openConfirmWith("The node waiting under "+id+" joins the tailnet as "+user+
		", without a browser login: the same thing opening its /register URL and logging "+
		"in would do. It keeps the settings its tailscale up asked for; advertised routes "+
		"still need r.", cmd, nil)
	if a.mode == modeConfirm {
		a.after = func(string) tea.Cmd {
			a.registered[id] = true
			a.setStatus(ui.StatusOK, "registered "+id+" as "+user)
			return nil
		}
	}
	return open
}
