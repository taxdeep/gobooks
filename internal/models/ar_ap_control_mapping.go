// 遵循project_guide.md
package models

import "time"

// ARAPControlDocType identifies which document type a control-account mapping applies to.
type ARAPControlDocType string

const (
	ARAPDocTypeInvoice         ARAPControlDocType = "invoice"
	ARAPDocTypeBill            ARAPControlDocType = "bill"
	ARAPDocTypeCreditNote      ARAPControlDocType = "credit_note"
	ARAPDocTypeCustomerReceipt ARAPControlDocType = "customer_receipt"
	ARAPDocTypeCustomerDeposit ARAPControlDocType = "customer_deposit"
	ARAPDocTypeVendorCredit    ARAPControlDocType = "vendor_credit"
)

// ARAPDocTypeSide maps each document type to its accounting side (AR vs AP).
var ARAPDocTypeSide = map[ARAPControlDocType]string{
	ARAPDocTypeInvoice:         "AR",
	ARAPDocTypeBill:            "AP",
	ARAPDocTypeCreditNote:      "AR",
	ARAPDocTypeCustomerReceipt: "AR",
	ARAPDocTypeCustomerDeposit: "AR",
	ARAPDocTypeVendorCredit:    "AP",
}

// AllARAPDocTypes returns the ordered slice of all supported document types.
func AllARAPDocTypes() []ARAPControlDocType {
	return []ARAPControlDocType{
		ARAPDocTypeInvoice,
		ARAPDocTypeBill,
		ARAPDocTypeCreditNote,
		ARAPDocTypeCustomerReceipt,
		ARAPDocTypeCustomerDeposit,
		ARAPDocTypeVendorCredit,
	}
}

// ARAPControlMapping routes a (company, book, document_type, currency_code) tuple to a
// specific control account. This replaces the hard-coded system_key / detail_account_type
// lookup and makes the AR/AP routing explicit, auditable, and book-aware.
//
// Resolution priority (highest to lowest):
//  1. Exact match:     (company, book_id>0, doc_type, currency_code)
//  2. Book-agnostic:   (company, book_id=0, doc_type, currency_code)
//  3. Default mapping: (company, book_id=0, doc_type, currency_code='')
//  4. Legacy system_key fallback (ar_{code} / ap_{code})
//  5. First active account by detail_account_type
//
// book_id = 0 means "applies to all books". Set book_id > 0 to override for a
// specific secondary book (e.g. a USD-functional IFRS book using a different AR account).
type ARAPControlMapping struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	// BookID = 0 means the mapping applies to all books.
	// BookID > 0 scopes the mapping to a specific accounting book.
	BookID uint `gorm:"not null;default:0;index"`

	// DocumentType is the type of document this mapping governs.
	DocumentType ARAPControlDocType `gorm:"type:text;not null"`

	// CurrencyCode is the ISO 4217 transaction currency this mapping applies to.
	// Empty string '' means this is the fallback mapping for any currency not
	// explicitly mapped (i.e., the "default" entry for this doc type).
	CurrencyCode string `gorm:"type:varchar(3);not null;default:''"`

	// ControlAccountID is the AR or AP control account to use.
	ControlAccountID uint    `gorm:"not null"`
	ControlAccount   Account `gorm:"foreignKey:ControlAccountID"`

	// Notes is an optional human-readable label (e.g. "USD Receivables").
	Notes string `gorm:"type:text;not null;default:''"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
