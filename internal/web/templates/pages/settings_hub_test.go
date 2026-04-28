package pages

import (
	"context"
	"strings"
	"testing"
)

func TestSettingsHubIncludesPrintTemplatesEntry(t *testing.T) {
	var sb strings.Builder
	vm := SettingsHubVM{HasCompany: true}

	if err := SettingsHub(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render settings hub: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		"Print Templates",
		`href="/pdf-templates"`,
		"Manage PDF and print templates",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected settings hub HTML to contain %q", want)
		}
	}
}
