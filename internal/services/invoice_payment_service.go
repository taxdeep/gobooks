// 遵循project_guide.md
package services

// invoice_payment_service.go — Link invoices to payment gateway requests.
//
// Key semantics:
//   - Creating a payment request does NOT mark the invoice as paid.
//   - Invoice payment status changes only when a future payment application
//     / transaction posting service processes a completed payment event.
//   - Payment request currency defaults to invoice currency. Future provider
//     connectors may introduce supported-currency validation or restrictions.
//
// Duplicate guard (current conservative strategy):
//   At most one active (draft/pending plus legacy created) payment request is allowed per
//   (invoice, gateway_account) combination. This prevents accidental double-
//   clicking and redundant requests. Future installment / partial payment
//   request support may relax this to a more granular rule.
//
// Active payment request statuses:
//   draft, pending, and legacy created rows — these are "in-flight" and block new requests.
// Terminal statuses:
//   paid, failed, cancelled, refunded, partially_refunded — these do NOT block.
//
// Naming note:
//   PaymentRequest uses ExternalRef (generic provider reference).
//   PaymentTransaction uses ExternalTxnRef (transaction-specific reference).
//   Both map to DB columns with matching names. This is intentional — requests
//   and transactions have different external reference semantics.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

var (
	ErrActivePaymentRequestExists      = errors.New("an active payment request already exists for this invoice and gateway")
	ErrDuplicateExternalTxnRef         = errors.New("a transaction with this external reference already exists for this gateway")
	ErrInvoiceNotPayable               = errors.New("invoice is not in a payable state (must be issued, sent, overdue, or partially paid)")
	ErrChannelInvoiceGatewayBlock      = errors.New("channel-origin invoices cannot use the payment gateway — collect via channel settlement instead")
	ErrFXInvoiceGatewayBlock           = errors.New("foreign-currency invoices cannot use the payment gateway in this version")
	ErrVerifiedGatewayCollectionExists = errors.New("a payment has already been confirmed by the payment provider for this invoice — apply the existing payment before creating a new request")
)

// activePaymentRequestStatuses defines statuses that count as "in-flight"
// for the duplicate guard. Terminal statuses (paid, failed, etc.) do not block.
var activePaymentRequestStatuses = []string{
	string(models.PaymentRequestDraft),
	string(models.PaymentRequestCreated),
	string(models.PaymentRequestPending),
}

// payableInvoiceStatuses defines which invoice statuses allow new payment requests.
var payableInvoiceStatuses = []models.InvoiceStatus{
	models.InvoiceStatusIssued,
	models.InvoiceStatusSent,
	models.InvoiceStatusOverdue,
	models.InvoiceStatusPartiallyPaid,
}

// IsInvoicePayable returns true if the invoice status allows payment requests.
func IsInvoicePayable(status models.InvoiceStatus) bool {
	for _, s := range payableInvoiceStatuses {
		if status == s {
			return true
		}
	}
	return false
}

// ── Invoice payment request creation ─────────────────────────────────────────

type InvoicePaymentRequestInput struct {
	CompanyID        uint
	InvoiceID        uint
	GatewayAccountID uint
	Description      string
	// Amount and Currency are auto-derived from invoice if zero/empty.
	Amount       decimal.Decimal
	CurrencyCode string
}

// CreatePaymentRequestForInvoice creates a payment request linked to an invoice.
// Only invoices in payable statuses (issued/sent/overdue/partially_paid) are accepted.
// Paid, voided, and draft invoices are rejected.
func CreatePaymentRequestForInvoice(db *gorm.DB, input InvoicePaymentRequestInput) (*models.PaymentRequest, error) {
	// 1. Validate invoice belongs to company.
	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", input.InvoiceID, input.CompanyID).
		First(&inv).Error; err != nil {
		return nil, fmt.Errorf("invoice not found")
	}

	// 2. Block channel-origin invoices — must collect via channel settlement.
	if inv.ChannelOrderID != nil {
		return nil, ErrChannelInvoiceGatewayBlock
	}

	// 3. Block FX invoices — gateway only supports base currency.
	var company models.Company
	if err := db.Where("id = ?", input.CompanyID).First(&company).Error; err != nil {
		return nil, fmt.Errorf("company not found")
	}
	if inv.CurrencyCode != "" && company.BaseCurrencyCode != "" && inv.CurrencyCode != company.BaseCurrencyCode {
		return nil, ErrFXInvoiceGatewayBlock
	}

	// 4. Validate invoice is in a payable state.
	if !IsInvoicePayable(inv.Status) {
		return nil, ErrInvoiceNotPayable
	}

	// 5. Validate gateway account belongs to company.
	var gw models.PaymentGatewayAccount
	if err := db.Where("id = ? AND company_id = ? AND is_active = true", input.GatewayAccountID, input.CompanyID).
		First(&gw).Error; err != nil {
		return nil, fmt.Errorf("gateway account not found or inactive")
	}

	// 6. Block if a verified gateway collection already exists for this invoice.
	// A payment_succeeded attempt means the provider has confirmed a payment; the
	// operator must apply/settle it before a new request is raised.
	if HasVerifiedGatewayCollectionForInvoice(db, input.InvoiceID, input.CompanyID) {
		return nil, ErrVerifiedGatewayCollectionExists
	}

	// 7. Duplicate guard: block if an active request exists for same invoice+gateway.
	var activeCount int64
	db.Model(&models.PaymentRequest{}).
		Where("company_id = ? AND invoice_id = ? AND gateway_account_id = ? AND status IN ?",
			input.CompanyID, input.InvoiceID, input.GatewayAccountID, activePaymentRequestStatuses).
		Count(&activeCount)
	if activeCount > 0 {
		return nil, ErrActivePaymentRequestExists
	}

	// 8. Derive defaults from invoice.
	// Subtract any unconsumed verified collections so the request amount reflects
	// the true remaining collectible balance, not a stale BalanceDue that ignores
	// provider-confirmed but not-yet-applied payments.
	amount := input.Amount
	if amount.IsZero() {
		effectiveBalance := inv.BalanceDue
		if effectiveBalance.IsPositive() {
			if unconsumed := UnconsumedVerifiedCollectionAmount(db, inv, input.CompanyID); unconsumed.IsPositive() {
				effectiveBalance = effectiveBalance.Sub(unconsumed)
			}
		}
		if effectiveBalance.IsPositive() {
			amount = effectiveBalance
		} else {
			amount = inv.Amount
		}
	}
	currency := input.CurrencyCode
	if currency == "" {
		currency = inv.CurrencyCode
	}
	description := input.Description
	if description == "" {
		description = "Payment for Invoice " + inv.InvoiceNumber
	}

// 9. Create payment request. Initial status is always pending regardless of
// creation entry point; "created" remains only as a legacy stored value.
	req := models.PaymentRequest{
		CompanyID:        input.CompanyID,
		GatewayAccountID: input.GatewayAccountID,
		InvoiceID:        &input.InvoiceID,
		CustomerID:       &inv.CustomerID,
		Amount:           amount,
		CurrencyCode:     currency,
		Status:           models.PaymentRequestPending,
		Description:      description,
	}
	if err := CreatePaymentRequest(db, &req); err != nil {
		return nil, fmt.Errorf("create payment request: %w", err)
	}

	return &req, nil
}

// ── Payment transaction duplicate guard ──────────────────────────────────────

// ValidateExternalTxnRefUnique checks that a non-empty ExternalTxnRef
// (the transaction-level external reference from the payment provider)
// is unique within (company, gateway_account).
func ValidateExternalTxnRefUnique(db *gorm.DB, companyID, gatewayAccountID uint, ref string) error {
	if ref == "" {
		return nil
	}
	var count int64
	db.Model(&models.PaymentTransaction{}).
		Where("company_id = ? AND gateway_account_id = ? AND external_txn_ref = ?",
			companyID, gatewayAccountID, ref).
		Count(&count)
	if count > 0 {
		return ErrDuplicateExternalTxnRef
	}
	return nil
}

// ── Query: payment requests for invoice ──────────────────────────────────────

func ListPaymentRequestsForInvoice(db *gorm.DB, companyID, invoiceID uint) ([]models.PaymentRequest, error) {
	var reqs []models.PaymentRequest
	err := db.Preload("GatewayAccount").
		Where("company_id = ? AND invoice_id = ?", companyID, invoiceID).
		Order("created_at DESC").
		Find(&reqs).Error
	return reqs, err
}
