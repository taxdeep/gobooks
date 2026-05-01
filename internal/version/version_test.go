package version

import (
	"fmt"
	"regexp"
	"testing"
)

func TestVersionUsesCanonicalFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9]\.[0-9A-Z]{2}\.[0-9]{3}\.[0-9A-Z]{4}$`)
	if !pattern.MatchString(Version) {
		t.Fatalf("version %q does not match X.YY.XXX.YYYY", Version)
	}
}

func TestVersionMatchesComponents(t *testing.T) {
	channelPattern := regexp.MustCompile(`^[0-9A-Z]{2}$`)
	buildPattern := regexp.MustCompile(`^[0-9A-Z]{4}$`)
	if !channelPattern.MatchString(ChannelCode) {
		t.Fatalf("channel code %q is not two-character uppercase base36", ChannelCode)
	}
	if !buildPattern.MatchString(BuildCode) {
		t.Fatalf("build code %q is not four-character uppercase base36", BuildCode)
	}

	expected := fmt.Sprintf("%d.%s.%03d.%s", Major, ChannelCode, Patch, BuildCode)
	if Version != expected {
		t.Fatalf("version: got %q want %q", Version, expected)
	}
}
