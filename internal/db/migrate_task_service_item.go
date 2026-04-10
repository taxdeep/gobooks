// 遵循project_guide.md
package db

import "gorm.io/gorm"

// migrateTaskServiceItem adds the product_service_id column to the tasks table.
//
// The column links a task to a specific service item from the Products & Services
// catalogue so that the correct revenue account and tax code are used when
// generating an invoice draft from the task.
//
// Guards:
//   - ADD COLUMN IF NOT EXISTS — safe on fresh installs and repeated runs.
//   - CREATE INDEX IF NOT EXISTS — idempotent.
//   - SQLite (test databases) is handled by GORM AutoMigrate on &models.Task{},
//     so no raw SQL guard is needed there.
func migrateTaskServiceItem(db *gorm.DB) error {
	if db.Dialector.Name() == "sqlite" {
		// SQLite test databases are handled by AutoMigrate above. Avoid running
		// the PostgreSQL ADD COLUMN syntax here.
		return nil
	}

	// PostgreSQL path: add column + index with existence guards.
	sqls := []string{
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS product_service_id BIGINT REFERENCES product_services(id)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_product_service ON tasks (product_service_id) WHERE product_service_id IS NOT NULL`,
	}
	for _, sql := range sqls {
		if err := db.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}
