package main

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// This file is the private-CA step of j (issue #15): before a join to an
// https login server, its certificate is checked from this machine against
// the system trust store; when its issuer is unknown here, the join says so
// before tailscale up can fail on it, and offers to trust the CA — one tui-cert
// keeps here, or a file picked — previewed per distribution, and then goes on.

// joinTLSMsg carries the certificate check of the login server.
type joinTLSMsg struct {
	server string
	result tailscale.TLSCheck
	detail string
}

// checkJoinTLS reads the login server's certificate in the background.
func (a *app) checkJoinTLS(server string) tea.Cmd {
	cmd, err := tailscale.BuildTLSCheck(server)
	if err != nil {
		a.askJoinServer(server, err.Error())
		return nil
	}
	a.setStatusf(ui.StatusInfo, "checking the certificate of %s…", tailscale.URLHost(server))
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := backend.Run(ctx, cmd)
		detail := ""
		if err != nil {
			detail = runner.FirstLine(err.Error())
			if first := runner.StatusLine(out); first != "" {
				detail = first
			}
		}
		return joinTLSMsg{server: server, result: tailscale.ClassifyTLSCheck(out, err),
			detail: detail}
	}
}

// tookJoinTLS goes on with the join, or stops at the CA step.
func (a *app) tookJoinTLS(msg joinTLSMsg) tea.Cmd {
	if a.join.server != msg.server || a.mode != modeBrowse {
		// The form was left, or another dialog took the screen.
		return nil
	}
	host := tailscale.URLHost(msg.server)
	switch msg.result {
	case tailscale.TLSVerified:
		a.setStatusf(ui.StatusOK, "the certificate of %s verifies", host)
		a.askJoinKey()
	case tailscale.TLSUntrusted:
		// The CAs tui-cert keeps here come first, by name; the file picker
		// is the way to a CA certificate copied over from elsewhere.
		a.joinTLSDetail = msg.detail
		a.setStatusf(ui.StatusWarn, "the certificate of %s does not verify here · "+
			"looking for its CA…", host)
		return a.readLocalPKI(pkiForJoin)
	case tailscale.TLSMismatch:
		a.setStatusf(ui.StatusWarn, "the certificate of %s is not issued for that name (%s): "+
			"tailscale will refuse it, and trusting a CA does not change that", host, msg.detail)
		a.askJoinKey()
	default:
		a.setStatusf(ui.StatusWarn, "could not reach %s from here (%s): the join may fail",
			host, msg.detail)
		a.askJoinKey()
	}
	return nil
}

// askJoinCA asks for the CA certificate to trust, in the file picker.
func (a *app) askJoinCA(value string, problem error, detail string) {
	host := tailscale.URLHost(a.join.server)
	help := "The certificate of " + host + " does not verify against this machine's trust " +
		"store"
	if detail != "" {
		help += " (" + detail + ")"
	}
	help += ", so tailscale up would fail. A control plane on a private tailnet is usually " +
		"served with a certificate from a local CA: pick that CA's certificate (PEM; " +
		"tui-cert's x on the control plane copies it to " + localCADir + ") and the " +
		"next dialog previews adding it to the trust store. esc stops the join."
	if hint := a.pkiHint(false); hint != "" {
		help += "\n\n" + hint
	}
	a.openFilePicker(inputJoinCA, "Trust the CA of "+host+"?", help,
		pickerStart(value, a.caStart()), certExtensions, problem)
}

// tookJoinCA builds the trust step for the typed CA.
func (a *app) tookJoinCA(value string) tea.Cmd {
	if !tailscale.ValidCAPath(value) {
		a.askJoinCA(value, fmt.Errorf("not an absolute, plain path to a certificate: %q", value), "")
		return nil
	}
	a.openPlan(tailscale.BuildCommand(tailscale.Request{
		Action: tailscale.ActionTrustCA, CAPath: value,
		CAName: tailscale.CAName(a.join.server), Distro: a.state.Distro}))
	return nil
}
