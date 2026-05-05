// 遵循project_guide.md
package services

// ar_phase2_test.go — AR Phase 2: Quote + SalesOrder CRUD, status flows,
// Quote→SalesOrder conversion.
//
// Tests verify:
//  1. Quote and SalesOrder creation with line calculation.
//  2. Status transitions (happy path and invalid transitions).
//  3. Only draft records may be edited.
//  4. Quote→SalesOrder conversion: atomic, Quote→converted, SalesOrder created.
//  5. Company isolation (cross-company access blocked).

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func phase2DB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Quote{},
		&models.QuoteLine{},
		&models.SalesOrder{},
		&models.SalesOrderLine{},
		&models.JournalEntry{},
		&models.Invoice{},
		&models.CreditNote{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func p2Company(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	c := models.Company{Name: name, BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p2Customer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p2CustomerWithCurrency(t *testing.T, db *gorm.DB, companyID uint, currencyCode string) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Currency Customer", CurrencyCode: currencyCode}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p2TaxCode(t *testing.T, db *gorm.DB, companyID uint, rate float64) uint {
	t.Helper()
	tc := models.TaxCode{
		CompanyID: companyID,
		Code:      "TAX",
		Rate:      decimal.NewFromFloat(rate),
	}
	if err := db.Create(&tc).Error; err != nil {
		t.Fatal(err)
	}
	return tc.ID
}

func p2RevenueAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	acct := models.Account{
		CompanyID:         companyID,
		Code:              "4000",
		Name:              "Sales",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: models.DetailServiceRevenue,
		IsActive:          true,
	}
	if err := db.Create(&acct).Error; err != nil {
		t.Fatal(err)
	}
	return acct.ID
}

func p2ProductService(t *testing.T, db *gorm.DB, companyID, revenueAccountID uint, name string) uint {
	t.Helper()
	item := models.ProductService{
		CompanyID:        companyID,
		Name:             name,
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: revenueAccountID,
		IsActive:         true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	return item.ID
}

// ── Quote CRUD ────────────────────────────────────────────────────────────────

func TestPhase2_CreateQuote(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)
	taxID := p2TaxCode(t, db, cid, 0.10)

	in := QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{
				TaxCodeID:   &taxID,
				Description: "Item A",
				Quantity:    decimal.NewFromInt(2),
				UnitPrice:   decimal.NewFromInt(100),
			},
		},
	}
	q, err := CreateQuote(db, cid, in)
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if q.Status != models.QuoteStatusDraft {
		t.Errorf("expected draft; got %s", q.Status)
	}
	if len(q.Lines) != 1 {
		t.Fatalf("expected 1 line; got %d", len(q.Lines))
	}
	// LineNet = 2 × 100 = 200; TaxAmount = 200 × 0.10 = 20; LineTotal = 220
	if !q.Lines[0].LineNet.Equal(decimal.NewFromInt(200)) {
		t.Errorf("LineNet: expected 200; got %s", q.Lines[0].LineNet)
	}
	if !q.Lines[0].TaxAmount.Equal(decimal.NewFromInt(20)) {
		t.Errorf("TaxAmount: expected 20; got %s", q.Lines[0].TaxAmount)
	}
	if !q.Lines[0].LineTotal.Equal(decimal.NewFromInt(220)) {
		t.Errorf("LineTotal: expected 220; got %s", q.Lines[0].LineTotal)
	}
	if !q.Total.Equal(decimal.NewFromInt(220)) {
		t.Errorf("Quote.Total: expected 220; got %s", q.Total)
	}
	if q.QuoteNumber == "" {
		t.Error("QuoteNumber must be set")
	}
}

func TestPhase2_QuoteCurrencyFollowsCustomer(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Quote Currency Co")
	custID := p2CustomerWithCurrency(t, db, cid, "USD")

	in := QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{
				Description: "Item A",
				Quantity:    decimal.NewFromInt(1),
				UnitPrice:   decimal.NewFromInt(100),
			},
		},
	}
	q, err := CreateQuote(db, cid, in)
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if q.CurrencyCode != "USD" {
		t.Fatalf("expected quote currency to follow customer USD, got %q", q.CurrencyCode)
	}

	updated, err := UpdateQuote(db, cid, q.ID, in)
	if err != nil {
		t.Fatalf("UpdateQuote: %v", err)
	}
	if updated.CurrencyCode != "USD" {
		t.Fatalf("expected updated quote currency to remain customer USD, got %q", updated.CurrencyCode)
	}
}

func TestPhase2_QuotePersistsSubmittedExchangeRate(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Quote FX Co")
	custID := p2CustomerWithCurrency(t, db, cid, "USD")

	q, err := CreateQuote(db, cid, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "USD",
		ExchangeRate: decimal.RequireFromString("1.33333333"),
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{Description: "Item A", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)},
		},
	})
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if !q.ExchangeRate.Equal(decimal.RequireFromString("1.33333333")) {
		t.Fatalf("expected quote exchange rate 1.33333333, got %s", q.ExchangeRate)
	}

	updated, err := UpdateQuote(db, cid, q.ID, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "USD",
		ExchangeRate: decimal.RequireFromString("1.44444444"),
		QuoteDate:    q.QuoteDate,
		Lines: []QuoteLineInput{
			{Description: "Item A", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)},
		},
	})
	if err != nil {
		t.Fatalf("UpdateQuote: %v", err)
	}
	if !updated.ExchangeRate.Equal(decimal.RequireFromString("1.44444444")) {
		t.Fatalf("expected updated quote exchange rate 1.44444444, got %s", updated.ExchangeRate)
	}
}

func TestPhase2_GetQuotePreloadsLineProductService(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)
	revenueID := p2RevenueAccount(t, db, cid)
	itemID := p2ProductService(t, db, cid, revenueID, "Implementation Service")

	q, err := CreateQuote(db, cid, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{
				ProductServiceID: &itemID,
				Description:      "Implementation work",
				Quantity:         decimal.NewFromInt(2),
				UnitPrice:        decimal.NewFromInt(150),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}

	got, err := GetQuote(db, cid, q.ID)
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}
	if len(got.Lines) != 1 {
		t.Fatalf("expected 1 line; got %d", len(got.Lines))
	}
	if got.Lines[0].ProductService == nil {
		t.Fatal("expected QuoteLine.ProductService to be preloaded")
	}
	if got.Lines[0].ProductService.Name != "Implementation Service" {
		t.Fatalf("ProductService.Name = %q", got.Lines[0].ProductService.Name)
	}
}

func TestPhase2_CreateQuote_NoCustomer(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	_, err := CreateQuote(db, cid, QuoteInput{
		Lines: []QuoteLineInput{{Description: "X", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)}},
	})
	if err == nil {
		t.Error("expected error for missing customer")
	}
}

func TestPhase2_CreateQuote_NoLines(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)
	_, err := CreateQuote(db, cid, QuoteInput{CustomerID: custID, QuoteDate: time.Now()})
	if err == nil {
		t.Error("expected error for missing lines")
	}
}

func TestPhase2_GetQuote_Isolation(t *testing.T) {
	db := phase2DB(t)
	cid1 := p2Company(t, db, "Co A")
	cid2 := p2Company(t, db, "Co B")
	custID := p2Customer(t, db, cid1)

	q, _ := CreateQuote(db, cid1, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{Description: "X", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)},
		},
	})

	// cid2 must not see cid1's quote.
	_, err := GetQuote(db, cid2, q.ID)
	if err == nil {
		t.Error("expected isolation error; cid2 should not see cid1 quote")
	}
	// cid1 should see its own quote.
	got, err := GetQuote(db, cid1, q.ID)
	if err != nil {
		t.Errorf("cid1 should see own quote: %v", err)
	}
	if got.ID != q.ID {
		t.Errorf("quote ID mismatch")
	}
}

func TestPhase2_UpdateQuote_DraftOnly(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)

	q, _ := CreateQuote(db, cid, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{Description: "X", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)},
		},
	})

	// Update draft — should succeed.
	updated, err := UpdateQuote(db, cid, q.ID, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{Description: "Y", Quantity: decimal.NewFromInt(3), UnitPrice: decimal.NewFromInt(50)},
		},
	})
	if err != nil {
		t.Fatalf("UpdateQuote on draft: %v", err)
	}
	if !updated.Total.Equal(decimal.NewFromInt(150)) {
		t.Errorf("expected total 150; got %s", updated.Total)
	}

	// Send the quote.
	if err := SendQuote(db, cid, q.ID, "actor"); err != nil {
		t.Fatalf("SendQuote: %v", err)
	}

	// Update sent quote — must fail.
	_, err = UpdateQuote(db, cid, q.ID, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{Description: "Z", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(1)},
		},
	})
	if err == nil {
		t.Error("expected error updating non-draft quote")
	}
}

// ── Quote status transitions ───────────────────────────────────────────────────

func TestPhase2_QuoteStatusFlow(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)

	newQuote := func() *models.Quote {
		q, err := CreateQuote(db, cid, QuoteInput{
			CustomerID:   custID,
			CurrencyCode: "CAD",
			QuoteDate:    time.Now(),
			Lines: []QuoteLineInput{
				{Description: "X", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)},
			},
		})
		if err != nil {
			t.Fatalf("CreateQuote: %v", err)
		}
		return q
	}

	// draft → sent → accepted
	t.Run("DraftSentAccepted", func(t *testing.T) {
		q := newQuote()
		if err := SendQuote(db, cid, q.ID, "actor"); err != nil {
			t.Fatalf("SendQuote: %v", err)
		}
		if err := AcceptQuote(db, cid, q.ID); err != nil {
			t.Fatalf("AcceptQuote: %v", err)
		}
		got, _ := GetQuote(db, cid, q.ID)
		if got.Status != models.QuoteStatusAccepted {
			t.Errorf("expected accepted; got %s", got.Status)
		}
	})

	// draft → sent → rejected
	t.Run("DraftSentRejected", func(t *testing.T) {
		q := newQuote()
		SendQuote(db, cid, q.ID, "actor")
		if err := RejectQuote(db, cid, q.ID); err != nil {
			t.Fatalf("RejectQuote: %v", err)
		}
		got, _ := GetQuote(db, cid, q.ID)
		if got.Status != models.QuoteStatusRejected {
			t.Errorf("expected rejected; got %s", got.Status)
		}
	})

	// draft → cancelled
	t.Run("DraftCancelled", func(t *testing.T) {
		q := newQuote()
		if err := CancelQuote(db, cid, q.ID); err != nil {
			t.Fatalf("CancelQuote: %v", err)
		}
		got, _ := GetQuote(db, cid, q.ID)
		if got.Status != models.QuoteStatusCancelled {
			t.Errorf("expected cancelled; got %s", got.Status)
		}
	})

	// invalid: accept draft (not sent)
	t.Run("InvalidAcceptDraft", func(t *testing.T) {
		q := newQuote()
		if err := AcceptQuote(db, cid, q.ID); err == nil {
			t.Error("expected error accepting draft quote")
		}
	})

	// invalid: cancel accepted quote
	t.Run("InvalidCancelAccepted", func(t *testing.T) {
		q := newQuote()
		SendQuote(db, cid, q.ID, "actor")
		AcceptQuote(db, cid, q.ID)
		if err := CancelQuote(db, cid, q.ID); err == nil {
			t.Error("expected error cancelling accepted quote")
		}
	})
}

// ── Quote → SalesOrder conversion ────────────────────────────────────────────

func TestPhase2_ConvertQuoteToSalesOrder(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)
	taxID := p2TaxCode(t, db, cid, 0.05)
	revenueID := p2RevenueAccount(t, db, cid)
	itemID := p2ProductService(t, db, cid, revenueID, "Consulting Service")

	q, _ := CreateQuote(db, cid, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		ExchangeRate: decimal.RequireFromString("1.25000000"),
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{
				ProductServiceID: &itemID,
				TaxCodeID:        &taxID,
				Description:      "Consulting",
				Quantity:         decimal.NewFromInt(10),
				UnitPrice:        decimal.NewFromInt(200),
			},
		},
	})

	// Accept the quote.
	SendQuote(db, cid, q.ID, "actor")
	AcceptQuote(db, cid, q.ID)

	so, err := ConvertQuoteToSalesOrder(db, cid, q.ID, "actor", nil)
	if err != nil {
		t.Fatalf("ConvertQuoteToSalesOrder: %v", err)
	}

	// SalesOrder should mirror Quote totals.
	if !so.Total.Equal(q.Total) {
		t.Errorf("SO.Total %s ≠ Quote.Total %s", so.Total, q.Total)
	}
	if !so.ExchangeRate.Equal(decimal.RequireFromString("1.25000000")) {
		t.Errorf("SO.ExchangeRate %s does not mirror Quote.ExchangeRate", so.ExchangeRate)
	}
	if so.Status != models.SalesOrderStatusDraft {
		t.Errorf("SO should be draft; got %s", so.Status)
	}
	if so.QuoteID == nil || *so.QuoteID != q.ID {
		t.Errorf("SO.QuoteID not linked to quote")
	}
	if so.OrderNumber == "" {
		t.Error("SO.OrderNumber must be set")
	}
	if len(so.Lines) != 1 {
		t.Fatalf("expected 1 SO line; got %d", len(so.Lines))
	}
	if so.Lines[0].ProductServiceID == nil || *so.Lines[0].ProductServiceID != itemID {
		t.Fatalf("SO line ProductServiceID = %v, want %d", so.Lines[0].ProductServiceID, itemID)
	}

	// Quote should now be converted with SalesOrderID set.
	reloaded, _ := GetQuote(db, cid, q.ID)
	if reloaded.Status != models.QuoteStatusConverted {
		t.Errorf("Quote should be converted; got %s", reloaded.Status)
	}
	if reloaded.SalesOrderID == nil || *reloaded.SalesOrderID != so.ID {
		t.Errorf("Quote.SalesOrderID not set to SO.ID")
	}

	// No JE should exist.
	var count int64
	db.Model(&models.JournalEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 JEs after conversion; got %d", count)
	}
}

func TestPhase2_ConvertQuote_InvalidStatus(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)

	// Draft quote — cannot convert.
	q, _ := CreateQuote(db, cid, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{Description: "X", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)},
		},
	})
	_, err := ConvertQuoteToSalesOrder(db, cid, q.ID, "actor", nil)
	if err == nil {
		t.Error("expected error converting draft quote")
	}

	// Rejected quote — cannot convert.
	SendQuote(db, cid, q.ID, "actor")
	RejectQuote(db, cid, q.ID)
	_, err = ConvertQuoteToSalesOrder(db, cid, q.ID, "actor", nil)
	if err == nil {
		t.Error("expected error converting rejected quote")
	}
}

func TestPhase2_ConvertQuote_Sent(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)

	// Sent quote (without Accept) can also be converted.
	q, _ := CreateQuote(db, cid, QuoteInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		QuoteDate:    time.Now(),
		Lines: []QuoteLineInput{
			{Description: "X", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)},
		},
	})
	SendQuote(db, cid, q.ID, "actor")
	_, err := ConvertQuoteToSalesOrder(db, cid, q.ID, "actor", nil)
	if err != nil {
		t.Errorf("expected sent quote to be convertible; got: %v", err)
	}
}

// ── SalesOrder CRUD ───────────────────────────────────────────────────────────

func TestPhase2_CreateSalesOrder(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)

	so, err := CreateSalesOrder(db, cid, SalesOrderInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		OrderDate:    time.Now(),
		Lines: []SalesOrderLineInput{
			{Description: "Widget", Quantity: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(40)},
		},
	})
	if err != nil {
		t.Fatalf("CreateSalesOrder: %v", err)
	}
	if so.Status != models.SalesOrderStatusDraft {
		t.Errorf("expected draft; got %s", so.Status)
	}
	if !so.Total.Equal(decimal.NewFromInt(200)) {
		t.Errorf("expected total 200; got %s", so.Total)
	}
	if so.OrderNumber == "" {
		t.Error("OrderNumber must be set")
	}
}

func TestPhase2_SalesOrderStatusFlow(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)

	newSO := func() *models.SalesOrder {
		so, err := CreateSalesOrder(db, cid, SalesOrderInput{
			CustomerID:   custID,
			CurrencyCode: "CAD",
			OrderDate:    time.Now(),
			Lines: []SalesOrderLineInput{
				{Description: "X", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)},
			},
		})
		if err != nil {
			t.Fatalf("CreateSalesOrder: %v", err)
		}
		return so
	}

	// draft → confirmed
	t.Run("Confirm", func(t *testing.T) {
		so := newSO()
		if err := ConfirmSalesOrder(db, cid, so.ID); err != nil {
			t.Fatalf("ConfirmSalesOrder: %v", err)
		}
		got, _ := GetSalesOrder(db, cid, so.ID)
		if got.Status != models.SalesOrderStatusConfirmed {
			t.Errorf("expected confirmed; got %s", got.Status)
		}
		if got.ConfirmedAt == nil {
			t.Error("ConfirmedAt must be set")
		}
	})

	// draft → cancelled
	t.Run("CancelDraft", func(t *testing.T) {
		so := newSO()
		if err := CancelSalesOrder(db, cid, so.ID); err != nil {
			t.Fatalf("CancelSalesOrder: %v", err)
		}
		got, _ := GetSalesOrder(db, cid, so.ID)
		if got.Status != models.SalesOrderStatusCancelled {
			t.Errorf("expected cancelled; got %s", got.Status)
		}
	})

	// confirmed → cancelled
	t.Run("CancelConfirmed", func(t *testing.T) {
		so := newSO()
		ConfirmSalesOrder(db, cid, so.ID)
		if err := CancelSalesOrder(db, cid, so.ID); err != nil {
			t.Fatalf("CancelSalesOrder confirmed: %v", err)
		}
	})

	// invalid: confirm already confirmed
	t.Run("DoubleConfirm", func(t *testing.T) {
		so := newSO()
		ConfirmSalesOrder(db, cid, so.ID)
		if err := ConfirmSalesOrder(db, cid, so.ID); err == nil {
			t.Error("expected error double-confirming")
		}
	})
}

func TestPhase2_SalesOrder_Isolation(t *testing.T) {
	db := phase2DB(t)
	cid1 := p2Company(t, db, "Co A")
	cid2 := p2Company(t, db, "Co B")
	custID := p2Customer(t, db, cid1)

	so, _ := CreateSalesOrder(db, cid1, SalesOrderInput{
		CustomerID:   custID,
		CurrencyCode: "CAD",
		OrderDate:    time.Now(),
		Lines: []SalesOrderLineInput{
			{Description: "X", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)},
		},
	})

	_, err := GetSalesOrder(db, cid2, so.ID)
	if err == nil {
		t.Error("expected isolation error; cid2 should not see cid1 sales order")
	}
	_, err = GetSalesOrder(db, cid1, so.ID)
	if err != nil {
		t.Errorf("cid1 should see own sales order: %v", err)
	}
}

// ── Document numbering ────────────────────────────────────────────────────────

func TestPhase2_DocumentNumbering(t *testing.T) {
	db := phase2DB(t)
	cid := p2Company(t, db, "Test Co")
	custID := p2Customer(t, db, cid)

	makeQuote := func() *models.Quote {
		q, err := CreateQuote(db, cid, QuoteInput{
			CustomerID:   custID,
			CurrencyCode: "CAD",
			QuoteDate:    time.Now(),
			Lines: []QuoteLineInput{
				{Description: "X", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)},
			},
		})
		if err != nil {
			t.Fatalf("CreateQuote: %v", err)
		}
		return q
	}

	q1 := makeQuote()
	q2 := makeQuote()
	q3 := makeQuote()

	if q1.QuoteNumber == q2.QuoteNumber || q2.QuoteNumber == q3.QuoteNumber {
		t.Errorf("QuoteNumbers must be unique; got %s, %s, %s", q1.QuoteNumber, q2.QuoteNumber, q3.QuoteNumber)
	}
	if q1.QuoteNumber != "QUO-0001" {
		t.Errorf("first QuoteNumber should be QUO-0001; got %s", q1.QuoteNumber)
	}
	if q2.QuoteNumber != "QUO-0002" {
		t.Errorf("second QuoteNumber should be QUO-0002; got %s", q2.QuoteNumber)
	}
}
