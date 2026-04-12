package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

const journalEntryFXMigrationVersion = "048_journal_entry_fx_support.sql"

type JournalEntryReadFXState struct {
	TransactionCurrencyCode    string
	TransactionCurrencyDisplay string
	BaseCurrencyCode           string
	ExchangeRate               decimal.Decimal
	ExchangeRateDate           time.Time
	ExchangeRateSource         string
	ExchangeRateSourceLabel    string
	IsForeignCurrency          bool
	SnapshotResolved           bool
	TransactionAmountsPresent  bool
	SnapshotNote               string
	ReversalAllowed            bool
	ReversalBlockedReason      string
}

const LegacyForeignJournalEntryReversalBlockedMessage = "This legacy foreign-currency journal entry cannot be reversed automatically because Gobooks could not reconstruct a reliable historical FX snapshot."

type JournalEntryFXResolver struct {
	db                 *gorm.DB
	baseCurrencyCode   string
	migrationChecked   bool
	migrationAppliedAt *time.Time
}

func NewJournalEntryFXResolver(db *gorm.DB, baseCurrencyCode string) *JournalEntryFXResolver {
	return &JournalEntryFXResolver{
		db:               db,
		baseCurrencyCode: normalizeCurrencyCode(baseCurrencyCode),
	}
}

// BuildJournalEntryReadFXState returns the immutable FX read model for a posted
// JE. New JEs read their persisted snapshot directly. Legacy JEs created before
// migration 048 are handled honestly: base-only history stays identity, linked
// foreign-source documents are reconstructed when possible, and otherwise the
// read path marks FX details unavailable instead of fabricating identity state.
func BuildJournalEntryReadFXState(db *gorm.DB, baseCurrencyCode string, je models.JournalEntry) (JournalEntryReadFXState, error) {
	return NewJournalEntryFXResolver(db, baseCurrencyCode).BuildReadState(je)
}

func (r *JournalEntryFXResolver) BuildReadState(je models.JournalEntry) (JournalEntryReadFXState, error) {
	baseCurrencyCode := r.baseCurrencyCode
	if baseCurrencyCode == "" {
		baseCurrencyCode = normalizeCurrencyCode(je.TransactionCurrencyCode)
	}

	state := JournalEntryReadFXState{
		TransactionCurrencyCode:   normalizeCurrencyCode(strings.TrimSpace(je.TransactionCurrencyCode)),
		BaseCurrencyCode:          baseCurrencyCode,
		ExchangeRate:              je.ExchangeRate.RoundBank(8),
		ExchangeRateDate:          je.ExchangeRateDate,
		ExchangeRateSource:        strings.TrimSpace(je.ExchangeRateSource),
		SnapshotResolved:          true,
		TransactionAmountsPresent: true,
		ReversalAllowed:           true,
	}
	if state.TransactionCurrencyCode == "" {
		state.TransactionCurrencyCode = baseCurrencyCode
	}
	if state.ExchangeRate.IsZero() {
		state.ExchangeRate = decimal.NewFromInt(1)
	}
	if state.ExchangeRateDate.IsZero() {
		state.ExchangeRateDate = je.EntryDate
	}
	state.ExchangeRateDate = normalizeDate(state.ExchangeRateDate)
	if state.ExchangeRateSource == "" {
		state.ExchangeRateSource = JournalEntryExchangeRateSourceIdentity
	}
	state.ExchangeRateSourceLabel = ExchangeRateSourceLabel(state.ExchangeRateSource)
	state.IsForeignCurrency = state.TransactionCurrencyCode != "" && state.TransactionCurrencyCode != baseCurrencyCode

	legacy, err := r.journalEntryPredatesFXMigration(je)
	if err != nil {
		return JournalEntryReadFXState{}, err
	}
	if legacy && strings.TrimSpace(string(je.SourceType)) != "" {
		legacyState, err := buildLegacyJournalEntryReadFXState(r.db, je, baseCurrencyCode)
		if err != nil {
			return JournalEntryReadFXState{}, err
		}
		if legacyState != nil {
			state = *legacyState
		}
	}

	finalizeJournalEntryReadFXState(&state)
	return state, nil
}

func (r *JournalEntryFXResolver) journalEntryPredatesFXMigration(je models.JournalEntry) (bool, error) {
	if je.CreatedAt.IsZero() {
		return false, nil
	}
	appliedAt, err := r.fxMigrationAppliedAt()
	if err != nil || appliedAt == nil {
		return false, err
	}
	return je.CreatedAt.Before(*appliedAt), nil
}

func (r *JournalEntryFXResolver) fxMigrationAppliedAt() (*time.Time, error) {
	if r.migrationChecked {
		return r.migrationAppliedAt, nil
	}
	r.migrationChecked = true
	if r.db == nil {
		return nil, nil
	}
	var appliedAtRaw string
	err := r.db.Raw(
		`SELECT applied_at FROM schema_migrations WHERE version = ? LIMIT 1`,
		journalEntryFXMigrationVersion,
	).Scan(&appliedAtRaw).Error
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "schema_migrations") {
			return nil, nil
		}
		return nil, err
	}
	appliedAtRaw = strings.TrimSpace(appliedAtRaw)
	if appliedAtRaw == "" {
		return nil, nil
	}
	appliedAt, err := parseSchemaMigrationAppliedAt(appliedAtRaw)
	if err != nil {
		return nil, err
	}
	r.migrationAppliedAt = &appliedAt
	return r.migrationAppliedAt, nil
}

func buildLegacyJournalEntryReadFXState(db *gorm.DB, je models.JournalEntry, baseCurrencyCode string) (*JournalEntryReadFXState, error) {
	switch je.SourceType {
	case models.LedgerSourceInvoice:
		var invoice models.Invoice
		err := db.Select("id", "company_id", "invoice_date", "currency_code", "exchange_rate", "amount", "amount_base").
			Where("id = ? AND company_id = ?", je.SourceID, je.CompanyID).
			First(&invoice).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				state := legacyUnavailableJournalEntryReadFXState(
					baseCurrencyCode,
					"This journal entry predates Gobooks FX snapshots and its linked invoice is no longer available. Historical FX details are unavailable.",
				)
				return &state, nil
			}
			return nil, err
		}
		return buildLegacyDocumentJournalEntryReadFXState(
			baseCurrencyCode,
			normalizeCurrencyCode(invoice.CurrencyCode),
			invoice.ExchangeRate,
			invoice.Amount,
			invoice.AmountBase,
			invoice.InvoiceDate,
			"invoice",
		), nil
	case models.LedgerSourceBill:
		var bill models.Bill
		err := db.Select("id", "company_id", "bill_date", "currency_code", "exchange_rate", "amount", "amount_base").
			Where("id = ? AND company_id = ?", je.SourceID, je.CompanyID).
			First(&bill).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				state := legacyUnavailableJournalEntryReadFXState(
					baseCurrencyCode,
					"This journal entry predates Gobooks FX snapshots and its linked bill is no longer available. Historical FX details are unavailable.",
				)
				return &state, nil
			}
			return nil, err
		}
		return buildLegacyDocumentJournalEntryReadFXState(
			baseCurrencyCode,
			normalizeCurrencyCode(bill.CurrencyCode),
			bill.ExchangeRate,
			bill.Amount,
			bill.AmountBase,
			bill.BillDate,
			"bill",
		), nil
	default:
		state := legacyUnavailableJournalEntryReadFXState(
			baseCurrencyCode,
			fmt.Sprintf("This %s journal entry predates Gobooks FX snapshots and no reliable historical FX source is available.", strings.TrimSpace(string(je.SourceType))),
		)
		return &state, nil
	}
}

func buildLegacyDocumentJournalEntryReadFXState(baseCurrencyCode, transactionCurrencyCode string, exchangeRate, amount, amountBase decimal.Decimal, date time.Time, documentLabel string) *JournalEntryReadFXState {
	if transactionCurrencyCode == "" || transactionCurrencyCode == baseCurrencyCode {
		return nil
	}

	resolvedRate := exchangeRate.RoundBank(8)
	if !resolvedRate.GreaterThan(decimal.Zero) && amount.GreaterThan(decimal.Zero) && amountBase.GreaterThan(decimal.Zero) {
		resolvedRate = amountBase.Div(amount).RoundBank(8)
	}

	note := fmt.Sprintf(
		"Legacy foreign-currency journal entry reconstructed from its linked %s. Original transaction-currency line amounts were not persisted before FX snapshots were introduced.",
		documentLabel,
	)
	if !resolvedRate.GreaterThan(decimal.Zero) {
		state := legacyUnavailableJournalEntryReadFXState(
			baseCurrencyCode,
			fmt.Sprintf(
				"This legacy journal entry was linked to a %s in %s, but Gobooks could not reconstruct a reliable historical FX rate. Transaction-currency line amounts were not persisted.",
				documentLabel,
				transactionCurrencyCode,
			),
		)
		state.TransactionCurrencyCode = transactionCurrencyCode
		state.ExchangeRateDate = normalizeDate(date)
		state.IsForeignCurrency = transactionCurrencyCode != baseCurrencyCode
		return &state
	}

	state := JournalEntryReadFXState{
		TransactionCurrencyCode:   transactionCurrencyCode,
		BaseCurrencyCode:          baseCurrencyCode,
		ExchangeRate:              resolvedRate,
		ExchangeRateDate:          normalizeDate(date),
		ExchangeRateSource:        JournalEntryExchangeRateSourceLegacyUnavailable,
		ExchangeRateSourceLabel:   ExchangeRateSourceLabel(JournalEntryExchangeRateSourceLegacyUnavailable),
		IsForeignCurrency:         true,
		SnapshotResolved:          true,
		TransactionAmountsPresent: false,
		SnapshotNote:              note,
	}
	return &state
}

func legacyUnavailableJournalEntryReadFXState(baseCurrencyCode, note string) JournalEntryReadFXState {
	return JournalEntryReadFXState{
		BaseCurrencyCode:          baseCurrencyCode,
		ExchangeRateSource:        JournalEntryExchangeRateSourceLegacyUnavailable,
		ExchangeRateSourceLabel:   ExchangeRateSourceLabel(JournalEntryExchangeRateSourceLegacyUnavailable),
		SnapshotResolved:          false,
		TransactionAmountsPresent: false,
		ReversalAllowed:           false,
		SnapshotNote:              note,
	}
}

func finalizeJournalEntryReadFXState(state *JournalEntryReadFXState) {
	if state == nil {
		return
	}
	state.BaseCurrencyCode = normalizeCurrencyCode(state.BaseCurrencyCode)
	state.TransactionCurrencyCode = normalizeCurrencyCode(state.TransactionCurrencyCode)
	if state.ExchangeRate.IsZero() {
		state.ExchangeRate = decimal.NewFromInt(1)
	}
	if state.ExchangeRateSource == "" {
		state.ExchangeRateSource = JournalEntryExchangeRateSourceIdentity
	}
	state.ExchangeRateSourceLabel = ExchangeRateSourceLabel(state.ExchangeRateSource)
	state.IsForeignCurrency = state.TransactionCurrencyCode != "" && state.TransactionCurrencyCode != state.BaseCurrencyCode
	state.ReversalAllowed = true
	state.ReversalBlockedReason = ""
	if state.IsForeignCurrency && state.ExchangeRateSource == JournalEntryExchangeRateSourceLegacyUnavailable {
		state.TransactionAmountsPresent = false
		if strings.TrimSpace(state.SnapshotNote) == "" {
			state.SnapshotNote = "This journal entry preserves a legacy FX snapshot. Original transaction-currency line amounts were unavailable, so only base-currency line amounts are shown."
		}
	}
	if state.TransactionCurrencyCode != "" {
		state.TransactionCurrencyDisplay = state.TransactionCurrencyCode
	} else {
		state.TransactionCurrencyDisplay = ExchangeRateSourceLabel(JournalEntryExchangeRateSourceLegacyUnavailable)
	}
	if !state.SnapshotResolved {
		state.ReversalAllowed = false
		state.ReversalBlockedReason = LegacyForeignJournalEntryReversalBlockedMessage
	}
	if state.IsForeignCurrency && !state.TransactionAmountsPresent {
		if state.SnapshotResolved {
			state.ReversalAllowed = true
			state.ReversalBlockedReason = ""
		} else {
			state.ReversalAllowed = false
			state.ReversalBlockedReason = LegacyForeignJournalEntryReversalBlockedMessage
		}
	}
}

func parseSchemaMigrationAppliedAt(raw string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse schema_migrations applied_at %q", raw)
}
