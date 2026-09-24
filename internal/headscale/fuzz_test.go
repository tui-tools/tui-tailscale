package headscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The family rule is that every package turning bytes it did not write into
// values the tool acts on carries a Go native fuzz test, seeded from the
// package's testdata — see
// https://github.com/tui-tools/tui-kit/blob/main/templates/FUZZING.md.
//
// This package has three list parsers (users, nodes, pre-auth keys) that read
// the headscale CLI's JSON. The invariant, not the output, is what is
// asserted: for any input at all, a parser never panics and never returns a
// value that breaks the model.
//
// Run one for real with, e.g.:
//
//	go test -run=^$ -fuzz=FuzzParseNodes -fuzztime=2m ./internal/headscale/

// seedFrom adds every fixture whose name starts with prefix to the corpus,
// alongside the shapes a real capture never has.
func seedFrom(f *testing.F, prefix string) {
	f.Helper()
	files, err := filepath.Glob("testdata/" + prefix + "*")
	if err != nil {
		f.Fatalf("glob: %v", err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // testdata is in the repository
		if err != nil {
			f.Fatalf("read %s: %v", path, err)
		}
		f.Add(string(data))
	}
	for _, seed := range []string{
		"", "\n", "\t", " ", ":", "::",
		"[]", "{}", "null", "[{}]", `[{"id":1}]`, `{"users":null}`,
		strings.Repeat("A", 4096),
	} {
		f.Add(seed)
	}
}

func FuzzParseUsers(f *testing.F) {
	seedFrom(f, "headscale-users")
	f.Fuzz(func(t *testing.T, data string) {
		users, err := ParseUsers([]byte(data))
		if err != nil {
			if users != nil {
				t.Fatalf("failed and still returned %d users", len(users))
			}
			return
		}
		// OIDC inference must never panic on whatever came back.
		_ = InferOIDC(users, nil)
	})
}

func FuzzParseNodes(f *testing.F) {
	seedFrom(f, "headscale-nodes")
	f.Fuzz(func(t *testing.T, data string) {
		nodes, err := ParseNodes([]byte(data))
		if err != nil {
			if nodes != nil {
				t.Fatalf("failed and still returned %d nodes", len(nodes))
			}
			return
		}
		_ = InferOIDC(nil, nodes)
		for _, n := range nodes {
			// The route views must hold for whatever came back.
			_ = RoutesText(n)
			_ = pendingRoutes(n)
		}
	})
}

func FuzzParsePreAuthKeys(f *testing.F) {
	seedFrom(f, "headscale-preauthkeys")
	f.Fuzz(func(t *testing.T, data string) {
		keys, err := ParsePreAuthKeys([]byte(data))
		if err != nil {
			if keys != nil {
				t.Fatalf("failed and still returned %d keys", len(keys))
			}
			return
		}
		for _, k := range keys {
			// The whole key must never survive: only a bounded prefix is kept.
			if len(k.KeyPrefix) > keyPrefixLen {
				t.Fatalf("key prefix longer than the cap: %q", k.KeyPrefix)
			}
		}
	})
}
