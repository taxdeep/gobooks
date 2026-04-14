// 遵循project_guide.md
package pages

import (
	"gobooks/internal/models"
	"gobooks/internal/services"
)

// ── PurchaseOrder VMs ─────────────────────────────────────────────────────────

// PurchaseOrdersVM is the view model for the Purchase Orders list page.
type PurchaseOrdersVM struct {
	HasCompany     bool
	PurchaseOrders []models.PurchaseOrder
	Vendors        []models.Vendor
	FilterStatus   string
	FilterVendor   string
	Created        bool
}

// PurchaseOrderDetailVM is the view model for a single PurchaseOrder detail / edit page.
type PurchaseOrderDetailVM struct {
	HasCompany    bool
	PurchaseOrder models.PurchaseOrder
	Vendors       []models.Vendor
	Accounts      []models.Account
	Products      []models.ProductService
	TaxCodes      []models.TaxCode
	FormError     string
	Saved         bool
	Confirmed     bool
	Cancelled     bool
}

// ── VendorPrepayment VMs ──────────────────────────────────────────────────────

// VendorPrepaymentsVM is the view model for the Vendor Prepayments list page.
type VendorPrepaymentsVM struct {
	HasCompany   bool
	Prepayments  []models.VendorPrepayment
	Vendors      []models.Vendor
	FilterStatus string
	FilterVendor string
	Created      bool
}

// VendorPrepaymentDetailVM is the view model for a single VendorPrepayment detail / edit page.
type VendorPrepaymentDetailVM struct {
	HasCompany  bool
	Prepayment  models.VendorPrepayment
	Vendors     []models.Vendor
	Accounts    []models.Account
	FormError   string
	Saved       bool
	Posted      bool
	Voided      bool
}

// ── VendorReturn VMs ──────────────────────────────────────────────────────────

// VendorReturnsVM is the view model for the Vendor Returns list page.
type VendorReturnsVM struct {
	HasCompany     bool
	Returns        []models.VendorReturn
	Vendors        []models.Vendor
	FilterStatus   string
	FilterVendor   string
	Created        bool
}

// VendorReturnDetailVM is the view model for a single VendorReturn detail / edit page.
type VendorReturnDetailVM struct {
	HasCompany bool
	Return     models.VendorReturn
	Vendors    []models.Vendor
	Bills      []models.Bill // open bills for same vendor
	FormError  string
	Saved      bool
	Submitted  bool
	Approved   bool
	Cancelled  bool
	Processed  bool
}

// ── VendorCreditNote VMs ──────────────────────────────────────────────────────

// VendorCreditNotesVM is the view model for the Vendor Credit Notes list page.
type VendorCreditNotesVM struct {
	HasCompany   bool
	CreditNotes  []models.VendorCreditNote
	Vendors      []models.Vendor
	FilterStatus string
	FilterVendor string
	Created      bool
}

// VendorCreditNoteDetailVM is the view model for a single VendorCreditNote detail / edit page.
type VendorCreditNoteDetailVM struct {
	HasCompany bool
	CreditNote models.VendorCreditNote
	Vendors    []models.Vendor
	Accounts   []models.Account
	Bills      []models.Bill // bills for same vendor
	FormError  string
	Saved      bool
	Posted     bool
	Voided     bool
}

// ── VendorRefund VMs ──────────────────────────────────────────────────────────

// VendorRefundsVM is the view model for the Vendor Refunds list page.
type VendorRefundsVM struct {
	HasCompany   bool
	Refunds      []models.VendorRefund
	Vendors      []models.Vendor
	FilterStatus string
	FilterVendor string
	Created      bool
}

// VendorRefundDetailVM is the view model for a single VendorRefund detail / edit page.
type VendorRefundDetailVM struct {
	HasCompany bool
	Refund     models.VendorRefund
	Vendors    []models.Vendor
	Accounts   []models.Account
	FormError  string
	Saved      bool
	Posted     bool
	Voided     bool
	Reversed   bool
}

// ── APAging VM ────────────────────────────────────────────────────────────────

// APAgingVM is the view model for the AP Aging report page.
type APAgingVM struct {
	HasCompany bool
	AsOf       string
	Report     *services.APAging
	FormError  string
}
