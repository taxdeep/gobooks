package ai

import "context"

type AccountingCommandIntent string
type AccountingActionLevel string

const (
	IntentCreateExpense      AccountingCommandIntent = "create_expense"
	IntentCreateInvoiceDraft AccountingCommandIntent = "create_invoice_draft"
	IntentCreateBillDraft    AccountingCommandIntent = "create_bill_draft"
	IntentExplainTransaction AccountingCommandIntent = "explain_transaction"
	IntentSearchTransaction  AccountingCommandIntent = "search_transaction"
	IntentSummarizeMonth     AccountingCommandIntent = "summarize_month"
	IntentReconcileBankItem  AccountingCommandIntent = "reconcile_bank_item"

	ActionReadOnly           AccountingActionLevel = "read_only"
	ActionSuggestOnly        AccountingActionLevel = "suggest_only"
	ActionCreateDraft        AccountingActionLevel = "create_draft"
	ActionPreparePosting     AccountingActionLevel = "prepare_posting"
	ActionAutoPostWithPolicy AccountingActionLevel = "auto_post_with_policy"
)

type AccountingCommandInput struct {
	CompanyID uint
	UserText  string
}

type AccountingActionPlan struct {
	Intent      AccountingCommandIntent
	ActionLevel AccountingActionLevel
	Summary     string
	Steps       []string
}

type AccountingValidationResult struct {
	OK       bool
	Reasons  []string
	Warnings []string
}

type AccountingCopilotPlanner interface {
	ParseCommand(ctx context.Context, input AccountingCommandInput) (AccountingActionPlan, error)
	ValidateActionPlan(ctx context.Context, plan AccountingActionPlan) (AccountingValidationResult, error)
	CreateDraftAfterConfirmation(ctx context.Context, plan AccountingActionPlan) (string, error)
}

type NoopAccountingCopilotPlanner struct{}

func (NoopAccountingCopilotPlanner) ParseCommand(context.Context, AccountingCommandInput) (AccountingActionPlan, error) {
	return AccountingActionPlan{ActionLevel: ActionSuggestOnly}, nil
}

func (NoopAccountingCopilotPlanner) ValidateActionPlan(context.Context, AccountingActionPlan) (AccountingValidationResult, error) {
	return AccountingValidationResult{OK: false, Reasons: []string{"Accounting copilot posting is not enabled in V1"}}, nil
}

func (NoopAccountingCopilotPlanner) CreateDraftAfterConfirmation(context.Context, AccountingActionPlan) (string, error) {
	return "", ErrAIDisabled
}
