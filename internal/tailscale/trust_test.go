package tailscale

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyTLSCheck(t *testing.T) {
	for text, want := range map[string]TLSCheck{
		"curl: (60) SSL certificate problem: unable to get local issuer certificate":  TLSUntrusted,
		"curl: (60) SSL certificate problem: self-signed certificate":                 TLSUntrusted,
		"curl: (60) SSL: no alternative certificate subject name matches target host": TLSMismatch,
		"curl: (6) Could not resolve host: headscale.example.com":                     TLSUnreachable,
		"curl: (28) Connection timed out after 8001 milliseconds":                     TLSUnreachable,
	} {
		if got := ClassifyTLSCheck(text, errors.New("exit status")); got != want {
			t.Errorf("%q = %s, want %s", text, got, want)
		}
	}
	if ClassifyTLSCheck("", nil) != TLSVerified {
		t.Error("no error is verified")
	}
	cmd, err := BuildTLSCheck("https://headscale.example.com/")
	if err != nil || strings.Join(cmd.Argv, " ") !=
		"curl -sS -o /dev/null --max-time 8 https://headscale.example.com/health" {
		t.Errorf("check = %q %v", cmd.Argv, err)
	}
	if escalates(cmd) {
		t.Error("the certificate check runs through the escalation prefix")
	}
}

// The trust step is the family's documented way on each of the three.
func TestTrustCAPerDistro(t *testing.T) {
	distro := func(osRelease string) Distro { return ParseDistro(osRelease) }
	ca := "/etc/tui-cert/ca.crt"
	name := CAName("https://headscale.lab.internal:8443")
	if name != "tui-tailscale-headscale.lab.internal" {
		t.Fatalf("name = %q", name)
	}
	for _, tc := range []struct {
		os   string
		want []string
	}{
		{"ID=ubuntu\nID_LIKE=debian\nVERSION_ID=24.04\n", []string{
			"install -m 644 " + ca + " /usr/local/share/ca-certificates/" + name + ".crt",
			"update-ca-certificates", "systemctl restart tailscaled"}},
		{"ID=fedora\nVERSION_ID=44\n", []string{
			"install -m 644 " + ca + " /etc/pki/ca-trust/source/anchors/" + name + ".crt",
			"update-ca-trust", "systemctl restart tailscaled"}},
		{"ID=arch\n", []string{"trust anchor --store " + ca, "systemctl restart tailscaled"}},
	} {
		plan, err := BuildCommand(Request{Action: ActionTrustCA, CAPath: ca, CAName: name,
			Distro: distro(tc.os)})
		if err != nil {
			t.Fatalf("%s: %v", tc.os, err)
		}
		var got []string
		for _, c := range plan.Steps {
			got = append(got, strings.Join(c.Argv, " "))
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", tc.os, got, tc.want)
		}
	}
	for _, bad := range []string{"ca.crt", "/etc/../ca.crt", "/etc/ca crt", "/etc/"} {
		if _, err := BuildCommand(Request{Action: ActionTrustCA, CAPath: bad, CAName: name,
			Distro: distro("ID=ubuntu\n")}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := BuildCommand(Request{Action: ActionTrustCA, CAPath: ca, CAName: name,
		Distro: distro("ID=gentoo\n")}); err == nil {
		t.Error("an unknown distribution got a plan")
	}
}

// Omarchy's pacman hook refuses a direct -Syu: the install there is a plain
// -S against the database `omarchy update` keeps, found on the lab's Omarchy
// Server guest.
func TestInstallOnOmarchyAvoidsTheUpgradeGuard(t *testing.T) {
	plan, err := BuildCommand(Request{Action: ActionInstall,
		Distro: ParseDistro("ID=omarchy-server\nID_LIKE=\"omarchy arch\"\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Steps[0].Argv, " "); got != "pacman -S --needed --noconfirm tailscale" {
		t.Errorf("omarchy install = %q", got)
	}
	arch, _ := BuildCommand(Request{Action: ActionInstall, Distro: ParseDistro("ID=arch\n")})
	if got := strings.Join(arch.Steps[0].Argv, " "); got != "pacman -Syu --needed --noconfirm tailscale" {
		t.Errorf("arch install = %q", got)
	}
}
