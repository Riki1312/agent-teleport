//go:build !unix

package claude

// processAlive cannot check processes on this platform, so every registered
// process counts as alive and its session is never changed.
func processAlive(int) bool {
	return true
}
