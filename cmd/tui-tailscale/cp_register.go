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
//
// With OIDC configured, R is the one way around the identity provider's
// policy (issue #26): a registration whose browser login the policy refused,
// or one whose login is at the provider now, is shown for what it is and R
// refuses it; any other registration R finishes only after a danger confirm
// that says it skips the provider's allow lists.

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

// registrables are the pending registrations R may finish, and why not for
// the first one it may not.
func (a *app) registrables() ([]headscale.Registration, string) {
	var out []headscale.Registration
	why := ""
	for _, r := range a.pendingRegistrations() {
		ok, reason := r.Registrable()
		if ok {
			out = append(out, r)
		} else if why == "" {
			why = reason
		}
	}
	return out, why
}

// registrationLines are the nodes screen's lines about the waiting nodes: how
// many, the newest with its age, and its register URL on a line of its own,
// flush left and unstyled, so a terminal selection copies only the URL. The
// registrations the identity provider refused get a line of their own.
func (a *app) registrationLines() []string {
	var waiting, refused []headscale.Registration
	for _, r := range a.pendingRegistrations() {
		if r.Refused {
			refused = append(refused, r)
		} else {
			waiting = append(waiting, r)
		}
	}
	var lines []string
	if len(waiting) > 0 {
		newest := waiting[0]
		line := "waiting to register: " + pluralCount(len(waiting), "node") + " · newest " +
			newest.AuthID + registrationAge(newest)
		switch {
		case newest.AtIdP:
			line += " · logging in at the identity provider, its browser finishes it:"
		case a.hsState.OIDCEnabled():
			line += " · open this URL in a browser to log in through the identity provider " +
				"(R registers it without the provider's policy):"
		default:
			line += " · R registers it as a user, or open this URL in a browser:"
		}
		lines = append(lines, a.theme.Warn.Render(ui.Truncate(line, a.width)))
		if url := headscale.RegisterURL(a.hsState.ControlPlane.ServerURL, newest.AuthID); url != "" {
			lines = append(lines, url)
		}
	}
	if len(refused) > 0 {
		newest := refused[0]
		// The reason leads: on a narrow terminal it is what must survive.
		line := "refused by the identity provider's policy (" + newest.RefusedReason() + "): " +
			pluralCount(len(refused), "node") + " · R will not register it · newest " +
			newest.AuthID + registrationAge(newest)
		lines = append(lines, a.theme.Danger.Render(ui.Truncate(line, a.width)))
	}
	return lines
}

// registrationAge is a registration's age, in parentheses, when the journal gave a time.
func registrationAge(r headscale.Registration) string {
	if r.Seen.IsZero() {
		return ""
	}
	return " (" + durationUnit(time.Since(r.Seen)) + " ago)"
}

// startRegister is R: pick the registration when there is more than one,
// then the user it registers as. A registration the identity provider
// refused, or one logging in there, is not offered.
func (a *app) startRegister() tea.Cmd {
	if !a.headscaleAnswers() {
		return nil
	}
	if len(a.pendingRegistrations()) == 0 {
		a.setStatus(ui.StatusInfo, "no node is waiting to register (one shows up here when a "+
			"client runs tailscale up --login-server without a key)")
		return nil
	}
	regs, why := a.registrables()
	if len(regs) == 0 {
		a.setStatus(ui.StatusWarn, why)
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
	regs, _ := a.registrables()
	for _, r := range regs {
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

// tookRegisterUser opens the confirm of the registration. On a control plane
// with OIDC it is a danger confirm that names what it skips: the identity
// provider's login, and with it every allow list config.yaml sets.
func (a *app) tookRegisterUser(user string) tea.Cmd {
	id := a.registerDraft
	a.registerDraft = ""
	for _, r := range a.pendingRegistrations() {
		if ok, why := r.Registrable(); r.AuthID == id && !ok {
			// Refused or at the provider since the list was read.
			a.setStatus(ui.StatusWarn, why)
			return nil
		}
	}
	cmd, err := headscale.BuildRegisterNode(id, user, headscale.HasAuthRegister(a.hsCompat.Version))
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	body := "The node waiting under " + id + " joins the tailnet as " + user +
		", without a browser login: the same thing opening its /register URL and logging " +
		"in would do. It keeps the settings its tailscale up asked for; advertised routes " +
		"still need r."
	oidc := a.hsState.OIDCEnabled()
	if oidc {
		body = "This registers the node without the identity provider's policy: nobody logs " +
			"in at the provider, and allowed_groups, allowed_domains and allowed_users are " +
			"not checked. The node waiting under " + id + " joins the tailnet as " + user +
			". To keep the policy, open its /register URL in a browser instead. It keeps the " +
			"settings its tailscale up asked for; advertised routes still need r."
	}
	open := a.openConfirmWith(body, cmd, nil)
	if a.mode == modeConfirm {
		if oidc {
			a.confirm.Title += ", bypassing the identity provider"
			a.confirm.Danger = true
		}
		a.after = func(string) tea.Cmd {
			a.registered[id] = true
			a.setStatus(ui.StatusOK, "registered "+id+" as "+user)
			return nil
		}
	}
	return open
}
