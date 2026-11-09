package e2e_test

// The tests build agent-teleport with go build, which Go's test cache cannot
// see. Depending on the CLI package makes every source file of the binary part
// of the cache key, so a cached result never outlives a change to the code it
// tested.
import _ "github.com/Riki1312/agent-teleport/internal/cli"
