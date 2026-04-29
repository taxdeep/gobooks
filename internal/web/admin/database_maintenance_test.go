// 遵循project_guide.md
package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/config"
)

func TestHandleAdminDatabaseBackup_RejectsUnsupportedDriver(t *testing.T) {
	s := &Server{
		DB:  testRebuildDB(t),
		Cfg: config.Config{Env: "test", DBName: "balanciz"},
	}
	app := fiber.New()
	app.Post("/admin/system/database/backup", withTestAdminUser(s.handleAdminDatabaseBackup))

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/admin/system/database/backup", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/system?flash=db_backup_unsupported" {
		t.Fatalf("Location = %q, want unsupported backup flash", loc)
	}
}

func TestHandleAdminDatabaseOptimize_SQLite(t *testing.T) {
	s := &Server{
		DB:  testRebuildDB(t),
		Cfg: config.Config{Env: "test"},
	}
	app := fiber.New()
	app.Post("/admin/system/database/optimize", withTestAdminUser(s.handleAdminDatabaseOptimize))

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/admin/system/database/optimize", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/system?flash=db_optimize_ok" {
		t.Fatalf("Location = %q, want optimize success flash", loc)
	}
}

func TestAdminSafeDatabaseBackupName(t *testing.T) {
	valid := []string{
		"balanciz_main_20260428_120000.sql",
		"balanciz_db-name_20260428_120000.sql",
	}
	for _, name := range valid {
		if !adminSafeDatabaseBackupName(name) {
			t.Fatalf("expected %q to be allowed", name)
		}
	}

	invalid := []string{
		"other.sql",
		"balanciz_main.dump",
		"balanciz_../main.sql",
		"balanciz_main/backup.sql",
	}
	for _, name := range invalid {
		if adminSafeDatabaseBackupName(name) {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
}
