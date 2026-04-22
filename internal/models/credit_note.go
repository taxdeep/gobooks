// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// CreditNoteStatus tracks the lifecycle of a customer credit note.
//
// Lifecycle:
//
//	draft → issued → (partially_applied) → fully_applied
//	      ↘ voided  (from draft or issued, before any application)
type CreditNoteStatus string

const (
	// CreditNoteStatusDraft — created but not yet posted to accounting.
	CreditNoteStatusDraft CreditNoteStatus = "draft"

	// CreditNoteStatusIssued — posted; JE created; balance is available for application.
	CreditNoteStatusIssued CreditNoteStatus = "issued"

	// CreditNoteStatusPartiallyApplied — some balance applied to invoice(s); remainder open.
	CreditNoteStatusPartiallyApplied CreditNoteStatus = "partially_applied"

	// CreditNoteStatusFullyApplied — all balance has been applied; no open amount remains.
	CreditNoteStatusFullyApplied CreditNoteStatus = "fully_applied"

	// CreditNoteStatusVoided — cancelled; reversal JE created; no recovery possible.
	CreditNoteStatusVoided CreditNoteStatus = "voided"
)

// CreditNoteReason classifies why the credit note was issued.
type CreditNoteReason string

const (
	CreditNoteReasonReturn      CreditNoteReason = "return"       // goods returned by customer
	CreditNoteReasonPriceAdj    CreditNoteReason = "price_adj"    // post-invoice price correction
	CreditNoteReasonGoodwill    CreditNoteReason = "goodwill"     // goodwill credit to customer
	CreditNoteReasonDuplicate   CreditNoteReason = "duplicate"    // duplicate invoice correction
	CreditNoteReasonOther       CreditNoteReason = "other"
)

// AllCreditNoteReasons returns reasons in display order.
func AllCreditNoteReasons() []CreditNoteReason {
	return []CreditNoteReason{
		CreditNoteReasonReturn,
		CreditNoteReasonPriceAdj,
		CreditNoteReasonGoodwill,
		CreditNoteReasonDuplicate,
		CreditNoteReasonOther,
	}
}

// CreditNoteReasonLabel returns a human-readable label.
func CreditNoteReasonLabel(r CreditNoteReason) string {
	switch r {
	case CreditNoteReasonReturn:
		return "Goods Return"
	case CreditNoteReasonPriceAdj:
		return "Price Adjustment"
	case CreditNoteReasonGoodwill:
		return "Goodwill"
	case CreditNoteReasonDuplicate:
		return "Duplicate Invoice"
	case CreditNoteReasonOther:
		return "Other"
	default:
		return string(r)
	}
}

// CreditNote is a formal AR document issued by the company to reduce a customer's
// outstanding balance or provide a credit. Governed by AR.9.
//
// A credit note may be linked to a specific invoice (InvoiceID set) or issued
// standalone (InvoiceID nil). Both paths produce a JE on posting.
//
// Posting journal (for credit note with GST, reverse of invoice):
//
//	DR  Revenue account       line.LineNet per line   (reverses previously recognised revenue)
//	DR  Sales Tax Payable     tax amount   per line   (reverses collected tax)
//	CR  Accounts Receivable   cn.Amount               (one line for gross total)
//
// BalanceRemaining = Amount − sum(CreditNoteApplication.AmountApplied).
// It decreases as the credit note is applied to invoice(s).
type CreditNote struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	// CreditNoteNumber is the human-readable document number (e.g. "CN-2024-0001").
	CreditNoteNumber string `gorm:"not null;index"`

	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	// InvoiceID optionally links to the original invoice being reversed / reduced.
	InvoiceID *uint    `gorm:"index"`
	Invoice   *Invoice `gorm:"foreignKey:InvoiceID"`

	CreditNoteDate time.Time `gorm:"not null"`

	Status CreditNoteStatus `gorm:"type:text;not null;default:'draft'"`
	Reason CreditNoteReason `gorm:"type:text;not null;default:'other'"`

	// Memo is an optional internal note; may appear on the printed document.
	Memo string `gorm:"not null;default:''"`

	// Cached totals (set by service before save).
	Subtotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	TaxTotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	Amount   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"` // Subtotal + TaxTotal

	// BalanceRemaining = Amount − Σ(CreditNoteApplication.AmountApplied).
	// Decremented on each application.
	BalanceRemaining decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// Multi-currency: same pattern as Invoice.
	// CurrencyCode blank = base currency.
	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	ExchangeRate decimal.Decimal `gorm:"type:numeric(20,8);not null;default:1"`
	AmountBase   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// JournalEntryID is set when the credit note is posted (nil = draft).
	JournalEntryID *uint         `gorm:"index"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	// State transition timestamps.
	IssuedAt *time.Time
	VoidedAt *time.Time

	// Customer snapshot at issue time (immutable after posting).
	CustomerNameSnapshot string `gorm:"not null;default:''"`

	Lines        []CreditNoteLine        `gorm:"foreignKey:CreditNoteID"`
	Applications []CreditNoteApplication `gorm:"foreignKey:CreditNoteID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreditNoteLine is one line item on a CreditNote.
//
// LineNet  = Qty × UnitPrice
// LineTax  = tax amount (from TaxCode applied to LineNet)
// LineTotal = LineNet + LineTax
type CreditNoteLine struct {
	ID           uint `gorm:"primaryKey"`
	CompanyID    uint `gorm:"not null;index"`
	CreditNoteID uint `gorm:"not null;index"`

	// SortOrder controls display sequence (1-based).
	SortOrder uint `gorm:"not null;default:1"`

	// ProductServiceID optionally links to the catalogue item.
	// When set, RevenueAccountID, Description, and UnitPrice may be pre-filled.
	//
	// IN.5 / Rule #4: when ProductService.IsStockItem=true the line
	// becomes a stock-return line on post — inventory is restored
	// at the authoritative original unit cost (legacy mode), or the
	// post is rejected for routing through Phase I.6 Return Receipt
	// (controlled mode).
	ProductServiceID *uint           `gorm:"index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	// OriginalInvoiceLineID — IN.5 trace back to the InvoiceLine
	// that originally sold this qty. Required when the line points
	// at a stock item (IsStockItem=true); ignored otherwise.
	//
	// Serves as the key to inventory.ReverseMovement at post time,
	// which uses the ORIGINAL movement's snapshot cost to book the
	// return (March's COGS reverses at March's cost, never at
	// today's weighted average).
	//
	// No DB-level FK constraint so mixed-company joins stay legal
	// at the schema layer. Cross-tenant + existence checks live in
	// credit_note_post.go.
	OriginalInvoiceLineID *uint        `gorm:"index"`
	OriginalInvoiceLine   *InvoiceLine `gorm:"foreignKey:OriginalInvoiceLineID"`

	// RevenueAccountID is the account to debit when posting (reversal of the original
	// revenue credit on the invoice). Required when posting the credit note.
	// Defaults to the ProductService.RevenueAccountID when that field is set.
	RevenueAccountID uint    `gorm:"not null;index"`
	RevenueAccount   Account `gorm:"foreignKey:RevenueAccountID"`

	// Description is shown on the printed document.
	Description string `gorm:"not null"`

	Qty       decimal.Decimal `gorm:"type:numeric(10,4);not null;default:1"`
	UnitPrice decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	// TaxCodeID is optional; nil = no tax on this line.
	TaxCodeID *uint    `gorm:"index"`
	TaxCode   *TaxCode `gorm:"foreignKey:TaxCodeID"`

	// Cached computed values (set by service before save).
	LineNet   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	LineTax   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	LineTotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreditNoteApplication records the allocation of a credit note's balance against
// one or more invoices. A credit note may be split across multiple invoices.
//
// The accounting reduction to AR already happened when the credit note was posted.
// This record is purely an AR open-item allocation — it tracks which invoice's
// BalanceDue was reduced and by how much.
type CreditNoteApplication struct {
	ID           uint    `gorm:"primaryKey"`
	CompanyID    uint    `gorm:"not null;index"`
	CreditNoteID uint    `gorm:"not null;index"`
	InvoiceID    uint    `gorm:"not null;index"`
	Invoice      Invoice `gorm:"foreignKey:InvoiceID"`

	// AmountApplied is in the credit note's document currency.
	AmountApplied decimal.Decimal `gorm:"type:numeric(18,2);not null"`

	// AmountAppliedBase is the base-currency equivalent at the credit note's
	// exchange rate. For base-currency credit notes this equals AmountApplied.
	AmountAppliedBase decimal.Decimal `gorm:"type:numeric(18,2);not null"`

	AppliedAt time.Time `gorm:"not null"`
	CreatedAt time.Time
}
