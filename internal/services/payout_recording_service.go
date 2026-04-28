// 遵循project_guide.md
package services

// payout_recording_service.go — Record channel payouts as JEs (Dr Bank, Cr Clearing).
//
// When a platform (e.g. Amazon) pays out to the company bank account, this
// service creates a JE that:
//   Dr Bank Account       (cash received)
//   Cr Clearing Account   (reduces the platform's balance owed)
//
// This completes the accounting cycle:
//   1. Invoice posting:    Dr AR, Cr Revenue         (sale recognized)
//   2. Settlement posting: Dr Fee Expense, Cr Clearing (fees recognized)
//   3. Payout recording:   Dr Bank, Cr Clearing      (cash received)
//
// After step 3, the clearing account balance is reduced by the payout amount.
// The bank-side journal line can then be reconciled against the bank statement
// using the existing QuickBooks-style reconciliation system.
//
// No separate bank_transactions table is needed — the JE bank line IS the
// bank transaction in Balanciz' existing architecture.
//
// This round only supports full-amount matching (payout.amount == JE amount).

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

var (
	ErrPayoutAlreadyRecorded = errors.New("payout has already been recorded for this settlement")
	ErrPayoutNotFound        = errors.New("settlement has no payout line")
	ErrNoBankAccount         = errors.New("bank account is required to record a payout")
	ErrPayoutAmountInvalid   = errors.New("settlement payout amount must be greater than zero")
	ErrPayoutNetMismatch     = errors.New("payout line total must match the settlement net amount before recording payout")
	ErrSettlementNetInvalid  = errors.New("settlement net amount must be greater than zero before recording payout")
)

// RecordPayoutInput holds parameters for recording a channel payout.
type RecordPayoutInput struct {
	CompanyID    uint
	SettlementID uint
	BankAccountID uint      // the bank/cash account to debit
	EntryDate    time.Time  // date of the payout receipt
	Memo         string
}

// RecordPayoutResult holds the outcome of a payout recording.
type RecordPayoutResult struct {
	JournalEntryID uint
	PayoutAmount   decimal.Decimal
}

// ValidatePayoutRecordable checks whether a settlement's payout can be recorded.
func ValidatePayoutRecordable(db *gorm.DB, companyID, settlementID uint) error {
	settlement, err := GetSettlement(db, companyID, settlementID)
	if err != nil {
		return fmt.Errorf("settlement not found")
	}

	if settlement.PayoutJournalEntryID != nil {
		return ErrPayoutAlreadyRecorded
	}

	// Check that a payout line exists.
	lines, _ := GetSettlementLines(db, companyID, settlementID)
	hasPayout := false
	for _, l := range lines {
		if l.LineType == models.SettlementLinePayout {
			hasPayout = true
			break
		}
	}
	if !hasPayout {
		return ErrPayoutNotFound
	}

	totals := ComputeSettlementTotals(lines)
	if !totals.PayoutAmount.GreaterThan(decimal.Zero) {
		return ErrPayoutAmountInvalid
	}
	if !totals.NetAmount.GreaterThan(decimal.Zero) {
		return ErrSettlementNetInvalid
	}
	if !totals.PayoutAmount.Equal(totals.NetAmount) {
		return fmt.Errorf("%w (payout %s, net %s)",
			ErrPayoutNetMismatch,
			totals.PayoutAmount.StringFixed(2),
			totals.NetAmount.StringFixed(2),
		)
	}

	// Check clearing account.
	mapping, _ := GetAccountingMapping(db, companyID, settlement.ChannelAccountID)
	if mapping == nil || mapping.ClearingAccountID == nil {
		return ErrNoClearingAccount
	}

	return nil
}

// RecordPayout creates a JE for a channel payout (Dr Bank, Cr Clearing).
func RecordPayout(db *gorm.DB, input RecordPayoutInput, actor string) (*RecordPayoutResult, error) {
	if err := ValidatePayoutRecordable(db, input.CompanyID, input.SettlementID); err != nil {
		return nil, err
	}
	if input.BankAccountID == 0 {
		return nil, ErrNoBankAccount
	}

	settlement, _ := GetSettlement(db, input.CompanyID, input.SettlementID)
	lines, _ := GetSettlementLines(db, input.CompanyID, input.SettlementID)
	mapping, _ := GetAccountingMapping(db, input.CompanyID, settlement.ChannelAccountID)
	clearingAcctID := *mapping.ClearingAccountID

	totals := ComputeSettlementTotals(lines)
	payoutAmount := totals.PayoutAmount

	if payoutAmount.IsZero() {
		return nil, ErrPayoutAmountInvalid
	}

	entryDate := input.EntryDate
	if entryDate.IsZero() {
		if settlement.SettlementDate != nil {
			entryDate = *settlement.SettlementDate
		} else {
			entryDate = time.Now()
		}
	}

	memo := input.Memo
	if memo == "" {
		memo = "Channel payout: " + settlement.ExternalSettlementID
	}

	var result RecordPayoutResult
	err := db.Transaction(func(tx *gorm.DB) error {
		// Create JE.
		je := models.JournalEntry{
			CompanyID:  input.CompanyID,
			EntryDate:  entryDate,
			JournalNo:  "PAYOUT-" + settlement.ExternalSettlementID,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourcePayout,
			SourceID:   settlement.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create journal entry: %w", err)
		}

		// Dr Bank.
		bankLine := models.JournalLine{
			CompanyID: input.CompanyID, JournalEntryID: je.ID,
			AccountID: input.BankAccountID,
			Debit: payoutAmount, Credit: decimal.Zero,
			Memo: memo,
		}
		if err := tx.Create(&bankLine).Error; err != nil {
			return fmt.Errorf("create bank debit line: %w", err)
		}

		// Cr Clearing.
		clearingLine := models.JournalLine{
			CompanyID: input.CompanyID, JournalEntryID: je.ID,
			AccountID: clearingAcctID,
			Debit: decimal.Zero, Credit: payoutAmount,
			Memo: memo,
		}
		if err := tx.Create(&clearingLine).Error; err != nil {
			return fmt.Errorf("create clearing credit line: %w", err)
		}

		// Ledger projection.
		if err := ProjectToLedger(tx, input.CompanyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        []models.JournalLine{bankLine, clearingLine},
			SourceType:   models.LedgerSourcePayout,
			SourceID:     settlement.ID,
		}); err != nil {
			return fmt.Errorf("project to ledger: %w", err)
		}

		// Mark settlement payout recorded.
		now := time.Now()
		if err := tx.Model(&models.ChannelSettlement{}).
			Where("id = ? AND company_id = ?", input.SettlementID, input.CompanyID).
			Updates(map[string]any{
				"payout_journal_entry_id": je.ID,
				"payout_recorded_at":     now,
			}).Error; err != nil {
			return fmt.Errorf("mark payout recorded: %w", err)
		}

		// Audit log.
		cid := input.CompanyID
		WriteAuditLogWithContextDetails(tx, "settlement.payout_recorded", "channel_settlement", input.SettlementID, actor,
			map[string]any{"company_id": input.CompanyID},
			&cid, nil, nil,
			map[string]any{
				"settlement_id":    input.SettlementID,
				"journal_entry_id": je.ID,
				"payout_amount":    payoutAmount.StringFixed(2),
				"bank_account_id":  input.BankAccountID,
			},
		)

		result = RecordPayoutResult{JournalEntryID: je.ID, PayoutAmount: payoutAmount}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}
