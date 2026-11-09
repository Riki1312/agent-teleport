package semver

import "testing"

func TestCompareOrdersReleasesAndPrereleases(t *testing.T) {
	ordered := []string{"0.9.10", "0.10.0-alpha.1", "0.10.0", "0.10.1", "2.1.319"}
	for index := 1; index < len(ordered); index++ {
		lower, higher := MustParse(ordered[index-1]), MustParse(ordered[index])

		// when, then
		if lower.Compare(higher) != -1 || higher.Compare(lower) != 1 || higher.Compare(higher) != 0 {
			t.Fatalf("%s and %s are not ordered", ordered[index-1], ordered[index])
		}
	}
}

func TestParseRejectsOtherShapes(t *testing.T) {
	for _, value := range []string{"", "1.2", "v1.2.3", "1.x.3", "1.2.3.4", "1.-2.3"} {
		// when
		_, err := Parse(value)

		// then
		if err == nil {
			t.Fatalf("Parse(%q) succeeded", value)
		}
	}
}
