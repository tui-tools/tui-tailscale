package headscale

import (
	"fmt"
	"regexp"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// A created pre-auth key is shown once and never stored (issue #30). Where
// the terminal is too narrow for the key's line, cutting it would show a key
// that headscale rejects, and printing it anywhere the tool reads back
// (--check, a log, the status line) would store it. The one alternative is
// one the operator picks explicitly and sees previewed: the key written once
// to a root-only file under /run, which is a tmpfs, so it is gone at the next
// boot at the latest.

// ShownOnceKeyDir is where the shown-once key file goes.
const ShownOnceKeyDir = "/run/tui-tailscale"

// preAuthKeyPattern is what a pre-auth key looks like: headscale's
// hskey-auth-<prefix>-<secret> since 0.26, a hex string before it. Nothing a
// shell or a path could read differently fits it.
var preAuthKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,255}$`)

// ValidPreAuthKey reports whether a value looks like a pre-auth key.
func ValidPreAuthKey(key string) bool { return preAuthKeyPattern.MatchString(key) }

// ShownOnceKeyPath is the file a key created at a given moment is written to.
// The name carries the time, not any part of the key.
func ShownOnceKeyPath(now time.Time) string {
	return ShownOnceKeyDir + "/preauth-" + now.Format("20060102-150405") + ".key"
}

// BuildWriteShownOnceKey assembles the write of a shown-once key to path:
// `install -D -m 600 /dev/stdin <path>`, owned by root. The key goes on
// stdin, which neither the preview nor the process list shows.
func BuildWriteShownOnceKey(key, path string) (runner.Command, error) {
	if !ValidPreAuthKey(key) {
		return runner.Command{}, fmt.Errorf("not a pre-auth key: nothing written")
	}
	return runner.Command{
		Argv: []string{"install", "-D", "-m", "600", "/dev/stdin", path},
		Description: "Write the pre-auth key to " + path +
			" (root only, mode 600)",
		Stdin: key + "\n",
	}, nil
}
