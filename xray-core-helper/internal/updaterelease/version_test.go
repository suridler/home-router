package updaterelease

import (
	"errors"
	"testing"
)

func TestValidateVersion(t *testing.T) {
	t.Parallel()
	valid := []string{
		"helper-v0.0.0",
		"helper-v1.2.3",
		"helper-v10.20.30",
		"helper-v1.2.3-alpha",
		"helper-v1.2.3-alpha.1",
		"helper-v1.2.3-0.3.7",
		"helper-v1.2.3-x-y.7",
		"helper-v1.2.3+build.5",
		"helper-v1.2.3-rc.1+build.5",
		"helper-v999999999999999999999.2.3",
	}
	for _, version := range valid {
		version := version
		t.Run("valid/"+version, func(t *testing.T) {
			t.Parallel()
			if err := ValidateVersion(version); err != nil {
				t.Fatalf("ValidateVersion(%q) = %v", version, err)
			}
		})
	}

	invalid := []string{
		"",
		"1.2.3",
		"v1.2.3",
		"helper-1.2.3",
		"helper-v1",
		"helper-v1.2",
		"helper-v1.2.3.4",
		"helper-v01.2.3",
		"helper-v1.02.3",
		"helper-v1.2.03",
		"helper-v1.2.3-",
		"helper-v1.2.3+",
		"helper-v1.2.3-alpha..1",
		"helper-v1.2.3-alpha.01",
		"helper-v1.2.3+build..1",
		"helper-v1.2.3+bad_value",
		"helper-v1.2.3+build+two",
		"helper-v1.2.3 ",
		" helper-v1.2.3",
		"helper-v1.2.3-alpha_beta",
		"helper-v1.2.3-ä",
		"helper-v1.2.3\n",
	}
	for _, version := range invalid {
		version := version
		t.Run("invalid/"+version, func(t *testing.T) {
			t.Parallel()
			err := ValidateVersion(version)
			if !errors.Is(err, ErrInvalidVersion) {
				t.Fatalf("ValidateVersion(%q) = %v, want ErrInvalidVersion", version, err)
			}
		})
	}
}

func TestCompareSemanticVersionPrecedence(t *testing.T) {
	t.Parallel()
	ordered := []string{
		"helper-v1.0.0-alpha",
		"helper-v1.0.0-alpha.1",
		"helper-v1.0.0-alpha.beta",
		"helper-v1.0.0-beta",
		"helper-v1.0.0-beta.2",
		"helper-v1.0.0-beta.11",
		"helper-v1.0.0-rc.1",
		"helper-v1.0.0",
		"helper-v1.0.1",
		"helper-v1.1.0",
		"helper-v2.0.0",
		"helper-v10.0.0",
		"helper-v999999999999999999999.0.0",
	}
	for first := 0; first < len(ordered); first++ {
		for second := 0; second < len(ordered); second++ {
			want := sign(first - second)
			got, err := Compare(ordered[first], ordered[second])
			if err != nil {
				t.Fatalf("Compare(%q, %q) error = %v", ordered[first], ordered[second], err)
			}
			if got != want {
				t.Fatalf("Compare(%q, %q) = %d, want %d", ordered[first], ordered[second], got, want)
			}
		}
	}
}

func TestCompareNumericPrereleaseIdentifiers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		first, second string
		want          int
	}{
		{"helper-v1.0.0-alpha.2", "helper-v1.0.0-alpha.11", -1},
		{"helper-v1.0.0-alpha.999999999999999999999", "helper-v1.0.0-alpha.2", 1},
		{"helper-v1.0.0-alpha.1", "helper-v1.0.0-alpha.beta", -1},
		{"helper-v1.0.0-alpha.beta", "helper-v1.0.0-alpha.1", 1},
		{"helper-v1.0.0-alpha", "helper-v1.0.0-alpha.0", -1},
		{"helper-v1.0.0-1", "helper-v1.0.0-alpha", -1},
	}
	for _, test := range tests {
		test := test
		t.Run(test.first+" vs "+test.second, func(t *testing.T) {
			t.Parallel()
			got, err := Compare(test.first, test.second)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Compare() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestCompareIgnoresBuildMetadata(t *testing.T) {
	t.Parallel()
	tests := [][2]string{
		{"helper-v1.2.3+build.1", "helper-v1.2.3+build.2"},
		{"helper-v1.2.3-rc.1+first", "helper-v1.2.3-rc.1+second"},
		{"helper-v1.2.3+001", "helper-v1.2.3+0002"},
	}
	for _, test := range tests {
		if got, err := Compare(test[0], test[1]); err != nil || got != 0 {
			t.Fatalf("Compare(%q, %q) = %d, %v; want 0, nil", test[0], test[1], got, err)
		}
	}
}

func TestCompareRejectsInvalidVersions(t *testing.T) {
	t.Parallel()
	if got, err := Compare("1.2.3", "helper-v1.2.3"); !errors.Is(err, ErrInvalidVersion) || got != 0 {
		t.Fatalf("Compare() = %d, %v; want invalid version", got, err)
	}
	if got, err := Compare("helper-v1.2.3", "helper-v1.2.03"); !errors.Is(err, ErrInvalidVersion) || got != 0 {
		t.Fatalf("Compare() = %d, %v; want invalid version", got, err)
	}
}

func sign(value int) int {
	switch {
	case value < 0:
		return -1
	case value > 0:
		return 1
	default:
		return 0
	}
}
