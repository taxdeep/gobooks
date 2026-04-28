package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

const (
	ExchangeRateRowSourceManual          = "manual"
	ExchangeRateRowSourceProviderFetched = "provider_fetched"
	ExchangeRateRowSourceLegacyUnknown   = "legacy_unknown"

	JournalEntryExchangeRateSourceIdentity        = "identity"
	JournalEntryExchangeRateSourceManual          = "manual"
	JournalEntryExchangeRateSourceCompanyOverride = "company_override"
	JournalEntryExchangeRateSourceSystemStored    = "system_stored"
	JournalEntryExchangeRateSourceProviderFetched = "provider_fetched"
	JournalEntryExchangeRateSourceLegacyUnavailable = "legacy_unavailable"
)

type ExchangeRateSnapshot struct {
	SnapshotID              *uint
	TransactionCurrencyCode string
	BaseCurrencyCode        string
	ExchangeRate            decimal.Decimal
	ExchangeRateDate        time.Time
	ExchangeRateSource      string
	SourceLabel             string
	IsIdentity              bool
}

type ExchangeRateProviderQuote struct {
	BaseCurrencyCode   string
	TargetCurrencyCode string
	Rate               decimal.Decimal
	EffectiveDate      time.Time
}

type ExchangeRateProvider interface {
	FetchRate(ctx context.Context, baseCurrencyCode, targetCurrencyCode string, date time.Time) (ExchangeRateProviderQuote, error)
}

type FrankfurterProvider struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewFrankfurterProvider() *FrankfurterProvider {
	return &FrankfurterProvider{
		BaseURL:    "https://api.frankfurter.dev/v2",
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *FrankfurterProvider) FetchRate(ctx context.Context, baseCurrencyCode, targetCurrencyCode string, date time.Time) (ExchangeRateProviderQuote, error) {
	baseCurrencyCode = normalizeCurrencyCode(baseCurrencyCode)
	targetCurrencyCode = normalizeCurrencyCode(targetCurrencyCode)
	if baseCurrencyCode == "" || targetCurrencyCode == "" {
		return ExchangeRateProviderQuote{}, fmt.Errorf("base and target currencies are required")
	}
	if baseCurrencyCode == targetCurrencyCode {
		return ExchangeRateProviderQuote{}, fmt.Errorf("base and target currencies must differ")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.frankfurter.dev/v2"
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ExchangeRateProviderQuote{}, err
	}
	u.Path = path.Join(u.Path, "rate", baseCurrencyCode, targetCurrencyCode)
	q := u.Query()
	q.Set("date", normalizeDate(date).Format("2006-01-02"))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ExchangeRateProviderQuote{}, err
	}

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ExchangeRateProviderQuote{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ExchangeRateProviderQuote{}, fmt.Errorf("frankfurter returned %s", resp.Status)
	}

	var body struct {
		Date  string      `json:"date"`
		Base  string      `json:"base"`
		Quote string      `json:"quote"`
		Rate  json.Number `json:"rate"`
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return ExchangeRateProviderQuote{}, fmt.Errorf("decode frankfurter response: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(body.Base), baseCurrencyCode) {
		return ExchangeRateProviderQuote{}, fmt.Errorf("frankfurter response did not include %s as the base currency", baseCurrencyCode)
	}
	if !strings.EqualFold(strings.TrimSpace(body.Quote), targetCurrencyCode) {
		return ExchangeRateProviderQuote{}, fmt.Errorf("frankfurter response did not include %s as the quote currency", targetCurrencyCode)
	}
	rate, err := decimal.NewFromString(body.Rate.String())
	if err != nil || !rate.GreaterThan(decimal.Zero) {
		return ExchangeRateProviderQuote{}, fmt.Errorf("frankfurter returned an invalid rate")
	}
	effectiveDate, err := time.Parse("2006-01-02", body.Date)
	if err != nil {
		return ExchangeRateProviderQuote{}, fmt.Errorf("frankfurter returned an invalid date")
	}
	return ExchangeRateProviderQuote{
		BaseCurrencyCode:   baseCurrencyCode,
		TargetCurrencyCode: targetCurrencyCode,
		Rate:               rate.RoundBank(8),
		EffectiveDate:      normalizeDate(effectiveDate),
	}, nil
}

type ExchangeRateResolveOptions struct {
	CompanyID              uint
	TransactionCurrencyCode string
	BaseCurrencyCode       string
	Date                   time.Time
	AllowProviderFetch     bool
	Provider               ExchangeRateProvider
}

// ResolveExchangeRateSnapshot performs local-first FX lookup and returns reusable snapshot semantics.
func ResolveExchangeRateSnapshot(ctx context.Context, db *gorm.DB, opts ExchangeRateResolveOptions) (ExchangeRateSnapshot, error) {
	txCurrency := normalizeCurrencyCode(opts.TransactionCurrencyCode)
	baseCurrency := normalizeCurrencyCode(opts.BaseCurrencyCode)
	day := normalizeDate(opts.Date)

	if txCurrency == "" || baseCurrency == "" {
		return ExchangeRateSnapshot{}, fmt.Errorf("transaction and base currencies are required")
	}
	if txCurrency == baseCurrency {
		return IdentityExchangeRateSnapshot(txCurrency, day), nil
	}

	if row, found, err := lookupExchangeRateRow(db, opts.CompanyID, txCurrency, baseCurrency, day); err != nil {
		return ExchangeRateSnapshot{}, err
	} else if found {
		return snapshotFromExchangeRateRow(row, opts.CompanyID), nil
	}

	if !opts.AllowProviderFetch {
		return ExchangeRateSnapshot{}, ErrNoRate
	}

	provider := opts.Provider
	if provider == nil {
		provider = NewFrankfurterProvider()
	}
	quote, err := provider.FetchRate(ctx, txCurrency, baseCurrency, day)
	if err != nil {
		return ExchangeRateSnapshot{}, err
	}
	row, err := UpsertExchangeRate(db, UpsertExchangeRateInput{
		Base:     quote.BaseCurrencyCode,
		Target:   quote.TargetCurrencyCode,
		Rate:     quote.Rate,
		RateType: "spot",
		Source:   ExchangeRateRowSourceProviderFetched,
		Date:     quote.EffectiveDate,
	})
	if err != nil {
		return ExchangeRateSnapshot{}, err
	}
	return snapshotFromExchangeRateRow(row, opts.CompanyID), nil
}

func IdentityExchangeRateSnapshot(currencyCode string, date time.Time) ExchangeRateSnapshot {
	day := normalizeDate(date)
	return ExchangeRateSnapshot{
		TransactionCurrencyCode: normalizeCurrencyCode(currencyCode),
		BaseCurrencyCode:        normalizeCurrencyCode(currencyCode),
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        day,
		ExchangeRateSource:      JournalEntryExchangeRateSourceIdentity,
		SourceLabel:             ExchangeRateSourceLabel(JournalEntryExchangeRateSourceIdentity),
		IsIdentity:              true,
	}
}

// ValidateStoredExchangeRateSnapshot enforces save-time exact local snapshot validation.
func ValidateStoredExchangeRateSnapshot(db *gorm.DB, companyID uint, snapshotID uint, transactionCurrencyCode, baseCurrencyCode string, rate decimal.Decimal, date time.Time) (ExchangeRateSnapshot, error) {
	if snapshotID == 0 {
		return ExchangeRateSnapshot{}, fmt.Errorf("an exchange-rate snapshot is required")
	}
	var row models.ExchangeRate
	if err := db.First(&row, snapshotID).Error; err != nil {
		return ExchangeRateSnapshot{}, err
	}
	if row.CompanyID != nil && *row.CompanyID != companyID {
		return ExchangeRateSnapshot{}, fmt.Errorf("exchange-rate snapshot does not belong to this company")
	}
	if normalizeCurrencyCode(row.BaseCurrencyCode) != normalizeCurrencyCode(transactionCurrencyCode) ||
		normalizeCurrencyCode(row.TargetCurrencyCode) != normalizeCurrencyCode(baseCurrencyCode) {
		return ExchangeRateSnapshot{}, fmt.Errorf("exchange-rate snapshot does not match the selected currencies")
	}
	if !normalizeDate(row.EffectiveDate).Equal(normalizeDate(date)) {
		return ExchangeRateSnapshot{}, fmt.Errorf("exchange-rate snapshot date does not match the submitted rate")
	}
	if !row.Rate.RoundBank(8).Equal(rate.RoundBank(8)) {
		return ExchangeRateSnapshot{}, fmt.Errorf("exchange-rate snapshot rate does not match the submitted rate")
	}
	return snapshotFromExchangeRateRow(row, companyID), nil
}

func NormalizeExchangeRateRowSource(source string) string {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case ExchangeRateRowSourceManual:
		return ExchangeRateRowSourceManual
	case ExchangeRateRowSourceProviderFetched:
		return ExchangeRateRowSourceProviderFetched
	default:
		return ExchangeRateRowSourceLegacyUnknown
	}
}

func ExchangeRateSourceLabel(source string) string {
	switch source {
	case JournalEntryExchangeRateSourceIdentity:
		return "Identity"
	case JournalEntryExchangeRateSourceManual:
		return "Manual"
	case JournalEntryExchangeRateSourceCompanyOverride:
		return "Company Override"
	case JournalEntryExchangeRateSourceProviderFetched:
		return "Latest"
	case JournalEntryExchangeRateSourceLegacyUnavailable:
		return "Unavailable (legacy)"
	default:
		return "Stored"
	}
}

func snapshotFromExchangeRateRow(row models.ExchangeRate, companyID uint) ExchangeRateSnapshot {
	snapshotID := row.ID
	source := JournalEntryExchangeRateSourceSystemStored
	if row.CompanyID != nil && *row.CompanyID == companyID {
		source = JournalEntryExchangeRateSourceCompanyOverride
	} else if NormalizeExchangeRateRowSource(row.Source) == ExchangeRateRowSourceProviderFetched {
		source = JournalEntryExchangeRateSourceProviderFetched
	}
	return ExchangeRateSnapshot{
		SnapshotID:              &snapshotID,
		TransactionCurrencyCode: normalizeCurrencyCode(row.BaseCurrencyCode),
		BaseCurrencyCode:        normalizeCurrencyCode(row.TargetCurrencyCode),
		ExchangeRate:            row.Rate.RoundBank(8),
		ExchangeRateDate:        normalizeDate(row.EffectiveDate),
		ExchangeRateSource:      source,
		SourceLabel:             ExchangeRateSourceLabel(source),
		IsIdentity:              false,
	}
}

func lookupExchangeRateRow(db *gorm.DB, companyID uint, baseCurrencyCode, targetCurrencyCode string, date time.Time) (models.ExchangeRate, bool, error) {
	day := normalizeDate(date)
	lookup := func(scopeCompanyID *uint) (models.ExchangeRate, bool, error) {
		q := db.Model(&models.ExchangeRate{}).
			Where("base_currency_code = ? AND target_currency_code = ?", baseCurrencyCode, targetCurrencyCode).
			Where("effective_date <= ?", day)
		if scopeCompanyID == nil {
			q = q.Where("company_id IS NULL")
		} else {
			q = q.Where("company_id = ?", *scopeCompanyID)
		}
		var row models.ExchangeRate
		if err := q.Order("effective_date DESC, id DESC").First(&row).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return models.ExchangeRate{}, false, nil
			}
			return models.ExchangeRate{}, false, err
		}
		return row, true, nil
	}

	if row, found, err := lookup(&companyID); err != nil {
		return models.ExchangeRate{}, false, err
	} else if found {
		return row, true, nil
	}
	return lookup(nil)
}

func normalizeDate(date time.Time) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
}
