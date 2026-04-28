// 遵循project_guide.md
package pdf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// engine.go — chromedp HTML→PDF wrapper.
//
// One shared allocator + browser context per process. Chromedp creates a
// new tab per PDF render via chromedp.NewContext (cheap), so we never
// share a tab across requests. The browser stays alive for the program's
// lifetime; Shutdown is called from main on graceful stop.
//
// Concurrency model:
//   • Allocator + browser ctx are init-once (sync.Once) on first render.
//   • Each render derives a per-request ctx with timeout, then closes it
//     via cancel — that closes the tab, not the browser.
//   • An optional sync.Mutex bounds concurrent renders to MaxConcurrent
//     (default 2). Headless Chrome handles concurrent tabs fine, but more
//     than 2-3 simultaneous PDF renders on a small VPS quickly saturates
//     CPU; the mutex queues callers fairly.
//
// Failure modes:
//   • Chromium binary missing → first render returns a wrapped chromedp
//     error. The engine panics nowhere; callers handle.
//   • Render timeout → caller's context.DeadlineExceeded; tab is killed.
//   • Browser crash → next render re-initialises the allocator (we detect
//     a closed-context error and reset the sync.Once).

// EngineConfig tunes the chromedp engine. Zero values use sensible defaults.
type EngineConfig struct {
	// MaxConcurrent caps the number of in-flight RenderPDF calls.
	// Default: 2. Chrome handles more but a small VPS doesn't.
	MaxConcurrent int
	// RenderTimeout caps per-render time (page load + print).
	// Default: 30s.
	RenderTimeout time.Duration
	// ChromePath optionally overrides the Chrome binary path. Empty = chromedp
	// auto-detection (looks at PATH for chromium / google-chrome / etc.).
	ChromePath string
	// HeadlessFlag is the headless mode flag passed to Chrome.
	// "new" (default) uses the new Headless mode; "old" uses legacy.
	HeadlessFlag string
	// WorkDir is a writable parent directory for Chrome profile/cache/runtime
	// data. Empty = os.TempDir()/balanciz-pdf. This matters on systemd/snap
	// deployments where HOME or XDG_RUNTIME_DIR may point at unwritable paths.
	WorkDir string
}

var (
	engineOnce       sync.Once
	engineErr        error
	allocatorCtx     context.Context
	allocatorCancel  context.CancelFunc
	browserCtx       context.Context
	browserCancel    context.CancelFunc
	engineConfig     EngineConfig
	engineSemaphore  chan struct{}
	resetEngineMutex sync.Mutex
)

// ConfigureEngine sets engine config. Must be called BEFORE the first
// RenderPDF call to take effect; later calls are ignored.
func ConfigureEngine(cfg EngineConfig) {
	resetEngineMutex.Lock()
	defer resetEngineMutex.Unlock()
	engineConfig = cfg
}

// RenderPDF converts an HTML document to a PDF byte stream using chromedp.
// Use the output of RenderHTML(...) as the html input.
//
// The optional pageSize / orientation arguments override the @page rule's
// paper choice — usually leave both empty so the @page rule from the
// template wins.
func RenderPDF(ctx context.Context, html string) ([]byte, error) {
	if err := ensureEngine(); err != nil {
		return nil, err
	}

	// Concurrency cap.
	select {
	case engineSemaphore <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-engineSemaphore }()

	// Per-render context with timeout. Inherit any deadline already on ctx.
	timeout := engineConfig.RenderTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	renderCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Tab context — closes the tab, not the browser.
	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	defer tabCancel()
	tabCtx, deadline := context.WithTimeout(tabCtx, timeout)
	defer deadline()

	// Use a data: URL so the renderer never hits the network. chromedp
	// auto-base64-encodes the body for us.
	dataURL := "data:text/html;base64," + base64Encode(html)

	var pdfBytes []byte
	err := chromedp.Run(tabCtx,
		chromedp.Navigate(dataURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(c context.Context) error {
			buf, _, e := page.PrintToPDF().
				WithPrintBackground(true).
				WithPreferCSSPageSize(true).
				Do(c)
			if e != nil {
				return e
			}
			pdfBytes = buf
			return nil
		}),
	)
	if err != nil {
		// On context-closed or tab-died errors, mark the engine for
		// re-init so the NEXT render gets a fresh browser. This handles
		// Chrome crashes without operator intervention.
		if errors.Is(err, context.Canceled) || errors.Is(renderCtx.Err(), context.DeadlineExceeded) {
			return nil, err
		}
		resetEngine()
		return nil, fmt.Errorf("pdf render: %w", err)
	}
	return pdfBytes, nil
}

// Shutdown closes the browser cleanly. Idempotent. Call from graceful
// shutdown in main; subsequent RenderPDF calls re-init the engine.
func Shutdown() {
	resetEngineMutex.Lock()
	defer resetEngineMutex.Unlock()
	if browserCancel != nil {
		browserCancel()
		browserCancel = nil
	}
	if allocatorCancel != nil {
		allocatorCancel()
		allocatorCancel = nil
	}
}

func ensureEngine() error {
	engineOnce.Do(func() {
		workDir, err := ensureChromeWorkDir(engineConfig.WorkDir)
		if err != nil {
			engineErr = fmt.Errorf("pdf engine init: %w", err)
			return
		}
		opts := append([]chromedp.ExecAllocatorOption{},
			chromedp.NoFirstRun,
			chromedp.NoDefaultBrowserCheck,
			chromedp.DisableGPU,
			chromedp.UserDataDir(filepath.Join(workDir, "profile")),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-dev-shm-usage", true),
			chromedp.Flag("no-zygote", true),
			chromedp.Env(
				"HOME="+filepath.Join(workDir, "home"),
				"XDG_CACHE_HOME="+filepath.Join(workDir, "cache"),
				"XDG_CONFIG_HOME="+filepath.Join(workDir, "config"),
				"XDG_RUNTIME_DIR="+filepath.Join(workDir, "runtime"),
			),
		)
		switch engineConfig.HeadlessFlag {
		case "old":
			opts = append(opts, chromedp.Flag("headless", "old"))
		default:
			opts = append(opts, chromedp.Headless)
		}
		chromePath, err := resolveChromePath(engineConfig.ChromePath)
		if err != nil {
			engineErr = fmt.Errorf("pdf engine init: %w", err)
			return
		}
		if chromePath != "" {
			opts = append(opts, chromedp.ExecPath(chromePath))
		}
		allocatorCtx, allocatorCancel = chromedp.NewExecAllocator(context.Background(), opts...)
		browserCtx, browserCancel = chromedp.NewContext(allocatorCtx)
		// Touch the browser so chromedp actually launches Chrome now (catches
		// a missing-binary error here instead of on first render).
		if err := chromedp.Run(browserCtx); err != nil {
			engineErr = fmt.Errorf("pdf engine init: %w", err)
			return
		}
		max := engineConfig.MaxConcurrent
		if max <= 0 {
			max = 2
		}
		engineSemaphore = make(chan struct{}, max)
	})
	return engineErr
}

func ensureChromeWorkDir(configured string) (string, error) {
	base := configured
	if base == "" {
		base = os.Getenv("PDF_CHROME_WORK_DIR")
	}
	if base == "" {
		base = filepath.Join(os.TempDir(), "balanciz-pdf")
	}

	dirs := []string{
		base,
		filepath.Join(base, "profile"),
		filepath.Join(base, "home"),
		filepath.Join(base, "cache"),
		filepath.Join(base, "config"),
		filepath.Join(base, "runtime"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create chrome work dir %q: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("chmod chrome work dir %q: %w", dir, err)
		}
	}
	return base, nil
}

// ChromeExecutableAvailable reports whether the PDF engine can find a
// Chrome-family binary that is suitable for server-side rendering. Snap
// Chromium is intentionally rejected because the snap launcher often requires
// a writable login home and /run/user directory before Chrome flags/env are
// applied.
func ChromeExecutableAvailable() bool {
	_, err := resolveChromePath("")
	return err == nil
}

func resolveChromePath(configured string) (string, error) {
	if path := strings.TrimSpace(configured); path != "" {
		return path, nil
	}
	if path := strings.TrimSpace(os.Getenv("PDF_CHROME_PATH")); path != "" {
		return path, nil
	}

	for _, candidate := range chromeCandidates() {
		found, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		if isSnapChromePath(found) {
			continue
		}
		return found, nil
	}

	return "", fmt.Errorf("no non-snap Chrome/Chromium executable found; install google-chrome-stable or set PDF_CHROME_PATH to a non-snap Chrome binary")
}

func chromeCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		return []string{
			"chrome",
			"chrome.exe",
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(os.Getenv("USERPROFILE"), `AppData\Local\Google\Chrome\Application\chrome.exe`),
			filepath.Join(os.Getenv("USERPROFILE"), `AppData\Local\Chromium\Application\chrome.exe`),
		}
	default:
		return []string{
			"google-chrome-stable",
			"google-chrome",
			"google-chrome-beta",
			"google-chrome-unstable",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome",
			"/opt/google/chrome/chrome",
			"headless-shell",
			"headless_shell",
			"chromium-browser",
			"chromium",
			"chrome",
		}
	}
}

func isSnapChromePath(path string) bool {
	clean := filepath.ToSlash(path)
	if strings.Contains(clean, "/snap/") {
		return true
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return strings.Contains(filepath.ToSlash(target), "/snap/")
}

// resetEngine clears the once flag so the next call re-initialises Chrome.
// Called when chromedp returns an error suggesting the browser process died.
func resetEngine() {
	resetEngineMutex.Lock()
	defer resetEngineMutex.Unlock()
	if browserCancel != nil {
		browserCancel()
		browserCancel = nil
	}
	if allocatorCancel != nil {
		allocatorCancel()
		allocatorCancel = nil
	}
	engineOnce = sync.Once{}
	engineErr = nil
}
