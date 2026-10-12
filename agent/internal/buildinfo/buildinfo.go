package buildinfo

import (
	"fmt"
	"regexp"
	"strings"
)

// Version and Repository are set by the release build's linker flags.
var (
	Version    = "dev"
	Repository = ""
)

var stableTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Display returns a concise human-readable build identity.
func Display() string {
	if Version == "" || Version == "dev" {
		return "cf-san development build"
	}
	if Repository == "" {
		return fmt.Sprintf("cf-san %s", Version)
	}
	return fmt.Sprintf("cf-san %s (%s)", Version, Repository)
}

// CompareStableTags compares two stable vMAJOR.MINOR.PATCH tags numerically.
// Prerelease tags, build metadata, and components with leading zeroes are not
// accepted. Decimal components are compared as strings to avoid integer-size
// limits on otherwise valid numeric components.
func CompareStableTags(a, b string) (int, error) {
	left, err := parseStableTag(a)
	if err != nil {
		return 0, err
	}
	right, err := parseStableTag(b)
	if err != nil {
		return 0, err
	}
	for i := range left {
		if comparison := compareDecimal(left[i], right[i]); comparison != 0 {
			return comparison, nil
		}
	}
	return 0, nil
}

func parseStableTag(tag string) ([3]string, error) {
	match := stableTag.FindStringSubmatch(tag)
	if match == nil {
		return [3]string{}, fmt.Errorf("invalid stable version tag %q: expected vMAJOR.MINOR.PATCH without leading zeroes", tag)
	}
	return [3]string{match[1], match[2], match[3]}, nil
}

func compareDecimal(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	switch strings.Compare(a, b) {
	case -1:
		return -1
	case 1:
		return 1
	default:
		return 0
	}
}
