package db

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"balanciz/internal/models"
)

func TestTaskModuleMigrationsAndModelsStayAligned(t *testing.T) {
	field, ok := reflect.TypeOf(models.TaskInvoiceSource{}).FieldByName("VoidedAt")
	if !ok {
		t.Fatal("TaskInvoiceSource.VoidedAt field not found")
	}
	if field.Type.String() != "*time.Time" {
		t.Fatalf("expected TaskInvoiceSource.VoidedAt to be *time.Time, got %s", field.Type.String())
	}
	invoiceIDField, ok := reflect.TypeOf(models.TaskInvoiceSource{}).FieldByName("InvoiceID")
	if !ok {
		t.Fatal("TaskInvoiceSource.InvoiceID field not found")
	}
	if invoiceIDField.Type.String() != "*uint" {
		t.Fatalf("expected TaskInvoiceSource.InvoiceID to be *uint, got %s", invoiceIDField.Type.String())
	}
	invoiceLineIDField, ok := reflect.TypeOf(models.TaskInvoiceSource{}).FieldByName("InvoiceLineID")
	if !ok {
		t.Fatal("TaskInvoiceSource.InvoiceLineID field not found")
	}
	if invoiceLineIDField.Type.String() != "*uint" {
		t.Fatalf("expected TaskInvoiceSource.InvoiceLineID to be *uint, got %s", invoiceLineIDField.Type.String())
	}

	migration042Path := filepath.Join("..", "..", "migrations", "042_task_module_batch1.sql")
	migration042Bytes, err := os.ReadFile(migration042Path)
	if err != nil {
		t.Fatalf("read 042 migration: %v", err)
	}
	migration042 := string(migration042Bytes)
	if !strings.Contains(migration042, "uq_product_services_company_system_code") {
		t.Fatalf("expected 042 migration to define uq_product_services_company_system_code, got %q", migration042)
	}

	migration043Path := filepath.Join("..", "..", "migrations", "043_task_invoice_sources_active_unique.sql")
	migration043Bytes, err := os.ReadFile(migration043Path)
	if err != nil {
		t.Fatalf("read 043 migration: %v", err)
	}
	migration043 := string(migration043Bytes)
	if !strings.Contains(migration043, "ADD COLUMN IF NOT EXISTS voided_at") {
		t.Fatalf("expected 043 migration to add voided_at, got %q", migration043)
	}
	if !strings.Contains(migration043, "uq_task_invoice_sources_active") {
		t.Fatalf("expected 043 migration to define uq_task_invoice_sources_active, got %q", migration043)
	}
	if !strings.Contains(migration043, "WHERE voided_at IS NULL") {
		t.Fatalf("expected 043 migration to scope uniqueness to active rows, got %q", migration043)
	}

	migration044Path := filepath.Join("..", "..", "migrations", "044_task_invoice_sources_release_refs.sql")
	migration044Bytes, err := os.ReadFile(migration044Path)
	if err != nil {
		t.Fatalf("read 044 migration: %v", err)
	}
	migration044 := string(migration044Bytes)
	if !strings.Contains(migration044, "ALTER COLUMN invoice_id DROP NOT NULL") {
		t.Fatalf("expected 044 migration to drop NOT NULL on invoice_id, got %q", migration044)
	}
	if !strings.Contains(migration044, "ALTER COLUMN invoice_line_id DROP NOT NULL") {
		t.Fatalf("expected 044 migration to drop NOT NULL on invoice_line_id, got %q", migration044)
	}
}

func TestTaskInvoiceSourceActiveUniqueConstraint_SQLSemantics(t *testing.T) {
	db := openMigrationTestDB(t)

	if err := db.Exec(`
CREATE TABLE task_invoice_sources (
	id INTEGER PRIMARY KEY,
	source_type TEXT NOT NULL,
	source_id INTEGER NOT NULL,
	voided_at DATETIME NULL
);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
CREATE UNIQUE INDEX uq_task_invoice_sources_active
	ON task_invoice_sources(source_type, source_id)
	WHERE voided_at IS NULL;`).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Exec(`INSERT INTO task_invoice_sources (id, source_type, source_id, voided_at) VALUES (1, 'task', 10, NULL)`).Error; err != nil {
		t.Fatalf("insert first active row: %v", err)
	}
	if err := db.Exec(`INSERT INTO task_invoice_sources (id, source_type, source_id, voided_at) VALUES (2, 'task', 10, NULL)`).Error; err == nil {
		t.Fatal("expected duplicate active bridge to fail, got nil")
	}

	voidedAt := time.Now().UTC()
	if err := db.Exec(`UPDATE task_invoice_sources SET voided_at = ? WHERE id = 1`, voidedAt).Error; err != nil {
		t.Fatalf("void first active row: %v", err)
	}
	if err := db.Exec(`INSERT INTO task_invoice_sources (id, source_type, source_id, voided_at) VALUES (3, 'task', 10, NULL)`).Error; err != nil {
		t.Fatalf("expected new active bridge after void to succeed, got %v", err)
	}
}

func TestProductServiceSystemCodeUniqueConstraint_SQLSemantics(t *testing.T) {
	db := openMigrationTestDB(t)

	if err := db.Exec(`
CREATE TABLE product_services (
	id INTEGER PRIMARY KEY,
	company_id INTEGER NOT NULL,
	system_code TEXT NULL
);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
CREATE UNIQUE INDEX uq_product_services_company_system_code
	ON product_services(company_id, system_code)
	WHERE system_code IS NOT NULL AND system_code <> '';`).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Exec(`INSERT INTO product_services (id, company_id, system_code) VALUES (1, 1, 'TASK_LABOR')`).Error; err != nil {
		t.Fatalf("insert first system item: %v", err)
	}
	if err := db.Exec(`INSERT INTO product_services (id, company_id, system_code) VALUES (2, 1, 'TASK_LABOR')`).Error; err == nil {
		t.Fatal("expected same-company duplicate system_code to fail, got nil")
	}
	if err := db.Exec(`INSERT INTO product_services (id, company_id, system_code) VALUES (3, 2, 'TASK_LABOR')`).Error; err != nil {
		t.Fatalf("expected cross-company same system_code to succeed, got %v", err)
	}
	if err := db.Exec(`INSERT INTO product_services (id, company_id, system_code) VALUES (4, 1, NULL)`).Error; err != nil {
		t.Fatalf("insert null system_code row: %v", err)
	}
	if err := db.Exec(`INSERT INTO product_services (id, company_id, system_code) VALUES (5, 1, NULL)`).Error; err != nil {
		t.Fatalf("expected second null system_code row to succeed, got %v", err)
	}
}
