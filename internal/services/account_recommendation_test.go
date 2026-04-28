// 遵循project_guide.md
package services

import (
	"testing"

	"github.com/glebarez/sqlite"
	"balanciz/internal/models"

	"gorm.io/gorm"
)

func testSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Account{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedAccount(t *testing.T, db *gorm.DB, companyID uint, code, name string, root models.RootAccountType, detail models.DetailAccountType) {
	t.Helper()
	if err := db.Create(&models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              name,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestSuggestAccountNameNormalized_emptyBank(t *testing.T) {
	name, reason := suggestAccountNameNormalized("", models.DetailBank)
	if name != "Bank" {
		t.Fatalf("name: got %q", name)
	}
	if reason != "Default name based on detail type." {
		t.Fatalf("reason: got %q", reason)
	}
}

func TestSuggestAccountNameNormalized_userWhitespace(t *testing.T) {
	name, reason := suggestAccountNameNormalized("  my  cash  ", models.DetailBank)
	if name != "my cash" {
		t.Fatalf("name: got %q", name)
	}
	if reason != "Normalized user-provided name." {
		t.Fatalf("reason: got %q", reason)
	}
}

func TestRuleGifiLookup_bankAndCOGS(t *testing.T) {
	c, d := ruleGifiLookup(models.RootAsset, models.DetailBank)
	if c != "1001" || d == "" {
		t.Fatalf("bank: code=%q desc=%q", c, d)
	}
	c2, _ := ruleGifiLookup(models.RootCostOfSales, models.DetailCostOfGoodsSold)
	if c2 != "8518" {
		t.Fatalf("cogs: got %q", c2)
	}
}

func TestRuleConfidenceString(t *testing.T) {
	if ruleConfidenceString(true, 2, false) != "high" {
		t.Fatal("expected high")
	}
	if ruleConfidenceString(false, 0, false) != "low" {
		t.Fatal("expected low")
	}
	if ruleConfidenceString(false, 0, true) != "medium" {
		t.Fatal("expected medium for prefix fallback")
	}
}

func TestComputeRuleBasedRecommendation_defaultCodeNoPeers(t *testing.T) {
	db := testSQLiteDB(t)
	const cid uint = 1
	req := RuleRecommendationRequest{
		RootAccountType:   string(models.RootExpense),
		DetailAccountType: string(models.DetailOfficeExpense),
		AccountName:       "",
		CodeLength:        5,
	}
	out, err := ComputeRuleBasedRecommendation(db, cid, req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Source != "rule" {
		t.Fatalf("source: %q", out.Source)
	}
	if out.SuggestedAccountCode != "60000" {
		t.Fatalf("expected 60000 for empty expense chart, got %q", out.SuggestedAccountCode)
	}
	if out.Confidence != "medium" {
		t.Fatalf("confidence: %q (expected medium: GIFI present, no peers)", out.Confidence)
	}
	if out.SuggestedGifiCode != "8810" {
		t.Fatalf("gifi: %q", out.SuggestedGifiCode)
	}
}

func TestComputeRuleBasedRecommendation_bankNextInGroup(t *testing.T) {
	db := testSQLiteDB(t)
	const cid uint = 1
	// Only 110xx bank accounts so the next slot in the bank group is 11030 (step 10).
	seedAccount(t, db, cid, "11000", "Bank", models.RootAsset, models.DetailBank)
	seedAccount(t, db, cid, "11010", "RBC", models.RootAsset, models.DetailBank)
	seedAccount(t, db, cid, "11020", "CIBC", models.RootAsset, models.DetailBank)

	req := RuleRecommendationRequest{
		RootAccountType:   string(models.RootAsset),
		DetailAccountType: string(models.DetailBank),
		AccountName:       "",
		CodeLength:        5,
	}
	out, err := ComputeRuleBasedRecommendation(db, cid, req)
	if err != nil {
		t.Fatal(err)
	}
	if out.SuggestedAccountCode != "11030" {
		t.Fatalf("expected next bank slot 11030, got %q (%s)", out.SuggestedAccountCode, out.Reason)
	}
	if out.Confidence != "high" {
		t.Fatalf("expected high confidence, got %q", out.Confidence)
	}
}

func TestComputeRuleBasedRecommendation_uniqueCode(t *testing.T) {
	db := testSQLiteDB(t)
	const cid uint = 1
	seedAccount(t, db, cid, "60000", "Office", models.RootExpense, models.DetailOfficeExpense)

	req := RuleRecommendationRequest{
		RootAccountType:   string(models.RootExpense),
		DetailAccountType: string(models.DetailOfficeExpense),
		CodeLength:        5,
	}
	out, err := ComputeRuleBasedRecommendation(db, cid, req)
	if err != nil {
		t.Fatal(err)
	}
	if out.SuggestedAccountCode == "60000" {
		t.Fatal("must not suggest duplicate code")
	}
}

func TestComputeRuleBasedRecommendation_codeLength6(t *testing.T) {
	db := testSQLiteDB(t)
	const cid uint = 1
	req := RuleRecommendationRequest{
		RootAccountType:   string(models.RootRevenue),
		DetailAccountType: string(models.DetailServiceRevenue),
		CodeLength:        6,
	}
	out, err := ComputeRuleBasedRecommendation(db, cid, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.SuggestedAccountCode) != 6 || out.SuggestedAccountCode[0] != '4' {
		t.Fatalf("expected 6-digit revenue code starting with 4, got %q", out.SuggestedAccountCode)
	}
}

func TestRecommendAccount_enhanceFalseVsTrue(t *testing.T) {
	db := testSQLiteDB(t)
	base := AccountRecommendationRequest{
		RootAccountType:   string(models.RootExpense),
		DetailAccountType: string(models.DetailOfficeExpense),
		CodeLength:        5,
	}
	r1, err := RecommendAccount(db, 1, base)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Source != "rule" {
		t.Fatalf("rule branch: %q", r1.Source)
	}
	base.Enhance = true
	r2, err := RecommendAccount(db, 1, base)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Source != "rule" {
		t.Fatalf("expected rule when AI off, got %q", r2.Source)
	}
	if !r2.AIUnavailable {
		t.Fatal("expected ai_unavailable when AI not configured")
	}
}

func TestRuleBasedAccountRecommendation_backwardCompatShape(t *testing.T) {
	db := testSQLiteDB(t)
	rec, err := RuleBasedAccountRecommendation(db, 1, 5, AccountRecommendationInput{
		RootAccountType:   string(models.RootAsset),
		DetailAccountType: string(models.DetailBank),
		AccountName:       "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Source != "rule" || rec.SuggestedAccountCode == "" {
		t.Fatalf("unexpected %+v", rec)
	}
}
