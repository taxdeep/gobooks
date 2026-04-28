// 遵循project_guide.md
package services

// settlement_reversal_service.go — Reverse posted settlement fee JE and payout JE.
//
// Settlement fee reversal:
//   Original: Dr FeeExpense, Cr Clearing (for each postable line)
//   Reversal: Dr Clearing, Cr FeeExpense (standard JE reversal via ReverseJournalEntry)
//
// Payout reversal:
//   Original: Dr Bank, Cr Clearing
//   Reversal: Dr Clearing, Cr Bank
//   Blocked if the bank-side journal line has been reconciled (ReconciliationID set).

import (
	"errors"
	"fmt"
	"time"

	"balanciz/internal/models"
	"gorm.io/gorm"
)

var (
	ErrSettlementNotReversible = errors.New("settlement fee posting has not been posted or is already reversed")
	ErrPayoutNotReversible     = errors.New("payout has not been recorded or is already reversed")
	ErrPayoutReconciled        = errors.New("cannot reverse payout: the bank-side journal line has been reconciled — undo the reconciliation first")
)

// ── Settlement fee reversal ──────────────────────────────────────────────────

// ValidateSettlementFeeReversible checks if the fee posting JE can be reversed.
func ValidateSettlementFeeReversible(db *gorm.DB, companyID, settlementID uint) error {
	s, err := GetSettlement(db, companyID, settlementID)
	if err != nil {
		return fmt.Errorf("settlement not found")
	}
	if s.PostedJournalEntryID == nil {
		return ErrSettlementNotReversible
	}
	if s.PostedReversalJEID != nil {
		return ErrSettlementNotReversible
	}
	return nil
}

// ReverseSettlementFeePosting reverses the fee JE for a settlement.
func ReverseSettlementFeePosting(db *gorm.DB, companyID, settlementID uint, actor string) (uint, error) {
	if err := ValidateSettlementFeeReversible(db, companyID, settlementID); err != nil {
		return 0, err
	}

	s, _ := GetSettlement(db, companyID, settlementID)

	var reversalJEID uint
	err := db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		reversalJEID, txErr = ReverseJournalEntry(tx, companyID, *s.PostedJournalEntryID, time.Now())
		if txErr != nil {
			return fmt.Errorf("reverse fee JE: %w", txErr)
		}

		// Mark settlement with reversal JE.
		return tx.Model(&models.ChannelSettlement{}).
			Where("id = ? AND company_id = ?", settlementID, companyID).
			Update("posted_reversal_je_id", reversalJEID).Error
	})
	if err != nil {
		return 0, err
	}

	cid := companyID
	WriteAuditLogWithContextDetails(db, "settlement.fee_reversed", "channel_settlement", settlementID, actor,
		map[string]any{"company_id": companyID}, &cid, nil, nil,
		map[string]any{"reversal_je_id": reversalJEID},
	)

	return reversalJEID, nil
}

// ── Payout reversal ──────────────────────────────────────────────────────────

// ValidatePayoutReversible checks if the payout JE can be reversed.
func ValidatePayoutReversible(db *gorm.DB, companyID, settlementID uint) error {
	s, err := GetSettlement(db, companyID, settlementID)
	if err != nil {
		return fmt.Errorf("settlement not found")
	}
	if s.PayoutJournalEntryID == nil {
		return ErrPayoutNotReversible
	}
	if s.PayoutReversalJEID != nil {
		return ErrPayoutNotReversible
	}

	// Check if any bank-side journal line from the payout JE is reconciled.
	var reconciledCount int64
	db.Model(&models.JournalLine{}).
		Where("journal_entry_id = ? AND company_id = ? AND reconciliation_id IS NOT NULL",
			*s.PayoutJournalEntryID, companyID).
		Count(&reconciledCount)
	if reconciledCount > 0 {
		return ErrPayoutReconciled
	}

	return nil
}

// ReversePayoutRecording reverses the payout JE for a settlement.
func ReversePayoutRecording(db *gorm.DB, companyID, settlementID uint, actor string) (uint, error) {
	if err := ValidatePayoutReversible(db, companyID, settlementID); err != nil {
		return 0, err
	}

	s, _ := GetSettlement(db, companyID, settlementID)

	var reversalJEID uint
	err := db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		reversalJEID, txErr = ReverseJournalEntry(tx, companyID, *s.PayoutJournalEntryID, time.Now())
		if txErr != nil {
			return fmt.Errorf("reverse payout JE: %w", txErr)
		}

		return tx.Model(&models.ChannelSettlement{}).
			Where("id = ? AND company_id = ?", settlementID, companyID).
			Update("payout_reversal_je_id", reversalJEID).Error
	})
	if err != nil {
		return 0, err
	}

	cid := companyID
	WriteAuditLogWithContextDetails(db, "settlement.payout_reversed", "channel_settlement", settlementID, actor,
		map[string]any{"company_id": companyID}, &cid, nil, nil,
		map[string]any{"reversal_je_id": reversalJEID},
	)

	return reversalJEID, nil
}
