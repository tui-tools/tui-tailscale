package main

import (
	"os"
	"testing"
)

// --demo stands alone for the sample tailnet and takes a case name for the
// situations the sample does not show; anything else is refused.
func TestDemoFlagCases(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devnull.Close() }()
	cases := []struct {
		args     []string
		demo     bool
		demoCase string
		fails    bool
	}{
		{args: nil},
		{args: []string{"--demo"}, demo: true},
		{args: []string{"--demo", "--check"}, demo: true},
		{args: []string{"--demo=" + demoNodeOnly}, demo: true, demoCase: demoNodeOnly},
		{args: []string{"--demo=" + demoPartialReset}, demo: true, demoCase: demoPartialReset},
		{args: []string{"--demo=nope"}, fails: true},
	}
	for _, c := range cases {
		opts, err := parseFlags(c.args, devnull)
		if c.fails {
			if err == nil {
				t.Errorf("%v: want an error", c.args)
			}
			continue
		}
		if err != nil || opts.demo != c.demo || opts.demoCase != c.demoCase {
			t.Errorf("%v: demo=%v case=%q err=%v", c.args, opts.demo, opts.demoCase, err)
		}
	}
}
