// Package semver parses and orders provider release versions such as 0.167.0,
// 2.1.319, or 0.168.0-alpha.1.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed release version. Prerelease labels are not ordered among
// themselves; any prerelease sorts before its release.
type Version struct {
	numbers    [3]int
	prerelease bool
}

// Parse parses a MAJOR.MINOR.PATCH version with an optional prerelease suffix.
func Parse(value string) (Version, error) {
	release, prerelease, _ := strings.Cut(value, "-")
	parts := strings.Split(release, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("unrecognized version %q", value)
	}
	parsed := Version{prerelease: prerelease != ""}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return Version{}, fmt.Errorf("unrecognized version %q", value)
		}
		parsed.numbers[index] = number
	}
	return parsed, nil
}

// MustParse parses a version known at compile time and panics if it is invalid.
func MustParse(value string) Version {
	parsed, err := Parse(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

// Compare returns -1, 0, or 1 as v sorts before, equal to, or after other.
func (v Version) Compare(other Version) int {
	for index := range v.numbers {
		if v.numbers[index] != other.numbers[index] {
			if v.numbers[index] < other.numbers[index] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.prerelease == other.prerelease:
		return 0
	case v.prerelease:
		return -1
	default:
		return 1
	}
}

// Range is the span of versions that one storage layout supports: every
// version from Minimum on, tested through NewestTested.
type Range struct {
	Minimum      string
	NewestTested string
}

// Select returns the candidate with the newest range whose minimum is at most
// version, and whether version is within that range's tested versions.
// Candidates are ordered oldest first; versions older than every range fail
// closed with an error that names product.
func Select[T any](product, version string, candidates []T, rangeOf func(T) Range) (T, bool, error) {
	var none T
	parsed, err := Parse(version)
	if err != nil {
		return none, false, fmt.Errorf("%s: %w", product, err)
	}
	for index := len(candidates) - 1; index >= 0; index-- {
		bounds := rangeOf(candidates[index])
		if parsed.Compare(MustParse(bounds.Minimum)) >= 0 {
			return candidates[index], parsed.Compare(MustParse(bounds.NewestTested)) <= 0, nil
		}
	}
	return none, false, fmt.Errorf("%s %s is unsupported; agent-teleport supports %s %s or newer", product, version, product, rangeOf(candidates[0]).Minimum)
}
