//go:build !unix

package codex

import "errors"

// lockThread fails closed on platforms without flock, where agent-teleport
// cannot coordinate with running Codex processes.
func lockThread(string, string) (func(), error) {
	return nil, errors.New("updating Codex sessions is supported only on Linux and macOS")
}
