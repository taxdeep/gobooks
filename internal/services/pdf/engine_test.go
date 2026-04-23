// 遵循project_guide.md
package pdf

import (
	"strings"
	"testing"
)

func TestBase64EncodeRoundsTrip(t *testing.T) {
	got := base64Encode("hello")
	if got != "aGVsbG8=" {
		t.Errorf("base64Encode: got %q want %q", got, "aGVsbG8=")
	}
}

func TestConfigureEngineStoresValues(t *testing.T) {
	// Save and restore so this test doesn't pollute later tests.
	saved := engineConfig
	defer func() { engineConfig = saved }()

	ConfigureEngine(EngineConfig{
		MaxConcurrent: 4,
		ChromePath:    "/custom/chrome",
		HeadlessFlag:  "old",
	})
	if engineConfig.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent: got %d want 4", engineConfig.MaxConcurrent)
	}
	if engineConfig.ChromePath != "/custom/chrome" {
		t.Errorf("ChromePath: got %q", engineConfig.ChromePath)
	}
	if engineConfig.HeadlessFlag != "old" {
		t.Errorf("HeadlessFlag: got %q", engineConfig.HeadlessFlag)
	}
}

func TestRenderHTMLOutputIsBase64Encodable(t *testing.T) {
	// Quick sanity: every HTML output the renderer produces must round-trip
	// through base64 without panic — the engine wraps it as a data: URL.
	in := RenderInput{
		DocumentType: "invoice",
		Schema:       DefaultSchema(),
	}
	html, err := RenderHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64Encode(html)
	if !strings.HasPrefix(encoded, "PCFkb2N0") { // base64("<!doct…")
		t.Errorf("base64 round-trip prefix unexpected: %q", encoded[:32])
	}
}
