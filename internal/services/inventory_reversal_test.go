// 遵循project_guide.md
package services

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
)

// ── Invoice void inventory reversal tests ────────────────────────────────────

func TestVoidInvoice_StockItem_RestoresInventory(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Opening: 50 @ $20
	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromInt(20),
		AsOfDate: time.Now(),
	})

	// Post invoice selling 10 units
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-VOID-1",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(1000), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(1000), BalanceDue: decimal.NewFromInt(1000),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget",
		Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(1000), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(1000),
	})

	if err := PostInvoice(db, s.companyID, inv.ID, "test", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	// Verify balance is 40 after sale
	bal, _ := GetBalance(db, s.companyID, s.stockItemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("Expected 40 after sale, got %s", bal.QuantityOnHand)
	}

	// Void the invoice
	if err := VoidInvoice(db, s.companyID, inv.ID, "test", nil); err != nil {
		t.Fatalf("VoidInvoice: %v", err)
	}

	// Balance should be restored to 50
	bal, _ = GetBalance(db, s.companyID, s.stockItemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("Expected 50 after void, got %s", bal.QuantityOnHand)
	}

	// Verify reversal movement was created
	var revMovs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?", s.companyID, "invoice_reversal").Find(&revMovs)
	if len(revMovs) != 1 {
		t.Fatalf("Expected 1 reversal movement, got %d", len(revMovs))
	}
	if !revMovs[0].QuantityDelta.Equal(decimal.NewFromInt(10)) {
		t.Errorf("Reversal qty expected +10, got %s", revMovs[0].QuantityDelta)
	}
}

func TestVoidInvoice_ServiceOnly_NoInventoryReversal(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Post service-only invoice
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-SVC-VOID",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(500), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.svcItemID, Description: "Consulting",
		Qty: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(500), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(500),
	})

	PostInvoice(db, s.companyID, inv.ID, "test", nil)
	VoidInvoice(db, s.companyID, inv.ID, "test", nil)

	// No reversal movements should exist
	var revCount int64
	db.Model(&models.InventoryMovement{}).Where("source_type = ?", "invoice_reversal").Count(&revCount)
	if revCount != 0 {
		t.Error("Service-only invoice void should not create inventory reversal movements")
	}
}

func TestVoidInvoice_DoubleVoid_Blocked(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromInt(20),
		AsOfDate: time.Now(),
	})

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-DBL-VOID",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(1000), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(1000), BalanceDue: decimal.NewFromInt(1000),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget",
		Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(1000), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(1000),
	})

	PostInvoice(db, s.companyID, inv.ID, "test", nil)
	VoidInvoice(db, s.companyID, inv.ID, "test", nil)

	// Second void should fail
	err := VoidInvoice(db, s.companyID, inv.ID, "test", nil)
	if err == nil {
		t.Fatal("Expected error on double void")
	}

	// Balance should still be 50 (not 60)
	bal, _ := GetBalance(db, s.companyID, s.stockItemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("Expected 50 after double-void attempt, got %s", bal.QuantityOnHand)
	}
}

func TestVoidInvoice_ReversalJEMatchesCOGS(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// 100 @ $30
	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(100), UnitCost: decimal.NewFromInt(30),
		AsOfDate: time.Now(),
	})

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-COGS-REV",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(2000), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(2000), BalanceDue: decimal.NewFromInt(2000),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget",
		Qty: decimal.NewFromInt(20), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(2000), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(2000),
	})

	PostInvoice(db, s.companyID, inv.ID, "test", nil)

	// Find the COGS amount from the original JE
	var origCOGSLine models.JournalLine
	db.Where("company_id = ? AND account_id = ?", s.companyID, s.cogsAcctID).First(&origCOGSLine)
	origCOGSAmount := origCOGSLine.Debit // 20 × $30 = $600

	VoidInvoice(db, s.companyID, inv.ID, "test", nil)

	// Find the reversal JE's COGS credit (should reverse the original debit)
	var reversalJE models.JournalEntry
	db.Where("company_id = ? AND source_type = ?", s.companyID, models.LedgerSourceReversal).First(&reversalJE)

	var revCOGSLine models.JournalLine
	db.Where("journal_entry_id = ? AND account_id = ?", reversalJE.ID, s.cogsAcctID).First(&revCOGSLine)

	if !revCOGSLine.Credit.Equal(origCOGSAmount) {
		t.Errorf("Reversal COGS credit %s does not match original debit %s", revCOGSLine.Credit, origCOGSAmount)
	}
}

// ── Bill void inventory reversal tests ───────────────────────────────────────

func TestVoidBill_StockItem_ReducesInventory(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Opening: 10 @ $15
	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromInt(15),
		AsOfDate: time.Now(),
	})

	// Post bill purchasing 20 @ $25
	bill := models.Bill{
		CompanyID: s.companyID, BillNumber: "BILL-VOID-1",
		VendorID: s.vendorID, BillDate: time.Now(),
		Status: models.BillStatusDraft,
		Subtotal: decimal.NewFromInt(500), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
	}
	db.Create(&bill)
	expID := s.expenseID
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, ExpenseAccountID: &expID,
		Description: "Widget", Qty: decimal.NewFromInt(20), UnitPrice: decimal.NewFromInt(25),
		LineNet: decimal.NewFromInt(500), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(500),
	})

	if err := PostBill(db, s.companyID, bill.ID, "test", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}

	// Balance should be 30 (10 + 20)
	bal, _ := GetBalance(db, s.companyID, s.stockItemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("Expected 30 after purchase, got %s", bal.QuantityOnHand)
	}

	// Void the bill
	if err := VoidBill(db, s.companyID, bill.ID, "test", nil); err != nil {
		t.Fatalf("VoidBill: %v", err)
	}

	// Balance should return to 10
	bal, _ = GetBalance(db, s.companyID, s.stockItemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("Expected 10 after void, got %s", bal.QuantityOnHand)
	}

	// Verify reversal movement
	var revMovs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?", s.companyID, "bill_reversal").Find(&revMovs)
	if len(revMovs) != 1 {
		t.Fatalf("Expected 1 bill reversal movement, got %d", len(revMovs))
	}
	if !revMovs[0].QuantityDelta.Equal(decimal.NewFromInt(-20)) {
		t.Errorf("Reversal qty expected -20, got %s", revMovs[0].QuantityDelta)
	}
}

func TestVoidBill_InsufficientStock_Blocked(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Post bill purchasing 20 @ $25 (no prior stock)
	bill := models.Bill{
		CompanyID: s.companyID, BillNumber: "BILL-VOID-INSUF",
		VendorID: s.vendorID, BillDate: time.Now(),
		Status: models.BillStatusDraft,
		Subtotal: decimal.NewFromInt(500), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
	}
	db.Create(&bill)
	expID := s.expenseID
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, ExpenseAccountID: &expID,
		Description: "Widget", Qty: decimal.NewFromInt(20), UnitPrice: decimal.NewFromInt(25),
		LineNet: decimal.NewFromInt(500), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(500),
	})

	PostBill(db, s.companyID, bill.ID, "test", nil)

	// Sell 15 of the 20 — balance becomes 5
	saleInv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-PARTIAL",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(1500), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(1500), BalanceDue: decimal.NewFromInt(1500),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&saleInv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: saleInv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget",
		Qty: decimal.NewFromInt(15), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(1500), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(1500),
	})
	PostInvoice(db, s.companyID, saleInv.ID, "test", nil)

	// Now try to void the bill (needs to remove 20, but only 5 on hand)
	err := VoidBill(db, s.companyID, bill.ID, "test", nil)
	if err == nil {
		t.Fatal("Expected error voiding bill with insufficient stock")
	}

	// Bill should still be posted (void failed)
	var b models.Bill
	db.First(&b, bill.ID)
	if b.Status == models.BillStatusVoided {
		t.Error("Bill should not be voided when stock is insufficient")
	}
}

func TestVoidBill_NonInventory_NoStockReversal(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Bill with service item only
	bill := models.Bill{
		CompanyID: s.companyID, BillNumber: "BILL-SVC-VOID",
		VendorID: s.vendorID, BillDate: time.Now(),
		Status: models.BillStatusDraft,
		Subtotal: decimal.NewFromInt(300), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(300), BalanceDue: decimal.NewFromInt(300),
	}
	db.Create(&bill)
	expID := s.expenseID
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.svcItemID, ExpenseAccountID: &expID,
		Description: "Consulting", Qty: decimal.NewFromInt(3), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(300), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(300),
	})

	PostBill(db, s.companyID, bill.ID, "test", nil)
	err := VoidBill(db, s.companyID, bill.ID, "test", nil)
	if err != nil {
		t.Fatalf("VoidBill: %v", err)
	}

	var revCount int64
	db.Model(&models.InventoryMovement{}).Where("source_type = ?", "bill_reversal").Count(&revCount)
	if revCount != 0 {
		t.Error("Non-inventory bill void should not create reversal movements")
	}
}

func TestVoidBill_DoubleVoid_Blocked(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	bill := models.Bill{
		CompanyID: s.companyID, BillNumber: "BILL-DBL-VOID",
		VendorID: s.vendorID, BillDate: time.Now(),
		Status: models.BillStatusDraft,
		Subtotal: decimal.NewFromInt(100), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(100), BalanceDue: decimal.NewFromInt(100),
	}
	db.Create(&bill)
	expID := s.expenseID
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.svcItemID, ExpenseAccountID: &expID,
		Description: "Consulting", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(100), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(100),
	})

	PostBill(db, s.companyID, bill.ID, "test", nil)
	VoidBill(db, s.companyID, bill.ID, "test", nil)

	err := VoidBill(db, s.companyID, bill.ID, "test", nil)
	if err == nil {
		t.Fatal("Expected error on double void")
	}
}
