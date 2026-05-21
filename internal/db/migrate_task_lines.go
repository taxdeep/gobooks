// éµå¾ªproject_guide.md
package db

import "gorm.io/gorm"

// migrateTaskLines backfills the new task_lines table from legacy single-line
// task fields. AutoMigrate creates the table; this guard keeps live databases
// compatible with the new multi-service task editor.
func migrateTaskLines(db *gorm.DB) error {
	if db.Dialector.Name() == "sqlite" {
		return nil
	}

	sqls := []string{
		`CREATE INDEX IF NOT EXISTS idx_task_lines_task ON task_lines (task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_task_lines_company_task ON task_lines (company_id, task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_task_lines_product_service ON task_lines (product_service_id) WHERE product_service_id IS NOT NULL`,
		`INSERT INTO task_lines (
			company_id, task_id, product_service_id, description, quantity, rate,
			line_uom, line_uom_factor, qty_in_stock_uom, sort_order, is_billable,
			invoice_id, invoice_line_id, created_at, updated_at
		)
		SELECT
			t.company_id, t.id, t.product_service_id, COALESCE(NULLIF(t.title, ''), 'Task labor'),
			t.quantity, t.rate, '', 1, t.quantity, 1, t.is_billable,
			t.invoice_id, t.invoice_line_id, t.created_at, t.updated_at
		FROM tasks t
		WHERE NOT EXISTS (
			SELECT 1 FROM task_lines tl WHERE tl.company_id = t.company_id AND tl.task_id = t.id
		)`,
	}
	for _, sql := range sqls {
		if err := db.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}
