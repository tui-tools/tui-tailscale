package main

import (
	"context"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	tuitailscale "github.com/tui-tools/tui-tailscale"
)

// backendName is the name the manifest gives the backend this tool drives:
// the tailscale client. tailscaled is its daemon and ships with it, so it is
// not probed separately.
const backendName = "tailscale"

// probeCompat reads the version of the client and classifies it against what
// the manifest declares: below the minimum, tested, or merely untested. The
// result goes in the header through ui.CompatFact, --check and --report.
//
// It never fails. A missing binary, a hung process or unparsable output all
// end as the "version unknown" badge, because a compatibility probe that can
// stop a tool from starting is worse than no probe.
func probeCompat(ctx context.Context, demo bool) compat.Result {
	// --demo drives an in-memory node, so probing the host would report a
	// version that has nothing to do with what is on screen.
	if demo {
		return compat.Result{}
	}
	m, err := manifest.Load(tuitailscale.ManifestJSON)
	if err != nil {
		return compat.Result{}
	}
	backend, ok := m.Backend(backendName)
	if !ok {
		return compat.Result{}
	}
	return compat.Probe(ctx, backend)
}
