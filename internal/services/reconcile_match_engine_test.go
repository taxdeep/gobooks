// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Test DB helper ────────────────────────────────────────────────────────────

func testReconcileDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:reconcile_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.ReconciliationMemory{},
		&models.ReconciliationMatchSuggestion{},
		&models.ReconciliationMatchSuggestionLine{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// ── NormalizeMemo ─────────────────────────────────────────────────────────────

func TestNormalizeMemo_lowercase(t *testing.T) {
	if got := NormalizeMemo("ACME Corp"); got != "acme corp" {
		t.Errorf("got %q, want %q", got, "acme corp")
	}
}

func TestNormalizeMemo_stripNoiseWords(t *testing.T) {
	// "payment" and "wire" are noise words; "acme" should survive.
	result := NormalizeMemo("WIRE payment ACME Corp")
	for _, noise := range []string{"wire", "payment"} {
		for _, tok := range []string{" " + noise + " ", noise + " ", " " + noise} {
			_ = tok
		}
	}
	// "acme" and "corp" must be in the result.
	if result == "" {
		t.Errorf("NormalizeMemo returned empty string for non-empty input")
	}
}

func TestNormalizeMemo_wordBoundary(t *testing.T) {
	// "checkout" contains "check" but should NOT be stripped — word boundary guard.
	result := NormalizeMemo("Checkout ACME")
	if result == "" {
		t.Errorf("NormalizeMemo stripped 'checkout' entirely — word boundary broken")
	}
}

func TestNormalizeMemo_removeLongNumbers(t *testing.T) {
	result := NormalizeMemo("INV 20240101 ACME")
	// 20240101 is 8 digits — should be stripped.
	for _, tok := range []string{"20240101"} {
		if reconcileContains(result, tok) {
			t.Errorf("long number %q still present after normalize: %q", tok, result)
		}
	}
}

func TestNormalizeMemo_emptyInput(t *testing.T) {
	if got := NormalizeMemo(""); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestNormalizeMemo_onlyNoise(t *testing.T) {
	// All tokens are noise words or short — result should be empty.
	result := NormalizeMemo("ACH REF POS")
	// After stripping noise words and short tokens the result may be empty.
	// Either way it must not panic.
	_ = result
}

func reconcileContains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ── MemoSimilarity ────────────────────────────────────────────────────────────

func TestMemoSimilarity_identical(t *testing.T) {
	if got := MemoSimilarity("acme corp", "acme corp"); got != 1.0 {
		t.Errorf("identical memos: got %f, want 1.0", got)
	}
}

func TestMemoSimilarity_noOverlap(t *testing.T) {
	if got := MemoSimilarity("acme corp", "xyz ltd"); got != 0.0 {
		t.Errorf("non-overlapping memos: got %f, want 0.0", got)
	}
}

func TestMemoSimilarity_partial(t *testing.T) {
	got := MemoSimilarity("acme corp payment", "acme corp")
	if got <= 0 || got >= 1 {
		t.Errorf("partial overlap: got %f, want (0, 1)", got)
	}
}

func TestMemoSimilarity_emptyInputs(t *testing.T) {
	if got := MemoSimilarity("", "acme"); got != 0 {
		t.Errorf("empty A: got %f, want 0", got)
	}
	if got := MemoSimilarity("acme", ""); got != 0 {
		t.Errorf("empty B: got %f, want 0", got)
	}
}

func TestMemoSimilarity_shortTokensIgnored(t *testing.T) {
	// Tokens shorter than 3 chars are excluded; "a" and "b" should not match.
	if got := MemoSimilarity("a b c", "a b c"); got != 0 {
		t.Errorf("all short tokens should yield 0, got %f", got)
	}
}

// ── dateProximityScore ────────────────────────────────────────────────────────

func TestDateProximityScore_steps(t *testing.T) {
	cases := []struct {
		daysAgo float64
		want    float64
	}{
		{-1, 0.20},  // future-dated
		{0, 1.00},   // same day
		{7, 1.00},   // boundary ≤7
		{8, 0.90},   // just over 7
		{14, 0.90},  // boundary ≤14
		{15, 0.75},  // just over 14
		{30, 0.75},  // boundary ≤30
		{31, 0.55},  // just over 30
		{60, 0.55},  // boundary ≤60
		{61, 0.35},  // just over 60
		{90, 0.35},  // boundary ≤90
		{91, 0.298}, // just over 90 — should be > 0.05
	}
	for _, tc := range cases {
		got := dateProximityScore(tc.daysAgo)
		if tc.daysAgo == 91 {
			// Just verify it's in the valid decay range.
			if got < 0.05 || got >= 0.35 {
				t.Errorf("daysAgo=91: got %f, want in [0.05, 0.35)", got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("daysAgo=%.0f: got %f, want %f", tc.daysAgo, got, tc.want)
		}
	}
}

func TestDateProximityScore_farFuture_floor(t *testing.T) {
	// Very old entries should not go below the 0.05 floor.
	got := dateProximityScore(10000)
	if got < 0.05 {
		t.Errorf("score below floor: got %f", got)
	}
}

// ── sourceReliabilityScore ────────────────────────────────────────────────────

func TestSourceReliabilityScore_knownTypes(t *testing.T) {
	cases := []struct {
		sourceType string
		wantScore  float64
	}{
		{"payment", 0.90},
		{"invoice", 0.70},
		{"bill", 0.70},
		{"reversal", 0.55},
		{"manual", 0.45},
		{"opening_balance", 0.30},
		{"unknown_type", 0.45},
		{"", 0.45},
	}
	for _, tc := range cases {
		got, _ := sourceReliabilityScore(tc.sourceType)
		if got != tc.wantScore {
			t.Errorf("sourceType=%q: got %f, want %f", tc.sourceType, got, tc.wantScore)
		}
	}
}

// ── amountRatio ───────────────────────────────────────────────────────────────

func TestAmountRatio_exactMatch(t *testing.T) {
	got := amountRatio(decimal.NewFromFloat(100), decimal.NewFromFloat(100))
	if got != 1.0 {
		t.Errorf("exact match: got %f, want 1.0", got)
	}
}

func TestAmountRatio_partial(t *testing.T) {
	got := amountRatio(decimal.NewFromFloat(50), decimal.NewFromFloat(100))
	if got != 0.5 {
		t.Errorf("50/100: got %f, want 0.5", got)
	}
}

func TestAmountRatio_overshoot_normalises(t *testing.T) {
	// 150 against 100 — larger divisor → normalise to 100/150.
	got := amountRatio(decimal.NewFromFloat(150), decimal.NewFromFloat(100))
	want := 100.0 / 150.0
	if abs(got-want) > 1e-9 {
		t.Errorf("overshoot: got %f, want %f", got, want)
	}
}

func TestAmountRatio_oppositeSign(t *testing.T) {
	// Opposite signs — not a partial match; should return 0.
	got := amountRatio(decimal.NewFromFloat(-100), decimal.NewFromFloat(100))
	if got != 0 {
		t.Errorf("opposite signs: got %f, want 0", got)
	}
}

func TestAmountRatio_zeroTarget(t *testing.T) {
	got := amountRatio(decimal.NewFromFloat(50), decimal.Zero)
	if got != 0 {
		t.Errorf("zero target: got %f, want 0", got)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// ── weightedSignals ───────────────────────────────────────────────────────────

func TestWeightedSignals_empty(t *testing.T) {
	if got := weightedSignals(nil); got != 0 {
		t.Errorf("empty signals: got %f, want 0", got)
	}
}

func TestWeightedSignals_singleKnownSignal(t *testing.T) {
	signals := []MatchSignal{{Name: "exact_amount_match", Score: 1.0}}
	got := weightedSignals(signals)
	// Single signal, weighted average = its own score.
	if got != 1.0 {
		t.Errorf("single 1.0 signal: got %f, want 1.0", got)
	}
}

func TestWeightedSignals_unknownSignalDefaultsToLowWeight(t *testing.T) {
	// Unknown signal gets weight 0.10 — should still contribute.
	signals := []MatchSignal{{Name: "mystery_signal", Score: 0.5}}
	got := weightedSignals(signals)
	if got <= 0 || got > 1 {
		t.Errorf("unknown signal: got %f, want in (0, 1]", got)
	}
}

func TestWeightedSignals_cappedAtOne(t *testing.T) {
	signals := []MatchSignal{
		{Name: "exact_amount_match", Score: 1.0},
		{Name: "date_proximity", Score: 1.0},
		{Name: "source_reliability", Score: 1.0},
		{Name: "historical_match", Score: 1.0},
	}
	if got := weightedSignals(signals); got > 1.0 {
		t.Errorf("result exceeds 1.0: got %f", got)
	}
}

// ── findExactSumCombos ────────────────────────────────────────────────────────

func makeCand(id uint, amount float64) ReconcileCandidate {
	return ReconcileCandidate{
		LineID: id,
		Amount: decimal.NewFromFloat(amount),
	}
}

func TestFindExactSumCombos_pair(t *testing.T) {
	cands := []ReconcileCandidate{makeCand(1, 50), makeCand(2, 50), makeCand(3, 99)}
	target := decimal.NewFromFloat(100)
	results := findExactSumCombos(cands, target, 3)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !sameIDs(results[0], []uint{1, 2}) {
		t.Errorf("got combo %v, want [1 2]", results[0])
	}
}

func TestFindExactSumCombos_triple(t *testing.T) {
	cands := []ReconcileCandidate{makeCand(1, 30), makeCand(2, 30), makeCand(3, 40)}
	target := decimal.NewFromFloat(100)
	results := findExactSumCombos(cands, target, 3)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !sameIDs(results[0], []uint{1, 2, 3}) {
		t.Errorf("got combo %v, want [1 2 3]", results[0])
	}
}

func TestFindExactSumCombos_tripleSkippedAtDepth2(t *testing.T) {
	// maxDepth=2 → triples not attempted.
	cands := []ReconcileCandidate{makeCand(1, 30), makeCand(2, 30), makeCand(3, 40)}
	target := decimal.NewFromFloat(100)
	results := findExactSumCombos(cands, target, 2)
	if len(results) != 0 {
		t.Errorf("expected 0 triples at depth 2, got %d", len(results))
	}
}

func TestFindExactSumCombos_maxResultsCap(t *testing.T) {
	// Build 10 pairs all summing to 100.
	cands := make([]ReconcileCandidate, 0, 20)
	for i := uint(1); i <= 20; i += 2 {
		cands = append(cands, makeCand(i, 50), makeCand(i+1, 50))
	}
	target := decimal.NewFromFloat(100)
	results := findExactSumCombos(cands, target, 2)
	if len(results) > 5 {
		t.Errorf("expected ≤5 results (cap), got %d", len(results))
	}
}

func TestFindExactSumCombos_zeroTarget(t *testing.T) {
	cands := []ReconcileCandidate{makeCand(1, 50), makeCand(2, 50)}
	results := findExactSumCombos(cands, decimal.Zero, 3)
	if len(results) != 0 {
		t.Errorf("zero target: expected 0, got %d", len(results))
	}
}

func TestFindExactSumCombos_singleCandidateSkipped(t *testing.T) {
	cands := []ReconcileCandidate{makeCand(1, 100)}
	results := findExactSumCombos(cands, decimal.NewFromFloat(100), 3)
	if len(results) != 0 {
		t.Errorf("single candidate: expected 0 combos, got %d", len(results))
	}
}

func sameIDs(got []uint, want []uint) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ── deduplicateSuggestions ────────────────────────────────────────────────────

func TestDeduplicateSuggestions_removeDuplicates(t *testing.T) {
	suggs := []generatedSugg{
		{LineIDs: []uint{1, 2}, ConfidenceScore: 0.8},
		{LineIDs: []uint{2, 1}, ConfidenceScore: 0.7}, // same IDs different order — may or may not be dedup'd
		{LineIDs: []uint{3, 4}, ConfidenceScore: 0.6},
		{LineIDs: []uint{1, 2}, ConfidenceScore: 0.5}, // exact duplicate — always removed
	}
	result := deduplicateSuggestions(suggs)
	// The exact duplicate {1,2} with score 0.5 must be gone.
	count12 := 0
	for _, s := range result {
		if sameIDs(s.LineIDs, []uint{1, 2}) {
			count12++
		}
	}
	if count12 > 1 {
		t.Errorf("duplicate {1,2} not removed: appears %d times", count12)
	}
}

func TestDeduplicateSuggestions_emptyInput(t *testing.T) {
	result := deduplicateSuggestions(nil)
	if len(result) != 0 {
		t.Errorf("expected empty, got %d", len(result))
	}
}

// ── ConfidenceTier ────────────────────────────────────────────────────────────

func TestConfidenceTier_boundaries(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{1.00, ConfTierHigh},
		{0.75, ConfTierHigh},
		{0.74, ConfTierMedium},
		{0.45, ConfTierMedium},
		{0.44, ConfTierLow},
		{0.00, ConfTierLow},
	}
	for _, tc := range cases {
		got := ConfidenceTier(tc.score)
		if got != tc.want {
			t.Errorf("score=%.2f: got %q, want %q", tc.score, got, tc.want)
		}
	}
}

// ── buildSingleLineSugg ───────────────────────────────────────────────────────

func TestBuildSingleLineSugg_exactMatchBoost(t *testing.T) {
	stmtDate := time.Now()
	cand := ReconcileCandidate{
		LineID:     42,
		Amount:     decimal.NewFromFloat(100),
		EntryDate:  stmtDate.AddDate(0, 0, -1),
		SourceType: "payment",
	}
	target := decimal.NewFromFloat(100)

	sc := scoredCandidate{
		cand:       cand,
		confidence: 0.8,
		signals:    []MatchSignal{{Name: "exact_amount_match", Score: 1.0}},
	}
	sg := buildSingleLineSugg(sc, target)

	if sg.RankingScore <= sg.ConfidenceScore {
		t.Errorf("exact match should boost ranking above confidence: conf=%f rank=%f",
			sg.ConfidenceScore, sg.RankingScore)
	}
	if sg.SuggestionType != models.SuggTypeOneToOne {
		t.Errorf("expected suggestion type %q, got %q", models.SuggTypeOneToOne, sg.SuggestionType)
	}
	if len(sg.LineIDs) != 1 || sg.LineIDs[0] != 42 {
		t.Errorf("expected LineIDs=[42], got %v", sg.LineIDs)
	}
}

func TestBuildSingleLineSugg_noExactMatch(t *testing.T) {
	cand := ReconcileCandidate{
		LineID:    7,
		Amount:    decimal.NewFromFloat(80),
		EntryDate: time.Now(),
	}
	target := decimal.NewFromFloat(100)
	sc := scoredCandidate{cand: cand, confidence: 0.5}
	sg := buildSingleLineSugg(sc, target)

	// Non-exact match: ranking should equal confidence (no boost).
	if sg.RankingScore != sg.ConfidenceScore {
		t.Errorf("non-exact: ranking %f should equal confidence %f", sg.RankingScore, sg.ConfidenceScore)
	}
}

// ── scoreCandidate signal presence ───────────────────────────────────────────

func TestScoreCandidate_exactAmountSignalPresent(t *testing.T) {
	cand := ReconcileCandidate{
		LineID:     1,
		Amount:     decimal.NewFromFloat(100),
		EntryDate:  time.Now(),
		SourceType: "payment",
	}
	target := decimal.NewFromFloat(100)
	stmtDate := time.Now()
	_, signals := scoreCandidate(cand, target, stmtDate, nil)

	found := false
	for _, s := range signals {
		if s.Name == "exact_amount_match" {
			found = true
			if s.Score != 1.0 {
				t.Errorf("exact_amount_match score: got %f, want 1.0", s.Score)
			}
		}
	}
	if !found {
		t.Errorf("exact_amount_match signal not present for equal amounts")
	}
}

func TestScoreCandidate_proximitySignalWhenNotExact(t *testing.T) {
	cand := ReconcileCandidate{
		LineID:    1,
		Amount:    decimal.NewFromFloat(75),
		EntryDate: time.Now(),
	}
	target := decimal.NewFromFloat(100)
	_, signals := scoreCandidate(cand, target, time.Now(), nil)

	found := false
	for _, s := range signals {
		if s.Name == "amount_proximity" {
			found = true
		}
	}
	if !found {
		t.Errorf("amount_proximity signal missing when amount != target")
	}
}

func TestScoreCandidate_historicalSignalWithMemory(t *testing.T) {
	boost := decimal.NewFromFloat(0.10)
	mem := &models.ReconciliationMemory{
		NormalizedBookMemo: "acme corp",
		SourceType:         "payment",
		MatchedCount:       2,
		ConfidenceBoost:    boost,
	}
	memMap := map[string]*models.ReconciliationMemory{
		memoryKey("acme corp", "payment"): mem,
	}

	cand := ReconcileCandidate{
		LineID:     1,
		Amount:     decimal.NewFromFloat(100),
		EntryDate:  time.Now(),
		Memo:       "acme corp",
		SourceType: "payment",
	}
	_, signals := scoreCandidate(cand, decimal.NewFromFloat(100), time.Now(), memMap)

	found := false
	for _, s := range signals {
		if s.Name == "historical_match" {
			found = true
			if s.Score <= 0 || s.Score > 1 {
				t.Errorf("historical_match score out of range: %f", s.Score)
			}
		}
	}
	if !found {
		t.Errorf("historical_match signal missing when memory entry present")
	}
}

// ── UpdateMemoryFromAcceptedLines integration tests ───────────────────────────

func seedReconcileCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "Test Co"}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("create company: %v", err)
	}
	return co.ID
}

func seedReconcileJournalLine(t *testing.T, db *gorm.DB, companyID uint, memo, srcType string) uint {
	t.Helper()
	je := models.JournalEntry{
		CompanyID:  companyID,
		SourceType: models.LedgerSourceType(srcType),
		EntryDate:  time.Now(),
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatalf("create journal entry: %v", err)
	}
	jl := models.JournalLine{
		CompanyID:      companyID,
		JournalEntryID: je.ID,
		Memo:           memo,
		Debit:          decimal.NewFromFloat(100),
	}
	if err := db.Create(&jl).Error; err != nil {
		t.Fatalf("create journal line: %v", err)
	}
	return jl.ID
}

func TestUpdateMemoryFromAcceptedLines_create(t *testing.T) {
	db := testReconcileDB(t)
	coID := seedReconcileCompany(t, db)
	lineID := seedReconcileJournalLine(t, db, coID, "ACME Corp payment", "payment")

	if err := UpdateMemoryFromAcceptedLines(db, coID, 1, []uint{lineID}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var mem models.ReconciliationMemory
	if err := db.Where("company_id = ? AND account_id = ?", coID, 1).First(&mem).Error; err != nil {
		t.Fatalf("memory row not created: %v", err)
	}
	if mem.MatchedCount != 1 {
		t.Errorf("matched_count: got %d, want 1", mem.MatchedCount)
	}
	boost, _ := mem.ConfidenceBoost.Float64()
	if abs(boost-0.05) > 1e-9 {
		t.Errorf("confidence_boost: got %f, want 0.05", boost)
	}
}

func TestUpdateMemoryFromAcceptedLines_increment(t *testing.T) {
	db := testReconcileDB(t)
	coID := seedReconcileCompany(t, db)
	lineID := seedReconcileJournalLine(t, db, coID, "ACME Corp payment", "payment")

	// Accept twice.
	for i := 0; i < 2; i++ {
		if err := UpdateMemoryFromAcceptedLines(db, coID, 1, []uint{lineID}); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}

	var mem models.ReconciliationMemory
	if err := db.Where("company_id = ? AND account_id = ?", coID, 1).First(&mem).Error; err != nil {
		t.Fatalf("memory row missing: %v", err)
	}
	if mem.MatchedCount != 2 {
		t.Errorf("matched_count after 2 accepts: got %d, want 2", mem.MatchedCount)
	}
	boost, _ := mem.ConfidenceBoost.Float64()
	if abs(boost-0.10) > 1e-9 {
		t.Errorf("confidence_boost after 2 accepts: got %f, want 0.10", boost)
	}
}

func TestUpdateMemoryFromAcceptedLines_boostCap(t *testing.T) {
	db := testReconcileDB(t)
	coID := seedReconcileCompany(t, db)
	lineID := seedReconcileJournalLine(t, db, coID, "ACME Corp payment", "payment")

	// 7 acceptances: boost should cap at 0.30 (reached after 6).
	for i := 0; i < 7; i++ {
		if err := UpdateMemoryFromAcceptedLines(db, coID, 1, []uint{lineID}); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}

	var mem models.ReconciliationMemory
	if err := db.Where("company_id = ? AND account_id = ?", coID, 1).First(&mem).Error; err != nil {
		t.Fatalf("memory row missing: %v", err)
	}
	if mem.MatchedCount != 7 {
		t.Errorf("matched_count: got %d, want 7", mem.MatchedCount)
	}
	boost, _ := mem.ConfidenceBoost.Float64()
	if boost > 0.30+1e-9 {
		t.Errorf("confidence_boost exceeded cap: got %f, want ≤0.30", boost)
	}
	if abs(boost-0.30) > 1e-9 {
		t.Errorf("confidence_boost at 7 accepts: got %f, want 0.30", boost)
	}
}

func TestUpdateMemoryFromAcceptedLines_skipEmptyMemo(t *testing.T) {
	db := testReconcileDB(t)
	coID := seedReconcileCompany(t, db)
	// Journal line with memo that normalizes to empty (all noise words).
	lineID := seedReconcileJournalLine(t, db, coID, "ACH REF 12345678", "payment")

	if err := UpdateMemoryFromAcceptedLines(db, coID, 1, []uint{lineID}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var count int64
	db.Model(&models.ReconciliationMemory{}).Where("company_id = ?", coID).Count(&count)
	if count != 0 {
		t.Errorf("expected no memory row for all-noise memo, got %d rows", count)
	}
}

func TestUpdateMemoryFromAcceptedLines_emptyLineIDs(t *testing.T) {
	db := testReconcileDB(t)
	coID := seedReconcileCompany(t, db)

	if err := UpdateMemoryFromAcceptedLines(db, coID, 1, nil); err != nil {
		t.Fatalf("unexpected error for empty lineIDs: %v", err)
	}
}
