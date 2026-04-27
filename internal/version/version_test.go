package version

import (
	"fmt"
	"regexp"
	"testing"
)

func TestVersionUsesCanonicalFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9]\.[0-9]{2}\.[0-9]{3}\.[0-9A-Z]{4}$`)
	if !pattern.MatchString(Version) {
		t.Fatalf("version %q does not match M.mm.ppp.BBBB", Version)
	}
}

func TestVersionMatchesComponents(t *testing.T) {
	buildPattern := regexp.MustCompile(`^[0-9A-Z]{4}$`)
	if !buildPattern.MatchString(BuildCode) {
		t.Fatalf("build code %q is not four-character uppercase base36", BuildCode)
	}

	expected := fmt.Sprintf("%d.%02d.%03d.%s", Major, Minor, Patch, BuildCode)
	if Version != expected {
		t.Fatalf("version: got %q want %q", Version, expected)
	}
}
