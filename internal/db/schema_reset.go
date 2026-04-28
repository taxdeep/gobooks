// 遵循project_guide.md
package db

import (
	"fmt"

	"gorm.io/gorm"
)

// ResetTarget describes the PostgreSQL database and schema a destructive reset will affect.
type ResetTarget struct {
	DatabaseName string
	SchemaName   string
	SessionUser  string
}

// QueryResetTarget returns current_database(), current_schema(), and current_user for disclosure before reset.
func QueryResetTarget(db *gorm.DB) (ResetTarget, error) {
	var t ResetTarget
	row := db.Raw(`SELECT current_database()::text, current_schema()::text, current_user::text`).Row()
	if err := row.Scan(&t.DatabaseName, &t.SchemaName, &t.SessionUser); err != nil {
		return ResetTarget{}, fmt.Errorf("query reset target: %w", err)
	}
	return t, nil
}

// ApplicationTablesSQL is the exact DROP TABLE statement used by DropAllApplicationObjects.
const ApplicationTablesSQL = `
DROP TABLE IF EXISTS
	bill_lines,
	invoice_lines,
	product_services,
	tax_codes,
	tax_components,
	tax_agencies,
	journal_lines,
	journal_entries,
	reconciliations,
	invoices,
	bills,
	customers,
	vendors,
	accounts,
	audit_logs,
	ai_connection_settings,
	numbering_settings,
	company_invitations,
	company_memberships,
	sessions,
	users,
	companies,
	sysadmin_sessions,
	sysadmin_users,
	system_logs,
	system_settings,
	user_company_permissions
CASCADE;
`

// DropAllApplicationObjects removes all Balanciz-owned tables in the current schema, then drops the
// Phase-1 enum company_role if present. Intended for development DBs when AutoMigrate must recreate
// a clean schema (no UI changes; same connection as the app).
func DropAllApplicationObjects(db *gorm.DB) error {
	if err := db.Exec(ApplicationTablesSQL).Error; err != nil {
		return fmt.Errorf("drop application tables: %w", err)
	}
	// Created by migrations/001_phase1_*.sql; safe after tables using it are gone.
	if err := db.Exec(`DROP TYPE IF EXISTS company_role CASCADE`).Error; err != nil {
		return fmt.Errorf("drop company_role type: %w", err)
	}
	return nil
}
