// 遵循project_guide.md
package services

// payment_gateway_service.go — Platform-agnostic payment gateway services.
//
// Gateway clearing account semantics:
//   Customer pays   → Dr GW Clearing, Cr AR/Revenue (gateway holds funds)
//   Gateway fee     → Dr Fee Expense, Cr GW Clearing (fee deducted from hold)
//   Payout to bank  → Dr Bank, Cr GW Clearing (funds transferred)
//
// This round implements foundation only — no automatic JE generation.
// Future provider adapters (Stripe, PayPal) will create payment_transactions
// via webhooks, then a posting service will generate JEs from those events.

import (
	"time"

	"gobooks/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ── Gateway Account CRUD ─────────────────────────────────────────────────────

func ListGatewayAccounts(db *gorm.DB, companyID uint) ([]models.PaymentGatewayAccount, error) {
	var accounts []models.PaymentGatewayAccount
	err := db.Where("company_id = ?", companyID).Order("display_name ASC").Find(&accounts).Error
	return accounts, err
}

func GetGatewayAccount(db *gorm.DB, companyID, id uint) (*models.PaymentGatewayAccount, error) {
	var acct models.PaymentGatewayAccount
	err := db.Where("id = ? AND company_id = ?", id, companyID).First(&acct).Error
	if err != nil {
		return nil, err
	}
	return &acct, nil
}

func CreateGatewayAccount(db *gorm.DB, acct *models.PaymentGatewayAccount) error {
	return db.Create(acct).Error
}

func UpdateGatewayAccount(db *gorm.DB, companyID uint, acct *models.PaymentGatewayAccount) error {
	return db.Where("id = ? AND company_id = ?", acct.ID, companyID).Save(acct).Error
}

// ── Payment Accounting Mapping CRUD ──────────────────────────────────────────

func GetPaymentAccountingMapping(db *gorm.DB, companyID, gatewayAccountID uint) (*models.PaymentAccountingMapping, error) {
	var m models.PaymentAccountingMapping
	err := db.Preload("ClearingAccount").Preload("FeeExpenseAccount").
		Preload("RefundAccount").Preload("PayoutBankAccount").
		Where("company_id = ? AND gateway_account_id = ?", companyID, gatewayAccountID).
		First(&m).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &m, err
}

func SavePaymentAccountingMapping(db *gorm.DB, m *models.PaymentAccountingMapping) error {
	var existing models.PaymentAccountingMapping
	err := db.Where("company_id = ? AND gateway_account_id = ?", m.CompanyID, m.GatewayAccountID).
		First(&existing).Error
	if err == gorm.ErrRecordNotFound {
		return db.Create(m).Error
	}
	if err != nil {
		return err
	}
	m.ID = existing.ID
	return db.Save(m).Error
}

// ── Payment Request CRUD ─────────────────────────────────────────────────────

func ListPaymentRequests(db *gorm.DB, companyID uint, limit int) ([]models.PaymentRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	var reqs []models.PaymentRequest
	err := db.Preload("GatewayAccount").
		Where("company_id = ?", companyID).
		Order("created_at DESC").
		Limit(limit).
		Find(&reqs).Error
	return reqs, err
}

func CreatePaymentRequest(db *gorm.DB, req *models.PaymentRequest) error {
	req.CreatedAt = time.Now()
	req.UpdatedAt = time.Now()
	return db.Create(req).Error
}

// ── Payment Transaction CRUD ─────────────────────────────────────────────────

func ListPaymentTransactions(db *gorm.DB, companyID uint, limit int) ([]models.PaymentTransaction, error) {
	if limit <= 0 {
		limit = 50
	}
	var txns []models.PaymentTransaction
	err := db.Preload("GatewayAccount").
		Where("company_id = ?", companyID).
		Order("created_at DESC").
		Limit(limit).
		Find(&txns).Error
	return txns, err
}

func CreatePaymentTransaction(db *gorm.DB, txn *models.PaymentTransaction) error {
	txn.CreatedAt = time.Now()
	txn.UpdatedAt = time.Now()
	if txn.RawPayload == nil {
		txn.RawPayload = datatypes.JSON("{}")
	}
	return db.Create(txn).Error
}

// ── Summary helper ───────────────────────────────────────────────────────────

type GatewayAccountSummary struct {
	Account      models.PaymentGatewayAccount
	RequestCount int64
	TxnCount     int64
}

func ListGatewayAccountSummaries(db *gorm.DB, companyID uint) ([]GatewayAccountSummary, error) {
	accounts, err := ListGatewayAccounts(db, companyID)
	if err != nil {
		return nil, err
	}
	summaries := make([]GatewayAccountSummary, len(accounts))
	for i, a := range accounts {
		var reqCount, txnCount int64
		db.Model(&models.PaymentRequest{}).Where("company_id = ? AND gateway_account_id = ?", companyID, a.ID).Count(&reqCount)
		db.Model(&models.PaymentTransaction{}).Where("company_id = ? AND gateway_account_id = ?", companyID, a.ID).Count(&txnCount)
		summaries[i] = GatewayAccountSummary{Account: a, RequestCount: reqCount, TxnCount: txnCount}
	}
	return summaries, nil
}

// ── Label helper ─────────────────────────────────────────────────────────────

func PaymentRequestStatusLabel(s models.PaymentRequestStatus) string {
	switch s {
	case models.PaymentRequestDraft:
		return "Draft"
	case models.PaymentRequestPaid:
		return "Paid"
	case models.PaymentRequestPending:
		return "Pending"
	case models.PaymentRequestFailed:
		return "Failed"
	case models.PaymentRequestCancelled:
		return "Cancelled"
	case models.PaymentRequestRefunded:
		return "Refunded"
	default:
		return string(s)
	}
}

func PaymentTxnStatusBadge(status string) string {
	switch status {
	case "completed":
		return "bg-success-soft text-success-hover"
	case "pending":
		return "bg-warning-soft text-warning"
	case "failed":
		return "bg-danger-soft text-danger"
	default:
		return "bg-background text-text-muted"
	}
}

func describeGatewayClearing() string {
	return `Gateway clearing represents funds held by the payment processor on behalf of the company.
When a customer pays, clearing increases. When the processor deducts fees or pays out to the bank, clearing decreases.
A zero clearing balance means all processed funds have been accounted for.`
}

// ExportGatewayClearing is exposed for future use; not called in this round.
var _ = describeGatewayClearing
