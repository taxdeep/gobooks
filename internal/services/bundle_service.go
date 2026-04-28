// 遵循project_guide.md
package services

// bundle_service.go — Bundle/Kit domain logic.
//
// A bundle is a sellable combination of inventory items. The bundle itself does
// not track stock; its components do. When a bundle is sold on an invoice, the
// posting engine expands the bundle into component-level stock movements and
// COGS fragments while keeping revenue on the bundle line.
//
// This file provides:
//   - Validation for bundle items and their components
//   - Component CRUD (save/load)
//   - Expansion helpers for invoice posting (stock validation, COGS, movements)

import (
	"fmt"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// ── Validation ───────────────────────────────────────────────────────────────

// ValidateBundleItemType checks that a bundle item has the correct type.
// Bundles must be non_inventory; inventory+bundle is not allowed.
func ValidateBundleItemType(itemType models.ProductServiceType) error {
	if itemType == models.ProductServiceTypeInventory {
		return fmt.Errorf("bundle items must be type Non-Inventory — inventory items cannot be bundles")
	}
	if itemType == models.ProductServiceTypeService {
		return fmt.Errorf("bundle items must be type Non-Inventory — service items cannot be bundles")
	}
	return nil
}

// ValidateBundleComponents checks that a set of components is valid for a bundle item.
// All component items must be inventory-type items in the same company.
func ValidateBundleComponents(db *gorm.DB, companyID, parentItemID uint, components []models.ItemComponent) error {
	if len(components) == 0 {
		return fmt.Errorf("bundle must have at least one component")
	}

	seen := map[uint]bool{}
	for i, c := range components {
		lineNum := i + 1

		if c.ComponentItemID == 0 {
			return fmt.Errorf("component %d: item is required", lineNum)
		}
		if c.ComponentItemID == parentItemID {
			return fmt.Errorf("component %d: item cannot reference itself", lineNum)
		}
		if c.Quantity.IsZero() || c.Quantity.IsNegative() {
			return fmt.Errorf("component %d: quantity must be positive", lineNum)
		}
		if seen[c.ComponentItemID] {
			return fmt.Errorf("component %d: duplicate component item", lineNum)
		}
		seen[c.ComponentItemID] = true

		// Verify component exists, belongs to company, is inventory type, and is active.
		var comp models.ProductService
		if err := db.Where("id = ? AND company_id = ?", c.ComponentItemID, companyID).
			First(&comp).Error; err != nil {
			return fmt.Errorf("component %d: item not found in this company", lineNum)
		}
		if comp.Type != models.ProductServiceTypeInventory {
			return fmt.Errorf("component %d (%s): must be an inventory item", lineNum, comp.Name)
		}
		if !comp.IsActive {
			return fmt.Errorf("component %d (%s): item is inactive", lineNum, comp.Name)
		}
		if comp.ItemStructureType == models.ItemStructureBundle {
			return fmt.Errorf("component %d (%s): nested bundles are not allowed", lineNum, comp.Name)
		}
	}

	return nil
}

// ── Component CRUD ───────────────────────────────────────────────────────────

// SaveBundleComponents replaces all components for a bundle item.
// Must be called inside a transaction with the item save.
func SaveBundleComponents(tx *gorm.DB, companyID, parentItemID uint, components []models.ItemComponent) error {
	// Delete existing components.
	if err := tx.Where("company_id = ? AND parent_item_id = ?", companyID, parentItemID).
		Delete(&models.ItemComponent{}).Error; err != nil {
		return fmt.Errorf("delete old components: %w", err)
	}

	// Insert new components.
	for i, c := range components {
		c.CompanyID = companyID
		c.ParentItemID = parentItemID
		c.SortOrder = i + 1
		if err := tx.Create(&c).Error; err != nil {
			return fmt.Errorf("create component %d: %w", i+1, err)
		}
	}

	return nil
}

// GetBundleComponents loads all components for a bundle item, ordered by sort_order.
// Returns empty slice for non-bundle items.
func GetBundleComponents(db *gorm.DB, companyID, parentItemID uint) ([]models.ItemComponent, error) {
	var components []models.ItemComponent
	err := db.Preload("ComponentItem").
		Where("company_id = ? AND parent_item_id = ?", companyID, parentItemID).
		Order("sort_order ASC").
		Find(&components).Error
	return components, err
}

// ── Invoice expansion helpers ────────────────────────────────────────────────

// ExpandedComponent represents a single component requirement from a bundle sale.
type ExpandedComponent struct {
	ComponentItem *models.ProductService
	RequiredQty   decimal.Decimal // invoice_line_qty × bundle_component_qty
}

// ExpandBundleLinesForInvoice expands all bundle lines on an invoice into
// component-level stock requirements. Non-bundle lines are ignored.
// The returned map is keyed by component item ID; quantities are aggregated
// if the same component appears in multiple bundle lines.
func ExpandBundleLinesForInvoice(db *gorm.DB, companyID uint, lines []models.InvoiceLine) ([]ExpandedComponent, error) {
	// Collect (bundleItemID → invoiceLineQty) for all bundle lines.
	type bundleNeed struct {
		itemID      uint
		invoiceQty  decimal.Decimal
	}
	var bundles []bundleNeed

	for _, l := range lines {
		if l.ProductService == nil {
			continue
		}
		if l.ProductService.ItemStructureType != models.ItemStructureBundle {
			continue
		}
		bundles = append(bundles, bundleNeed{
			itemID:     l.ProductService.ID,
			invoiceQty: l.Qty,
		})
	}

	if len(bundles) == 0 {
		return nil, nil
	}

	// Aggregate component requirements.
	componentReqs := map[uint]*ExpandedComponent{} // component_item_id → aggregated requirement

	for _, b := range bundles {
		components, err := GetBundleComponents(db, companyID, b.itemID)
		if err != nil {
			return nil, fmt.Errorf("load bundle %d components: %w", b.itemID, err)
		}
		for _, c := range components {
			needed := b.invoiceQty.Mul(c.Quantity)
			if existing, ok := componentReqs[c.ComponentItemID]; ok {
				existing.RequiredQty = existing.RequiredQty.Add(needed)
			} else {
				componentReqs[c.ComponentItemID] = &ExpandedComponent{
					ComponentItem: &c.ComponentItem,
					RequiredQty:   needed,
				}
			}
		}
	}

	// Convert map to slice.
	result := make([]ExpandedComponent, 0, len(componentReqs))
	for _, ec := range componentReqs {
		result = append(result, *ec)
	}
	return result, nil
}
