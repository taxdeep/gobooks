// 遵循project_guide.md
package services

// customer_statement_service.go — CustomerStatement: account statement for a customer.
//
// A CustomerStatement is a read-only view of all AR activity for a customer
// in a given date range. It is not an accounting object — it queries and
// aggregates existing AR objects.
//
// Statement line types included:
//   - Invoice issued (adds to balance)
//   - CustomerReceipt confirmed (reduces balance)
//   - CreditNote issued (reduces balance)
//   - CustomerDeposit applied to invoice (reduces balance)
//   - ARRefund posted (reduces balance — funds returned to customer)
//   - ARWriteOff posted (reduces balance)
//
// The opening balance is the sum of all unpaid invoice amounts for invoices
// issued before the start date that still had a balance at the start date.

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Output types ──────────────────────────────────────────────────────────────

// StatementLineType classifies each line in a customer statement.
type StatementLineType string

const (
	StatementLineInvoice        StatementLineType = "invoice"
	StatementLineReceipt        StatementLineType = "receipt"
	StatementLineCreditNote     StatementLineType = "credit_note"
	StatementLineDepositApplied StatementLineType = "deposit_applied"
	StatementLineRefund         StatementLineType = "refund"
	StatementLineWriteOff       StatementLineType = "write_off"
)

// StatementLine is one activity line in a customer statement.
type StatementLine struct {
	Date        time.Time
	Type        StatementLineType
	Reference   string          // document number
	Description string
	Debit       decimal.Decimal // amount added to balance (invoices)
	Credit      decimal.Decimal // amount reducing balance (receipts, CNs, etc.)
	Balance     decimal.Decimal // running balance after this line
	SourceID    uint            // PK of the source document
}

// CustomerStatement is the full statement output for one customer.
type CustomerStatement struct {
	Customer       models.Customer
	FromDate       time.Time
	ToDate         time.Time
	OpeningBalance decimal.Decimal
	Lines          []StatementLine
	ClosingBalance decimal.Decimal
}

// ── Query ─────────────────────────────────────────────────────────────────────

// GetCustomerStatement builds a statement of account for a customer
// between fromDate and toDate (inclusive).
func GetCustomerStatement(db *gorm.DB, companyID, customerID uint, fromDate, toDate time.Time) (*CustomerStatement, error) {
	var customer models.Customer
	if err := db.Where("id = ? AND company_id = ?", customerID, companyID).First(&customer).Error; err != nil {
		return nil, fmt.Errorf("load customer: %w", err)
	}

	stmt := &CustomerStatement{
		Customer: customer,
		FromDate: fromDate,
		ToDate:   toDate,
	}

	// ── Opening balance ──────────────────────────────────────────────────────
	// Sum of invoice balance_due for invoices whose invoice_date < fromDate
	// and that still have balance_due > 0.
	var openingResult struct {
		Total decimal.Decimal
	}
	db.Model(&models.Invoice{}).
		Select("COALESCE(SUM(balance_due), 0) as total").
		Where("company_id = ? AND customer_id = ? AND invoice_date < ? AND balance_due > 0",
			companyID, customerID, fromDate).
		Scan(&openingResult)
	stmt.OpeningBalance = openingResult.Total

	// ── Collect lines ────────────────────────────────────────────────────────
	var lines []StatementLine

	// Invoices issued in period
	var invoices []models.Invoice
	db.Where("company_id = ? AND customer_id = ? AND invoice_date BETWEEN ? AND ?",
		companyID, customerID, fromDate, toDate).
		Order("invoice_date asc").Find(&invoices)
	for _, inv := range invoices {
		lines = append(lines, StatementLine{
			Date:        inv.InvoiceDate,
			Type:        StatementLineInvoice,
			Reference:   inv.InvoiceNumber,
			Description: "Invoice",
			Debit:       inv.Amount,
			SourceID:    inv.ID,
		})
	}

	// CustomerReceipts confirmed in period
	var receipts []models.CustomerReceipt
	db.Where("company_id = ? AND customer_id = ? AND status NOT IN ? AND confirmed_at BETWEEN ? AND ?",
		companyID, customerID,
		[]models.CustomerReceiptStatus{models.CustomerReceiptStatusDraft, models.CustomerReceiptStatusVoided},
		fromDate, toDate).
		Order("confirmed_at asc").Find(&receipts)
	for _, rcpt := range receipts {
		lines = append(lines, StatementLine{
			Date:        safeTime(rcpt.ConfirmedAt, rcpt.ReceiptDate),
			Type:        StatementLineReceipt,
			Reference:   rcpt.ReceiptNumber,
			Description: "Receipt",
			Credit:      rcpt.Amount,
			SourceID:    rcpt.ID,
		})
	}

	// CreditNotes issued in period
	var creditNotes []models.CreditNote
	db.Where("company_id = ? AND customer_id = ? AND status IN ? AND credit_note_date BETWEEN ? AND ?",
		companyID, customerID,
		[]models.CreditNoteStatus{models.CreditNoteStatusIssued, models.CreditNoteStatusPartiallyApplied, models.CreditNoteStatusFullyApplied},
		fromDate, toDate).
		Order("credit_note_date asc").Find(&creditNotes)
	for _, cn := range creditNotes {
		lines = append(lines, StatementLine{
			Date:        cn.CreditNoteDate,
			Type:        StatementLineCreditNote,
			Reference:   cn.CreditNoteNumber,
			Description: "Credit Note",
			Credit:      cn.Amount,
			SourceID:    cn.ID,
		})
	}

	// ARRefunds posted in period
	var refunds []models.ARRefund
	db.Where("company_id = ? AND customer_id = ? AND status = ? AND posted_at BETWEEN ? AND ?",
		companyID, customerID, string(models.ARRefundStatusPosted), fromDate, toDate).
		Order("posted_at asc").Find(&refunds)
	for _, ref := range refunds {
		lines = append(lines, StatementLine{
			Date:        safeTime(ref.PostedAt, ref.RefundDate),
			Type:        StatementLineRefund,
			Reference:   ref.RefundNumber,
			Description: "Refund",
			Credit:      ref.Amount,
			SourceID:    ref.ID,
		})
	}

	// ARWriteOffs posted in period
	var writeOffs []models.ARWriteOff
	db.Where("company_id = ? AND customer_id = ? AND status = ? AND posted_at BETWEEN ? AND ?",
		companyID, customerID, string(models.ARWriteOffStatusPosted), fromDate, toDate).
		Order("posted_at asc").Find(&writeOffs)
	for _, wo := range writeOffs {
		lines = append(lines, StatementLine{
			Date:        safeTime(wo.PostedAt, wo.WriteOffDate),
			Type:        StatementLineWriteOff,
			Reference:   wo.WriteOffNumber,
			Description: "Write-Off",
			Credit:      wo.Amount,
			SourceID:    wo.ID,
		})
	}

	// Sort lines by date ascending.
	sort.Slice(lines, func(i, j int) bool {
		return lines[i].Date.Before(lines[j].Date)
	})

	// Compute running balance.
	running := stmt.OpeningBalance
	for i := range lines {
		running = running.Add(lines[i].Debit).Sub(lines[i].Credit)
		lines[i].Balance = running
	}

	stmt.Lines = lines
	stmt.ClosingBalance = running
	return stmt, nil
}

// safeTime returns t if non-nil, otherwise fallback.
func safeTime(t *time.Time, fallback time.Time) time.Time {
	if t != nil {
		return *t
	}
	return fallback
}
