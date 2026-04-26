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
	LedgerSourceInvoice LedgerSourceType = "invoice"
	LedgerSourceBill    LedgerSourceType = "bill"
	// LedgerSourceReceipt is used by Phase H inbound-Receipt posting
	// (Dr Inventory / Cr GR/IR) under companies.receipt_required=true.
	LedgerSourceReceipt        LedgerSourceType = "receipt"
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
	// LedgerSourceShipment is used by Phase I.3 Shipment post under
	// companies.shipment_required=true — Dr COGS / Cr Inventory at
	// ship time. Invoice under the same flag only books AR/Revenue
	// (LedgerSourceInvoice), so the two sources stay distinct in
	// source-document drilldown reports.
	LedgerSourceShipment LedgerSourceType = "shipment"
	// LedgerSourceExpense is used by IN.2 Expense post — Dr per-line
	// ExpenseAccount (or InventoryAccount for stock lines under Rule
	// #4) / Cr PaymentAccount. Stock-item lines on Expense under
	// legacy mode also form inventory_movements with source_type='expense';
	// under controlled mode (receipt_required=true) Expense post
	// rejects stock lines entirely so Receipt remains the only
	// inbound-inventory surface.
	LedgerSourceExpense LedgerSourceType = "expense"
	// LedgerSourceARReturnReceipt is used by Phase I.6a.2
	// ARReturnReceipt post under companies.shipment_required=true —
	// Dr Inventory / Cr COGS at the traced original-sale cost
	// (read from the original Invoice's inventory_movement via
	// CreditNoteLine.OriginalInvoiceLineID). Under controlled mode
	// (I.6a.3) this becomes the Rule #4 movement owner for AR-return
	// stock lines; the paired CreditNote books only the revenue leg
	// (Dr Revenue / Cr AR). Under legacy mode (shipment_required=false)
	// ARReturnReceipt post is a status-flip only and this source_type
	// is not emitted — IN.5's CreditNote continues to own movement.
	LedgerSourceARReturnReceipt LedgerSourceType = "ar_return_receipt"
	// LedgerSourceVendorReturnShipment is used by Phase I.6b.2
	// VendorReturnShipment post under companies.receipt_required=true —
	// Dr AP / Cr Inventory at the traced original-receipt cost
	// (read from the original Bill's inventory_movement via
	// VendorCreditNoteLine.OriginalBillLineID). Under controlled mode
	// (I.6b.3) this becomes the Rule #4 movement owner for AP-return
	// stock lines; the paired VendorCreditNote skips the JE for stock
	// lines. Under legacy mode (receipt_required=false) VRS post is a
	// status-flip only and this source_type is not emitted — IN.6a's
	// VCN continues to own the reversal path.
	//
	// Asymmetry vs AR side: VRS posts BOTH legs (Dr AP + Cr Inventory),
	// where the AR-side split was ARR books inventory-leg only + CN
	// books revenue-leg only. This is because AP purchase accounting
	// (Dr Inventory / Cr AP) has no separate "revenue" leg — the
	// reversal needs only two accounts on one document to self-balance.
	LedgerSourceVendorReturnShipment LedgerSourceType = "vendor_return_shipment"
)

// LedgerEntry is one row in the accounting fact layer.
//
// It is a 1:1 projection of a posted JournalLine into the general ledger.
// LedgerEntry rows are created by the posting engine at the moment a
// JournalEntry is committed (status → posted). They are never edited or deleted.
//
// Reversal semantics: when a reversal JournalEntry is posted, its JournalLines
// produce new LedgerEntry rows (with swapped debit/credit). Current document
// void flows keep the original rows active and let report readers hide the
// original+reversal pair; legacy flows may mark original rows reversed.
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
