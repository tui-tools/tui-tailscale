package main

import (
	"context"
	"os"
	"path"
	"strings"
	"testing/fstest"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
	"github.com/tui-tools/tui-tailscale/internal/tailscale"
)

// This file holds the two ways a path is chosen (issue #15): the pairs and
// CAs tui-cert reports, offered by name first, and the kit's file picker for
// anything else. The certificate, key and CA paths used to be typed; the
// validation that followed the typing still follows the picking.

// otherFile is the pick lists' way to the file picker.
const otherFile = "other file…"

// Extensions the pickers list: a certificate and a CA are PEM files under
// either name, and a key is .key or .pem.
var (
	certExtensions = []string{".pem", ".crt"}
	keyExtensions  = []string{".pem", ".key"}
)

// pkiPurpose tells which step asked for tui-cert's report.
type pkiPurpose int

const (
	pkiForServer pkiPurpose = iota
	pkiForJoin
)

// localPKIMsg carries tui-cert's report to the step that asked for it.
type localPKIMsg struct {
	purpose pkiPurpose
	pki     headscale.LocalPKI
}

// readLocalPKI reads tui-cert's report in the background.
func (a *app) readLocalPKI(purpose pkiPurpose) tea.Cmd {
	hs := a.hs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
		defer cancel()
		return localPKIMsg{purpose: purpose, pki: hs.ReadLocalPKI(ctx)}
	}
}

// tookLocalPKI goes on with the step that asked for tui-cert's report.
func (a *app) tookLocalPKI(msg localPKIMsg) tea.Cmd {
	if a.mode != modeBrowse {
		// Another dialog took the screen while tui-cert was read.
		return nil
	}
	a.pki = msg.pki
	switch msg.purpose {
	case pkiForServer:
		if a.cpDraft.transport != headscale.TransportOwnCert {
			return nil
		}
		a.offerIssuedPairs()
	case pkiForJoin:
		if a.join.server == "" {
			return nil
		}
		a.offerLocalCAs()
	}
	return nil
}

// pkiHint is the line the file picker's help ends with: where a local CA
// comes from, or why the one here could not be read.
func (a *app) pkiHint(forServer bool) string {
	switch {
	case !a.pki.Installed:
		return headscale.CertTool + " makes a local CA: " + headscale.CertToolURL
	case a.pki.Error != "":
		return headscale.CertTool + " could not be read: " + a.pki.Error
	case forServer && len(a.pki.Pairs) == 0:
		return headscale.CertTool + " has issued no pair here yet: its e issues one " +
			"for this server's name or IP, with owner headscale."
	case !forServer && len(a.pki.CAs) == 0:
		return headscale.CertTool + " keeps no local CA on this machine: copy the CA " +
			"certificate over (tui-cert's x shows the command) and pick it here."
	}
	return ""
}

// offerIssuedPairs lists the pairs tui-cert issued, or goes straight to the
// file picker when there is none.
func (a *app) offerIssuedPairs() {
	cp := a.hsState.ControlPlane
	if len(a.pki.Pairs) == 0 {
		a.askCertPath(cp.TLSCertPath, nil)
		return
	}
	a.pairChoices = map[string]headscale.IssuedPair{}
	options := make([]string, 0, len(a.pki.Pairs)+1)
	current := ""
	for _, p := range a.pki.Pairs {
		label := p.Label()
		options = append(options, label)
		a.pairChoices[label] = p
		if p.CertPath == cp.TLSCertPath {
			current = label
		}
	}
	options = append(options, otherFile)
	a.picker = ui.NewPicker("Own certificate — a pair tui-cert issued, or another file",
		options, current)
	a.pickerPurpose = pickerIssuedPair
	a.mode = modePicker
}

// tookIssuedPair takes a pair tui-cert issued, both paths at once, and checks
// it like a picked one; "other file…" opens the picker.
func (a *app) tookIssuedPair(choice string) tea.Cmd {
	pair, ok := a.pairChoices[choice]
	a.pairChoices = nil
	if !ok {
		a.askCertPath(a.hsState.ControlPlane.TLSCertPath, nil)
		return nil
	}
	a.cpDraft.certPath, a.cpDraft.keyPath = pair.CertPath, pair.KeyPath
	return a.checkTLSFiles()
}

// offerLocalCAs lists the CAs tui-cert keeps on this machine, or goes straight
// to the file picker when there is none.
func (a *app) offerLocalCAs() {
	if len(a.pki.CAs) == 0 {
		a.askJoinCA("", nil, a.joinTLSDetail)
		return
	}
	a.caChoices = map[string]string{}
	options := make([]string, 0, len(a.pki.CAs)+1)
	for _, ca := range a.pki.CAs {
		label := ca.Label()
		options = append(options, label)
		a.caChoices[label] = ca.CertPath
	}
	options = append(options, otherFile)
	a.picker = ui.NewPicker("The certificate of "+tailscale.URLHost(a.join.server)+" does not verify "+
		"here — trust which CA?", options, "")
	a.pickerPurpose = pickerJoinCA
	a.mode = modePicker
}

// tookLocalCA previews trusting the chosen CA; "other file…" opens the
// picker.
func (a *app) tookLocalCA(choice string) tea.Cmd {
	certPath, ok := a.caChoices[choice]
	a.caChoices = nil
	if !ok {
		a.askJoinCA("", nil, a.joinTLSDetail)
		return nil
	}
	return a.tookJoinCA(certPath)
}

// openFilePicker opens the kit's file picker for one purpose, with the reason
// the last answer was refused on top of the help when there is one.
func (a *app) openFilePicker(purpose inputPurpose, title, help, start string,
	extensions []string, problem error) {
	if problem != nil {
		a.setStatus(ui.StatusError, problem.Error())
		help = "✗ " + problem.Error() + "\n\n" + help
	}
	a.filePicker = ui.NewFilePicker(ui.FilePickerOptions{
		Title: title, Help: help, Start: start, Extensions: extensions, FS: a.files})
	a.inputPurpose = purpose
	a.mode = modeFilePicker
}

// handleFilePicker resolves the file picker. What was picked goes through the
// same step a typed path went through, validation included.
func (a *app) handleFilePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd, _ := a.filePicker.Update(msg)
	if !a.filePicker.Done {
		return a, cmd
	}
	accepted, value := a.filePicker.Accepted, a.filePicker.Value()
	purpose := a.inputPurpose
	a.filePicker = ui.FilePicker{}
	a.inputPurpose = inputNone
	a.mode = modeBrowse
	if !accepted || value == "" {
		a.cancelled()
		return a, nil
	}
	if purpose == inputJoinCA {
		return a, a.tookJoinCA(value)
	}
	return a, a.handleControlPlaneInput(purpose, value)
}

// pickerStart is where a file picker opens: the current value, or a directory
// that usually holds such files when there is none.
func pickerStart(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// keyBeside proposes the key that goes with a certificate: tui-cert's
// privkey.pem beside its fullchain.pem, a .key beside a .crt.
func keyBeside(cert string) string {
	switch {
	case path.Base(cert) == headscale.IssuedChainFile:
		return path.Join(path.Dir(cert), headscale.IssuedKeyFile)
	case strings.HasSuffix(cert, ".crt"):
		return strings.TrimSuffix(cert, ".crt") + ".key"
	}
	return path.Dir(cert)
}

// demoFiles is the file picker's filesystem under --demo: a made-up tree, so
// a screenshot never shows the machine it was rendered on.
func demoFiles() ui.FileSystem {
	pem := &fstest.MapFile{Data: []byte("demo"), Mode: 0o644}
	return ui.FileSystemFromFS(fstest.MapFS{
		"etc/headscale/config.yaml":                               pem,
		"etc/headscale/tls/headscale.example.com.crt":             pem,
		"etc/headscale/tls/headscale.example.com.key":             pem,
		"etc/ssl/certs/ca-certificates.crt":                       pem,
		"etc/ssl/tui-cert/headscale.example.com.crt":              pem,
		"etc/ssl/tui-cert/headscale.example.com.key":              pem,
		"etc/tui-cert/ca/homelab-ca/ca.crt":                       pem,
		"etc/tui-cert/issued/headscale.example.com/fullchain.pem": pem,
		"etc/tui-cert/issued/headscale.example.com/privkey.pem":   pem,
		"home/user/homelab-ca.crt":                                pem,
		"home/user/notes.txt":                                     pem,
	})
}

// localCADir is where tui-cert keeps its CAs, and where its export command
// (x) copies a CA certificate to on another host.
const localCADir = "/etc/tui-cert/ca"

// caStart is where the CA picker opens on a client: tui-cert's CA directory
// when a CA was copied there, the home directory otherwise.
func (a *app) caStart() string {
	fsys := a.files
	if fsys == nil {
		fsys = ui.OSFileSystem{}
	}
	if info, err := fsys.Stat(localCADir); err == nil && info.IsDir() {
		return localCADir
	}
	if a.files != nil {
		return "/home/user"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}
