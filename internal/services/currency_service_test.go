// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func testCurrencyDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:currency_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Currency{},
		&models.CompanyCurrency{},
		&models.ExchangeRate{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func fxDate(year, month, day int) time.Time {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

func fxRate(f float64) decimal.Decimal {
	return decimal.NewFromFloat(f)
}

func insertRate(t *testing.T, db *gorm.DB, companyID *uint, base, target string, r decimal.Decimal, date time.Time) {
	t.Helper()
	er := models.ExchangeRate{
		CompanyID:          companyID,
		BaseCurrencyCode:   base,
		TargetCurrencyCode: target,
		Rate:               r,
		RateType:           "spot",
		EffectiveDate:      date,
	}
	if err := db.Create(&er).Error; err != nil {
		t.Fatalf("insertRate: %v", err)
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestGetExchangeRate_ExactDate: system rate found on the exact queried date.
func TestGetExchangeRate_ExactDate(t *testing.T) {
	db := testCurrencyDB(t)
	insertRate(t, db, nil, "CAD", "USD", fxRate(0.73), fxDate(2024, 6, 15))

	got, err := GetExchangeRate(db, nil, "CAD", "USD", fxDate(2024, 6, 15))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(fxRate(0.73)) {
		t.Fatalf("expected 0.73, got %s", got)
	}
}

// TestGetExchangeRate_NearestPrevious: no rate on the exact date; falls back to the
// most recent prior-date rate.
func TestGetExchangeRate_NearestPrevious(t *testing.T) {
	db := testCurrencyDB(t)
	insertRate(t, db, nil, "CAD", "USD", fxRate(0.71), fxDate(2024, 6, 1))
	insertRate(t, db, nil, "CAD", "USD", fxRate(0.72), fxDate(2024, 6, 10))
	// No rate on 2024-06-15; expect the 6-10 rate (most recent prior).

	got, err := GetExchangeRate(db, nil, "CAD", "USD", fxDate(2024, 6, 15))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(fxRate(0.72)) {
		t.Fatalf("expected 0.72 (nearest prior), got %s", got)
	}
}

// TestGetExchangeRate_NoRate: only a future rate exists; ErrNoRate is returned.
func TestGetExchangeRate_NoRate(t *testing.T) {
	db := testCurrencyDB(t)
	// Future rate — should not be returned for a past query date.
	insertRate(t, db, nil, "CAD", "USD", fxRate(0.74), fxDate(2024, 6, 20))

	_, err := GetExchangeRate(db, nil, "CAD", "USD", fxDate(2024, 6, 15))
	if !errors.Is(err, ErrNoRate) {
		t.Fatalf("expected ErrNoRate, got %v", err)
	}
}

// TestGetExchangeRate_CompanyOverride: a company-specific rate on the same date
// takes precedence over the system rate.
func TestGetExchangeRate_CompanyOverride(t *testing.T) {
	db := testCurrencyDB(t)
	companyID := uint(1)

	// System rate.
	insertRate(t, db, nil, "CAD", "USD", fxRate(0.73), fxDate(2024, 6, 15))
	// Company override — should win.
	insertRate(t, db, &companyID, "CAD", "USD", fxRate(0.75), fxDate(2024, 6, 15))

	got, err := GetExchangeRate(db, &companyID, "CAD", "USD", fxDate(2024, 6, 15))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(fxRate(0.75)) {
		t.Fatalf("expected 0.75 (company override), got %s", got)
	}
}

// ── UpsertExchangeRate tests ──────────────────────────────────────────────────

// TestUpsertExchangeRate_CreateNew verifies that a new rate is inserted when
// none exists for the given (base, target, rateType, date) combination.
func TestUpsertExchangeRate_CreateNew(t *testing.T) {
	db := testCurrencyDB(t)
	in := UpsertExchangeRateInput{
		Base:   "CAD",
		Target: "USD",
		Rate:   fxRate(0.73),
		Date:   fxDate(2024, 7, 1),
	}
	er, err := UpsertExchangeRate(db, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if er.ID == 0 {
		t.Error("expected non-zero ID after insert")
	}
	if !er.Rate.Equal(fxRate(0.73)) {
		t.Errorf("expected rate 0.73, got %s", er.Rate)
	}
}

// TestUpsertExchangeRate_ReusesExactSnapshot verifies that calling
// UpsertExchangeRate again with the exact same immutable snapshot reuses the
// existing row instead of creating noise duplicates.
func TestUpsertExchangeRate_ReusesExactSnapshot(t *testing.T) {
	db := testCurrencyDB(t)
	in := UpsertExchangeRateInput{
		Base:   "CAD",
		Target: "USD",
		Rate:   fxRate(0.73),
		Source: ExchangeRateRowSourceProviderFetched,
		Date:   fxDate(2024, 7, 1),
	}
	first, _ := UpsertExchangeRate(db, in)

	second, err := UpsertExchangeRate(db, in)
	if err != nil {
		t.Fatalf("unexpected error on exact reinsert: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("expected same row ID for exact snapshot; first=%d second=%d", first.ID, second.ID)
	}
	var count int64
	if err := db.Model(&models.ExchangeRate{}).Where("base_currency_code = ? AND target_currency_code = ?", "CAD", "USD").Count(&count).Error; err != nil {
		t.Fatalf("count exact snapshots: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one stored snapshot, got %d", count)
	}
}

func TestUpsertExchangeRate_AppendsNewSameDaySnapshot(t *testing.T) {
	db := testCurrencyDB(t)
	first, err := UpsertExchangeRate(db, UpsertExchangeRateInput{
		Base:   "CAD",
		Target: "USD",
		Rate:   fxRate(0.73),
		Source: ExchangeRateRowSourceProviderFetched,
		Date:   fxDate(2024, 7, 1),
	})
	if err != nil {
		t.Fatalf("create first snapshot: %v", err)
	}
	second, err := UpsertExchangeRate(db, UpsertExchangeRateInput{
		Base:   "CAD",
		Target: "USD",
		Rate:   fxRate(0.75),
		Source: ExchangeRateRowSourceProviderFetched,
		Date:   fxDate(2024, 7, 1),
	})
	if err != nil {
		t.Fatalf("create second snapshot: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("expected same-day changed rate to append a new snapshot row")
	}

	got, err := GetExchangeRate(db, nil, "CAD", "USD", fxDate(2024, 7, 1))
	if err != nil {
		t.Fatalf("lookup latest same-day rate: %v", err)
	}
	if !got.Equal(fxRate(0.75)) {
		t.Fatalf("expected latest same-day snapshot 0.75, got %s", got)
	}
}

// TestUpsertExchangeRate_RejectsZeroRate verifies that a zero rate is rejected.
func TestUpsertExchangeRate_RejectsZeroRate(t *testing.T) {
	db := testCurrencyDB(t)
	_, err := UpsertExchangeRate(db, UpsertExchangeRateInput{
		Base: "CAD", Target: "USD", Rate: fxRate(0), Date: fxDate(2024, 7, 1),
	})
	if err == nil {
		t.Error("expected error for zero rate")
	}
}

// TestUpsertExchangeRate_RejectsSameCurrency verifies base == target is rejected.
func TestUpsertExchangeRate_RejectsSameCurrency(t *testing.T) {
	db := testCurrencyDB(t)
	_, err := UpsertExchangeRate(db, UpsertExchangeRateInput{
		Base: "CAD", Target: "CAD", Rate: fxRate(1), Date: fxDate(2024, 7, 1),
	})
	if err == nil {
		t.Error("expected error for same base and target currency")
	}
}

// TestDeleteExchangeRate verifies that a rate can be deleted and is then gone.
func TestDeleteExchangeRate(t *testing.T) {
	db := testCurrencyDB(t)
	er, _ := UpsertExchangeRate(db, UpsertExchangeRateInput{
		Base: "CAD", Target: "USD", Rate: fxRate(0.73), Date: fxDate(2024, 7, 1),
	})

	if err := DeleteExchangeRate(db, nil, er.ID); err != nil {
		t.Fatalf("DeleteExchangeRate: %v", err)
	}

	_, err := GetExchangeRate(db, nil, "CAD", "USD", fxDate(2024, 7, 1))
	if !errors.Is(err, ErrNoRate) {
		t.Errorf("expected ErrNoRate after delete, got %v", err)
	}
}

// TestGetExchangeRate_FallsBackToSystem: company-specific rate exists for a different
// company; lookup falls back to the system rate for the requesting company.
func TestGetExchangeRate_FallsBackToSystem(t *testing.T) {
	db := testCurrencyDB(t)
	companyID := uint(1)
	otherID := uint(2)

	// System rate.
	insertRate(t, db, nil, "CAD", "USD", fxRate(0.73), fxDate(2024, 6, 15))
	// Override only for a different company.
	insertRate(t, db, &otherID, "CAD", "USD", fxRate(0.80), fxDate(2024, 6, 15))

	got, err := GetExchangeRate(db, &companyID, "CAD", "USD", fxDate(2024, 6, 15))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(fxRate(0.73)) {
		t.Fatalf("expected 0.73 (system fallback), got %s", got)
	}
}
