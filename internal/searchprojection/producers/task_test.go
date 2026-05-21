package producers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func TestTaskDocument_Mapping(t *testing.T) {
	taskDate := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)
	task := models.Task{
		ID:           42,
		CompanyID:    7,
		Title:        "April bookkeeping cleanup",
		TaskDate:     taskDate,
		Quantity:     decimal.NewFromInt(2),
		Rate:         decimal.NewFromInt(125),
		CurrencyCode: "CAD",
		IsBillable:   true,
		Status:       models.TaskStatusCompleted,
		Notes:        "Bank feeds and reconciliation review",
		Customer:     models.Customer{ID: 9, Name: "Northwind"},
		ProductService: &models.ProductService{
			ID:   3,
			Name: "Bookkeeping",
		},
	}

	doc := TaskDocument(task)
	if doc.EntityType != EntityTypeTask {
		t.Fatalf("EntityType = %q, want %q", doc.EntityType, EntityTypeTask)
	}
	if doc.CompanyID != 7 || doc.EntityID != 42 {
		t.Fatalf("unexpected IDs: %+v", doc)
	}
	if doc.DocNumber != "TASK-42" {
		t.Fatalf("DocNumber = %q", doc.DocNumber)
	}
	if doc.Title != "Northwind" {
		t.Fatalf("Title = %q", doc.Title)
	}
	for _, want := range []string{"Task", "2026-05-19", "billable", "Bookkeeping"} {
		if !strings.Contains(doc.Subtitle, want) {
			t.Fatalf("Subtitle %q missing %q", doc.Subtitle, want)
		}
	}
	if doc.Amount != "250.00" || doc.Currency != "CAD" {
		t.Fatalf("unexpected amount/currency: %+v", doc)
	}
	if doc.Status != "completed" {
		t.Fatalf("Status = %q", doc.Status)
	}
	if doc.URLPath != "/tasks/42" {
		t.Fatalf("URLPath = %q", doc.URLPath)
	}
	if !strings.Contains(doc.Memo, "April bookkeeping cleanup") || !strings.Contains(doc.Memo, "Bank feeds") {
		t.Fatalf("Memo should include title and notes, got %q", doc.Memo)
	}
}

func TestProjectTask_LoadsFromDBAndUpserts(t *testing.T) {
	db := newTaskProducerTestDB(t)
	customer := models.Customer{CompanyID: 1, Name: "Acme"}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	task := models.Task{
		CompanyID:    1,
		CustomerID:   customer.ID,
		Title:        "Setup",
		TaskDate:     time.Now(),
		Quantity:     decimal.NewFromInt(1),
		Rate:         decimal.NewFromInt(10),
		CurrencyCode: "CAD",
		IsBillable:   true,
		Status:       models.TaskStatusOpen,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	rec := &recordingProjector{}
	if err := ProjectTask(context.Background(), db, rec, task.CompanyID, task.ID); err != nil {
		t.Fatal(err)
	}
	if len(rec.upserts) != 1 {
		t.Fatalf("upserts=%d, want 1", len(rec.upserts))
	}
	if got := rec.upserts[0]; got.EntityType != EntityTypeTask || got.Title != "Acme" {
		t.Fatalf("unexpected upsert: %+v", got)
	}
}

func TestProjectTask_RejectsCrossTenantID(t *testing.T) {
	db := newTaskProducerTestDB(t)
	customer := models.Customer{CompanyID: 2, Name: "Other"}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	task := models.Task{
		CompanyID:    2,
		CustomerID:   customer.ID,
		Title:        "Foreign task",
		TaskDate:     time.Now(),
		Quantity:     decimal.NewFromInt(1),
		Rate:         decimal.NewFromInt(10),
		CurrencyCode: "CAD",
		Status:       models.TaskStatusOpen,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	rec := &recordingProjector{}
	err := ProjectTask(context.Background(), db, rec, 1, task.ID)
	if !errors.Is(err, ErrEntityNotInCompany) {
		t.Fatalf("expected ErrEntityNotInCompany, got %v", err)
	}
	if len(rec.upserts) != 0 {
		t.Fatalf("cross-tenant task should not upsert, got %+v", rec.upserts)
	}
}

func newTaskProducerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:producers_task_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Customer{}, &models.ProductService{}, &models.Task{}, &models.TaskLine{}); err != nil {
		t.Fatal(err)
	}
	return db
}
