// 遵循project_guide.md
//
// Package producers contains the per-domain code that translates GORM
// business entities into searchprojection.Document values and calls
// Projector.Upsert / Delete. Organised by entity family:
//
//	contact.go   — Customer + Vendor
//	product.go   — ProductService (Phase 2)
//	transaction.go — Invoice / Bill / Quote / SO / Payment / …  (Phase 3)
//
// Each producer function is explicit and idempotent — callers invoke it
// after a successful GORM commit for the affected entity. No GORM hooks,
// no outbox: the goal is for every projection write to be obviously
// traceable from the call site that triggered it.
package producers

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"

	"gobooks/internal/logging"
	"gobooks/internal/models"
	"gobooks/internal/searchprojection"
)

// ErrEntityNotInCompany is returned when a producer is asked to project an
// entity ID that doesn't belong to the supplied companyID. Strong signal of
// a programmer bug in the calling handler — the entity was loaded without
// re-checking tenant ownership, or a stray ID was passed in from user input.
//
// Returning an error rather than panicking on the theory that production
// should never crash on a single malformed projection — but the caller MUST
// log it: silent drop = invisible cross-tenant leak attempt.
var ErrEntityNotInCompany = errors.New("producers: entity does not belong to the supplied company")

// Entity-type discriminators. Must match the SmartPicker entity keys
// so the Phase 4 engine layer can serve both old-style per-entity
// requests (entity=customer) and new-style global search from the same
// projection table.
const (
	EntityTypeCustomer = "customer"
	EntityTypeVendor   = "vendor"
)

// ProjectCustomer refreshes the search_documents row for one customer.
// Loads the full record from GORM scoped by (id, company_id), builds a
// Document, and upserts via the Projector. The companyID is the
// authoritative tenant scope from the request context — if the row's
// stored company_id disagrees (or the row doesn't exist for this
// company), the call returns ErrEntityNotInCompany and skips the upsert.
//
// Invoke from:
//   - customer create handler, after successful db.Create
//   - customer update handler, after successful db.Save
//   - customer lifecycle handler, after SetCustomerActive
//   - cmd/search-backfill, for every existing customer on first run
//
// A nil projector is a legitimate "projection disabled" state and
// returns nil without logging — used during tests and tools that
// only need the GORM side of the work.
func ProjectCustomer(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, customerID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectCustomer: companyID is required")
	}
	var c models.Customer
	err := db.Where("id = ? AND company_id = ?", customerID, companyID).First(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectCustomer: load customer %d for company %d: %w", customerID, companyID, err)
	}
	doc := CustomerDocument(c)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectCustomer upsert failed",
			"customer_id", customerID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

// DeleteCustomerProjection removes the search row for a customer that's
// being hard-deleted. Soft-delete / deactivation goes through
// ProjectCustomer (the row stays with status=inactive so the operator
// can still reach the record via search to reactivate).
func DeleteCustomerProjection(ctx context.Context, p searchprojection.Projector, companyID, customerID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeCustomer, customerID)
}

// CustomerDocument maps a models.Customer to a searchprojection.Document.
// Exported so the backfill CLI can reuse it without going through the
// single-row Load path in ProjectCustomer.
func CustomerDocument(c models.Customer) searchprojection.Document {
	status := "active"
	if !c.IsActive {
		status = "inactive"
	}
	// Subtitle format mirrors the QuickBooks quick-search dropdown:
	//   "Customer · <email>"  (email omitted when blank)
	subtitle := "Customer"
	if c.Email != "" {
		subtitle = "Customer · " + c.Email
	}
	// Memo field feeds low-priority matching (e.g. company notes), but
	// Customer doesn't have a memo column — feed the address so "bur"
	// still finds customers in Burnaby. Harmless when empty.
	memo := assembleCustomerAddressLine(c)

	return searchprojection.Document{
		CompanyID:  c.CompanyID,
		EntityType: EntityTypeCustomer,
		EntityID:   c.ID,
		Title:      c.Name,
		Subtitle:   subtitle,
		Memo:       memo,
		DocDate:    &c.CreatedAt, // use creation time for recency ordering
		Status:     status,
		URLPath:    "/customers/" + strconv.FormatUint(uint64(c.ID), 10),
	}
}

// ProjectVendor refreshes the search_documents row for one vendor.
// Same contract as ProjectCustomer — caller MUST pass the authoritative
// companyID; cross-tenant rows are rejected with ErrEntityNotInCompany.
func ProjectVendor(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, vendorID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectVendor: companyID is required")
	}
	var v models.Vendor
	err := db.Where("id = ? AND company_id = ?", vendorID, companyID).First(&v).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectVendor: load vendor %d for company %d: %w", vendorID, companyID, err)
	}
	doc := VendorDocument(v)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectVendor upsert failed",
			"vendor_id", vendorID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

// DeleteVendorProjection mirrors DeleteCustomerProjection.
func DeleteVendorProjection(ctx context.Context, p searchprojection.Projector, companyID, vendorID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeVendor, vendorID)
}

// VendorDocument maps models.Vendor → Document.
func VendorDocument(v models.Vendor) searchprojection.Document {
	status := "active"
	if !v.IsActive {
		status = "inactive"
	}
	subtitle := "Vendor"
	if v.Email != "" {
		subtitle = "Vendor · " + v.Email
	} else if v.Phone != "" {
		subtitle = "Vendor · " + v.Phone
	}
	// Vendor has a combined Address + Notes — both feed low-priority memo.
	memo := v.Address
	if v.Notes != "" {
		if memo != "" {
			memo = memo + " " + v.Notes
		} else {
			memo = v.Notes
		}
	}
	return searchprojection.Document{
		CompanyID:  v.CompanyID,
		EntityType: EntityTypeVendor,
		EntityID:   v.ID,
		Title:      v.Name,
		Subtitle:   subtitle,
		Memo:       memo,
		DocDate:    &v.CreatedAt,
		Status:     status,
		URLPath:    "/vendors/" + strconv.FormatUint(uint64(v.ID), 10),
	}
}

// assembleCustomerAddressLine joins the address fragments into a single
// searchable line. Empty fragments are skipped so the result doesn't
// contain double spaces.
func assembleCustomerAddressLine(c models.Customer) string {
	parts := []string{
		c.AddrStreet1,
		c.AddrStreet2,
		c.AddrCity,
		c.AddrProvince,
		c.AddrPostalCode,
		c.AddrCountry,
	}
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out == "" {
			out = p
		} else {
			out = out + " " + p
		}
	}
	return out
}
