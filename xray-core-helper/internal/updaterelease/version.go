// Package updaterelease resolves published helper releases and stages a
// verified, unpacked release bundle. It performs no installation and does not
// execute any candidate code.
package updaterelease

import (
	"errors"
	"strings"
)

const versionPrefix = "helper-v"

// ErrInvalidVersion reports a release version that is not a helper-prefixed,
// strict semantic version.
var ErrInvalidVersion = errors.New("invalid release version")

type parsedVersion struct {
	core       [3]string
	prerelease []string
}

// ValidateVersion reports whether version is a "helper-v" prefixed strict
// semantic version. Build metadata is accepted but has no precedence.
func ValidateVersion(version string) error {
	_, err := parseVersion(version)
	return err
}

// Compare orders two helper release versions using semantic version precedence.
// It returns -1, 0 or 1. Build metadata is ignored.
func Compare(a, b string) (int, error) {
	first, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	second, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for index := range first.core {
		if result := compareNumeric(first.core[index], second.core[index]); result != 0 {
			return result, nil
		}
	}
	return comparePrerelease(first.prerelease, second.prerelease), nil
}

func parseVersion(value string) (parsedVersion, error) {
	var version parsedVersion
	if !strings.HasPrefix(value, versionPrefix) {
		return version, ErrInvalidVersion
	}
	rest := strings.TrimPrefix(value, versionPrefix)
	if rest == "" || strings.ContainsAny(rest, " \t\r\n") {
		return version, ErrInvalidVersion
	}

	coreAndPrerelease := rest
	if index := strings.IndexByte(coreAndPrerelease, '+'); index >= 0 {
		build := coreAndPrerelease[index+1:]
		coreAndPrerelease = coreAndPrerelease[:index]
		if build == "" || strings.ContainsRune(build, '+') {
			return version, ErrInvalidVersion
		}
		if !validIdentifiers(build, false) {
			return version, ErrInvalidVersion
		}
	}

	core := coreAndPrerelease
	if index := strings.IndexByte(coreAndPrerelease, '-'); index >= 0 {
		prerelease := coreAndPrerelease[index+1:]
		core = coreAndPrerelease[:index]
		if prerelease == "" || !validIdentifiers(prerelease, true) {
			return version, ErrInvalidVersion
		}
		version.prerelease = strings.Split(prerelease, ".")
	}

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version, ErrInvalidVersion
	}
	for index, part := range parts {
		if !validNumeric(part) {
			return version, ErrInvalidVersion
		}
		version.core[index] = part
	}
	return version, nil
}

func validIdentifiers(value string, rejectLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		for index := 0; index < len(identifier); index++ {
			character := identifier[index]
			if character >= '0' && character <= '9' ||
				character >= 'a' && character <= 'z' ||
				character >= 'A' && character <= 'Z' ||
				character == '-' {
				continue
			}
			return false
		}
		if rejectLeadingZero && len(identifier) > 1 && identifier[0] == '0' &&
			isDigits(identifier) {
			return false
		}
	}
	return true
}

func validNumeric(value string) bool {
	return value != "" && (len(value) == 1 || value[0] != '0') && isDigits(value)
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func compareNumeric(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

func comparePrerelease(a, b []string) int {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}
	for index := 0; index < len(a) && index < len(b); index++ {
		firstNumeric := validNumeric(a[index])
		secondNumeric := validNumeric(b[index])
		var result int
		switch {
		case firstNumeric && secondNumeric:
			result = compareNumeric(a[index], b[index])
		case firstNumeric:
			result = -1
		case secondNumeric:
			result = 1
		default:
			result = strings.Compare(a[index], b[index])
		}
		if result != 0 {
			return result
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
