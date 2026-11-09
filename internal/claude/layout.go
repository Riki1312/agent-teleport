package claude

import (
	"fmt"

	"github.com/Riki1312/agent-teleport/internal/semver"
)

// layout describes the transcript format written by a range of Claude Code
// versions.
type layout struct {
	// minimum is the oldest Claude Code version supported with this layout.
	minimum string
	// newestTested is the newest version verified by the two-home test. Newer
	// versions are accepted with a warning while their transcripts still pass
	// structural validation.
	newestTested string
	// environmentAttachments are the attachment types that record the
	// session's environment, such as its working directory, system prompt, and
	// instruction files. Claude Code replays them when it resumes a session, so
	// import moves every workspace path they mention.
	environmentAttachments map[string]struct{}
}

// layouts lists the supported transcript layouts, oldest first. Each layout is
// verified by the two-home test against every Claude Code version in CI.
var layouts = []layout{
	{
		minimum:      "2.1.317",
		newestTested: "2.1.319",
		environmentAttachments: map[string]struct{}{
			"environment":     {},
			"instructions":    {},
			"prompt_snapshot": {},
		},
	},
}

// layoutFor returns the layout of a Claude Code version and whether that
// version is within the tested range. Versions older than every layout fail
// closed.
func layoutFor(version string) (layout, bool, error) {
	parsed, err := semver.Parse(version)
	if err != nil {
		return layout{}, false, fmt.Errorf("Claude Code: %w", err)
	}
	for index := len(layouts) - 1; index >= 0; index-- {
		candidate := layouts[index]
		if parsed.Compare(semver.MustParse(candidate.minimum)) >= 0 {
			tested := parsed.Compare(semver.MustParse(candidate.newestTested)) <= 0
			return candidate, tested, nil
		}
	}
	return layout{}, false, fmt.Errorf("Claude Code %s is unsupported; agent-teleport supports Claude Code %s or newer", version, layouts[0].minimum)
}

func (l layout) isEnvironmentAttachment(attachmentType string) bool {
	_, ok := l.environmentAttachments[attachmentType]
	return ok
}
