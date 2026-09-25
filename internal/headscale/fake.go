package headscale

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/runner"
)

// Fake is the in-memory control plane behind --demo and the tests. It
// previews exactly the commands the real backend would, then applies them to
// its own state instead of to the machine, so the UI cannot tell it from the
// real thing and a test can assert that the command that ran is the command
// the preview showed.
//
// It is the same tailnet internal/tailscale's demo node belongs to: the
// control plane at https://headscale.example.com, where the demo node
// (example-node) is registered under user@example.com, exit-gateway serves as
// an approved exit node, and office-router serves 192.0.2.0/24 with a second
// subnet, 198.51.100.0/24, still waiting for approval — the route `r` has to
// approve. Every address is from the tailnet ranges (100.64.0.0/10,
// fd7a:115c:a1e0::/48) or a documentation range, every name is example.com,
// and every key is an obvious placeholder.
type Fake struct {
	mu    sync.Mutex
	state State
	// config is the demo's /etc/headscale/config.yaml, kept as text so the
	// control-plane flows are exercised for real: the diff the confirm dialog
	// shows under --demo is computed by the same editor that runs on a host.
	config string
	// serviceState and serviceEnabled are the demo unit's is-active and
	// is-enabled answers. They live outside the parsed state so a re-read of
	// the configuration keeps them, the way a real re-read would.
	serviceState, serviceEnabled string
	// stats is the demo's filesystem as `stat` would report it: who owns
	// headscale's state files and the files this tool writes. chown applies to
	// it, and the ownership check reads it, exactly like the real ones.
	stats map[string]FileStat
	run   *runner.Fake
	// absent is whether headscale is on the fake machine; detected whether
	// the backend has found it — which, like the real backend's runner cache,
	// only changes on Reprobe. refuse counts the CLI reads that fail while the
	// server is still coming up.
	absent, detected bool
	refuse           int
	// Launched records the tools f handed the terminal to, for the tests.
	Launched []string
	// pki is what the demo's tui-cert reports: one local CA and the pair it
	// issued for the control plane.
	pki LocalPKI
	// configMissing is a deleted /etc/headscale/config.yaml (and with it
	// /var/lib/headscale): the partial reset of issue #18, which the
	// package reinstall undoes.
	configMissing bool
}

// demoNewPreAuthKey is the one-time key the demo "creates". Plainly fake.
const demoNewPreAuthKey = "demodemodemodemodemodemodemodemodemodemo1234"

// DemoServerURL is the demo control plane's server_url: the login server the
// demo node of internal/tailscale is joined to.
const DemoServerURL = "https://headscale.example.com"

// NewFake returns a Fake preloaded with the demo control plane. Its unit is
// running but disabled — started by hand after the package installed it, the
// way a fresh install usually ends up, and gone after the next reboot — which
// is what makes the enable at the end of S and O visible under --demo, and
// what the readiness line points at first.
func NewFake() *Fake {
	f := &Fake{state: demoState(), config: demoHeadscaleConfig,
		serviceState: "active", serviceEnabled: "disabled", stats: demoStats(),
		detected: true, pki: DemoLocalPKI()}
	f.run = &runner.Fake{Prefix: "sudo -n", Hook: f.apply}
	f.reloadControlPlane()
	return f
}

// demoHeadscaleConfig is a plausible, already-configured headscale
// configuration: enough of the real file's shape — comments, blank lines,
// nested sections — that editing it in the demo proves the editor keeps
// everything it does not touch. Every host in it is a documentation name.
const demoHeadscaleConfig = `# headscale configuration (demo)

# Behind a reverse proxy: TLS ends in front, headscale listens on loopback.
server_url: https://headscale.example.com
listen_addr: 127.0.0.1:8080
metrics_listen_addr: 127.0.0.1:9090

tls_letsencrypt_hostname: ""
tls_cert_path: ""
tls_key_path: ""

# The Noise protocol key the control channel is encrypted with.
noise:
  private_key_path: /var/lib/headscale/noise_private.key

database:
  type: sqlite
  sqlite:
    path: /var/lib/headscale/db.sqlite

prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48

oidc:
  only_start_if_oidc_is_available: true
  issuer: https://idp.example.com/realms/demo
  client_id: headscale
  client_secret_path: /etc/headscale/oidc_client_secret
  scope: ["openid", "profile", "email"]
  allowed_domains: ["example.com"]
  allowed_groups: []
  allowed_users: []
  pkce:
    enabled: true

dns:
  magic_dns: true
  base_domain: tailnet.example.com
  override_local_dns: true
  nameservers:
    global: ["1.1.1.1", "1.0.0.1"]
    # The office network's names resolve through its own resolver.
    split: {"corp.example.com": ["10.0.0.2"]}
  search_domains: ["tailnet.example.com"]
  extra_records: [{name: "grafana.tailnet.example.com", type: "A", value: "100.64.0.3"}]

log:
  level: info
`

// demoStats is the demo's filesystem. Everything belongs where it should
// except the noise private key, which is root's: the leftover of a
// `sudo headscale configtest` run before the first start. It is mode 644, so
// the running demo server could still read it, which keeps the demo coherent;
// on a real host the same mistake is usually 600 and the service fails.
func demoStats() map[string]FileStat {
	stats := map[string]FileStat{}
	for _, st := range []FileStat{
		{Path: HeadscaleStateDir, User: "headscale", Group: "headscale", Mode: 0o750},
		{Path: HeadscaleStateDir + "/noise_private.key", User: "root", Group: "root", Mode: 0o644},
		{Path: HeadscaleStateDir + "/db.sqlite", User: "headscale", Group: "headscale", Mode: 0o640},
		{Path: OIDCClientSecretPath, User: "headscale", Group: "headscale", Mode: 0o600},
		{Path: HeadscaleConfigPath, User: "root", Group: "root", Mode: 0o644},
		// Two certificate pairs for the own-certificate transport: the one
		// tui-cert issues into its root-only directory, which the service
		// account cannot enter, and a copy installed for headscale.
		{Path: "/etc", User: "root", Group: "root", Mode: 0o755},
		{Path: "/etc/headscale", User: "root", Group: "root", Mode: 0o755},
		{Path: "/etc/ssl", User: "root", Group: "root", Mode: 0o755},
		{Path: "/etc/ssl/tui-cert", User: "root", Group: "root", Mode: 0o700},
		{Path: "/etc/ssl/tui-cert/headscale.example.com.crt", User: "root", Group: "root", Mode: 0o644},
		{Path: "/etc/ssl/tui-cert/headscale.example.com.key", User: "root", Group: "root", Mode: 0o600},
		{Path: "/etc/headscale/tls", User: "root", Group: "headscale", Mode: 0o750},
		{Path: "/etc/headscale/tls/headscale.example.com.crt", User: "root", Group: "headscale", Mode: 0o644},
		{Path: "/etc/headscale/tls/headscale.example.com.key", User: "root", Group: "headscale", Mode: 0o640},
		// The pair tui-cert's local CA issued with owner headscale: S offers
		// it by name before any file picker.
		{Path: "/etc/tui-cert", User: "root", Group: "root", Mode: 0o755},
		{Path: "/etc/tui-cert/issued", User: "root", Group: "root", Mode: 0o755},
		{Path: demoIssuedDir, User: "root", Group: "root", Mode: 0o755},
		{Path: demoIssuedDir + "/" + IssuedChainFile, User: "headscale", Group: "headscale", Mode: 0o644},
		{Path: demoIssuedDir + "/" + IssuedKeyFile, User: "headscale", Group: "headscale", Mode: 0o600},
	} {
		stats[st.Path] = st
	}
	return stats
}

// demoIssuedDir is where the demo's local CA put the control plane's pair.
const demoIssuedDir = "/etc/tui-cert/issued/headscale.example.com"

// DemoLocalPKI is the demo's tui-cert: homelab-ca, not trusted yet, and the
// pair it issued for the control plane, with a documentation-range IP SAN.
func DemoLocalPKI() LocalPKI {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	return LocalPKI{
		Installed: true,
		CAs: []LocalCA{{Name: "homelab-ca", CertPath: "/etc/tui-cert/ca/homelab-ca/ca.crt",
			Subject:     "homelab-ca",
			Fingerprint: "CC:70:FC:C3:4E:7F:A0:82:CB:5D:96:BD:60:86:65:A4:E7:4F:11:37:95:98:DE:F9:EC:62:00:99:99:9B:47:AE",
			NotAfter:    now.AddDate(10, 0, 0), CanIssue: true}},
		Pairs: []IssuedPair{{CA: "homelab-ca", Subject: "headscale.example.com",
			SANs:     []string{"headscale.example.com", "192.0.2.10"},
			CertPath: demoIssuedDir + "/" + IssuedChainFile,
			KeyPath:  demoIssuedDir + "/" + IssuedKeyFile,
			NotAfter: now.AddDate(1, 0, 0)}},
	}
}

// SetLocalPKI replaces what the demo's tui-cert reports; a zero value is a
// machine without tui-cert.
func (f *Fake) SetLocalPKI(pki LocalPKI) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pki = pki
}

// ReadLocalPKI answers from the demo's tui-cert.
func (f *Fake) ReadLocalPKI(_ context.Context) LocalPKI {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pki
}

// SetStat replaces one path's owner and mode in the demo's filesystem, so a
// test can stage the ownership it wants to drive a flow from.
func (f *Fake) SetStat(st FileStat) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats[st.Path] = st
	f.reloadControlPlane()
}

// Stat answers the way `stat` would for the demo's filesystem.
func (f *Fake) Stat(_ context.Context, paths []string) map[string]FileStat {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statPaths(paths)
}

// statPaths answers the way `stat` would for the demo's filesystem.
func (f *Fake) statPaths(paths []string) map[string]FileStat {
	out := map[string]FileStat{}
	for _, p := range paths {
		if st, ok := f.stats[p]; ok {
			out[p] = st
		}
	}
	return out
}

// chown applies a previewed chown to the demo's filesystem.
func (f *Fake) chown(argv []string) (string, error) {
	recursive := len(argv) == 4 && argv[1] == "-R"
	owner, target := argv[len(argv)-2], argv[len(argv)-1]
	user, group, ok := strings.Cut(owner, ":")
	if !ok {
		return "", fmt.Errorf("chown: invalid owner %q", owner)
	}
	for p, st := range f.stats {
		if p == target || (recursive && strings.HasPrefix(p, target+"/")) {
			st.User, st.Group = user, group
			f.stats[p] = st
		}
	}
	f.reloadControlPlane()
	return "", nil
}

// SetConfigMissing deletes the demo's config.yaml and state directory, the
// way a partial reset leaves a host: headscale installed, its unit failed and
// disabled, nothing to read or edit until i reinstalls the package.
func (f *Fake) SetConfigMissing() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configMissing = true
	f.serviceState, f.serviceEnabled = "failed", "disabled"
	for p := range f.stats {
		if p == HeadscaleConfigPath || p == HeadscaleStateDir ||
			strings.HasPrefix(p, HeadscaleStateDir+"/") {
			delete(f.stats, p)
		}
	}
	f.state.Users, f.state.Nodes, f.state.PreAuthKeys = nil, nil, nil
	f.state.Registrations = nil
	f.reloadControlPlane()
}

// demoPackageConfig is the example configuration the package ships, which a
// reinstall puts back: loopback server_url and placeholders, so S is next.
const demoPackageConfig = `# headscale example configuration (as packaged)
server_url: http://127.0.0.1:8080
listen_addr: 127.0.0.1:8080
dns:
  magic_dns: true
  base_domain: example.com
`

// reinstall applies the package reinstall: a deleted configuration comes back
// as the package's example, and the state directory with it.
func (f *Fake) reinstall() {
	if !f.configMissing {
		return
	}
	f.configMissing = false
	f.config = demoPackageConfig
	f.serviceState = "inactive"
	f.stats[HeadscaleConfigPath] = FileStat{Path: HeadscaleConfigPath, User: "root",
		Group: "root", Mode: 0o644}
	f.stats[HeadscaleStateDir] = FileStat{Path: HeadscaleStateDir, User: "headscale",
		Group: "headscale", Mode: 0o750}
	f.reloadControlPlane()
}

// reloadControlPlane re-reads the demo's configuration into the state, the way
// a reload on a real host would.
func (f *Fake) reloadControlPlane() {
	if f.configMissing {
		_, dirThere := f.stats[HeadscaleStateDir]
		f.state.ControlPlane = ControlPlane{ConfigPath: HeadscaleConfigPath,
			Error:         "cat: " + HeadscaleConfigPath + ": No such file or directory",
			ConfigMissing: true, StateDirMissing: !dirThere,
			ServiceState: f.serviceState, ServiceEnabled: f.serviceEnabled}
		return
	}
	cp, err := ParseHeadscaleConfig([]byte(f.config))
	if err != nil {
		f.state.ControlPlane = ControlPlane{
			ConfigPath: HeadscaleConfigPath, Error: err.Error()}
		return
	}
	cp.ServiceState = f.serviceState
	cp.ServiceEnabled = f.serviceEnabled
	// The demo's unit runs headscale as its own user, which is the case the
	// secret write has to get right: an `install` without -o would leave the
	// service unable to read its own secret.
	cp.ServiceUser, cp.ServiceGroup = "headscale", "headscale"
	cp.Ownership = CheckOwnership(cp, f.statPaths(OwnershipPaths(cp)))
	f.state.ControlPlane = cp
}

// SetService sets the demo unit's is-active and is-enabled answers, so a test
// can put the fake in the state it wants to drive a flow from (a fresh
// install is "inactive" and "disabled").
func (f *Fake) SetService(active, enabled string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serviceState, f.serviceEnabled = active, enabled
	f.reloadControlPlane()
}

// SetConfig replaces the demo's config.yaml, so a test can drive a flow from
// another starting point: the file the package ships, or one set up for
// another transport.
func (f *Fake) SetConfig(config string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.config = config
	f.reloadControlPlane()
}

// Name identifies the backend.
func (f *Fake) Name() string { return "demo" }

// Preview renders the command the way the real backend would.
func (f *Fake) Preview(cmd runner.Command) string {
	if !escalates(cmd) {
		return cmd.String()
	}
	return f.run.Preview(cmd)
}

// Run applies a confirmed command to the in-memory state.
func (f *Fake) Run(ctx context.Context, cmd runner.Command) (string, error) {
	return f.run.Run(ctx, cmd)
}

// Commands returns every command the fake was asked to run, for the tests.
func (f *Fake) Commands() []runner.Command { return f.run.Ran }

// SetAbsent takes headscale off the fake machine, with the tui-tools
// repository not set up yet: `i` installs it, and the next Reprobe finds it.
func (f *Fake) SetAbsent() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.absent, f.detected = true, false
}

// Reprobe finds headscale again, when it is there.
func (f *Fake) Reprobe() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detected = !f.absent
}

// Load returns a copy of the sample state. With the demo unit stopped it
// answers the way the real backend does: the configuration and the unit's
// state, and no lists, because the CLI would have nothing to talk to.
func (f *Fake) Load(_ context.Context) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.detected {
		return State{Distro: f.state.Distro}, nil
	}
	state := f.state
	if f.refuse > 0 {
		// The socket is not up yet: the CLI's own error, as the first read
		// after an install on a real host printed it.
		f.refuse--
		state.Error = "connecting to headscale: dial unix /var/run/headscale/headscale.sock: " +
			"connect: permission denied"
		state.Users, state.Nodes, state.PreAuthKeys = nil, nil, nil
		return state, nil
	}
	state.Users = append([]User(nil), f.state.Users...)
	state.Nodes = append([]Node(nil), f.state.Nodes...)
	state.PreAuthKeys = append([]PreAuthKey(nil), f.state.PreAuthKeys...)
	state.Registrations = append([]Registration(nil), f.state.Registrations...)
	if msg := NotRunningMessage(state.ControlPlane); msg != "" {
		state.Error, state.NotRunning = msg, true
		state.Users, state.Nodes, state.PreAuthKeys = nil, nil, nil
	}
	return state, nil
}

// apply mutates the sample state the way the real command would. It is the
// runner.Fake hook, so it runs only for a command that was previewed and
// confirmed — the same path the real backend takes.
func (f *Fake) apply(cmd runner.Command) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	argv := cmd.Argv
	switch {
	case len(argv) == 5 && argv[0] == "headscale" && argv[1] == "nodes" && argv[2] == "expire":
		return f.expireNode(argv[4])
	case len(argv) == 6 && argv[0] == "headscale" && argv[1] == "nodes" && argv[2] == "delete" && argv[5] == "--force":
		return f.deleteNode(argv[4])
	case len(argv) == 6 && argv[0] == "headscale" && argv[1] == "nodes" && argv[2] == "rename":
		return f.renameNode(argv[4], argv[5])
	case len(argv) >= 6 && argv[0] == "headscale" && argv[1] == "nodes" && argv[2] == "approve-routes":
		return f.approveRoutes(argv[4], argv[5:])
	case len(argv) >= 7 && argv[0] == "headscale" && argv[1] == "preauthkeys" && argv[2] == "create":
		return f.createPreAuthKey(argv)
	case len(argv) == 4 && argv[0] == "headscale" && argv[1] == "users" && argv[2] == "create":
		return f.createUser(argv[3])
	case len(argv) == 7 && argv[0] == "headscale" && argv[2] == "register":
		return f.register(argv[4], argv[6])
	case len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" &&
		strings.Contains(argv[2], HeadscaleConfigPath):
		return f.writeHeadscaleConfig(cmd.Stdin)
	case len(argv) > 1 && argv[0] == "install" && argv[len(argv)-1] == OIDCClientSecretPath:
		// The demo records that a secret exists and drops the value, which is
		// exactly what the real flow does: nothing but the file ever holds it.
		f.state.ControlPlane.OIDC.ClientSecretSet = true
		return "", nil
	case len(argv) >= 4 && argv[0] == "systemctl" && argv[1] == "show":
		return "User=headscale\nGroup=headscale", nil
	case len(argv) == 3 && argv[0] == "systemctl" && argv[1] == "restart":
		f.serviceState = "active"
		f.state.ControlPlane.ServiceState = f.serviceState
		return "", nil
	case len(argv) >= 3 && argv[0] == "systemctl" && argv[1] == "enable":
		f.serviceEnabled = "enabled"
		if hasArg(argv, "--now") {
			f.serviceState = "active"
		}
		f.state.ControlPlane.ServiceState = f.serviceState
		f.state.ControlPlane.ServiceEnabled = f.serviceEnabled
		return "Created symlink /etc/systemd/system/multi-user.target.wants/headscale.service.", nil
	case (len(argv) == 3 || len(argv) == 4) && argv[0] == "chown":
		return f.chown(argv)
	case len(argv) >= 2 && argv[0] == "curl" && !hasArg(argv, "-o"):
		return demoDiscoveryDocument, nil
	case len(argv) >= 2 && argv[0] == "gpg" && hasArg(argv, "--show-keys"):
		// The downloaded repository key, read back: the pinned one.
		return "pub:-:4096:1:389120B277E4FB44:1790000000:::-:::scESC::::::23::0:\n" +
			"fpr:::::::::" + RepoFingerprint + ":\n", nil
	case len(argv) >= 1 && (argv[0] == "apt-get" || argv[0] == "dnf" || argv[0] == "pacman") &&
		(hasArg(argv, PackageName) || hasArg(argv, "tui-tools/"+PackageName)):
		// The package install puts headscale on the machine; its socket
		// takes a moment to answer after it. A reinstall puts back a
		// deleted configuration.
		if f.absent {
			f.absent, f.refuse = false, 1
		}
		f.reinstall()
		return "", nil
	case len(argv) >= 1 && isInstallStep(argv[0]):
		// The companion install changes files and packages the demo does not
		// model.
		return "", nil
	default:
		return "", fmt.Errorf("demo backend does not know how to apply %q", cmd.String())
	}
}

// isInstallStep reports whether a binary is one of the companion install's.
func isInstallStep(bin string) bool {
	switch bin {
	case "install", "curl", "gpg", "chmod", "tee", "rpm", "apt-get", "dnf",
		"pacman", "pacman-key":
		return true
	}
	return false
}

// demoDiscoveryDocument is what the demo's IdP answers to a discovery read.
const demoDiscoveryDocument = `{"issuer":"https://idp.example.com/realms/demo",` +
	`"authorization_endpoint":"https://idp.example.com/realms/demo/protocol/openid-connect/auth"}`

// writeHeadscaleConfig applies the configuration write: the demo keeps the new
// text and re-reads it, so the panel and the next diff both reflect it.
func (f *Fake) writeHeadscaleConfig(content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("refusing to write an empty configuration")
	}
	secretSet := f.state.ControlPlane.OIDC.ClientSecretSet
	// `cp -p` takes the backup with config.yaml's own owner and mode.
	if cfg, ok := f.stats[HeadscaleConfigPath]; ok {
		cfg.Path = HeadscaleConfigBackupPath
		f.stats[cfg.Path] = cfg
	}
	f.config = content
	f.reloadControlPlane()
	f.state.ControlPlane.OIDC.ClientSecretSet =
		secretSet || f.state.ControlPlane.OIDC.ClientSecretSet
	return "", nil
}

// DemoFirewall is the demo host's firewall as tui-firewall reads it: ufw
// denying input by default, with 443/tcp open for the control plane and
// nothing for tailscale's 41641/udp, so the readiness line has the node port
// to point at.
func DemoFirewall() Firewall {
	fw, _ := ParseTuiFirewallCheck(`{"enabled": true, "model": {"Groups": [{"Name": "rules",
		"Default": {"Incoming": "deny"}, "Rules": [
		{"Action": "LIMIT", "Direction": "IN", "Proto": "tcp", "Ports": "22", "From": "Anywhere"},
		{"Action": "ALLOW", "Direction": "IN", "Proto": "tcp", "Ports": "80,443", "From": "Anywhere"}]}]}}`)
	fw.Launchable = true
	return fw
}

// SetFirewall replaces the demo host's firewall, so a test can close a port.
func (f *Fake) SetFirewall(fw Firewall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Firewall = fw
}

// LaunchFirewall records the hand-over and starts nothing: the demo reaches
// every key, and handing the terminal to a tool that may not be installed is
// not something a demo may do.
func (f *Fake) LaunchFirewall() (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.state.Firewall.Launchable {
		return nil, fmt.Errorf("%s is not installed (it comes from pkgs.tui.tools)", FirewallTool)
	}
	f.Launched = append(f.Launched, FirewallTool)
	return &demoProcess{name: FirewallTool}, nil
}

// demoProcess is the hand-over that does not happen: it prints one line where
// the tool would have drawn.
type demoProcess struct {
	name string
	out  io.Writer
}

// Run writes the line that stands in for the tool.
func (d *demoProcess) Run() error {
	out := d.out
	if out == nil {
		out = os.Stdout
	}
	_, err := fmt.Fprintf(out, "demo: %s would run here, with the terminal to itself\n", d.name)
	return err
}

// SetStdin is ignored: nothing reads.
func (d *demoProcess) SetStdin(io.Reader) {}

// SetStdout is where the stand-in line goes.
func (d *demoProcess) SetStdout(w io.Writer) { d.out = w }

// SetStderr is ignored: nothing fails.
func (d *demoProcess) SetStderr(io.Writer) {}

// String is the command line the real hand-over would run.
func (d *demoProcess) String() string { return d.name }

// DemoAuthID is the registration the demo has waiting: a laptop that ran
// `tailscale up --login-server` and has not logged in yet.
const DemoAuthID = "hskey-authreq-DemoLaptopWaiting0001"

// SetNodes replaces the registered nodes, so a test can start from a control
// plane with none.
func (f *Fake) SetNodes(nodes []Node) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Nodes = append([]Node(nil), nodes...)
}

// SetPreAuthKeys replaces the pre-auth keys, so a test can stage a spent or
// expired one.
func (f *Fake) SetPreAuthKeys(keys []PreAuthKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.PreAuthKeys = append([]PreAuthKey(nil), keys...)
}

// SetRegistrations replaces the pending registrations, so a test can stage
// the ones it wants.
func (f *Fake) SetRegistrations(regs []Registration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Registrations = regs
}

// register applies `headscale auth register` (or its older name): the
// registration becomes a node of that user.
func (f *Fake) register(authID, user string) (string, error) {
	for i, reg := range f.state.Registrations {
		if reg.AuthID != authID {
			continue
		}
		known := false
		for _, u := range f.state.Users {
			known = known || u.Name == user
		}
		if !known {
			return "", fmt.Errorf("user not found: %s", user)
		}
		f.state.Registrations = append(f.state.Registrations[:i:i], f.state.Registrations[i+1:]...)
		next := fmt.Sprintf("%d", len(f.state.Nodes)+1)
		f.state.Nodes = append(f.state.Nodes, Node{ID: next, Name: "laptop", GivenName: "laptop",
			User: user, IPAddresses: []string{"100.64.0." + next, "fd7a:115c:a1e0::" + next},
			LastSeen: time.Now(), Online: true, RegisterMethod: "cli",
			Expiry: time.Now().Add(180 * 24 * time.Hour)})
		return "Node laptop registered", nil
	}
	return "", fmt.Errorf("auth ID not found: %s", authID)
}

func (f *Fake) deleteNode(id string) (string, error) {
	nodes := f.state.Nodes
	for i := range nodes {
		if nodes[i].ID == id {
			f.state.Nodes = append(nodes[:i:i], nodes[i+1:]...)
			return "Node destroyed", nil
		}
	}
	return "", fmt.Errorf("no such node: %s", id)
}

// approveRoutes applies `headscale nodes approve-routes`: the list replaces
// the node's approvals, and what is served is what is both advertised and
// approved.
func (f *Fake) approveRoutes(id string, args []string) (string, error) {
	var routes []string
	switch {
	case len(args) == 1 && args[0] == "--routes=":
	case len(args) == 2 && args[0] == "--routes":
		routes = strings.Split(args[1], ",")
	default:
		return "", fmt.Errorf("approve-routes: unexpected arguments %q", args)
	}
	for i := range f.state.Nodes {
		n := &f.state.Nodes[i]
		if n.ID != id {
			continue
		}
		n.ApprovedRoutes = routes
		n.SubnetRoutes = nil
		for _, r := range NodeRoutes(*n) {
			if r.Advertised && r.Approved {
				n.SubnetRoutes = append(n.SubnetRoutes, r.Route)
			}
		}
		return "Node updated", nil
	}
	return "", fmt.Errorf("no such node: %s", id)
}

func (f *Fake) renameNode(id, name string) (string, error) {
	for i := range f.state.Nodes {
		if f.state.Nodes[i].ID == id {
			f.state.Nodes[i].GivenName = name
			return "Node renamed", nil
		}
	}
	return "", fmt.Errorf("no such node: %s", id)
}

// createPreAuthKey applies `headscale preauthkeys create`, growing the key
// list and answering with the full one-time key the way headscale prints it.
// Like upstream, the full key appears only in this answer: the state keeps the
// prefix alone.
func (f *Fake) createPreAuthKey(argv []string) (string, error) {
	userID, expiration := "", "24h"
	reusable, ephemeral := false, false
	for i := 3; i < len(argv); i++ {
		switch argv[i] {
		case "--user":
			i++
			if i < len(argv) {
				userID = argv[i]
			}
		case "--reusable":
			reusable = true
		case "--ephemeral":
			ephemeral = true
		case "--expiration":
			i++
			if i < len(argv) {
				expiration = argv[i]
			}
		}
	}
	userName := ""
	for _, u := range f.state.Users {
		if u.ID == userID {
			userName = u.Name
		}
	}
	if userName == "" {
		return "", fmt.Errorf("no such user: %s", userID)
	}
	d, err := parseSimpleDuration(expiration)
	if err != nil {
		return "", err
	}
	next := fmt.Sprintf("%d", len(f.state.PreAuthKeys)+1)
	f.state.PreAuthKeys = append(f.state.PreAuthKeys, PreAuthKey{
		ID: next, User: userName, KeyPrefix: demoNewPreAuthKey[:10],
		Reusable: reusable, Ephemeral: ephemeral,
		Expiration: time.Now().Add(d), CreatedAt: time.Now(),
	})
	return demoNewPreAuthKey, nil
}

// parseSimpleDuration reads the integer-plus-unit durations ValidExpiration
// accepts, including the d/w/y units time.ParseDuration does not know.
func parseSimpleDuration(s string) (time.Duration, error) {
	if !ValidExpiration(s) {
		return 0, fmt.Errorf("not a valid expiration: %q", s)
	}
	n := 0
	_, _ = fmt.Sscanf(s[:len(s)-1], "%d", &n) // best-effort: 0 on mismatch is fine for the fake
	unit := map[byte]time.Duration{
		's': time.Second, 'm': time.Minute, 'h': time.Hour,
		'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour, 'y': 365 * 24 * time.Hour,
	}[s[len(s)-1]]
	return time.Duration(n) * unit, nil
}

func (f *Fake) expireNode(id string) (string, error) {
	for i := range f.state.Nodes {
		if f.state.Nodes[i].ID == id {
			f.state.Nodes[i].Expiry = time.Now().Add(-time.Minute)
			f.state.Nodes[i].Online = false
			return "Node expired", nil
		}
	}
	return "", fmt.Errorf("no such node: %s", id)
}

func (f *Fake) createUser(name string) (string, error) {
	for _, u := range f.state.Users {
		if u.Name == name {
			return "", fmt.Errorf("user %q already exists", name)
		}
	}
	next := fmt.Sprintf("%d", len(f.state.Users)+1)
	f.state.Users = append(f.state.Users, User{
		ID: next, Name: name, CreatedAt: time.Now(),
	})
	return "User created", nil
}

// demoState is the sample control plane. Times are relative to now, so the
// view reads sensibly however long after this was written it runs.
func demoState() State {
	now := time.Now()
	return State{
		Present:      true,
		OIDCInferred: true,
		Distro: pkgmgr.Distro{ID: "ubuntu", PrettyName: "Ubuntu 24.04 LTS",
			VersionID: "24.04", Like: []string{"debian"}},
		Repo: RepoState{Configured: true, Detail: "/etc/apt/sources.list.d/tui-tools.list " +
			"is an apt sources file naming pkgs.tui.tools"},
		Users: []User{
			{ID: "1", Name: "user@example.com", DisplayName: "Example User",
				Email: "user@example.com", Provider: "oidc",
				ProviderID: "https://idp.example.com/realms/demo/user",
				CreatedAt:  now.Add(-30 * 24 * time.Hour)},
			{ID: "2", Name: "ops@example.com", DisplayName: "Example Ops",
				Email: "ops@example.com", Provider: "oidc",
				ProviderID: "https://idp.example.com/realms/demo/ops",
				CreatedAt:  now.Add(-12 * 24 * time.Hour)},
		},
		Nodes: []Node{
			// The demo node of internal/tailscale: this host, logged in with
			// a browser against the IdP.
			{ID: "1", Name: "example-node", GivenName: "example-node", User: "user@example.com",
				IPAddresses: []string{"100.64.0.1", "fd7a:115c:a1e0::1"},
				LastSeen:    now.Add(-5 * time.Second), Online: true,
				RegisterMethod: "oidc",
				Expiry:         now.Add(150 * 24 * time.Hour)},
			// Its exit node, approved.
			{ID: "2", Name: "exit-gateway", GivenName: "exit-gateway", User: "user@example.com",
				IPAddresses: []string{"100.64.0.2", "fd7a:115c:a1e0::2"},
				LastSeen:    now.Add(-2 * time.Minute), Online: true,
				RegisterMethod:  "authkey",
				AvailableRoutes: []string{"0.0.0.0/0", "::/0"},
				ApprovedRoutes:  []string{"0.0.0.0/0", "::/0"},
				SubnetRoutes:    []string{"0.0.0.0/0", "::/0"}},
			// A subnet router in the office: one subnet approved and served,
			// a second one it advertises that nobody approved yet.
			{ID: "3", Name: "office-router", GivenName: "office-router", User: "ops@example.com",
				IPAddresses: []string{"100.64.0.3", "fd7a:115c:a1e0::3"},
				LastSeen:    now.Add(-40 * time.Second), Online: true,
				RegisterMethod:  "authkey",
				AvailableRoutes: []string{"192.0.2.0/24", "198.51.100.0/24"},
				ApprovedRoutes:  []string{"192.0.2.0/24"},
				SubnetRoutes:    []string{"192.0.2.0/24"}},
		},
		Registrations: []Registration{{AuthID: DemoAuthID, Seen: now.Add(-90 * time.Second)}},
		Firewall:      DemoFirewall(),
		PreAuthKeys: []PreAuthKey{
			{ID: "1", User: "ops@example.com", KeyPrefix: "0123456789", Reusable: true,
				Ephemeral: false, Used: true,
				Expiration: now.Add(24 * time.Hour),
				CreatedAt:  now.Add(-2 * time.Hour),
				ACLTags:    []string{"tag:router"}},
			// The single-use key exit-gateway joined with: spent, so the
			// keys screen marks it (issue #19).
			{ID: "2", User: "user@example.com", KeyPrefix: "abcdef0123", Reusable: false,
				Ephemeral: false, Used: true,
				Expiration: now.Add(20 * time.Hour),
				CreatedAt:  now.Add(-4 * time.Hour)},
		},
	}
}
