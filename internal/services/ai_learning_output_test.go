package services

import (
	"testing"
	"time"

	"balanciz/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testAILearningOutputDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Invoice{},
		&models.Bill{},
		&models.CompanyNotificationSettings{},
		&models.AIJobRun{},
		&models.AIRequestLog{},
		&models.ReportUsageEvent{},
		&models.ReportUsageStat{},
		&models.DashboardUserWidget{},
		&models.DashboardWidgetSuggestion{},
		&models.ActionCenterTask{},
		&models.ActionCenterTaskEvent{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestAIJobRunAndRequestLog(t *testing.T) {
	db := testAILearningOutputDB(t)
	companyID := uint(1)
	run, err := StartAIJobRun(db, AIJobRunInput{
		CompanyID:        &companyID,
		JobType:          models.AIJobDashboardRecommendation,
		TriggerType:      models.AIJobTriggerTest,
		InputSummaryJSON: `{"redacted":true}`,
	})
	if err != nil {
		t.Fatalf("StartAIJobRun: %v", err)
	}
	if run.Status != models.AIJobStatusRunning || run.StartedAt == nil {
		t.Fatalf("unexpected run: %+v", run)
	}
	if _, err := CreateAIRequestLog(db, AIRequestLogInput{
		CompanyID:         &companyID,
		JobRunID:          &run.ID,
		TaskType:          "dashboard_widget_recommendation",
		Status:            models.AIRequestStatusSkipped,
		InputRedactedJSON: `{"summary":"redacted"}`,
		ErrorMessage:      "AI gateway disabled",
	}); err != nil {
		t.Fatalf("CreateAIRequestLog: %v", err)
	}
	if err := FinishAIJobRun(db, run, models.AIJobStatusSucceeded, map[string]any{"ok": true}, nil, ""); err != nil {
		t.Fatalf("FinishAIJobRun: %v", err)
	}
	var loaded models.AIJobRun
	if err := db.First(&loaded, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if loaded.Status != models.AIJobStatusSucceeded || loaded.FinishedAt == nil {
		t.Fatalf("finished run not persisted: %+v", loaded)
	}
	var logs int64
	db.Model(&models.AIRequestLog{}).Where("company_id = ? AND status = ?", companyID, models.AIRequestStatusSkipped).Count(&logs)
	if logs != 1 {
		t.Fatalf("request logs = %d, want 1", logs)
	}
}

func TestReportUsageRecordsStatsCompanyScoped(t *testing.T) {
	db := testAILearningOutputDB(t)
	userID := uuid.New()
	for i := 0; i < 3; i++ {
		if err := RecordReportUsage(db, ReportUsageInput{
			CompanyID: 1,
			UserID:    &userID,
			ReportKey: "ar-aging",
			EventType: models.ReportUsageOpened,
		}); err != nil {
			t.Fatalf("record company 1 report usage: %v", err)
		}
	}
	if err := RecordReportUsage(db, ReportUsageInput{
		CompanyID: 2,
		UserID:    &userID,
		ReportKey: "ar-aging",
		EventType: models.ReportUsageExported,
	}); err != nil {
		t.Fatalf("record company 2 report usage: %v", err)
	}
	var c1 models.ReportUsageStat
	if err := db.Where("company_id = ? AND scope_type = ? AND user_id IS NULL AND report_key = ?", 1, models.SmartPickerScopeCompany, "ar-aging").First(&c1).Error; err != nil {
		t.Fatalf("load company 1 stat: %v", err)
	}
	if c1.OpenCount != 3 || c1.ExportCount != 0 {
		t.Fatalf("company 1 stat = %+v, want 3 opens only", c1)
	}
	var c2 models.ReportUsageStat
	if err := db.Where("company_id = ? AND scope_type = ? AND user_id IS NULL AND report_key = ?", 2, models.SmartPickerScopeCompany, "ar-aging").First(&c2).Error; err != nil {
		t.Fatalf("load company 2 stat: %v", err)
	}
	if c2.OpenCount != 0 || c2.ExportCount != 1 {
		t.Fatalf("company 2 stat = %+v, want 1 export only", c2)
	}
}

func TestDashboardSuggestionLifecycle(t *testing.T) {
	db := testAILearningOutputDB(t)
	userID := uuid.New()
	for i := 0; i < 3; i++ {
		if err := RecordReportUsage(db, ReportUsageInput{
			CompanyID: 1,
			UserID:    &userID,
			ReportKey: "ar-aging",
			EventType: models.ReportUsageOpened,
		}); err != nil {
			t.Fatalf("record report usage: %v", err)
		}
	}
	suggestions, err := GenerateDashboardWidgetSuggestions(db, 1, &userID, DashboardSuggestionOptions{})
	if err != nil {
		t.Fatalf("GenerateDashboardWidgetSuggestions: %v", err)
	}
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %d, want 1", len(suggestions))
	}
	if suggestions[0].WidgetKey != "ar_aging" || suggestions[0].Status != models.DashboardSuggestionPending {
		t.Fatalf("unexpected suggestion: %+v", suggestions[0])
	}
	if suggestions[0].EvidenceJSON == "" || suggestions[0].EvidenceJSON == "{}" {
		t.Fatalf("expected evidence json")
	}
	accepted, err := AcceptDashboardWidgetSuggestion(db, 1, &userID, suggestions[0].ID)
	if err != nil {
		t.Fatalf("AcceptDashboardWidgetSuggestion: %v", err)
	}
	if accepted.Status != models.DashboardSuggestionAccepted {
		t.Fatalf("accepted status = %s", accepted.Status)
	}
	var widgets int64
	db.Model(&models.DashboardUserWidget{}).Where("company_id = ? AND user_id = ? AND widget_key = ? AND active = ?", 1, userID, "ar_aging", true).Count(&widgets)
	if widgets != 1 {
		t.Fatalf("dashboard widgets = %d, want 1", widgets)
	}
	again, err := GenerateDashboardWidgetSuggestions(db, 1, &userID, DashboardSuggestionOptions{})
	if err != nil {
		t.Fatalf("GenerateDashboardWidgetSuggestions again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("duplicate suggestions generated: %+v", again)
	}
	otherCompany, err := GenerateDashboardWidgetSuggestions(db, 2, &userID, DashboardSuggestionOptions{})
	if err != nil {
		t.Fatalf("GenerateDashboardWidgetSuggestions company 2: %v", err)
	}
	if len(otherCompany) != 0 {
		t.Fatalf("company 2 suggestions leaked: %+v", otherCompany)
	}
}

func TestActionCenterGenerationAndTransitions(t *testing.T) {
	db := testAILearningOutputDB(t)
	seedActionCenterCompany(t, db, 1)
	seedActionCenterCompany(t, db, 2)
	customer := models.Customer{CompanyID: 1, Name: "Customer A", IsActive: true}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatalf("create customer: %v", err)
	}
	vendor := models.Vendor{CompanyID: 1, Name: "Vendor A", IsActive: true}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	invDue := now.AddDate(0, 0, -10)
	billDue := now.AddDate(0, 0, 3)
	if err := db.Create(&models.Invoice{
		CompanyID:     1,
		CustomerID:    customer.ID,
		InvoiceNumber: "INV-OVERDUE",
		InvoiceDate:   now.AddDate(0, 0, -40),
		DueDate:       &invDue,
		Status:        models.InvoiceStatusIssued,
		Amount:        decimal.NewFromInt(100),
		BalanceDue:    decimal.NewFromInt(100),
	}).Error; err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	if err := db.Create(&models.Bill{
		CompanyID:  1,
		VendorID:   vendor.ID,
		BillNumber: "BILL-DUE",
		BillDate:   now.AddDate(0, 0, -10),
		DueDate:    &billDue,
		Status:     models.BillStatusPosted,
		Amount:     decimal.NewFromInt(200),
		BalanceDue: decimal.NewFromInt(200),
	}).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}
	tasks, err := GenerateActionCenterTasks(db, 1, nil, ActionCenterOptions{Now: now})
	if err != nil {
		t.Fatalf("GenerateActionCenterTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2: %+v", len(tasks), tasks)
	}
	types := map[string]bool{}
	for _, task := range tasks {
		types[task.TaskType] = true
		if task.Reason == "" || task.EvidenceJSON == "" || task.Fingerprint == "" {
			t.Fatalf("task missing explainability fields: %+v", task)
		}
	}
	if !types["invoices_overdue"] || !types["bills_due_soon"] {
		t.Fatalf("missing expected task types: %+v", types)
	}
	if _, err := GenerateActionCenterTasks(db, 1, nil, ActionCenterOptions{Now: now}); err != nil {
		t.Fatalf("GenerateActionCenterTasks repeat: %v", err)
	}
	var count int64
	db.Model(&models.ActionCenterTask{}).Where("company_id = ?", 1).Count(&count)
	if count != 2 {
		t.Fatalf("dedupe count = %d, want 2", count)
	}
	if err := DismissActionCenterTask(db, 1, nil, tasks[0].ID); err != nil {
		t.Fatalf("DismissActionCenterTask: %v", err)
	}
	var events int64
	db.Model(&models.ActionCenterTaskEvent{}).Where("company_id = ? AND task_id = ? AND event_type = ?", 1, tasks[0].ID, models.ActionTaskEventDismissed).Count(&events)
	if events != 1 {
		t.Fatalf("dismiss events = %d, want 1", events)
	}
	company2Tasks, err := GenerateActionCenterTasks(db, 2, nil, ActionCenterOptions{Now: now})
	if err != nil {
		t.Fatalf("GenerateActionCenterTasks company 2: %v", err)
	}
	if len(company2Tasks) != 0 {
		t.Fatalf("company 2 task leak: %+v", company2Tasks)
	}
}

func seedActionCenterCompany(t *testing.T, db *gorm.DB, companyID uint) {
	t.Helper()
	company := models.Company{
		ID:               companyID,
		Name:             "Company",
		EntityType:       models.EntityTypeIncorporated,
		BusinessType:     models.BusinessTypeProfessionalCorp,
		Industry:         models.IndustryOther,
		IncorporatedDate: "2026-01-01",
		FiscalYearEnd:    "12-31",
		BusinessNumber:   "BN",
		AddressLine:      "123 Main",
		City:             "Vancouver",
		Province:         "BC",
		PostalCode:       "V1V1V1",
		Country:          "Canada",
		BaseCurrencyCode: "CAD",
		IsActive:         true,
	}
	if err := db.Create(&company).Error; err != nil {
		t.Fatalf("create company: %v", err)
	}
	notif := models.CompanyNotificationSettings{
		CompanyID:              companyID,
		EmailEnabled:           true,
		EmailVerificationReady: true,
		SMTPHost:               "smtp.example.test",
		SMTPFromEmail:          "billing@example.test",
	}
	if err := db.Create(&notif).Error; err != nil {
		t.Fatalf("create notification settings: %v", err)
	}
}
