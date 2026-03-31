// 遵循project_guide.md
package models

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// ── Invoice status ──────────────────────────────────────────────────────────

// InvoiceStatus tracks the lifecycle of an invoice.
// Full status machine: draft → issued → sent → (partially_paid/overdue) → paid | voided
//
// draft: Initial state, not yet posted to accounting.
// issued: Status changed but not yet marked as sent by user; JE created if auto-post enabled.
// sent: Invoice sent to customer (email logged); still awaiting payment.
// paid: Payment received in full; invoice archived.
// overdue: Payment deadline passed; awaiting payment (optional state for reporting).
// partially_paid: Partial payment received; balance due remains.
// voided: Invoice cancelled; reversal JE created; no recovery possible.
type InvoiceStatus string

const (
	InvoiceStatusDraft         InvoiceStatus = "draft"
	InvoiceStatusIssued        InvoiceStatus = "issued"
	InvoiceStatusSent          InvoiceStatus = "sent"
	InvoiceStatusPaid          InvoiceStatus = "paid"
	InvoiceStatusPartiallyPaid InvoiceStatus = "partially_paid"
	InvoiceStatusOverdue       InvoiceStatus = "overdue"
	InvoiceStatusVoided        InvoiceStatus = "voided"
)

// AllInvoiceStatuses returns statuses in display order.
func AllInvoiceStatuses() []InvoiceStatus {
	return []InvoiceStatus{
		InvoiceStatusDraft,
		InvoiceStatusIssued,
		InvoiceStatusSent,
		InvoiceStatusPartiallyPaid,
		InvoiceStatusPaid,
		InvoiceStatusOverdue,
		InvoiceStatusVoided,
	}
}

// InvoiceStatusLabel returns a human-readable label.
func InvoiceStatusLabel(s InvoiceStatus) string {
	switch s {
	case InvoiceStatusDraft:
		return "Draft"
	case InvoiceStatusIssued:
		return "Issued"
	case InvoiceStatusSent:
		return "Sent"
	case InvoiceStatusPaid:
		return "Paid"
	case InvoiceStatusPartiallyPaid:
		return "Partially Paid"
	case InvoiceStatusOverdue:
		return "Overdue"
	case InvoiceStatusVoided:
		return "Voided"
	default:
		return string(s)
	}
}

// ParseInvoiceStatus parses a raw string, returning an error if unrecognised.
func ParseInvoiceStatus(s string) (InvoiceStatus, error) {
	switch InvoiceStatus(s) {
	case InvoiceStatusDraft, InvoiceStatusIssued, InvoiceStatusSent, InvoiceStatusPaid, InvoiceStatusPartiallyPaid, InvoiceStatusOverdue, InvoiceStatusVoided:
		return InvoiceStatus(s), nil
	default:
		return "", fmt.Errorf("unknown invoice status: %q", s)
	}
}

// ── Invoice terms ───────────────────────────────────────────────────────────

// InvoiceTerms controls when payment is due.
type InvoiceTerms string

const (
	InvoiceTermsNet30         InvoiceTerms = "net_30"
	InvoiceTermsNet15         InvoiceTerms = "net_15"
	InvoiceTermsNet60         InvoiceTerms = "net_60"
	InvoiceTermsDueOnReceipt  InvoiceTerms = "due_on_receipt"
	InvoiceTermsCustom        InvoiceTerms = "custom"
)

// AllInvoiceTerms returns terms in display order.
func AllInvoiceTerms() []InvoiceTerms {
	return []InvoiceTerms{
		InvoiceTermsNet30,
		InvoiceTermsNet15,
		InvoiceTermsNet60,
		InvoiceTermsDueOnReceipt,
		InvoiceTermsCustom,
	}
}

// InvoiceTermsLabel returns a human-readable label.
func InvoiceTermsLabel(t InvoiceTerms) string {
	switch t {
	case InvoiceTermsNet30:
		return "Net 30"
	case InvoiceTermsNet15:
		return "Net 15"
	case InvoiceTermsNet60:
		return "Net 60"
	case InvoiceTermsDueOnReceipt:
		return "Due on Receipt"
	case InvoiceTermsCustom:
		return "Custom"
	default:
		return string(t)
	}
}

// InvoiceTermsDays returns the number of days to add to invoice_date to compute
// due_date for standard terms. Returns 0 for due_on_receipt and -1 for custom.
func InvoiceTermsDays(t InvoiceTerms) int {
	switch t {
	case InvoiceTermsNet15:
		return 15
	case InvoiceTermsNet30:
		return 30
	case InvoiceTermsNet60:
		return 60
	case InvoiceTermsDueOnReceipt:
		return 0
	default: // custom
		return -1
	}
}

// ComputeDueDate returns invoice_date + terms days for standard terms.
// Returns nil for custom terms (caller must supply their own due_date).
func ComputeDueDate(invoiceDate time.Time, terms InvoiceTerms) *time.Time {
	days := InvoiceTermsDays(terms)
	if days < 0 {
		return nil
	}
	d := invoiceDate.AddDate(0, 0, days)
	return &d
}

// ── Invoice + InvoiceLine models ────────────────────────────────────────────

// Invoice is the header for a customer sales invoice.
//
// Amount is the cached grand total (= Subtotal + TaxTotal).
// For invoices created before line-item support, Amount holds the lump-sum total;
// Subtotal and TaxTotal default to 0 and Lines will be empty.
//
// Status lifecycle: draft → issued → sent → (partially_paid/overdue) → paid | voided.
// A JournalEntry is generated on posting (when status transitions from draft → issued).
// JournalEntryID is set once and never changed; voiding creates a reversal JE (no deletion).
//
// Snapshots (CustomerName*, PrincipalAccount*): preserve customer and account state
// at posting time for immutable audit trail; never updated after initial posting.
//
// BalanceDue is calculated field (Amount - payments_recorded); not directly assigned.
// TemplateID optionally links to an invoice template for rendering configuration.
// IssuedAt/SentAt/VoidedAt track state transition timestamps for reporting and compliance.
type Invoice struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	InvoiceNumber string   `gorm:"not null;index"`
	CustomerID    uint     `gorm:"not null;index"`
	Customer      Customer `gorm:"foreignKey:CustomerID"`

	InvoiceDate time.Time `gorm:"not null"`

	// Terms controls the standard due-date calculation.
	Terms InvoiceTerms `gorm:"type:text;not null;default:'net_30'"`
	// DueDate is computed from Terms+InvoiceDate (or set explicitly for custom terms).
	DueDate *time.Time

	// Status is the invoice lifecycle state.
	Status InvoiceStatus `gorm:"type:text;not null;default:'draft'"`

	// Amount is the cached grand total (Subtotal + TaxTotal).
	Amount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// Subtotal is the cached sum of all InvoiceLine.LineNet values.
	Subtotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// TaxTotal is the cached sum of all InvoiceLine.LineTax values.
	TaxTotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	Memo string `gorm:"not null;default:''"`

	// JournalEntryID links the posted accounting entry (nil = not yet posted).
	JournalEntryID *uint         `gorm:"index"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	// TemplateID optionally links to an invoice template for rendering.
	TemplateID *uint             `gorm:"index"`
	Template   *InvoiceTemplate   `gorm:"foreignKey:TemplateID"`

	// State tracking timestamps (set by service layer on status transitions)
	IssuedAt *time.Time `gorm:"index"` // set when status changes to issued/sent
	SentAt   *time.Time                // set when email is sent
	VoidedAt *time.Time                // set when status changes to voided

	// BalanceDue = Amount - (sum of payments recorded)
	// Calculated field; not directly assigned by create/update handlers.
	BalanceDue decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0;index"`

	// Snapshots: preserve customer state at posting time (immutable)
	CustomerNameSnapshot    string `gorm:"not null;default:''"`
	CustomerEmailSnapshot   string `gorm:"not null;default:''"`
	CustomerAddressSnapshot string `gorm:"not null;default:''"`

	// Snapshots: preserve revenue account details at posting time (for audit trail)
	PrincipalAccountIDSnapshot   *uint  `gorm:"index"`
	PrincipalAccountNameSnapshot string `gorm:"not null;default:''"`
	PrincipalAccountCodeSnapshot string `gorm:"not null;default:''"`

	Lines []InvoiceLine `gorm:"foreignKey:InvoiceID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// InvoiceLine is one line item on an Invoice.
//
// LineNet  = Qty × UnitPrice  (pre-tax amount)
// LineTax  = sum of tax component amounts (from TaxCode applied to LineNet)
// LineTotal = LineNet + LineTax
//
// All three are cached on save by the invoice service.
// Description is required; ProductServiceID is optional (free-form lines allowed).
type InvoiceLine struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`
	InvoiceID uint `gorm:"not null;index"`

	// SortOrder controls display sequence within the invoice (1-based).
	SortOrder uint `gorm:"not null;default:1"`

	// ProductServiceID optionally links to the product/service catalogue.
	// When set, Description, UnitPrice, and TaxCodeID are pre-filled from the item.
	ProductServiceID *uint           `gorm:"index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	// Description is shown on the printed invoice; required.
	Description string `gorm:"not null"`

	Qty       decimal.Decimal `gorm:"type:numeric(10,4);not null;default:1"`
	UnitPrice decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	// TaxCodeID is optional; nil = no tax on this line.
	TaxCodeID *uint    `gorm:"index"`
	TaxCode   *TaxCode `gorm:"foreignKey:TaxCodeID"`

	// Cached computed values (set by invoice service before save).
	LineNet   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	LineTax   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	LineTotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ── Bill status ─────────────────────────────────────────────────────────────

// BillStatus tracks the lifecycle of a purchase bill.
type BillStatus string

const (
	BillStatusDraft  BillStatus = "draft"
	BillStatusPosted BillStatus = "posted" // JE generated, AP liability recorded
	BillStatusPaid   BillStatus = "paid"
	BillStatusVoided BillStatus = "voided"
)

// ── Bill model ───────────────────────────────────────────────────────────────

// Bill is a purchase bill header.
// Duplicate detection: same company, same vendor_id, same bill_number (case-insensitive).
//
// Amount is the cached grand total (= Subtotal + TaxTotal).
// For bills created before line-item support, Amount holds the lump-sum total;
// Subtotal and TaxTotal default to 0 and Lines will be empty.
//
// Status lifecycle: draft → posted → paid (or voided at any pre-paid stage).
// A JournalEntry is generated on posting; JournalEntryID is set once and never changed.
type Bill struct {
	ID uint `gorm:"primaryKey"`

	CompanyID uint `gorm:"not null;index"`

	BillNumber string `gorm:"not null;index"`
	VendorID   uint   `gorm:"not null;index"`
	Vendor     Vendor `gorm:"foreignKey:VendorID"`

	BillDate time.Time  `gorm:"not null"`
	Status   BillStatus `gorm:"type:text;not null;default:'draft'"`

	// Terms and DueDate mirror the invoice payment-terms pattern.
	Terms   InvoiceTerms `gorm:"type:text;not null;default:'net_30'"`
	DueDate *time.Time   `gorm:"index"`

	// Amount is the cached grand total (Subtotal + TaxTotal).
	Amount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// Subtotal is the cached sum of all BillLine.LineNet values.
	Subtotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// TaxTotal is the cached sum of all BillLine.LineTax values.
	TaxTotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	Memo string `gorm:"not null;default:''"`

	// JournalEntryID links the posted accounting entry (nil = not yet posted).
	JournalEntryID *uint         `gorm:"index"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	Lines []BillLine `gorm:"foreignKey:BillID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ── BillLine model ───────────────────────────────────────────────────────────

// BillLine is one line item on a Bill.
//
// LineNet  = Qty × UnitPrice  (pre-tax amount)
// LineTax  = tax amount derived from TaxCode applied to LineNet
// LineTotal = LineNet + LineTax
//
// All three are cached on save by the bill service.
//
// ExpenseAccountID is the GL account to debit for this line's cost. Required
// for posting. It holds both the base line cost and the non-recoverable tax
// portion (non-recoverable tax increases the cost of the purchase).
//
// If a TaxCode has a PurchaseRecoverableAccount, the recoverable portion is
// posted to that account separately; only the non-recoverable portion is
// rolled into the expense debit.
type BillLine struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`
	BillID    uint `gorm:"not null;index"`

	// SortOrder controls display sequence within the bill (1-based).
	SortOrder uint `gorm:"not null;default:1"`

	// ProductServiceID optionally links to the product/service catalogue.
	ProductServiceID *uint           `gorm:"index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	// Description is shown on the printed bill; required.
	Description string `gorm:"not null"`

	Qty       decimal.Decimal `gorm:"type:numeric(10,4);not null;default:1"`
	UnitPrice decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	// TaxCodeID is optional; nil = no tax on this line.
	// The TaxCode's Scope must include "purchase" or "both" to affect bills.
	TaxCodeID *uint    `gorm:"index"`
	TaxCode   *TaxCode `gorm:"foreignKey:TaxCodeID"`

	// ExpenseAccountID is the GL account to debit for the cost of this line
	// (base cost + non-recoverable tax). Validated non-zero at posting time.
	ExpenseAccountID *uint    `gorm:"index"`
	ExpenseAccount   *Account `gorm:"foreignKey:ExpenseAccountID"`

	// Cached computed values (set by bill service before save).
	LineNet   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	LineTax   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	LineTotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
