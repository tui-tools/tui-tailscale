package headscale

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tui-tools/tui-kit/runner"
)

// The commands the users, nodes and pre-auth keys screens preview and run.
// Each builder validates what it is given before it becomes an argv, so a
// value can never turn into a second argument or a flag.

// BuildExpireNode assembles `headscale nodes expire --identifier <id>`.
func BuildExpireNode(nodeID string) (runner.Command, error) {
	if !validID(nodeID) {
		return runner.Command{}, fmt.Errorf("not a valid node id: %q", nodeID)
	}
	return runner.Command{
		Argv:        []string{"headscale", "nodes", "expire", "--identifier", nodeID},
		Description: "Expire node " + nodeID,
		Destructive: true,
	}, nil
}

// BuildCreateUser assembles `headscale users create <name>`.
func BuildCreateUser(name string) (runner.Command, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\n/") {
		return runner.Command{}, fmt.Errorf("not a valid user name: %q", name)
	}
	return runner.Command{
		Argv:        []string{"headscale", "users", "create", name},
		Description: "Create user " + name,
	}, nil
}

// BuildCreatePreAuthKey assembles `headscale preauthkeys create`. The created
// key is printed once by headscale itself; the caller shows it once and never
// stores it — the same contract headscale's own CLI has.
func BuildCreatePreAuthKey(userID string, reusable, ephemeral bool, expiration string) (runner.Command, error) {
	if !validID(userID) {
		return runner.Command{}, fmt.Errorf("not a valid user id: %q", userID)
	}
	if expiration == "" {
		expiration = "24h"
	}
	if !ValidExpiration(expiration) {
		return runner.Command{}, fmt.Errorf("not a valid expiration (try 24h, 30m, 7d): %q", expiration)
	}
	argv := []string{"headscale", "preauthkeys", "create", "--user", userID}
	if reusable {
		argv = append(argv, "--reusable")
	}
	if ephemeral {
		argv = append(argv, "--ephemeral")
	}
	argv = append(argv, "--expiration", expiration)
	return runner.Command{
		Argv:        argv,
		Description: "Create pre-auth key for user " + userID,
	}, nil
}

// BuildDeleteNode assembles `headscale nodes delete`. --force skips
// headscale's own prompt because this tool's confirm dialog already is the
// prompt, and a nested interactive question would hang the runner.
func BuildDeleteNode(nodeID string) (runner.Command, error) {
	if !validID(nodeID) {
		return runner.Command{}, fmt.Errorf("not a valid node id: %q", nodeID)
	}
	return runner.Command{
		Argv:        []string{"headscale", "nodes", "delete", "--identifier", nodeID, "--force"},
		Description: "Delete node " + nodeID,
		Destructive: true,
	}, nil
}

// BuildRenameNode assembles `headscale nodes rename`.
func BuildRenameNode(nodeID, name string) (runner.Command, error) {
	if !validID(nodeID) {
		return runner.Command{}, fmt.Errorf("not a valid node id: %q", nodeID)
	}
	if !ValidNodeName(name) {
		return runner.Command{}, fmt.Errorf("not a valid node name: %q", name)
	}
	return runner.Command{
		Argv:        []string{"headscale", "nodes", "rename", "--identifier", nodeID, name},
		Description: "Rename node " + nodeID + " to " + name,
	}, nil
}

// expirationPattern is a simple duration: an integer and one unit letter, the
// forms headscale documents (30m, 24h, 7d…).
var expirationPattern = regexp.MustCompile(`^[0-9]{1,5}[smhdwy]$`)

// ValidExpiration reports whether s is a plausible pre-auth key expiration.
func ValidExpiration(s string) bool { return expirationPattern.MatchString(s) }

// nodeNamePattern is a DNS-label-shaped machine name, which is what headscale
// accepts for a rename.
var nodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// ValidNodeName reports whether s is a plausible node name.
func ValidNodeName(s string) bool { return nodeNamePattern.MatchString(s) }

// validID reports whether s is a bare non-negative integer id.
func validID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
