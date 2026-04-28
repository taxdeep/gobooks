// 遵循project_guide.md
package services

// settlement_posting_service.go — Generate JE from channel settlement lines.
//
// Accounting direction rules (current phase):
//
//   POSTABLE line types and their JE direction:
//     fee           → Dr FeeExpenseAccount,       Cr ClearingAccount
//     shipping_fee  → Dr ShippingExpenseAccount,   Cr ClearingAccount
//     refund        → Dr RefundAccount,            Cr ClearingAccount
//     adjustment    → amount > 0: Dr MappedAccount, Cr ClearingAccount
//                     amount < 0: Dr ClearingAccount, Cr MappedAccount
//     reserve       → Dr MappedAccount,            Cr ClearingAccount
//
//   SKIPPED line types (not included in JE):
//     sale          — Revenue is already recognized via invoices. The sale line
//                     in a settlement is a reference for net-amount calculation,
//                     not a new revenue event. Posting it would double-count revenue.
//     payout        — Represents actual cash disbursement from the platform to the
//                     bank. This is a bank-side event that should be matched during
//                     bank reconciliation, not posted from the settlement layer.
//
//   The clearing account acts as a suspense/intermediary account representing
//   "money the platform owes us." When fees are recognized, clearing is credited
//   (reducing the amount owed). When the payout arrives in the bank, clearing
//   is debited to zero (handled by future bank reconciliation).

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

var (
	ErrSettlementAlreadyPosted = errors.New("settlement has already been posted")
	ErrSettlementNotPostable   = errors.New("settlement is not postable")
	ErrNoClearingAccount       = errors.New("clearing account not configured for this channel — set it in Accounting Mappings")
)

// PostableLineTypes are the settlement line types that generate JE lines.
// sale and payout are intentionally excluded (see module doc).
var PostableLineTypes = map[models.SettlementLineType]bool{
	models.SettlementLineFee:         true,
	models.SettlementLineShippingFee: true,
	models.SettlementLineRefund:      true,
	models.SettlementLineAdjustment:  true,
	models.SettlementLineReserve:     true,
}

// IsPostableLineType returns true if the line type should generate a JE line.
func IsPostableLineType(lt models.SettlementLineType) bool {
	return PostableLineTypes[lt]
}

// ── Validation ───────────────────────────────────────────────────────────────

// ValidateSettlementPostable checks whether a settlement can be posted to a JE.
func ValidateSettlementPostable(db *gorm.DB, companyID, settlementID uint) error {
	settlement, err := GetSettlement(db, companyID, settlementID)
	if err != nil {
		return fmt.Errorf("settlement not found")
	}

	if settlement.PostedJournalEntryID != nil {
		return ErrSettlementAlreadyPosted
	}

	lines, err := GetSettlementLines(db, companyID, settlementID)
	if err != nil || len(lines) == 0 {
		return fmt.Errorf("%w: no lines", ErrSettlementNotPostable)
	}

	// Check that we have at least one postable line with a mapped account.
	postableCount := 0
	for _, l := range lines {
		if !IsPostableLineType(l.LineType) {
			continue // skipped types don't need accounts
		}
		postableCount++
		if l.MappedAccountID == nil {
			return fmt.Errorf("%w: line %q (type %s) has no mapped account",
				ErrSettlementNotPostable, l.Description, l.LineType)
		}
	}

	if postableCount == 0 {
		return fmt.Errorf("%w: no postable lines (sale/payout lines are skipped)", ErrSettlementNotPostable)
	}

	// Check clearing account exists.
	mapping, _ := GetAccountingMapping(db, companyID, settlement.ChannelAccountID)
	if mapping == nil || mapping.ClearingAccountID == nil {
		return ErrNoClearingAccount
	}

	return nil
}

// ── Posting ──────────────────────────────────────────────────────────────────

// PostSettlementToJournalEntry generates a JE from the postable lines of a settlement.
func PostSettlementToJournalEntry(db *gorm.DB, companyID, settlementID uint, actor string) (*models.JournalEntry, error) {
	// 1. Validate.
	if err := ValidateSettlementPostable(db, companyID, settlementID); err != nil {
		return nil, err
	}

	settlement, _ := GetSettlement(db, companyID, settlementID)
	lines, _ := GetSettlementLines(db, companyID, settlementID)
	mapping, _ := GetAccountingMapping(db, companyID, settlement.ChannelAccountID)
	clearingAcctID := *mapping.ClearingAccountID

	// 2. Build JE lines from postable settlement lines.
	type jlPair struct {
		debitAcct  uint
		creditAcct uint
		amount     decimal.Decimal
		memo       string
	}

	var pairs []jlPair
	for _, l := range lines {
		if !IsPostableLineType(l.LineType) {
			continue
		}
		if l.MappedAccountID == nil {
			continue
		}

		amt := l.Amount.Abs()
		if amt.IsZero() {
			continue
		}

		expenseAcct := *l.MappedAccountID
		memo := models.SettlementLineTypeLabel(l.LineType)
		if l.Description != "" {
			memo += ": " + l.Description
		}

		// Direction: fees/refunds/shipping decrease the platform's balance (credit clearing).
		// Adjustments can go either way based on sign.
		if l.LineType == models.SettlementLineAdjustment && l.Amount.IsNegative() {
			// Negative adjustment: Dr Clearing, Cr mapped.
			pairs = append(pairs, jlPair{debitAcct: clearingAcctID, creditAcct: expenseAcct, amount: amt, memo: memo})
		} else {
			// Normal: Dr expense/mapped, Cr clearing.
			pairs = append(pairs, jlPair{debitAcct: expenseAcct, creditAcct: clearingAcctID, amount: amt, memo: memo})
		}
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("%w: no postable lines after filtering", ErrSettlementNotPostable)
	}

	// 3. Determine entry date.
	entryDate := time.Now()
	if settlement.SettlementDate != nil {
		entryDate = *settlement.SettlementDate
	}

	// 4. Transaction: create JE, lines, ledger, mark settlement.
	var je models.JournalEntry
	err := db.Transaction(func(tx *gorm.DB) error {
		je = models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  entryDate,
			JournalNo:  "SETTLE-" + settlement.ExternalSettlementID,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceSettlement,
			SourceID:   settlement.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create journal entry: %w", err)
		}

		var createdLines []models.JournalLine
		for _, p := range pairs {
			debitLine := models.JournalLine{
				CompanyID: companyID, JournalEntryID: je.ID,
				AccountID: p.debitAcct, Debit: p.amount, Credit: decimal.Zero,
				Memo: p.memo,
			}
			if err := tx.Create(&debitLine).Error; err != nil {
				return fmt.Errorf("create debit line: %w", err)
			}
			createdLines = append(createdLines, debitLine)

			creditLine := models.JournalLine{
				CompanyID: companyID, JournalEntryID: je.ID,
				AccountID: p.creditAcct, Debit: decimal.Zero, Credit: p.amount,
				Memo: p.memo,
			}
			if err := tx.Create(&creditLine).Error; err != nil {
				return fmt.Errorf("create credit line: %w", err)
			}
			createdLines = append(createdLines, creditLine)
		}

		// Ledger projection.
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        createdLines,
			SourceType:   models.LedgerSourceSettlement,
			SourceID:     settlement.ID,
		}); err != nil {
			return fmt.Errorf("project to ledger: %w", err)
		}

		// Mark settlement posted.
		now := time.Now()
		if err := tx.Model(&models.ChannelSettlement{}).
			Where("id = ? AND company_id = ?", settlementID, companyID).
			Updates(map[string]any{
				"posted_journal_entry_id": je.ID,
				"posted_at":              now,
			}).Error; err != nil {
			return fmt.Errorf("mark settlement posted: %w", err)
		}

		// Audit log.
		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "settlement.posted", "channel_settlement", settlementID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"settlement_id":    settlementID,
				"journal_entry_id": je.ID,
				"line_count":       len(pairs),
			},
		)
	})

	if err != nil {
		return nil, err
	}
	return &je, nil
}
