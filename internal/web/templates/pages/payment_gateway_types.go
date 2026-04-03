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
	FormError  string
}

type PaymentMappingsVM struct {
	HasCompany      bool
	GatewayAccounts []models.PaymentGatewayAccount
	GLAccounts      []models.Account
	Mappings        map[uint]*models.PaymentAccountingMapping
	Saved           bool
	FormError       string
}

type PaymentRequestsVM struct {
	HasCompany bool
	Requests   []models.PaymentRequest
	Accounts   []models.PaymentGatewayAccount
	Created    bool
	FormError  string
}

type PaymentTransactionsVM struct {
	HasCompany   bool
	Transactions []models.PaymentTransaction
	Accounts     []models.PaymentGatewayAccount
	Created      bool
	JustPosted   bool

	// TxnStates maps txn_id → unified action state (accounting + application + actions).
	TxnStates map[uint]services.PaymentActionState

	JustApplied       bool
	JustRefundApplied bool
	JustUnapplied     bool
	FormError         string
}
