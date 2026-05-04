package layout

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"

	"balanciz/internal/web/templates/ui"
)

type fakeUserValueContext struct {
	values map[interface{}]interface{}
}

func (f *fakeUserValueContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (f *fakeUserValueContext) Done() <-chan struct{}       { return nil }
func (f *fakeUserValueContext) Err() error                  { return nil }
func (f *fakeUserValueContext) Value(key interface{}) interface{} {
	if f.values == nil {
		return nil
	}
	return f.values[key]
}
func (f *fakeUserValueContext) SetUserValue(key interface{}, value interface{}) {
	if f.values == nil {
		f.values = make(map[interface{}]interface{})
	}
	f.values[key] = value
}

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
	if !strings.Contains(html, `/static/doc_item_picker.js?v=3`) {
		t.Fatalf("expected main layout to reference doc_item_picker.js?v=3, got %q", html)
	}
	if !strings.Contains(html, `/static/doc_transaction_editor.js?v=5`) {
		t.Fatalf("expected main layout to reference doc_transaction_editor.js?v=5, got %q", html)
	}
	if !strings.Contains(html, `window.balancizFetch`) {
		t.Fatalf("expected main layout to expose balancizFetch, got %q", html)
	}
}

func TestLayoutAuth_UsesSmartPickerV8(t *testing.T) {
	html := renderLayoutComponent(t, LayoutAuth("Auth", templ.NopComponent))
	if !strings.Contains(html, `/static/smart_picker.js?v=9`) {
		t.Fatalf("expected auth layout to reference smart_picker.js?v=9, got %q", html)
	}
	if !strings.Contains(html, `window.balancizFetch`) {
		t.Fatalf("expected auth layout to expose balancizFetch, got %q", html)
	}
}

func TestLayout_RendersCompanySwitcherFromFiberContext(t *testing.T) {
	reqCtx := &fakeUserValueContext{}
	ui.AttachSidebarData(reqCtx, ui.SidebarData{
		CompanyName: "Apexsolu Limited",
		PlanName:    "Starter",
		SwitcherRows: []ui.SwitcherRow{
			{CompanyIDStr: "1", Name: "Apexsolu Limited", IsActive: true},
			{CompanyIDStr: "2", Name: "BizEdge LLC"},
		},
	})

	var sb strings.Builder
	if err := Layout("Test", ui.SidebarVM{HasCompany: true}, templ.NopComponent).Render(reqCtx, &sb); err != nil {
		t.Fatalf("render layout: %v", err)
	}

	html := sb.String()
	if !strings.Contains(html, "Apexsolu Limited") {
		t.Fatalf("expected company name in top-bar switcher, got %q", html)
	}
	if !strings.Contains(html, "Switch business to...") {
		t.Fatalf("expected switcher dropdown heading, got %q", html)
	}
	if !strings.Contains(html, `/select-company`) {
		t.Fatalf("expected company switch forms, got %q", html)
	}
}
