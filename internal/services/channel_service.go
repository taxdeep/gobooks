// 遵循project_guide.md
package services

// channel_service.go — Platform-agnostic channel integration services.
//
// Provides CRUD for channel accounts, item mappings, raw order management,
// and the mapping resolver that connects external SKUs to Balanciz items.
// No marketplace-specific logic lives here — all connector logic is deferred
// to future platform-specific packages.

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ── Channel Account CRUD ─────────────────────────────────────────────────────

func ListChannelAccounts(db *gorm.DB, companyID uint) ([]models.SalesChannelAccount, error) {
	var accounts []models.SalesChannelAccount
	err := db.Where("company_id = ?", companyID).Order("display_name ASC").Find(&accounts).Error
	return accounts, err
}

func GetChannelAccount(db *gorm.DB, companyID, id uint) (*models.SalesChannelAccount, error) {
	var acct models.SalesChannelAccount
	err := db.Where("id = ? AND company_id = ?", id, companyID).First(&acct).Error
	if err != nil {
		return nil, err
	}
	return &acct, nil
}

func CreateChannelAccount(db *gorm.DB, acct *models.SalesChannelAccount) error {
	return db.Create(acct).Error
}

func UpdateChannelAccount(db *gorm.DB, companyID uint, acct *models.SalesChannelAccount) error {
	return db.Where("id = ? AND company_id = ?", acct.ID, companyID).Save(acct).Error
}

func DeleteChannelAccount(db *gorm.DB, companyID, id uint) error {
	// Check for linked mappings or orders before deleting.
	var mapCount int64
	db.Model(&models.ItemChannelMapping{}).Where("company_id = ? AND channel_account_id = ?", companyID, id).Count(&mapCount)
	if mapCount > 0 {
		return fmt.Errorf("cannot delete channel account: %d item mappings exist", mapCount)
	}
	var orderCount int64
	db.Model(&models.ChannelOrder{}).Where("company_id = ? AND channel_account_id = ?", companyID, id).Count(&orderCount)
	if orderCount > 0 {
		return fmt.Errorf("cannot delete channel account: %d orders exist", orderCount)
	}
	return db.Where("id = ? AND company_id = ?", id, companyID).Delete(&models.SalesChannelAccount{}).Error
}

// ── Item Channel Mapping CRUD ────────────────────────────────────────────────

func ListItemMappings(db *gorm.DB, companyID uint) ([]models.ItemChannelMapping, error) {
	var mappings []models.ItemChannelMapping
	err := db.Preload("Item").Preload("ChannelAccount").
		Where("company_id = ?", companyID).
		Order("created_at DESC").
		Find(&mappings).Error
	return mappings, err
}

// ErrDuplicateMapping is returned when an active mapping already exists for the
// same (channel_account_id, marketplace_id, external_sku) combination.
var ErrDuplicateMapping = errors.New("an active mapping already exists for this channel/marketplace/SKU combination")

func CreateItemMapping(db *gorm.DB, m *models.ItemChannelMapping) error {
	// Check for duplicate active mapping.
	var count int64
	q := db.Model(&models.ItemChannelMapping{}).
		Where("company_id = ? AND channel_account_id = ? AND external_sku = ? AND is_active = true",
			m.CompanyID, m.ChannelAccountID, m.ExternalSKU)
	if m.MarketplaceID != nil {
		q = q.Where("marketplace_id = ?", *m.MarketplaceID)
	} else {
		q = q.Where("marketplace_id IS NULL")
	}
	q.Count(&count)
	if count > 0 {
		return ErrDuplicateMapping
	}
	return db.Create(m).Error
}

func UpdateItemMapping(db *gorm.DB, companyID uint, m *models.ItemChannelMapping) error {
	return db.Where("id = ? AND company_id = ?", m.ID, companyID).Save(m).Error
}

// ── Mapping Resolver ─────────────────────────────────────────────────────────

// MappingResult holds the resolution of an external SKU to a Balanciz item.
type MappingResult struct {
	Item          *models.ProductService
	MappingStatus models.ChannelMappingStatus
	MappingID     uint // 0 if unmapped
}

// ResolveMappedItem finds the Balanciz item mapped to an external SKU.
// Returns unmapped if no mapping exists; needs_review if multiple active mappings
// match (ambiguous); mapped_exact or mapped_bundle for a single match.
func ResolveMappedItem(db *gorm.DB, companyID, channelAccountID uint, marketplaceID, externalSKU string) (*MappingResult, error) {
	var mappings []models.ItemChannelMapping
	q := db.Preload("Item").
		Where("company_id = ? AND channel_account_id = ? AND external_sku = ? AND is_active = true",
			companyID, channelAccountID, externalSKU)
	if marketplaceID != "" {
		q = q.Where("marketplace_id = ?", marketplaceID)
	}

	if err := q.Find(&mappings).Error; err != nil {
		return nil, err
	}

	if len(mappings) == 0 {
		return &MappingResult{MappingStatus: models.MappingStatusUnmapped}, nil
	}

	if len(mappings) > 1 {
		// Ambiguous: multiple active mappings for the same SKU.
		return &MappingResult{MappingStatus: models.MappingStatusNeedsReview}, nil
	}

	mapping := mappings[0]
	status := models.MappingStatusMappedExact
	if mapping.Item.ItemStructureType == models.ItemStructureBundle {
		status = models.MappingStatusMappedBundle
	}

	return &MappingResult{
		Item:          &mapping.Item,
		MappingStatus: status,
		MappingID:     mapping.ID,
	}, nil
}

// ── Channel Order CRUD ───────────────────────────────────────────────────────

func ListChannelOrders(db *gorm.DB, companyID uint, limit int) ([]models.ChannelOrder, error) {
	if limit <= 0 {
		limit = 50
	}
	var orders []models.ChannelOrder
	err := db.Preload("ChannelAccount").
		Where("company_id = ?", companyID).
		Order("imported_at DESC").
		Limit(limit).
		Find(&orders).Error
	return orders, err
}

func GetChannelOrder(db *gorm.DB, companyID, id uint) (*models.ChannelOrder, error) {
	var order models.ChannelOrder
	err := db.Preload("ChannelAccount").
		Where("id = ? AND company_id = ?", id, companyID).
		First(&order).Error
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func GetChannelOrderLines(db *gorm.DB, companyID, orderID uint) ([]models.ChannelOrderLine, error) {
	var lines []models.ChannelOrderLine
	err := db.Preload("MappedItem").
		Where("company_id = ? AND channel_order_id = ?", companyID, orderID).
		Order("id ASC").
		Find(&lines).Error
	return lines, err
}

// CreateChannelOrderWithLines creates a channel order and its lines in a transaction.
// Lines are auto-resolved against item mappings.
func CreateChannelOrderWithLines(db *gorm.DB, order *models.ChannelOrder, lines []models.ChannelOrderLine) error {
	return db.Transaction(func(tx *gorm.DB) error {
		order.ImportedAt = time.Now()
		if err := tx.Create(order).Error; err != nil {
			return fmt.Errorf("create channel order: %w", err)
		}

		for i := range lines {
			lines[i].CompanyID = order.CompanyID
			lines[i].ChannelOrderID = order.ID

			// Auto-resolve mapping.
			var mpID string
			if order.MarketplaceID != nil {
				mpID = *order.MarketplaceID
			}
			result, err := ResolveMappedItem(tx, order.CompanyID, order.ChannelAccountID, mpID, lines[i].ExternalSKU)
			if err == nil && result != nil {
				lines[i].MappingStatus = result.MappingStatus
				if result.Item != nil {
					itemID := result.Item.ID
					lines[i].MappedItemID = &itemID
				}
			} else {
				lines[i].MappingStatus = models.MappingStatusUnmapped
			}

			if lines[i].RawPayload == nil {
				lines[i].RawPayload = datatypes.JSON("{}")
			}
			if err := tx.Create(&lines[i]).Error; err != nil {
				return fmt.Errorf("create order line %d: %w", i+1, err)
			}
		}

		return nil
	})
}

// ── Channel order summary helpers ────────────────────────────────────────────

// ChannelOrderSummary holds aggregated info for the order list page.
type ChannelOrderSummary struct {
	Order       models.ChannelOrder
	LineCount   int
	UnmappedCount int
	TotalAmount decimal.Decimal
}

func ListChannelOrderSummaries(db *gorm.DB, companyID uint, limit int) ([]ChannelOrderSummary, error) {
	orders, err := ListChannelOrders(db, companyID, limit)
	if err != nil {
		return nil, err
	}

	summaries := make([]ChannelOrderSummary, len(orders))
	for i, o := range orders {
		var lineCount, unmappedCount int64
		db.Model(&models.ChannelOrderLine{}).Where("channel_order_id = ?", o.ID).Count(&lineCount)
		db.Model(&models.ChannelOrderLine{}).Where("channel_order_id = ? AND mapping_status = ?", o.ID, models.MappingStatusUnmapped).Count(&unmappedCount)

		summaries[i] = ChannelOrderSummary{
			Order:         o,
			LineCount:     int(lineCount),
			UnmappedCount: int(unmappedCount),
		}
	}
	return summaries, nil
}
