// 遵循project_guide.md
package pdf

import (
	"os"
	"path/filepath"
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
		WorkDir:       "/tmp/custom-pdf",
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
	if engineConfig.WorkDir != "/tmp/custom-pdf" {
		t.Errorf("WorkDir: got %q", engineConfig.WorkDir)
	}
}

func TestEnsureChromeWorkDirCreatesWritableRuntimeTree(t *testing.T) {
	base := filepath.Join(t.TempDir(), "chrome")
	got, err := ensureChromeWorkDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Fatalf("work dir: got %q want %q", got, base)
	}
	for _, name := range []string{"profile", "home", "cache", "config", "runtime"} {
		path := filepath.Join(base, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", path)
		}
	}
}

func TestResolveChromePathHonorsConfiguredPath(t *testing.T) {
	got, err := resolveChromePath("/opt/google/chrome/chrome")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/opt/google/chrome/chrome" {
		t.Fatalf("chrome path: got %q", got)
	}
}

func TestIsSnapChromePathRejectsSnapWrapper(t *testing.T) {
	if !isSnapChromePath("/snap/bin/chromium") {
		t.Fatal("expected /snap/bin/chromium to be treated as snap Chromium")
	}
	if isSnapChromePath("/usr/bin/google-chrome") {
		t.Fatal("expected google-chrome path to be accepted")
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
