package main

import (
	"context"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	tuitailscale "github.com/tui-tools/tui-tailscale"
)

// The two backends the manifest declares, one per exec site: the tailscale
// client (tailscaled is its daemon and ships with it, so it is not probed
// separately) and headscale, the control plane.
const (
	backendName      = "tailscale"
	backendHeadscale = "headscale"
)

// probeCompat reads the version of each backend the manifest declares and
// classifies it against what has been tested: below the minimum, tested, or
// merely untested. The results go in the header through ui.CompatFact,
// --check and --report.
//
// It never fails. A missing binary, a hung process or unparsable output all
// end as the "version unknown" badge, because a compatibility probe that can
// stop a tool from starting is worse than no probe.
func probeCompat(ctx context.Context, demo bool) []compat.Result {
	// --demo drives an in-memory node and control plane, so probing the host
	// would report versions that have nothing to do with what is on screen.
	if demo {
		return nil
	}
	m, err := manifest.Load(tuitailscale.ManifestJSON)
	if err != nil {
		return nil
	}
	var results []compat.Result
	for _, name := range []string{backendName, backendHeadscale} {
		backend, ok := m.Backend(name)
		if !ok {
			continue
		}
		results = append(results, compat.Probe(ctx, backend))
	}
	return results
}

// compatFor returns the probed result for one backend, or the zero result when
// it was not probed.
func compatFor(results []compat.Result, backend string) compat.Result {
	for _, r := range results {
		if r.Backend == backend {
			return r
		}
	}
	return compat.Result{}
}
