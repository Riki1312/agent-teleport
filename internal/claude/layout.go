package claude

import "github.com/Riki1312/agent-teleport/internal/semver"

// layout describes the transcript format written by a range of Claude Code
// versions.
type layout struct {
	// minimum is the oldest Claude Code version supported with this layout.
	minimum string
	// newestTested is the newest version verified by the two-home test. Newer
	// versions are accepted with a warning while their transcripts still pass
	// structural validation.
	newestTested string
}

// layouts lists the supported transcript layouts, oldest first. Each layout is
// verified by the two-home test against every Claude Code version in CI.
var layouts = []layout{
	{
		minimum:      "2.1.317",
		newestTested: "2.1.321",
	},
}

// layoutFor returns the layout of a Claude Code version and whether that
// version is within the tested range. Versions older than every layout fail
// closed.
func layoutFor(version string) (layout, bool, error) {
	return semver.Select("Claude Code", version, layouts, func(candidate layout) semver.Range {
		return semver.Range{Minimum: candidate.minimum, NewestTested: candidate.newestTested}
	})
}
