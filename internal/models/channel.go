// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
)

// ── Channel type ─────────────────────────────────────────────────────────────

// ChannelType identifies the external sales platform.
type ChannelType string

const (
	ChannelTypeAmazon       ChannelType = "amazon"
	ChannelTypeShopify      ChannelType = "shopify"
	ChannelTypeWooCommerce  ChannelType = "woocommerce"
	ChannelTypeTemu         ChannelType = "temu"
	ChannelTypeEbay         ChannelType = "ebay"
	ChannelTypeCSVImport    ChannelType = "csv_import"
	ChannelTypeManualImport ChannelType = "manual_import"
)

// AllChannelTypes returns the supported channel types in display order.
func AllChannelTypes() []ChannelType {
	return []ChannelType{
		ChannelTypeAmazon, ChannelTypeShopify, ChannelTypeWooCommerce,
		ChannelTypeTemu, ChannelTypeEbay, ChannelTypeCSVImport, ChannelTypeManualImport,
	}
}

// ChannelTypeLabel returns a human-readable label.
func ChannelTypeLabel(ct ChannelType) string {
	switch ct {
	case ChannelTypeAmazon:
		return "Amazon"
	case ChannelTypeShopify:
		return "Shopify"
	case ChannelTypeWooCommerce:
		return "WooCommerce"
	case ChannelTypeTemu:
		return "Temu"
	case ChannelTypeEbay:
		return "eBay"
	case ChannelTypeCSVImport:
		return "CSV Import"
	case ChannelTypeManualImport:
		return "Manual"
	default:
		return string(ct)
	}
}

// ChannelMappingStatusLabel returns a human-readable label for mapping status.
func ChannelMappingStatusLabel(s ChannelMappingStatus) string {
	switch s {
	case MappingStatusMappedExact:
		return "Mapped"
	case MappingStatusMappedBundle:
		return "Mapped (Bundle)"
	case MappingStatusUnmapped:
		return "Unmapped"
	case MappingStatusNeedsReview:
		return "Needs Review"
	default:
		return string(s)
	}
}

// ── Channel auth status ──────────────────────────────────────────────────────

type ChannelAuthStatus string

const (
	ChannelAuthPending      ChannelAuthStatus = "pending"
	ChannelAuthConnected    ChannelAuthStatus = "connected"
	ChannelAuthDisconnected ChannelAuthStatus = "disconnected"
	ChannelAuthError        ChannelAuthStatus = "error"
)

// ── Mapping status ───────────────────────────────────────────────────────────

// ChannelMappingStatus tracks whether an imported order line has been matched
// to a Balanciz item.
type ChannelMappingStatus string

const (
	MappingStatusMappedExact  ChannelMappingStatus = "mapped_exact"
	MappingStatusMappedBundle ChannelMappingStatus = "mapped_bundle"
	MappingStatusUnmapped     ChannelMappingStatus = "unmapped"
	MappingStatusNeedsReview  ChannelMappingStatus = "needs_review"
)

// ── Sales channel account ────────────────────────────────────────────────────

// SalesChannelAccount represents one external sales channel account connected
// by a company (e.g. an Amazon Seller Central account for US marketplace).
//
// No OAuth tokens are stored here; credential management is deferred to a future
// secure vault layer. This table captures identity and sync state only.
type SalesChannelAccount struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ChannelType        ChannelType       `gorm:"type:text;not null"`
	DisplayName        string            `gorm:"type:text;not null;default:''"`
	Region             string            `gorm:"type:text;not null;default:''"`
	ExternalAccountRef *string           `gorm:"type:text"`
	AuthStatus         ChannelAuthStatus `gorm:"type:text;not null;default:'pending'"`
	LastSyncAt         *time.Time
	IsActive           bool `gorm:"not null;default:true"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ── Item ↔ channel SKU mapping ───────────────────────────────────────────────

// ItemChannelMapping links a Balanciz ProductService to an external platform
// listing. One item can have multiple mappings across different marketplaces.
//
// external_sku is the seller SKU on the platform; asin / fnsku are
// Amazon-specific identifiers (nullable for non-Amazon channels).
type ItemChannelMapping struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ItemID           uint                `gorm:"not null;index"`
	Item             ProductService      `gorm:"foreignKey:ItemID"`
	ChannelAccountID uint                `gorm:"not null;index"`
	ChannelAccount   SalesChannelAccount `gorm:"foreignKey:ChannelAccountID"`

	ChannelType   ChannelType `gorm:"type:text;not null"`
	MarketplaceID *string     `gorm:"type:text"`
	ExternalSKU   string      `gorm:"type:text;not null;default:''"`
	ASIN          *string     `gorm:"type:text"`
	FNSKU         *string     `gorm:"type:text"`
	ListingStatus *string     `gorm:"type:text"`
	IsActive      bool        `gorm:"not null;default:true"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ── Channel order (raw import layer) ─────────────────────────────────────────

// ChannelOrder holds an imported external order before it enters the Balanciz
// business flow. Orders must go through mapping + validation before they can
// be converted to invoices or trigger inventory movements.
//
// raw_payload stores the platform's original JSON for auditing and re-processing.
type ChannelOrder struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ChannelAccountID uint                `gorm:"not null;index"`
	ChannelAccount   SalesChannelAccount `gorm:"foreignKey:ChannelAccountID"`

	ExternalOrderID string         `gorm:"type:text;not null;default:''"`
	MarketplaceID   *string        `gorm:"type:text"`
	OrderDate       *time.Time     `gorm:"type:date"`
	OrderStatus     string         `gorm:"type:text;not null;default:'imported'"`
	CurrencyCode    string         `gorm:"type:text;not null;default:''"`
	RawPayload      datatypes.JSON `gorm:"not null"`
	ImportedAt      time.Time      `gorm:"not null"`
	SyncedAt        *time.Time

	// ConvertedInvoiceID links to the draft invoice created from this order.
	// Non-nil means this order has been converted and should not be converted again.
	ConvertedInvoiceID *uint `gorm:"index"`
}

// ── Channel order line ───────────────────────────────────────────────────────

// ChannelOrderLine is one line item from an imported channel order.
// mapped_item_id is set when the external SKU is matched to a Balanciz item.
// mapping_status tracks the match state for the import review workflow.
type ChannelOrderLine struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ChannelOrderID uint         `gorm:"not null;index"`
	ChannelOrder   ChannelOrder `gorm:"foreignKey:ChannelOrderID"`

	ExternalLineID string          `gorm:"type:text;not null;default:''"`
	ExternalSKU    string          `gorm:"type:text;not null;default:''"`
	ASIN           *string         `gorm:"type:text"`
	Quantity       decimal.Decimal `gorm:"type:numeric(10,4);not null;default:0"`
	ItemPrice      *decimal.Decimal `gorm:"type:numeric(18,2)"`
	TaxAmount      *decimal.Decimal `gorm:"type:numeric(18,2)"`
	DiscountAmount *decimal.Decimal `gorm:"type:numeric(18,2)"`

	MappedItemID  *uint                `gorm:"index"`
	MappedItem    *ProductService      `gorm:"foreignKey:MappedItemID"`
	MappingStatus ChannelMappingStatus `gorm:"type:text;not null;default:'unmapped'"`

	RawPayload datatypes.JSON `gorm:"not null"`
	CreatedAt  time.Time
}

// ── Settlement line type ─────────────────────────────────────────────────────

type SettlementLineType string

const (
	SettlementLineSale        SettlementLineType = "sale"
	SettlementLineFee         SettlementLineType = "fee"
	SettlementLineShippingFee SettlementLineType = "shipping_fee"
	SettlementLineRefund      SettlementLineType = "refund"
	SettlementLineAdjustment  SettlementLineType = "adjustment"
	SettlementLineReserve     SettlementLineType = "reserve"
	SettlementLinePayout      SettlementLineType = "payout"
)

// AllSettlementLineTypes returns the supported line types.
func AllSettlementLineTypes() []SettlementLineType {
	return []SettlementLineType{
		SettlementLineSale, SettlementLineFee, SettlementLineShippingFee,
		SettlementLineRefund, SettlementLineAdjustment, SettlementLineReserve, SettlementLinePayout,
	}
}

// SettlementLineTypeLabel returns a human-readable label.
func SettlementLineTypeLabel(t SettlementLineType) string {
	switch t {
	case SettlementLineSale:
		return "Sale"
	case SettlementLineFee:
		return "Fee"
	case SettlementLineShippingFee:
		return "Shipping Fee"
	case SettlementLineRefund:
		return "Refund"
	case SettlementLineAdjustment:
		return "Adjustment"
	case SettlementLineReserve:
		return "Reserve"
	case SettlementLinePayout:
		return "Payout"
	default:
		return string(t)
	}
}

// ── Channel settlement (raw import layer) ────────────────────────────────────

// ChannelSettlement holds an imported settlement/payout report from an external
// channel. Like channel_orders, settlements land here first and are NOT
// automatically posted to the GL.
type ChannelSettlement struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ChannelAccountID     uint                `gorm:"not null;index"`
	ChannelAccount       SalesChannelAccount `gorm:"foreignKey:ChannelAccountID"`
	ExternalSettlementID string              `gorm:"type:text;not null;default:''"`
	SettlementDate       *time.Time          `gorm:"type:date"`
	CurrencyCode         string              `gorm:"type:text;not null;default:''"`
	GrossAmount          decimal.Decimal     `gorm:"type:numeric(18,2);not null;default:0"`
	FeeAmount            decimal.Decimal     `gorm:"type:numeric(18,2);not null;default:0"`
	NetAmount            decimal.Decimal     `gorm:"type:numeric(18,2);not null;default:0"`
	RawPayload           datatypes.JSON      `gorm:"not null"`

	Lines []ChannelSettlementLine `gorm:"foreignKey:SettlementID"`

	// Posting state: non-nil means the settlement fees have been posted to a JE.
	PostedJournalEntryID *uint      `gorm:"index"`
	PostedAt             *time.Time
	// Fee posting reversal: non-nil means the fee JE has been reversed.
	PostedReversalJEID   *uint      `gorm:"index"`

	// Payout recording state: non-nil means the payout has been recorded (Dr Bank, Cr Clearing).
	PayoutJournalEntryID *uint      `gorm:"index"`
	PayoutRecordedAt     *time.Time
	// Payout reversal: non-nil means the payout JE has been reversed.
	PayoutReversalJEID   *uint      `gorm:"index"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChannelSettlementLine is one line item in a settlement report.
// mapped_account_id links to a GL account for future posting.
type ChannelSettlementLine struct {
	ID           uint `gorm:"primaryKey"`
	CompanyID    uint `gorm:"not null;index"`
	SettlementID uint `gorm:"not null;index"`

	LineType    SettlementLineType `gorm:"type:text;not null"`
	Description string             `gorm:"type:text;not null;default:''"`
	ExternalRef string             `gorm:"type:text;not null;default:''"`
	Amount      decimal.Decimal    `gorm:"type:numeric(18,2);not null;default:0"`

	MappedAccountID *uint    `gorm:"index"`
	MappedAccount   *Account `gorm:"foreignKey:MappedAccountID"`

	RawPayload datatypes.JSON `gorm:"not null"`
	CreatedAt  time.Time
}

// ── Channel accounting mapping ───────────────────────────────────────────────

// ChannelAccountingMapping defines which GL accounts to use when posting
// channel-originated transactions (Amazon fees, refunds, shipping, tax).
// One row per channel account. All account FKs must belong to the same company.
//
// This is a schema reservation for future settlement/fee posting.
// No posting logic references this table in the current phase.
type ChannelAccountingMapping struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ChannelAccountID uint                `gorm:"not null;uniqueIndex:uq_channel_acct_mappings_gorm"`
	ChannelAccount   SalesChannelAccount `gorm:"foreignKey:ChannelAccountID"`

	ClearingAccountID         *uint    `gorm:"index"`
	ClearingAccount           *Account `gorm:"foreignKey:ClearingAccountID"`
	FeeExpenseAccountID       *uint    `gorm:"index"`
	FeeExpenseAccount         *Account `gorm:"foreignKey:FeeExpenseAccountID"`
	RefundAccountID           *uint
	RefundAccount             *Account `gorm:"foreignKey:RefundAccountID"`
	ShippingIncomeAccountID   *uint
	ShippingIncomeAccount     *Account `gorm:"foreignKey:ShippingIncomeAccountID"`
	ShippingExpenseAccountID  *uint
	ShippingExpenseAccount    *Account `gorm:"foreignKey:ShippingExpenseAccountID"`
	MarketplaceTaxAccountID   *uint
	MarketplaceTaxAccount     *Account `gorm:"foreignKey:MarketplaceTaxAccountID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
