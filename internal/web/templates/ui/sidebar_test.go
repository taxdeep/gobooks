package ui_test

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/web/templates/ui"
)

func TestSidebarSettingsIncludesTemplatesEntry(t *testing.T) {
	var sb strings.Builder
	if err := ui.Sidebar(ui.SidebarVM{Active: "Templates", HasCompany: true}).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render sidebar: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		`href="/settings/templates"`,
		"Templates",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected sidebar HTML to contain %q", want)
		}
	}
}
