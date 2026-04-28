// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testChannelDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:channel_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.ProductService{},
		&models.SalesChannelAccount{},
		&models.ItemChannelMapping{},
		&models.ChannelOrder{},
		&models.ChannelOrderLine{},
	); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}
	return db
}

func seedChannelCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "Channel Co", IsActive: true}
	db.Create(&co)
	return co.ID
}

func seedChannelItems(t *testing.T, db *gorm.DB, companyID uint) (widgetID, bundleID uint) {
	t.Helper()
	acct := models.Account{CompanyID: companyID, Code: "4000", Name: "Rev", RootAccountType: models.RootRevenue, DetailAccountType: "revenue", IsActive: true}
	db.Create(&acct)

	widget := models.ProductService{CompanyID: companyID, Name: "Widget", Type: models.ProductServiceTypeInventory, RevenueAccountID: acct.ID, IsActive: true}
	widget.ApplyTypeDefaults()
	db.Create(&widget)

	bundle := models.ProductService{CompanyID: companyID, Name: "Kit", Type: models.ProductServiceTypeNonInventory, ItemStructureType: models.ItemStructureBundle, RevenueAccountID: acct.ID, CanBeSold: true, IsActive: true}
	db.Create(&bundle)

	return widget.ID, bundle.ID
}

// ── Channel Account CRUD ─────────────────────────────────────────────────────

func TestChannelAccount_CRUD(t *testing.T) {
	db := testChannelDB(t)
	companyID := seedChannelCompany(t, db)

	acct := models.SalesChannelAccount{
		CompanyID: companyID, ChannelType: models.ChannelTypeAmazon,
		DisplayName: "Amazon US", Region: "US", AuthStatus: models.ChannelAuthPending, IsActive: true,
	}
	if err := CreateChannelAccount(db, &acct); err != nil {
		t.Fatal(err)
	}
	if acct.ID == 0 {
		t.Fatal("Account not created")
	}

	accounts, err := ListChannelAccounts(db, companyID)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("Expected 1 account, got %d", len(accounts))
	}

	if err := DeleteChannelAccount(db, companyID, acct.ID); err != nil {
		t.Fatal(err)
	}

	accounts, _ = ListChannelAccounts(db, companyID)
	if len(accounts) != 0 {
		t.Fatal("Account not deleted")
	}
}

func TestChannelAccount_CompanyIsolation(t *testing.T) {
	db := testChannelDB(t)
	co1 := seedChannelCompany(t, db)
	co2 := seedChannelCompany(t, db)

	acct := models.SalesChannelAccount{CompanyID: co1, ChannelType: models.ChannelTypeShopify, DisplayName: "Shop", AuthStatus: models.ChannelAuthPending, IsActive: true}
	CreateChannelAccount(db, &acct)

	accounts, _ := ListChannelAccounts(db, co2)
	if len(accounts) != 0 {
		t.Fatal("Company 2 should not see company 1's accounts")
	}
}

// ── Mapping Resolver ─────────────────────────────────────────────────────────

func TestResolveMappedItem_ExactMatch(t *testing.T) {
	db := testChannelDB(t)
	companyID := seedChannelCompany(t, db)
	widgetID, _ := seedChannelItems(t, db, companyID)

	acct := models.SalesChannelAccount{CompanyID: companyID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ", AuthStatus: models.ChannelAuthPending, IsActive: true}
	CreateChannelAccount(db, &acct)

	m := models.ItemChannelMapping{CompanyID: companyID, ItemID: widgetID, ChannelAccountID: acct.ID, ChannelType: models.ChannelTypeAmazon, ExternalSKU: "AMZ-WIDGET-001", IsActive: true}
	CreateItemMapping(db, &m)

	result, err := ResolveMappedItem(db, companyID, acct.ID, "", "AMZ-WIDGET-001")
	if err != nil {
		t.Fatal(err)
	}
	if result.MappingStatus != models.MappingStatusMappedExact {
		t.Errorf("Expected mapped_exact, got %s", result.MappingStatus)
	}
	if result.Item == nil || result.Item.ID != widgetID {
		t.Error("Wrong item resolved")
	}
}

func TestResolveMappedItem_BundleMatch(t *testing.T) {
	db := testChannelDB(t)
	companyID := seedChannelCompany(t, db)
	_, bundleID := seedChannelItems(t, db, companyID)

	acct := models.SalesChannelAccount{CompanyID: companyID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ", AuthStatus: models.ChannelAuthPending, IsActive: true}
	CreateChannelAccount(db, &acct)

	m := models.ItemChannelMapping{CompanyID: companyID, ItemID: bundleID, ChannelAccountID: acct.ID, ChannelType: models.ChannelTypeAmazon, ExternalSKU: "AMZ-KIT-001", IsActive: true}
	CreateItemMapping(db, &m)

	result, err := ResolveMappedItem(db, companyID, acct.ID, "", "AMZ-KIT-001")
	if err != nil {
		t.Fatal(err)
	}
	if result.MappingStatus != models.MappingStatusMappedBundle {
		t.Errorf("Expected mapped_bundle, got %s", result.MappingStatus)
	}
}

func TestResolveMappedItem_Unmapped(t *testing.T) {
	db := testChannelDB(t)
	companyID := seedChannelCompany(t, db)

	acct := models.SalesChannelAccount{CompanyID: companyID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ", AuthStatus: models.ChannelAuthPending, IsActive: true}
	CreateChannelAccount(db, &acct)

	result, err := ResolveMappedItem(db, companyID, acct.ID, "", "UNKNOWN-SKU")
	if err != nil {
		t.Fatal(err)
	}
	if result.MappingStatus != models.MappingStatusUnmapped {
		t.Errorf("Expected unmapped, got %s", result.MappingStatus)
	}
	if result.Item != nil {
		t.Error("Unmapped result should have nil item")
	}
}

// ── Channel Orders ───────────────────────────────────────────────────────────

func TestCreateChannelOrder_AutoResolvesMapping(t *testing.T) {
	db := testChannelDB(t)
	companyID := seedChannelCompany(t, db)
	widgetID, _ := seedChannelItems(t, db, companyID)

	acct := models.SalesChannelAccount{CompanyID: companyID, ChannelType: models.ChannelTypeShopify, DisplayName: "Shop", AuthStatus: models.ChannelAuthPending, IsActive: true}
	CreateChannelAccount(db, &acct)

	// Create mapping for widget
	CreateItemMapping(db, &models.ItemChannelMapping{
		CompanyID: companyID, ItemID: widgetID, ChannelAccountID: acct.ID,
		ChannelType: models.ChannelTypeShopify, ExternalSKU: "SHOP-W1", IsActive: true,
	})

	// Create order with one mapped and one unmapped line.
	order := models.ChannelOrder{
		CompanyID: companyID, ChannelAccountID: acct.ID,
		ExternalOrderID: "ORD-001", OrderStatus: "imported",
		RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelOrderLine{
		{ExternalSKU: "SHOP-W1", Quantity: decimal.NewFromInt(3), RawPayload: datatypes.JSON("{}")},
		{ExternalSKU: "UNKNOWN-SKU", Quantity: decimal.NewFromInt(1), RawPayload: datatypes.JSON("{}")},
	}

	if err := CreateChannelOrderWithLines(db, &order, lines); err != nil {
		t.Fatal(err)
	}

	// Verify lines have correct mapping status.
	savedLines, _ := GetChannelOrderLines(db, companyID, order.ID)
	if len(savedLines) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(savedLines))
	}

	foundMapped := false
	foundUnmapped := false
	for _, l := range savedLines {
		if l.ExternalSKU == "SHOP-W1" && l.MappingStatus == models.MappingStatusMappedExact {
			foundMapped = true
			if l.MappedItemID == nil || *l.MappedItemID != widgetID {
				t.Error("Mapped line should point to widget")
			}
		}
		if l.ExternalSKU == "UNKNOWN-SKU" && l.MappingStatus == models.MappingStatusUnmapped {
			foundUnmapped = true
			if l.MappedItemID != nil {
				t.Error("Unmapped line should have nil mapped_item_id")
			}
		}
	}
	if !foundMapped {
		t.Error("Expected one mapped line")
	}
	if !foundUnmapped {
		t.Error("Expected one unmapped line")
	}
}

func TestChannelOrder_CompanyIsolation(t *testing.T) {
	db := testChannelDB(t)
	co1 := seedChannelCompany(t, db)
	co2 := seedChannelCompany(t, db)

	acct := models.SalesChannelAccount{CompanyID: co1, ChannelType: models.ChannelTypeManualImport, DisplayName: "Manual", AuthStatus: models.ChannelAuthPending, IsActive: true}
	CreateChannelAccount(db, &acct)

	order := models.ChannelOrder{CompanyID: co1, ChannelAccountID: acct.ID, ExternalOrderID: "X-1", OrderStatus: "imported", RawPayload: datatypes.JSON("{}")}
	CreateChannelOrderWithLines(db, &order, nil)

	orders, _ := ListChannelOrders(db, co2, 50)
	if len(orders) != 0 {
		t.Error("Company 2 should not see company 1's orders")
	}
}
