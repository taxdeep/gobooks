package layout

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"gobooks/internal/web/templates/ui"
)

func renderLayoutComponent(t *testing.T, c templ.Component) string {
	t.Helper()

	var sb strings.Builder
	if err := c.Render(context.Background(), &sb); err != nil {
		t.Fatalf("render layout: %v", err)
	}
	return sb.String()
}

func TestLayout_UsesSmartPickerV8(t *testing.T) {
	html := renderLayoutComponent(t, Layout("Test", ui.SidebarVM{}, templ.NopComponent))
	if !strings.Contains(html, `/static/smart_picker.js?v=9`) {
		t.Fatalf("expected main layout to reference smart_picker.js?v=9, got %q", html)
	}
	if !strings.Contains(html, `window.gobooksFetch`) {
		t.Fatalf("expected main layout to expose gobooksFetch, got %q", html)
	}
}

func TestLayoutAuth_UsesSmartPickerV8(t *testing.T) {
	html := renderLayoutComponent(t, LayoutAuth("Auth", templ.NopComponent))
	if !strings.Contains(html, `/static/smart_picker.js?v=9`) {
		t.Fatalf("expected auth layout to reference smart_picker.js?v=9, got %q", html)
	}
	if !strings.Contains(html, `window.gobooksFetch`) {
		t.Fatalf("expected auth layout to expose gobooksFetch, got %q", html)
	}
}
