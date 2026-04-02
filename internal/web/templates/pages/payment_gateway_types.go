// 遵循project_guide.md
package pages

import (
	"gobooks/internal/models"
	"gobooks/internal/services"
)

type PaymentGatewaysVM struct {
	HasCompany bool
	Accounts   []services.GatewayAccountSummary
	Created    bool
}

type PaymentMappingsVM struct {
	HasCompany      bool
	GatewayAccounts []models.PaymentGatewayAccount
	GLAccounts      []models.Account
	Mappings        map[uint]*models.PaymentAccountingMapping
	Saved           bool
}

type PaymentRequestsVM struct {
	HasCompany bool
	Requests   []models.PaymentRequest
	Accounts   []models.PaymentGatewayAccount
	Created    bool
}

type PaymentTransactionsVM struct {
	HasCompany   bool
	Transactions []models.PaymentTransaction
	Accounts     []models.PaymentGatewayAccount
	Created      bool
	JustPosted   bool
	// Blockers maps txn_id → blocker reason (empty = postable).
	Blockers       map[uint]string
	// AppBlockers maps txn_id → application blocker reason (empty = applicable).
	AppBlockers       map[uint]string
	RefundBlockers    map[uint]string
	UnapplyBlockers   map[uint]string
	JustApplied       bool
	JustRefundApplied bool
	JustUnapplied     bool
}
