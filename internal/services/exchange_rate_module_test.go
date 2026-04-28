package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

type fakeExchangeRateProvider struct {
	calls int
	quote ExchangeRateProviderQuote
	err   error
}

func (p *fakeExchangeRateProvider) FetchRate(ctx context.Context, baseCurrencyCode, targetCurrencyCode string, date time.Time) (ExchangeRateProviderQuote, error) {
	p.calls++
	if p.err != nil {
		return ExchangeRateProviderQuote{}, p.err
	}
	return p.quote, nil
}

func TestResolveExchangeRateSnapshot_LocalHitSkipsProvider(t *testing.T) {
	db := testCurrencyDB(t)
	companyID := uint(7)
	insertRate(t, db, nil, "USD", "CAD", fxRate(1.37), fxDate(2026, 4, 10))
	provider := &fakeExchangeRateProvider{}

	snapshot, err := ResolveExchangeRateSnapshot(context.Background(), db, ExchangeRateResolveOptions{
		CompanyID:               companyID,
		TransactionCurrencyCode: "USD",
		BaseCurrencyCode:        "CAD",
		Date:                    fxDate(2026, 4, 10),
		AllowProviderFetch:      true,
		Provider:                provider,
	})
	if err != nil {
		t.Fatalf("ResolveExchangeRateSnapshot: %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("expected provider to be skipped, got %d calls", provider.calls)
	}
	if snapshot.ExchangeRateSource != JournalEntryExchangeRateSourceSystemStored {
		t.Fatalf("expected system_stored source, got %q", snapshot.ExchangeRateSource)
	}
}

func TestResolveExchangeRateSnapshot_LocalMissFetchesAndStoresProviderRate(t *testing.T) {
	db := testCurrencyDB(t)
	companyID := uint(7)
	provider := &fakeExchangeRateProvider{
		quote: ExchangeRateProviderQuote{
			BaseCurrencyCode:   "USD",
			TargetCurrencyCode: "CAD",
			Rate:               decimal.RequireFromString("1.41000000"),
			EffectiveDate:      fxDate(2026, 4, 10),
		},
	}

	snapshot, err := ResolveExchangeRateSnapshot(context.Background(), db, ExchangeRateResolveOptions{
		CompanyID:               companyID,
		TransactionCurrencyCode: "USD",
		BaseCurrencyCode:        "CAD",
		Date:                    fxDate(2026, 4, 10),
		AllowProviderFetch:      true,
		Provider:                provider,
	})
	if err != nil {
		t.Fatalf("ResolveExchangeRateSnapshot: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("expected provider to be called once, got %d", provider.calls)
	}
	if snapshot.SnapshotID == nil || *snapshot.SnapshotID == 0 {
		t.Fatal("expected stored snapshot id")
	}
	if snapshot.ExchangeRateSource != JournalEntryExchangeRateSourceProviderFetched {
		t.Fatalf("expected provider_fetched source, got %q", snapshot.ExchangeRateSource)
	}

	var storedCount int64
	if err := db.Model(&models.ExchangeRate{}).
		Where("base_currency_code = ? AND target_currency_code = ? AND source = ?", "USD", "CAD", ExchangeRateRowSourceProviderFetched).
		Count(&storedCount).Error; err != nil {
		t.Fatalf("count stored provider rows: %v", err)
	}
	if storedCount != 1 {
		t.Fatalf("expected one stored provider row, got %d", storedCount)
	}
}

func TestResolveExchangeRateSnapshot_ProviderFailureReturnsError(t *testing.T) {
	db := testCurrencyDB(t)
	provider := &fakeExchangeRateProvider{err: errors.New("boom")}

	_, err := ResolveExchangeRateSnapshot(context.Background(), db, ExchangeRateResolveOptions{
		CompanyID:               1,
		TransactionCurrencyCode: "USD",
		BaseCurrencyCode:        "CAD",
		Date:                    fxDate(2026, 4, 10),
		AllowProviderFetch:      true,
		Provider:                provider,
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestResolveExchangeRateSnapshot_HistoricalNearestPriorLocalRate(t *testing.T) {
	db := testCurrencyDB(t)
	insertRate(t, db, nil, "USD", "CAD", fxRate(1.31), fxDate(2026, 4, 8))
	insertRate(t, db, nil, "USD", "CAD", fxRate(1.33), fxDate(2026, 4, 9))

	snapshot, err := ResolveExchangeRateSnapshot(context.Background(), db, ExchangeRateResolveOptions{
		CompanyID:               1,
		TransactionCurrencyCode: "USD",
		BaseCurrencyCode:        "CAD",
		Date:                    fxDate(2026, 4, 10),
		AllowProviderFetch:      false,
	})
	if err != nil {
		t.Fatalf("ResolveExchangeRateSnapshot: %v", err)
	}
	if !snapshot.ExchangeRate.Equal(fxRate(1.33)) {
		t.Fatalf("expected nearest prior 1.33, got %s", snapshot.ExchangeRate)
	}
	if !snapshot.ExchangeRateDate.Equal(fxDate(2026, 4, 9)) {
		t.Fatalf("expected stored effective date 2026-04-09, got %s", snapshot.ExchangeRateDate.Format("2006-01-02"))
	}
}

func TestResolveExchangeRateSnapshot_SameCurrencyIdentityPath(t *testing.T) {
	db := testCurrencyDB(t)

	snapshot, err := ResolveExchangeRateSnapshot(context.Background(), db, ExchangeRateResolveOptions{
		CompanyID:               1,
		TransactionCurrencyCode: "CAD",
		BaseCurrencyCode:        "CAD",
		Date:                    fxDate(2026, 4, 10),
		AllowProviderFetch:      true,
	})
	if err != nil {
		t.Fatalf("ResolveExchangeRateSnapshot: %v", err)
	}
	if !snapshot.IsIdentity {
		t.Fatal("expected identity snapshot")
	}
	if snapshot.SnapshotID != nil {
		t.Fatal("identity snapshot should not point at exchange_rates row")
	}
	if !snapshot.ExchangeRate.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("expected identity rate 1, got %s", snapshot.ExchangeRate)
	}
}

func TestFrankfurterProvider_FetchRate_DecodesSinglePairResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/rate/USD/CAD" {
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("date"); got != "2026-04-10" {
			t.Fatalf("expected historical date query, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"date":"2026-04-10","base":"USD","quote":"CAD","rate":1.3742}`))
	}))
	defer server.Close()

	provider := &FrankfurterProvider{
		BaseURL:    server.URL + "/v2",
		HTTPClient: server.Client(),
	}
	quote, err := provider.FetchRate(context.Background(), "usd", "cad", fxDate(2026, 4, 10))
	if err != nil {
		t.Fatalf("FetchRate: %v", err)
	}
	if quote.BaseCurrencyCode != "USD" || quote.TargetCurrencyCode != "CAD" {
		t.Fatalf("unexpected pair %+v", quote)
	}
	if !quote.Rate.Equal(decimal.RequireFromString("1.37420000")) {
		t.Fatalf("expected 1.37420000, got %s", quote.Rate)
	}
	if !quote.EffectiveDate.Equal(fxDate(2026, 4, 10)) {
		t.Fatalf("expected effective date 2026-04-10, got %s", quote.EffectiveDate.Format("2006-01-02"))
	}
}

func TestFrankfurterProvider_FetchRate_ProviderFailurePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failure", http.StatusBadGateway)
	}))
	defer server.Close()

	provider := &FrankfurterProvider{
		BaseURL:    server.URL + "/v2",
		HTTPClient: server.Client(),
	}
	if _, err := provider.FetchRate(context.Background(), "USD", "CAD", fxDate(2026, 4, 10)); err == nil {
		t.Fatal("expected provider failure")
	}
}

func TestValidateStoredExchangeRateSnapshot_AllowsEarlierShownSnapshotAfterLaterSameDayInsert(t *testing.T) {
	db := testCurrencyDB(t)
	first, err := UpsertExchangeRate(db, UpsertExchangeRateInput{
		Base:   "USD",
		Target: "CAD",
		Rate:   decimal.RequireFromString("1.33000000"),
		Source: ExchangeRateRowSourceProviderFetched,
		Date:   fxDate(2026, 4, 10),
	})
	if err != nil {
		t.Fatalf("create first snapshot: %v", err)
	}
	if _, err := UpsertExchangeRate(db, UpsertExchangeRateInput{
		Base:   "USD",
		Target: "CAD",
		Rate:   decimal.RequireFromString("1.34000000"),
		Source: ExchangeRateRowSourceProviderFetched,
		Date:   fxDate(2026, 4, 10),
	}); err != nil {
		t.Fatalf("create second snapshot: %v", err)
	}

	snapshot, err := ValidateStoredExchangeRateSnapshot(
		db,
		7,
		first.ID,
		"USD",
		"CAD",
		decimal.RequireFromString("1.33000000"),
		fxDate(2026, 4, 10),
	)
	if err != nil {
		t.Fatalf("ValidateStoredExchangeRateSnapshot should still accept the original shown snapshot: %v", err)
	}
	if snapshot.SnapshotID == nil || *snapshot.SnapshotID != first.ID {
		t.Fatalf("expected to accept original snapshot id %d, got %+v", first.ID, snapshot.SnapshotID)
	}
}
