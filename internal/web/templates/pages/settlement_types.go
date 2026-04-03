// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// AccountingMappingsVM is the view-model for the channel accounting mappings page.
type AccountingMappingsVM struct {
	HasCompany      bool
	ChannelAccounts []models.SalesChannelAccount
	GLAccounts      []models.Account
	FormError       string
	Mappings        map[uint]*models.ChannelAccountingMapping // channel_account_id → mapping
	Saved           bool
}

// SettlementSummary holds a settlement with its unmapped line count.
type SettlementSummary struct {
	Settlement    models.ChannelSettlement
	UnmappedCount int64
}

// SettlementsVM is the view-model for the settlements list page.
type SettlementsVM struct {
	HasCompany  bool
	Settlements []SettlementSummary
	Accounts    []models.SalesChannelAccount
	Created     bool
	CreateError bool
}

// SettlementDetailVM is the view-model for a single settlement detail page.
type SettlementDetailVM struct {
	HasCompany    bool
	Settlement    models.ChannelSettlement
	Lines         []models.ChannelSettlementLine
	UnmappedCount int

	// Fee posting state
	IsPostable    bool
	PostableError string
	JustPosted    bool

	// Payout recording state
	IsPayoutRecordable bool
	PayoutError        string
	PayoutSubmitError  string
	JustPayout         bool
	BankAccounts       []models.Account

	// Reversal state
	IsFeeReversible    bool
	FeeReverseError    string
	JustFeeReversed    bool
	IsPayoutReversible bool
	PayoutReverseError string
	JustPayoutReversed bool
}
