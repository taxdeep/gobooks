// 遵循project_guide.md
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

// EntityTypeProductService is the canonical type discriminator for
// product / service rows in search_documents. Matches the SmartPicker
// provider key ("product_service") so Phase 4 can unify both paths.
const EntityTypeProductService = "product_service"

// ProjectProductService refreshes the search_documents row for one
// product or service. Mirrors ProjectCustomer / ProjectVendor: load via
// GORM scoped by (id, company_id), map to Document, Upsert.
//
// Invoke after successful write from:
//   - handleProductServiceCreate   (post-transaction)
//   - handleProductServiceUpdate   (post-transaction)
//   - handleProductServiceInactive (after is_active flip)
//   - cmd/search-backfill          (bulk scan; companyID iterated explicitly)
func ProjectProductService(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, itemID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectProductService: companyID is required")
	}
	var item models.ProductService
	err := db.Where("id = ? AND company_id = ?", itemID, companyID).First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectProductService: load item %d for company %d: %w", itemID, companyID, err)
	}
	doc := ProductServiceDocument(item)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectProductService upsert failed",
			"item_id", itemID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

// DeleteProductServiceProjection removes the row for a hard-deleted item.
// ProductService uses soft-delete (IsActive=false) for the standard
// lifecycle; true deletes are rare. Kept symmetric with the contact
// producers for completeness.
func DeleteProductServiceProjection(ctx context.Context, p searchprojection.Projector, companyID, itemID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeProductService, itemID)
}

// ProductServiceDocument maps models.ProductService → searchprojection.Document.
// Exported so cmd/search-backfill can build the Document directly from a
// row already loaded during a scan without a second First() round-trip.
func ProductServiceDocument(item models.ProductService) searchprojection.Document {
	status := "active"
	if !item.IsActive {
		status = "inactive"
	}

	// Subtitle format: "<kind> · <SKU?>  price N/A omitted".
	// Kind labels match the Item picker's option text ("stock" / "service")
	// so the operator's mental model stays consistent across surfaces.
	kind := "service"
	if item.IsStockItem {
		kind = "stock"
	}
	subtitle := kind
	if item.SKU != "" {
		subtitle = kind + " · " + item.SKU
	}
	if !item.DefaultPrice.IsZero() {
		subtitle = subtitle + " · $" + item.DefaultPrice.StringFixed(2)
	}

	// Memo feeds low-priority substring matching. Use Description so
	// searches like "photo" find "Photography service" when neither Name
	// nor SKU contains the term.
	memo := item.Description

	return searchprojection.Document{
		CompanyID:  item.CompanyID,
		EntityType: EntityTypeProductService,
		EntityID:   item.ID,
		DocNumber:  item.SKU, // SKU drives exact-code matching
		Title:      item.Name,
		Subtitle:   subtitle,
		Memo:       memo,
		DocDate:    &item.CreatedAt,
		Status:     status,
		URLPath:    "/products-services?item_id=" + strconv.FormatUint(uint64(item.ID), 10),
	}
}
