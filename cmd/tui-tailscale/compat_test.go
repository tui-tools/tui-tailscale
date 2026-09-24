package main

import (
	"context"
	"testing"

	"github.com/tui-tools/tui-kit/manifest"
	tuitailscale "github.com/tui-tools/tui-tailscale"
)

// The embedded manifest is what the header reads, so its backends block
// cannot be malformed for long. It declares both ends: the client and the
// control plane.
func TestEmbeddedManifestDeclaresItsBackends(t *testing.T) {
	m, err := manifest.Load(tuitailscale.ManifestJSON)
	if err != nil {
		t.Fatalf("the embedded tool.json does not parse: %v", err)
	}
	if m.Name != toolName {
		t.Errorf("manifest name = %q, want %q", m.Name, toolName)
	}
	for _, name := range []string{backendName, backendHeadscale} {
		backend, ok := m.Backend(name)
		if !ok {
			t.Fatalf("no %s backend in the manifest", name)
		}
		if len(backend.VersionCommand) == 0 {
			t.Errorf("%s declares no version command", name)
		}
	}
}

func TestProbeCompatSkipsDemo(t *testing.T) {
	if got := probeCompat(context.Background(), true); len(got) != 0 {
		t.Errorf("demo probe = %+v, want nothing", got)
	}
}

// The probe runs against whatever this machine has. It must produce a Result
// per backend either way — that is the promise: a compatibility probe never
// fails a tool.
func TestProbeCompatOnThisMachine(t *testing.T) {
	got := probeCompat(context.Background(), false)
	if len(got) != 2 || got[0].Backend != backendName || got[1].Backend != backendHeadscale {
		t.Fatalf("probe = %+v, want tailscale then headscale", got)
	}
	for _, r := range got {
		t.Logf("this machine: %s %s (%s)", r.Backend, r.Version, r.Status)
	}
}
