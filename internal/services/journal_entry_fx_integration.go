package services

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

type JournalEntrySnapshotCandidate struct {
	ExchangeRate       decimal.Decimal
	ExchangeRateDate   time.Time
	ExchangeRateSource string
	SnapshotID         *uint
}

type PrepareJournalEntryInput struct {
	CompanyID                uint
	EntryDate                time.Time
	JournalNo                string
	TransactionCurrencyCode  string
	Snapshot                 JournalEntrySnapshotCandidate
	LineDrafts               []JournalLineDraft
}

type PreparedJournalEntry struct {
	JournalEntry     models.JournalEntry
	JournalLines     []models.JournalLine
	AcceptedSnapshot ExchangeRateSnapshot
}

// PrepareJournalEntryForSave applies JE-specific FX integration on top of reusable currency/rate/conversion modules.
func PrepareJournalEntryForSave(db *gorm.DB, input PrepareJournalEntryInput) (PreparedJournalEntry, error) {
	ctx, err := LoadCompanyCurrencyContext(db, input.CompanyID)
	if err != nil {
		return PreparedJournalEntry{}, fmt.Errorf("load company currency context: %w", err)
	}

	transactionCurrencyCode, err := NormalizeTransactionCurrencyCode(ctx, input.TransactionCurrencyCode)
	if err != nil {
		return PreparedJournalEntry{}, err
	}

	validLines, err := ValidateJournalLines(input.LineDrafts)
	if err != nil {
		return PreparedJournalEntry{}, err
	}

	snapshot, err := acceptJournalEntrySnapshot(db, input.CompanyID, ctx.BaseCurrencyCode, transactionCurrencyCode, input.EntryDate, input.Snapshot)
	if err != nil {
		return PreparedJournalEntry{}, err
	}

	conversionLines := make([]FXLineAmounts, 0, len(validLines))
	for _, line := range validLines {
		conversionLines = append(conversionLines, FXLineAmounts{
			TxDebit:  line.Debit,
			TxCredit: line.Credit,
		})
	}
	converted, err := ConvertJournalLineAmounts(conversionLines, snapshot.ExchangeRate)
	if err != nil {
		return PreparedJournalEntry{}, err
	}

	for i := range validLines {
		validLines[i].CompanyID = input.CompanyID
		validLines[i].TxDebit = converted.Lines[i].TxDebit
		validLines[i].TxCredit = converted.Lines[i].TxCredit
		validLines[i].Debit = converted.Lines[i].Debit
		validLines[i].Credit = converted.Lines[i].Credit
	}

	if err := EnsureJournalLineReferencesBelongToCompany(db, input.CompanyID, transactionCurrencyCode, validLines); err != nil {
		return PreparedJournalEntry{}, err
	}

	je := models.JournalEntry{
		CompanyID:               input.CompanyID,
		EntryDate:               input.EntryDate,
		JournalNo:               strings.TrimSpace(input.JournalNo),
		Status:                  models.JournalEntryStatusPosted,
		TransactionCurrencyCode: transactionCurrencyCode,
		ExchangeRate:            snapshot.ExchangeRate,
		ExchangeRateDate:        snapshot.ExchangeRateDate,
		ExchangeRateSource:      snapshot.ExchangeRateSource,
	}

	return PreparedJournalEntry{
		JournalEntry:     je,
		JournalLines:     validLines,
		AcceptedSnapshot: snapshot,
	}, nil
}

func acceptJournalEntrySnapshot(db *gorm.DB, companyID uint, baseCurrencyCode, transactionCurrencyCode string, entryDate time.Time, candidate JournalEntrySnapshotCandidate) (ExchangeRateSnapshot, error) {
	entryDay := normalizeDate(entryDate)
	if transactionCurrencyCode == baseCurrencyCode {
		return ExchangeRateSnapshot{
			TransactionCurrencyCode: transactionCurrencyCode,
			BaseCurrencyCode:        baseCurrencyCode,
			ExchangeRate:            decimal.NewFromInt(1),
			ExchangeRateDate:        entryDay,
			ExchangeRateSource:      JournalEntryExchangeRateSourceIdentity,
			SourceLabel:             ExchangeRateSourceLabel(JournalEntryExchangeRateSourceIdentity),
			IsIdentity:              true,
		}, nil
	}

	if strings.EqualFold(strings.TrimSpace(candidate.ExchangeRateSource), JournalEntryExchangeRateSourceManual) {
		if !candidate.ExchangeRate.GreaterThan(decimal.Zero) {
			return ExchangeRateSnapshot{}, fmt.Errorf("enter a valid exchange rate greater than 0")
		}
		rateDate := candidate.ExchangeRateDate
		if rateDate.IsZero() {
			rateDate = entryDay
		}
		return ExchangeRateSnapshot{
			TransactionCurrencyCode: transactionCurrencyCode,
			BaseCurrencyCode:        baseCurrencyCode,
			ExchangeRate:            candidate.ExchangeRate.RoundBank(8),
			ExchangeRateDate:        normalizeDate(rateDate),
			ExchangeRateSource:      JournalEntryExchangeRateSourceManual,
			SourceLabel:             ExchangeRateSourceLabel(JournalEntryExchangeRateSourceManual),
		}, nil
	}

	if candidate.SnapshotID == nil || *candidate.SnapshotID == 0 {
		return ExchangeRateSnapshot{}, fmt.Errorf("refresh the exchange rate before saving this foreign-currency journal entry")
	}
	if !candidate.ExchangeRate.GreaterThan(decimal.Zero) {
		return ExchangeRateSnapshot{}, fmt.Errorf("refresh the exchange rate before saving this foreign-currency journal entry")
	}
	if candidate.ExchangeRateDate.IsZero() {
		return ExchangeRateSnapshot{}, fmt.Errorf("refresh the exchange rate before saving this foreign-currency journal entry")
	}

	return ValidateStoredExchangeRateSnapshot(
		db,
		companyID,
		*candidate.SnapshotID,
		transactionCurrencyCode,
		baseCurrencyCode,
		candidate.ExchangeRate.RoundBank(8),
		candidate.ExchangeRateDate,
	)
}
