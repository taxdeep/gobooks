// 遵循project_guide.md
package services

// payment_posting_service.go — Generate JE from payment gateway transactions.
//
// Accounting rules:
//
//   charge/capture → Dr GW Clearing, Cr AR
//     Customer paid via gateway; gateway holds funds; AR reduced.
//     Requires: payment_request → invoice linkage.
//
//   fee → Dr Fee Expense, Cr GW Clearing
//     Gateway deducted a fee from the held funds.
//
//   refund → Dr Refund Account, Cr GW Clearing
//     Gateway refund event; reduces funds held. This is the payment-side
//     refund only — not a revenue reversal or credit note.
//
//   payout → Dr Bank, Cr GW Clearing
//     Gateway paid out to the company bank account.
//
//   chargeback → Dr Chargeback Account, Cr GW Clearing
//     Card network forcibly reverses a prior payment. Semantically different
//     from a voluntary refund — typically represents a loss/expense to the company.
//     Requires ChargebackAccountID in the gateway accounting mapping.
//
//   dispute → NOT SUPPORTED for financial posting. Disputes are tracked via the
//     GatewayDispute model; only when a dispute is lost does a chargeback
//     PaymentTransaction get created (which can then be posted).
//
// Posting does NOT automatically mark the invoice as paid.
// Future: a payment application service will update invoice.BalanceDue.

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

var (
	ErrPaymentTxnAlreadyPosted   = errors.New("payment transaction has already been posted")
	ErrPaymentTxnNotPostable     = errors.New("payment transaction is not postable")
	ErrPaymentTxnTypeUnsupported = errors.New("payment transaction type not supported for posting: dispute")
	ErrPaymentTxnNoInvoiceLink   = errors.New("charge/capture requires a linked payment request with an invoice")
	ErrPaymentTxnNoARAccount     = errors.New("no active Accounts Receivable account found")
)

// postablePaymentTxnTypes defines which transaction types can be posted.
var postablePaymentTxnTypes = map[models.PaymentTransactionType]bool{
	models.TxnTypeCharge:     true,
	models.TxnTypeCapture:    true,
	models.TxnTypeFee:        true,
	models.TxnTypeRefund:     true,
	models.TxnTypePayout:     true,
	models.TxnTypeChargeback: true,
}

// ── Validation ───────────────────────────────────────────────────────────────

// PaymentPostBlocker describes why a transaction can't be posted.
func PaymentPostBlocker(db *gorm.DB, companyID, txnID uint) string {
	err := ValidatePaymentTransactionPostable(db, companyID, txnID)
	if err == nil {
		return ""
	}
	return err.Error()
}

// ValidatePaymentTransactionPostable checks if a transaction can be posted.
func ValidatePaymentTransactionPostable(db *gorm.DB, companyID, txnID uint) error {
	var txn models.PaymentTransaction
	if err := db.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn).Error; err != nil {
		return fmt.Errorf("transaction not found")
	}

	if txn.PostedJournalEntryID != nil {
		return ErrPaymentTxnAlreadyPosted
	}

	if !postablePaymentTxnTypes[txn.TransactionType] {
		return ErrPaymentTxnTypeUnsupported
	}

	if txn.Amount.IsZero() {
		return fmt.Errorf("%w: amount is zero", ErrPaymentTxnNotPostable)
	}

	// Check accounting mapping.
	mapping, _ := GetPaymentAccountingMapping(db, companyID, txn.GatewayAccountID)
	if mapping == nil || mapping.ClearingAccountID == nil {
		return fmt.Errorf("%w: clearing account not configured", ErrPaymentTxnNotPostable)
	}

	switch txn.TransactionType {
	case models.TxnTypeCharge, models.TxnTypeCapture:
		// Requires payment_request → invoice linkage for AR resolution.
		if txn.PaymentRequestID == nil {
			return ErrPaymentTxnNoInvoiceLink
		}
		var req models.PaymentRequest
		if err := db.Where("id = ? AND company_id = ?", *txn.PaymentRequestID, companyID).First(&req).Error; err != nil {
			return ErrPaymentTxnNoInvoiceLink
		}
		if req.InvoiceID == nil {
			return ErrPaymentTxnNoInvoiceLink
		}
		// Block channel-origin invoices from gateway posting.
		var linkedInv models.Invoice
		if err := db.Where("id = ? AND company_id = ?", *req.InvoiceID, companyID).First(&linkedInv).Error; err == nil {
			if linkedInv.ChannelOrderID != nil {
				return ErrChannelInvoiceGatewayBlock
			}
		}
	case models.TxnTypeFee:
		if mapping.FeeExpenseAccountID == nil {
			return fmt.Errorf("%w: fee expense account not configured", ErrPaymentTxnNotPostable)
		}
	case models.TxnTypeRefund:
		if mapping.RefundAccountID == nil {
			return fmt.Errorf("%w: refund account not configured", ErrPaymentTxnNotPostable)
		}
	case models.TxnTypePayout:
		if mapping.PayoutBankAccountID == nil {
			return fmt.Errorf("%w: payout bank account not configured", ErrPaymentTxnNotPostable)
		}
	case models.TxnTypeChargeback:
		if mapping.ChargebackAccountID == nil {
			return fmt.Errorf("%w: chargeback account not configured", ErrPaymentTxnNotPostable)
		}
	}

	return nil
}

// ── Posting ──────────────────────────────────────────────────────────────────

// PostPaymentTransactionToJournalEntry generates a JE from a payment transaction.
func PostPaymentTransactionToJournalEntry(db *gorm.DB, companyID, txnID uint, actor string) (*models.JournalEntry, error) {
	if err := ValidatePaymentTransactionPostable(db, companyID, txnID); err != nil {
		return nil, err
	}

	var txn models.PaymentTransaction
	db.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn)

	mapping, _ := GetPaymentAccountingMapping(db, companyID, txn.GatewayAccountID)
	clearingAcctID := *mapping.ClearingAccountID

	// Build the debit/credit pair based on transaction type.
	var debitAcctID, creditAcctID uint
	var memo string

	switch txn.TransactionType {
	case models.TxnTypeCharge, models.TxnTypeCapture:
		// Dr GW Clearing, Cr AR.
		debitAcctID = clearingAcctID
		// Resolve AR account.
		var arAcct models.Account
		if err := db.Where("company_id = ? AND detail_account_type = ? AND is_active = true",
			companyID, string(models.DetailAccountsReceivable)).
			Order("code asc").First(&arAcct).Error; err != nil {
			return nil, ErrPaymentTxnNoARAccount
		}
		creditAcctID = arAcct.ID
		memo = "Payment received: " + models.PaymentTransactionTypeLabel(txn.TransactionType)

	case models.TxnTypeFee:
		debitAcctID = *mapping.FeeExpenseAccountID
		creditAcctID = clearingAcctID
		memo = "Gateway fee"

	case models.TxnTypeRefund:
		debitAcctID = *mapping.RefundAccountID
		creditAcctID = clearingAcctID
		memo = "Gateway refund"

	case models.TxnTypePayout:
		debitAcctID = *mapping.PayoutBankAccountID
		creditAcctID = clearingAcctID
		memo = "Gateway payout"

	case models.TxnTypeChargeback:
		// Dr Chargeback Account (loss/expense), Cr GW Clearing.
		// The card network has forcibly reversed the payment; the clearing account
		// is reduced and the loss is recognised in the chargeback expense account.
		debitAcctID = *mapping.ChargebackAccountID
		creditAcctID = clearingAcctID
		memo = "Chargeback"
	}

	amount := txn.Amount.Abs()

	// Transaction.
	var je models.JournalEntry
	err := db.Transaction(func(tx *gorm.DB) error {
		je = models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  txn.CreatedAt,
			JournalNo:  "PGTXN-" + txn.ExternalTxnRef,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourcePaymentGateway,
			SourceID:   txn.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create journal entry: %w", err)
		}

		debitLine := models.JournalLine{
			CompanyID: companyID, JournalEntryID: je.ID,
			AccountID: debitAcctID, Debit: amount, Credit: decimal.Zero, Memo: memo,
		}
		if err := tx.Create(&debitLine).Error; err != nil {
			return fmt.Errorf("create debit line: %w", err)
		}

		creditLine := models.JournalLine{
			CompanyID: companyID, JournalEntryID: je.ID,
			AccountID: creditAcctID, Debit: decimal.Zero, Credit: amount, Memo: memo,
		}
		if err := tx.Create(&creditLine).Error; err != nil {
			return fmt.Errorf("create credit line: %w", err)
		}

		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        []models.JournalLine{debitLine, creditLine},
			SourceType:   models.LedgerSourcePaymentGateway,
			SourceID:     txn.ID,
		}); err != nil {
			return fmt.Errorf("project to ledger: %w", err)
		}

		now := time.Now()
		return tx.Model(&models.PaymentTransaction{}).
			Where("id = ? AND company_id = ?", txnID, companyID).
			Updates(map[string]any{
				"posted_journal_entry_id": je.ID,
				"posted_at":              now,
			}).Error
	})

	if err != nil {
		return nil, err
	}
	return &je, nil
}
