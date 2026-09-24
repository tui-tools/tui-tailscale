package tailscale

import (
	"strings"
	"testing"
)

// The family rule is that every package turning bytes it did not write into
// values the tool acts on carries a Go native fuzz test, seeded from the
// package's testdata — see
// https://github.com/tui-tools/tui-kit/blob/main/templates/FUZZING.md.
//
// This package has three such inputs: the client's status and prefs output,
// the output of `tailscale up` a login URL is taken from, and the join form's
// answers, which become an argv. The targets assert invariants — what a caller
// may assume for any input at all — rather than outputs.

// FuzzParseStatus feeds arbitrary output to the status parser. It must never
// panic, and a success must describe a node the UI can render: a peer list
// without an entry the table would choke on.
func FuzzParseStatus(f *testing.F) {
	for _, name := range []string{"status-running.json", "status-needs-login.json",
		"status-with-warning.txt", "status-not-running.txt"} {
		f.Add(fixture(f, name))
	}
	f.Add("")
	f.Add("{")
	f.Add(`{"BackendState":"Running","Peer":{"k":null}}`)
	f.Add(`{"BackendState":"Running","Peer":{"k":{"AllowedIPs":["x","0.0.0.0/0","1.2.3.4/33"]}}}`)
	f.Fuzz(func(t *testing.T, out string) {
		st, err := ParseStatus(out)
		if err != nil {
			return
		}
		for _, p := range st.Peers {
			for _, r := range p.Routes {
				if isDefaultRoute(r) {
					t.Fatalf("a default route leaked into a peer's routes: %q", p.Routes)
				}
				if !strings.Contains(r, "/") {
					t.Fatalf("a route that is not a prefix: %q", r)
				}
			}
		}
	})
}

// FuzzParsePrefs feeds arbitrary output to the prefs parser: it must never
// panic.
func FuzzParsePrefs(f *testing.F) {
	f.Add(fixture(f, "debug-prefs.json"))
	f.Add("")
	f.Add(`{"AdvertiseRoutes":[1,2]}`)
	f.Fuzz(func(_ *testing.T, out string) {
		_, _ = ParsePrefs(out)
	})
}

// FuzzParseLoginURL feeds arbitrary `tailscale up` output to the login URL
// finder. Whatever it returns is shown to the user as the link to open, so it
// is one http(s) URL with no whitespace in it, or nothing.
func FuzzParseLoginURL(f *testing.F) {
	f.Add(fixture(f, "up-interactive.txt"))
	f.Add("To authenticate, visit:")
	f.Add("To authenticate, visit:\n\nhttps://a b")
	f.Fuzz(func(t *testing.T, out string) {
		url := ParseLoginURL(out)
		if url == "" {
			return
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			t.Fatalf("not a URL: %q", url)
		}
		if strings.ContainsAny(url, " \t\r\n") {
			t.Fatalf("whitespace in the URL: %q", url)
		}
	})
}

// FuzzBuildJoin feeds arbitrary join-form answers through the one place the
// join's argv is built. A refusal carries nothing runnable; a success keeps
// the key off every argv and every description, hands it to exactly one
// command's stdin, and removes the key file afterwards.
func FuzzBuildJoin(f *testing.F) {
	f.Add("https://headscale.example.com", "0123456789abcdef", "example-node", "192.0.2.0/24")
	f.Add("https://headscale.example.com", "", "", "")
	f.Add("--reset", "a b", "-x", "0.0.0.0/0")
	f.Add("http://100.64.0.1:8080", "tskey-auth-k\n--x", "a\nb", "192.0.2.1/24")
	f.Fuzz(func(t *testing.T, server, key, hostname, routes string) {
		plan, err := BuildCommand(Request{Action: ActionJoin, LoginServer: server,
			AuthKey: key, Hostname: hostname, Routes: SplitList(routes)})
		if err != nil {
			if len(plan.Commands()) != 0 {
				t.Fatalf("a refusal carries commands: %+v", plan)
			}
			return
		}
		// The key must not influence any argv or description: the same answers
		// with another key of the same shape build the same command lines. That
		// is the invariant "the key is on no command line", without tripping on
		// a key that happens to be a substring of a fixed flag.
		if key != "" {
			other, err := BuildCommand(Request{Action: ActionJoin, LoginServer: server,
				AuthKey: strings.Repeat("Z", len(key)), Hostname: hostname,
				Routes: SplitList(routes)})
			if err != nil {
				t.Fatalf("a key of the same shape was refused: %v", err)
			}
			a, b := plan.Commands(), other.Commands()
			if len(a) != len(b) {
				t.Fatalf("the key changed the number of commands")
			}
			for i := range a {
				if a[i].String() != b[i].String() || a[i].Description != b[i].Description {
					t.Fatalf("the key reaches an argv or a description: %q", a[i])
				}
			}
		}
		stdin := 0
		for _, c := range plan.Commands() {
			if len(c.Argv) == 0 || c.Argv[0] == "" {
				t.Fatalf("a command with no program: %+v", c)
			}
			for _, arg := range c.Argv {
				if strings.ContainsAny(arg, "\n\r") {
					t.Fatalf("a newline in an argument would split the preview: %q", arg)
				}
			}
			if key != "" && c.Stdin == key {
				stdin++
			}
		}
		if key != "" {
			if stdin != 1 {
				t.Fatalf("the key reaches %d stdins, want 1", stdin)
			}
			if len(plan.Cleanup) == 0 {
				t.Fatal("the key file is never removed")
			}
		}
		up := plan.Steps[len(plan.Steps)-1]
		if up.Argv[0] != "tailscale" || up.Argv[1] != "up" || up.Argv[len(up.Argv)-1] != "--reset" {
			t.Fatalf("the join does not end in `tailscale up … --reset`: %q", up.Argv)
		}
	})
}
