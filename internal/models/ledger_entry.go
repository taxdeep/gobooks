// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// LedgerEntryStatus describes the lifecycle state of a ledger entry.
type LedgerEntryStatus string

const (
	// LedgerEntryStatusActive is the normal state: the entry is live and
	// contributes to account balances and period reports.
	LedgerEntryStatusActive LedgerEntryStatus = "active"

	// LedgerEntryStatusReversed means the originating journal entry has been
	// reversed. The row is kept for audit completeness but must be excluded
	// from balance calculations.
	LedgerEntryStatusReversed LedgerEntryStatus = "reversed"
)

// LedgerSourceType identifies the kind of business document that triggered a posting.
type LedgerSourceType string

const (
	LedgerSourceInvoice        LedgerSourceType = "invoice"
	LedgerSourceBill           LedgerSourceType = "bill"
	LedgerSourcePayment        LedgerSourceType = "payment"
	LedgerSourceReversal       LedgerSourceType = "reversal"
	LedgerSourceManual         LedgerSourceType = "manual"
	LedgerSourceOpeningBalance LedgerSourceType = "opening_balance"
	// LedgerSourceRevaluation is used by period-end unrealized-FX revaluation JEs.
	LedgerSourceRevaluation LedgerSourceType = "revaluation"
	// LedgerSourceSettlement is used by channel settlement fee/refund posting.
	LedgerSourceSettlement LedgerSourceType = "settlement"
	// LedgerSourcePayout is used by channel settlement payout recording (Dr Bank, Cr Clearing).
	// Distinct from LedgerSourceSettlement so clearing reports can separate fees from payouts.
	LedgerSourcePayout LedgerSourceType = "payout"
	// LedgerSourcePaymentGateway is used by payment transaction posting.
	LedgerSourcePaymentGateway LedgerSourceType = "payment_gateway"
	// LedgerSourceGatewayPayout is used by gateway payout bridge (Dr Bank / Dr Fee / Cr Clearing).
	// Distinct from LedgerSourcePayout (channel) so queries can isolate gateway payout JEs.
	LedgerSourceGatewayPayout LedgerSourceType = "gateway_payout"
	// LedgerSourceBankCharge is used by bank service charge entries auto-created during reconciliation setup.
	LedgerSourceBankCharge LedgerSourceType = "bank_charge"
	// LedgerSourceBankInterest is used by bank interest earned entries auto-created during reconciliation setup.
	LedgerSourceBankInterest LedgerSourceType = "bank_interest"
	// LedgerSourceCreditNote is used by customer credit note postings.
	LedgerSourceCreditNote LedgerSourceType = "credit_note"
	// LedgerSourceCustomerDeposit is used by customer deposit posting (Dr Bank Cr DepositLiability).
	LedgerSourceCustomerDeposit LedgerSourceType = "customer_deposit"
	// LedgerSourceDepositApplication is used when a deposit is applied to an invoice (Dr Liability Cr AR).
	LedgerSourceDepositApplication LedgerSourceType = "deposit_application"
	// LedgerSourceCustomerReceipt is used by customer receipt confirmation (Dr Bank Cr AR).
	LedgerSourceCustomerReceipt LedgerSourceType = "customer_receipt"
	// LedgerSourceARRefund is used by AR refund posting (Dr liability/AR Cr Bank).
	LedgerSourceARRefund LedgerSourceType = "ar_refund"
	// LedgerSourceARWriteOff is used by AR bad-debt write-off posting (Dr Expense Cr AR).
	LedgerSourceARWriteOff LedgerSourceType = "ar_write_off"
	// LedgerSourceVendorPrepayment is used by vendor prepayment posting (Dr PrepaymentAsset Cr Bank).
	LedgerSourceVendorPrepayment LedgerSourceType = "vendor_prepayment"
	// LedgerSourceVendorCreditNote is used by vendor credit note posting (Dr AP Cr PurchaseReturns).
	LedgerSourceVendorCreditNote LedgerSourceType = "vendor_credit_note"
	// LedgerSourceVendorRefund is used by vendor refund posting (Dr Bank Cr PrepaymentAsset/AP).
	LedgerSourceVendorRefund LedgerSourceType = "vendor_refund"
)

// LedgerEntry is one row in the accounting fact layer.
//
// It is a 1:1 projection of a posted JournalLine into the general ledger.
// LedgerEntry rows are created by the posting engine at the moment a
// JournalEntry is committed (status → posted). They are never edited or deleted.
//
// Reversal semantics: when a reversal JournalEntry is posted, its JournalLines
// produce new LedgerEntry rows (with swapped debit/credit). The original
// LedgerEntry rows for the reversed JournalEntry are marked status=reversed
// but remain in the table for full audit traceability.
//
// Reconstruction guarantee: the entire ledger_entries table can be rebuilt
// from journal_entries + journal_lines at any time. It is a projection, not
// the primary source of truth.
//
// Query patterns this table is optimised for:
//   - Account balance as of a date:  WHERE company_id=? AND account_id=? AND posting_date<=? AND status='active'
//   - Trial balance:                  GROUP BY account_id
//   - General ledger report:          WHERE company_id=? AND account_id=? ORDER BY posting_date, id
//   - Source document drilldown:      WHERE source_type=? AND source_id=?
type LedgerEntry struct {
	ID uint `gorm:"primaryKey"`

	// CompanyID enforces tenant isolation. Copied redundantly from the parent
	// JournalEntry so that account-level queries never need to join journal_entries.
	CompanyID uint `gorm:"not null;index"`

	// JournalEntryID links back to the double-entry header.
	JournalEntryID uint         `gorm:"not null;index"`
	JournalEntry   JournalEntry `gorm:"foreignKey:JournalEntryID"`

	// SourceType identifies the originating business document type.
	// SourceID is the PK in that document's table; 0 for manual entries.
	SourceType LedgerSourceType `gorm:"type:text;not null;default:''"`
	SourceID   uint             `gorm:"not null;default:0"`

	// AccountID is the GL account affected.
	AccountID uint    `gorm:"not null;index"`
	Account   Account `gorm:"foreignKey:AccountID"`

	// PostingDate is the accounting date for period assignment.
	// Copied from journal_entries.entry_date at post time.
	PostingDate time.Time `gorm:"type:date;not null"`

	// DebitAmount and CreditAmount are the amounts for this posting.
	// Exactly one should be non-zero for a normal line.
	DebitAmount  decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	CreditAmount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// Status is active for live entries and reversed for entries whose
	// parent journal entry has been reversed.
	Status LedgerEntryStatus `gorm:"type:text;not null;default:'active'"`

	CreatedAt time.Time
}
