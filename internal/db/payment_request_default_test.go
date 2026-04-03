package db

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gobooks/internal/models"
)

func TestPaymentRequestStatusDefault_ModelAndMigrationStayAligned(t *testing.T) {
	field, ok := reflect.TypeOf(models.PaymentRequest{}).FieldByName("Status")
	if !ok {
		t.Fatal("PaymentRequest.Status field not found")
	}

	gormTag := field.Tag.Get("gorm")
	if !strings.Contains(gormTag, "default:'pending'") {
		t.Fatalf("expected PaymentRequest.Status gorm tag to default to pending, got %q", gormTag)
	}

	migrationPath := filepath.Join("..", "..", "migrations", "041_payment_request_status_default_pending.sql")
	sqlBytes, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(sqlBytes)
	if !strings.Contains(sql, "ALTER TABLE payment_requests") {
		t.Fatalf("expected migration to alter payment_requests, got %q", sql)
	}
	if !strings.Contains(sql, "ALTER COLUMN status SET DEFAULT 'pending'") {
		t.Fatalf("expected migration to set default pending, got %q", sql)
	}
}
