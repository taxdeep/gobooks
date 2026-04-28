// 遵循project_guide.md
package pages

import (
	"balanciz/internal/models"
	"balanciz/internal/services"
)

// ── Batch 23: Payment Reverse Exception VMs ───────────────────────────────────

type PaymentReverseExceptionListVM struct {
	HasCompany   bool
	Exceptions   []models.PaymentReverseException
	JustActioned bool
	FormError    string
}

type PaymentReverseExceptionDetailVM struct {
	HasCompany   bool
	Exception    *models.PaymentReverseException
	JustActioned bool
	ActionError  string

	// Linked transaction summaries (nil if not found).
	ReverseTxn  *models.PaymentTransaction
	OriginalTxn *models.PaymentTransaction

	// Rollup is the Batch 25 structured allocation summary.  Nil when the
	// rollup service was unable to load the data (degrades gracefully).
	Rollup *services.PaymentReverseDetailRollup

	// Batch 26: Hook policy and recent attempt history.
	Hooks    []services.PRHook
	Attempts []models.PaymentReverseResolutionAttempt

	// HookActioned is true when a hook was just executed (POST redirect).
	HookActioned bool
	// HookError is non-empty when a hook execution returned a user-facing error.
	HookError string
}

type PaymentGatewaysVM struct {
	HasCompany bool
	Breadcrumb []SettingsBreadcrumbPart
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

	JustApplied              bool
	JustRefundApplied        bool
	JustChargebackApplied    bool
	JustUnapplied            bool
	// Batch 22: multi-alloc reverse apply just completed.
	JustReverseAllocApplied  bool
	FormError                string

	// Batch 22: ReverseAllocations maps reverse_txn_id → its PaymentReverseAllocation rows.
	// Only populated for transactions where IsReverseAllocated == true.
	ReverseAllocations map[uint][]models.PaymentReverseAllocation
}
