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
