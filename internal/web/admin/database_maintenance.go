// 遵循project_guide.md
package admin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
	"balanciz/internal/web/templates/admintmpl"
)

const (
	adminDatabaseBackupTimeout   = 5 * time.Minute
	adminDatabaseOptimizeTimeout = 2 * time.Minute
	adminDatabaseBackupLimit     = 5
)

type databaseBackupResult struct {
	Name     string
	Size     int64
	Duration time.Duration
}

func (s *Server) adminDatabaseMaintenanceVM() admintmpl.AdminDatabaseMaintenanceVM {
	driver := s.databaseDriverName()
	vm := admintmpl.AdminDatabaseMaintenanceVM{
		Driver:             driver,
		BackupDir:          adminDatabaseBackupDir(),
		Backups:            adminListDatabaseBackups(adminDatabaseBackupLimit),
		BackupAvailable:    driver == "postgres",
		OptimizeAvailable:  driver == "postgres" || driver == "sqlite",
		BackupDisabledNote: "",
		OptimizeNote:       adminDatabaseOptimizeNote(driver),
	}
	if driver != "postgres" {
		vm.BackupAvailable = false
		vm.BackupDisabledNote = "Database backup currently supports PostgreSQL via pg_dump."
		return vm
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		vm.BackupAvailable = false
		vm.BackupDisabledNote = "pg_dump is not available on this server. Install PostgreSQL client tools to enable backups."
	}
	return vm
}

func (s *Server) handleAdminDatabaseBackup(c *fiber.Ctx) error {
	actor := AdminUserFromCtx(c).Email
	started := time.Now()
	result, err := s.createDatabaseBackup(context.Background())
	if err != nil {
		services.TryWriteAuditLog(s.DB, "admin.system.database_backup_failed", "system", 0, actor,
			map[string]any{
				"actor_type": "sysadmin",
				"driver":     s.databaseDriverName(),
				"error":      err.Error(),
			},
		)
		return c.Redirect("/admin/system?flash="+adminDatabaseBackupFlash(err), fiber.StatusSeeOther)
	}

	services.TryWriteAuditLog(s.DB, "admin.system.database_backup_created", "system", 0, actor,
		map[string]any{
			"actor_type":  "sysadmin",
			"driver":      s.databaseDriverName(),
			"file":        result.Name,
			"size_bytes":  result.Size,
			"duration_ms": result.Duration.Milliseconds(),
			"started_at":  started.Format(time.RFC3339),
		},
	)

	return c.Redirect("/admin/system?flash=db_backup_ok", fiber.StatusSeeOther)
}

func (s *Server) handleAdminDatabaseBackupDownload(c *fiber.Ctx) error {
	name := strings.TrimSpace(c.Params("name"))
	if !adminSafeDatabaseBackupName(name) {
		return fiber.ErrNotFound
	}

	root := filepath.Clean(adminDatabaseBackupDir())
	path := filepath.Clean(filepath.Join(root, name))
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fiber.ErrNotFound
	}
	if _, err := os.Stat(path); err != nil {
		return fiber.ErrNotFound
	}
	return c.Download(path, name)
}

func (s *Server) handleAdminDatabaseOptimize(c *fiber.Ctx) error {
	actor := AdminUserFromCtx(c).Email
	started := time.Now()
	if err := s.optimizeDatabase(context.Background()); err != nil {
		services.TryWriteAuditLog(s.DB, "admin.system.database_optimize_failed", "system", 0, actor,
			map[string]any{
				"actor_type": "sysadmin",
				"driver":     s.databaseDriverName(),
				"error":      err.Error(),
			},
		)
		return c.Redirect("/admin/system?flash="+adminDatabaseOptimizeFlash(err), fiber.StatusSeeOther)
	}

	services.TryWriteAuditLog(s.DB, "admin.system.database_optimized", "system", 0, actor,
		map[string]any{
			"actor_type":  "sysadmin",
			"driver":      s.databaseDriverName(),
			"duration_ms": time.Since(started).Milliseconds(),
		},
	)
	return c.Redirect("/admin/system?flash=db_optimize_ok", fiber.StatusSeeOther)
}

func (s *Server) createDatabaseBackup(parent context.Context) (databaseBackupResult, error) {
	if s.databaseDriverName() != "postgres" {
		return databaseBackupResult{}, errDatabaseBackupUnsupported
	}
	pgDump, err := exec.LookPath("pg_dump")
	if err != nil {
		return databaseBackupResult{}, errDatabaseBackupMissingTool
	}

	dir := adminDatabaseBackupDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return databaseBackupResult{}, fmt.Errorf("create backup directory: %w", err)
	}

	now := time.Now().UTC()
	name := fmt.Sprintf("balanciz_%s_%s.sql", adminSanitizeBackupPart(s.Cfg.DBName), now.Format("20060102_150405_000000000"))
	path := filepath.Join(dir, name)

	ctx, cancel := context.WithTimeout(parent, adminDatabaseBackupTimeout)
	defer cancel()

	args := []string{
		"--host", s.Cfg.DBHost,
		"--port", s.Cfg.DBPort,
		"--username", s.Cfg.DBUser,
		"--format", "plain",
		"--no-owner",
		"--no-privileges",
		"--file", path,
		s.Cfg.DBName,
	}
	cmd := exec.CommandContext(ctx, pgDump, args...)
	if s.Cfg.DBPassword != "" {
		cmd.Env = append(os.Environ(), "PGPASSWORD="+s.Cfg.DBPassword)
	}

	started := time.Now()
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		_ = os.Remove(path)
		return databaseBackupResult{}, errDatabaseBackupTimeout
	}
	if err != nil {
		_ = os.Remove(path)
		return databaseBackupResult{}, fmt.Errorf("pg_dump failed: %w: %s", err, adminLimitCommandOutput(out))
	}

	info, err := os.Stat(path)
	if err != nil {
		return databaseBackupResult{}, fmt.Errorf("stat backup file: %w", err)
	}
	return databaseBackupResult{
		Name:     name,
		Size:     info.Size(),
		Duration: time.Since(started),
	}, nil
}

func (s *Server) optimizeDatabase(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, adminDatabaseOptimizeTimeout)
	defer cancel()

	switch s.databaseDriverName() {
	case "postgres":
		if err := s.DB.WithContext(ctx).Exec("VACUUM (ANALYZE)").Error; err != nil {
			return fmt.Errorf("vacuum analyze failed: %w", err)
		}
	case "sqlite":
		if err := s.DB.WithContext(ctx).Exec("VACUUM").Error; err != nil {
			return fmt.Errorf("vacuum failed: %w", err)
		}
		if err := s.DB.WithContext(ctx).Exec("ANALYZE").Error; err != nil {
			return fmt.Errorf("analyze failed: %w", err)
		}
	default:
		return errDatabaseOptimizeUnsupported
	}

	if ctx.Err() == context.DeadlineExceeded {
		return errDatabaseOptimizeTimeout
	}
	return nil
}

func (s *Server) databaseDriverName() string {
	if s == nil || s.DB == nil || s.DB.Dialector == nil {
		return "unknown"
	}
	return s.DB.Dialector.Name()
}

func adminDatabaseBackupDir() string {
	return filepath.Join("data", "backups")
}

func adminListDatabaseBackups(limit int) []admintmpl.AdminDatabaseBackupVM {
	entries, err := os.ReadDir(adminDatabaseBackupDir())
	if err != nil {
		return nil
	}
	backups := make([]admintmpl.AdminDatabaseBackupVM, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !adminSafeDatabaseBackupName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := entry.Name()
		backups = append(backups, admintmpl.AdminDatabaseBackupVM{
			Name:        name,
			SizeBytes:   info.Size(),
			CreatedAt:   info.ModTime(),
			DownloadURL: "/admin/system/backups/" + url.PathEscape(name),
		})
	}
	sort.SliceStable(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})
	if limit > 0 && len(backups) > limit {
		backups = backups[:limit]
	}
	return backups
}

func adminSafeDatabaseBackupName(name string) bool {
	if !strings.HasPrefix(name, "balanciz_") || !strings.HasSuffix(name, ".sql") {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func adminSanitizeBackupPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "database"
	}
	return b.String()
}

func adminLimitCommandOutput(out []byte) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "pg_dump exited with an error and no output"
	}
	if len(text) > 500 {
		return text[:500] + "..."
	}
	return text
}

func adminDatabaseOptimizeNote(driver string) string {
	switch driver {
	case "postgres":
		return "Runs VACUUM (ANALYZE) to refresh planner statistics and reclaim reusable space."
	case "sqlite":
		return "Runs VACUUM and ANALYZE for local SQLite environments."
	default:
		return "Optimization is available for PostgreSQL and SQLite only."
	}
}

func adminDatabaseBackupFlash(err error) string {
	switch {
	case errors.Is(err, errDatabaseBackupMissingTool):
		return "db_backup_missing_tool"
	case errors.Is(err, errDatabaseBackupUnsupported):
		return "db_backup_unsupported"
	case errors.Is(err, errDatabaseBackupTimeout):
		return "db_backup_timeout"
	default:
		return "db_backup_err"
	}
}

func adminDatabaseOptimizeFlash(err error) string {
	switch {
	case errors.Is(err, errDatabaseOptimizeUnsupported):
		return "db_optimize_unsupported"
	case errors.Is(err, errDatabaseOptimizeTimeout):
		return "db_optimize_timeout"
	default:
		return "db_optimize_err"
	}
}

var (
	errDatabaseBackupMissingTool   = errors.New("pg_dump is not available")
	errDatabaseBackupUnsupported   = errors.New("database backup is only supported for PostgreSQL")
	errDatabaseBackupTimeout       = errors.New("database backup timed out")
	errDatabaseOptimizeUnsupported = errors.New("database optimization is not supported for this database driver")
	errDatabaseOptimizeTimeout     = errors.New("database optimization timed out")
)
