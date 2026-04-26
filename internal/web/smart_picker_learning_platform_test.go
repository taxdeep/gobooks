package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	appai "gobooks/internal/ai"
	"gobooks/internal/config"
	"gobooks/internal/models"
)

func TestSmartPickerRanking_CompanyBoundaryAndHintStatus(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Boundary A")
	otherCompanyID := seedCompany(t, db, "SP Boundary B")
	officeID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	travelID := seedSPAccount(t, db, companyID, "6200", "Travel", models.RootExpense, true)
	now := time.Now().UTC()

	if err := db.Create(&models.SmartPickerUsageStat{
		CompanyID:      otherCompanyID,
		ScopeType:      models.SmartPickerScopeCompany,
		Context:        "expense_form_category",
		EntityType:     "account",
		EntityID:       travelID,
		SelectCount:    99,
		LastSelectedAt: &now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.SmartPickerRankingHint{
		CompanyID:        companyID,
		Context:          "expense_form_category",
		EntityType:       "account",
		EntityID:         travelID,
		BoostScore:       decimalFromFloat(10),
		Confidence:       decimalFromFloat(1),
		Source:           models.SmartPickerSourceAI,
		Status:           models.SmartPickerSuggestionPending,
		ValidationStatus: models.SmartPickerValidationValid,
		CreatedAt:        now,
		UpdatedAt:        now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	acceleration := NewSmartPickerAcceleration()
	t.Cleanup(acceleration.cache.Close)
	result, _, err := acceleration.Search(db, &ExpenseAccountProvider{}, SmartPickerContext{
		CompanyID: companyID,
		Context:   "expense_form_category",
		Limit:     20,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) < 2 || result.Candidates[0].ID != fmt.Sprintf("%d", officeID) {
		t.Fatalf("cross-company stats or pending hint affected ranking: %+v", result.Candidates)
	}

	if err := db.Create(&models.SmartPickerRankingHint{
		CompanyID:        companyID,
		Context:          "expense_form_category",
		EntityType:       "account",
		EntityID:         travelID,
		BoostScore:       decimalFromFloat(10),
		Confidence:       decimalFromFloat(1),
		Source:           models.SmartPickerSourceAdmin,
		Status:           models.SmartPickerSuggestionActive,
		ValidationStatus: models.SmartPickerValidationValid,
		CreatedAt:        now,
		UpdatedAt:        now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	acceleration.InvalidateCompany(companyID)
	result, source, err := acceleration.Search(db, &ExpenseAccountProvider{}, SmartPickerContext{
		CompanyID: companyID,
		Context:   "expense_form_category",
		Limit:     20,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if source != "ranked" || result.Candidates[0].ID != fmt.Sprintf("%d", travelID) {
		t.Fatalf("expected active valid hint to rank travel first, source=%s result=%+v", source, result.Candidates)
	}
}

func TestSmartPickerUsage_CrossCompanySelectionRejected(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Reject A")
	otherCompanyID := seedCompany(t, db, "SP Reject B")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	otherVendorID := seedSPVendor(t, db, otherCompanyID, "Secret Vendor", "secret@example.com", "")
	app := testRouteApp(t, db)

	payload, err := json.Marshal(map[string]any{
		"entity":             "vendor",
		"context":            "expense_form_vendor",
		"event_type":         "select",
		"selected_entity_id": fmt.Sprintf("%d", otherVendorID),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := makeJSONRequest(t, http.MethodPost, "/api/smart-picker/usage", payload, rawToken)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-company selected entity, got %d", resp.StatusCode)
	}
}

func TestSmartPickerPairStats_BoostTargetWithAnchor(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Pair Co")
	vendorID := seedSPVendor(t, db, companyID, "Amazon", "ap@example.com", "")
	officeID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	travelID := seedSPAccount(t, db, companyID, "6200", "Travel", models.RootExpense, true)
	now := time.Now().UTC()

	if err := db.Create(&models.SmartPickerPairStat{
		CompanyID:              companyID,
		ScopeType:              models.SmartPickerScopeCompany,
		SourceContext:          "expense_form_vendor",
		AnchorEntityType:       "vendor",
		AnchorEntityID:         vendorID,
		TargetContext:          "expense_form_category",
		TargetEntityType:       "account",
		TargetEntityID:         travelID,
		SelectCount:            9,
		TotalAnchorSelectCount: 10,
		ConfidenceScore:        decimalFromFloat(0.9),
		LastSelectedAt:         &now,
		UpdatedAt:              now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	result, source, err := NewSmartPickerAcceleration().Search(db, &ExpenseAccountProvider{}, SmartPickerContext{
		CompanyID:        companyID,
		Context:          "expense_form_category",
		Limit:            20,
		AnchorContext:    "expense_form_vendor",
		AnchorEntityType: "vendor",
		AnchorEntityID:   &vendorID,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if source != "ranked" || len(result.Candidates) < 2 || result.Candidates[0].ID != fmt.Sprintf("%d", travelID) || result.Candidates[1].ID != fmt.Sprintf("%d", officeID) {
		t.Fatalf("expected pair stat to boost travel first, source=%s result=%+v", source, result.Candidates)
	}
}

func TestSmartPickerDecisionTrace_EnabledStoresBreakdown(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Trace Co")
	seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)

	_, _, err := NewSmartPickerAcceleration().Search(db, &ExpenseAccountProvider{}, SmartPickerContext{
		CompanyID:       companyID,
		Context:         "expense_form_category",
		Limit:           20,
		Query:           "office",
		TraceEnabled:    true,
		TraceSampleRate: 1,
	}, "office")
	if err != nil {
		t.Fatal(err)
	}
	var trace models.SmartPickerDecisionTrace
	if err := db.Where("company_id = ? AND context = ?", companyID, "expense_form_category").First(&trace).Error; err != nil {
		t.Fatalf("expected decision trace, got %v", err)
	}
	if trace.TraceJSON == "" || !containsAll(trace.TraceJSON, "final_score", "text_match_score") {
		t.Fatalf("trace missing score components: %s", trace.TraceJSON)
	}
}

func TestAIGateway_NoOpWhenDisabled(t *testing.T) {
	gateway := appai.NewGateway(config.Config{}, appai.NoopAIProvider{})
	resp, err := gateway.RunStructuredTask(context.Background(), appai.StructuredTaskRequest{
		TaskType:   appai.TaskSmartPickerLearningSummary,
		Capability: appai.CapabilityStructuredOutput,
		InputJSON:  "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != appai.GatewayStatusSkipped {
		t.Fatalf("expected skipped noop response, got %+v", resp)
	}
}

func TestSmartPickerLearningWorker_CreatesVisibleRunAndProfiles(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Learning Co")
	accountID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	now := time.Now().UTC()
	if err := db.Create(&models.SmartPickerUsageStat{
		CompanyID:      companyID,
		ScopeType:      models.SmartPickerScopeCompany,
		Context:        "expense_form_category",
		EntityType:     "account",
		EntityID:       accountID,
		SelectCount:    4,
		LastSelectedAt: &now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	s := &Server{Cfg: config.Config{}, DB: db}
	run, err := s.RunSmartPickerLearning(context.Background(), companyID, smartPickerLearningOptions{
		WindowStart: now.AddDate(0, 0, -30),
		WindowEnd:   now,
		TriggerType: models.AIJobTriggerTest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.AIJobStatusSucceeded {
		t.Fatalf("expected succeeded run, got %+v", run)
	}
	var profile models.SmartPickerLearningProfile
	if err := db.Where("company_id = ? AND job_run_id = ?", companyID, run.ID).First(&profile).Error; err != nil {
		t.Fatalf("expected learning profile, got %v", err)
	}
	var reqLog models.AIRequestLog
	if err := db.Where("company_id = ? AND job_run_id = ? AND status = ?", companyID, run.ID, models.AIRequestStatusSkipped).First(&reqLog).Error; err != nil {
		t.Fatalf("expected skipped AI request log, got %v", err)
	}
}

func TestSmartPickerLearningWorker_StoresAISuggestionsPending(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP AI Suggest Co")
	accountID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	now := time.Now().UTC()
	cfg := config.Config{
		AIGatewayEnabled:             true,
		SmartPickerAILearningEnabled: true,
	}
	output := fmt.Sprintf(`{
		"ranking_suggestions":[{"context":"expense_form_category","entity_type":"account","entity_id":"%d","suggested_boost":20,"confidence":0.9,"reason":"Often selected"}],
		"alias_suggestions":[{"context":"expense_form_category","entity_type":"account","entity_id":"%d","alias":"office stuff","confidence":0.8,"reason":"Repeated query"}],
		"summary":"Office supplies is frequently selected."
	}`, accountID, accountID)
	gateway := appai.NewGateway(cfg, fakeStructuredAIProvider{output: output})
	s := &Server{Cfg: cfg, DB: db}

	run, err := s.RunSmartPickerLearning(context.Background(), companyID, smartPickerLearningOptions{
		WindowStart: now.AddDate(0, 0, -30),
		WindowEnd:   now,
		TriggerType: models.AIJobTriggerTest,
		Gateway:     &gateway,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.AIJobStatusSucceeded {
		t.Fatalf("expected succeeded run, got %+v", run)
	}
	var hint models.SmartPickerRankingHint
	if err := db.Where("company_id = ? AND job_run_id = ? AND source = ?", companyID, run.ID, models.SmartPickerSourceAI).First(&hint).Error; err != nil {
		t.Fatalf("expected AI hint, got %v", err)
	}
	if hint.Status != models.SmartPickerSuggestionPending || hint.BoostScore.InexactFloat64() > 5 {
		t.Fatalf("expected pending capped AI hint, got %+v", hint)
	}
	var alias models.SmartPickerAliasSuggestion
	if err := db.Where("company_id = ? AND job_run_id = ?", companyID, run.ID).First(&alias).Error; err != nil {
		t.Fatalf("expected AI alias suggestion, got %v", err)
	}
	if alias.Status != models.SmartPickerSuggestionPending {
		t.Fatalf("expected pending alias, got %+v", alias)
	}
}

type fakeStructuredAIProvider struct {
	output string
}

func (p fakeStructuredAIProvider) Name() string { return "fake" }

func (p fakeStructuredAIProvider) Supports(appai.TaskType, appai.Capability) bool { return true }

func (p fakeStructuredAIProvider) CompleteStructured(context.Context, appai.StructuredTaskRequest, appai.ModelSelection) (appai.StructuredTaskResponse, error) {
	return appai.StructuredTaskResponse{
		Status:     appai.GatewayStatusSucceeded,
		Provider:   "fake",
		Model:      "fake-model",
		OutputJSON: p.output,
	}, nil
}

func makeJSONRequest(t *testing.T, method, path string, payload []byte, rawToken string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, path, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(CSRFHeaderName, "csrf-test")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf-test", Path: "/"})
	return req
}

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}
