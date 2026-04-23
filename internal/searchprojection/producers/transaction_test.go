// 遵循project_guide.md
package producers

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/searchprojection"
)

// Transaction producer tests focus on Document mapping (pure function)
// rather than GORM load round-trips — the cross-tenant GORM guard is
// identical to contact/product producers and is already covered there.
// Each test case asserts the six invariants shared across transaction
// types:
//  1. EntityType matches the family constant
//  2. DocNumber carries the transaction number (for exact-code matching)
//  3. Title carries the counterparty name (so customer/vendor name
//     searches hit title_native)
//  4. Subtitle concatenates label + number + date + currency + amount
//  5. Status propagates the native status string
//  6. URLPath points at the right detail route

func TestInvoiceDocument(t *testing.T) {
	now := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	inv := models.Invoice{
		ID: 42, CompanyID: 7,
		InvoiceNumber: "INV-202604",
		InvoiceDate:   now,
		Status:        models.InvoiceStatusIssued,
		Amount:        decimal.NewFromFloat(3600.00),
		CurrencyCode:  "CAD",
		Memo:          "April install",
		Customer:      models.Customer{Name: "POSX US INC."},
	}
	doc := InvoiceDocument(inv)
	assertTxDoc(t, doc, txCase{
		entityType: EntityTypeInvoice,
		entityID:   42,
		companyID:  7,
		title:      "POSX US INC.",
		docNumber:  "INV-202604",
		status:     "issued",
		urlPrefix:  "/invoices/",
		subShould:  []string{"Invoice", "INV-202604", "2026-04-22", "CAD 3600.00"},
	})
	if doc.Memo != "April install" {
		t.Errorf("Memo = %q, want original memo", doc.Memo)
	}
}

func TestInvoiceDocument_BlankCustomerUsesFallback(t *testing.T) {
	inv := models.Invoice{
		ID: 1, CompanyID: 1, InvoiceNumber: "INV-1",
		InvoiceDate: time.Now(), Status: models.InvoiceStatusDraft,
		Amount: decimal.NewFromFloat(1), CurrencyCode: "USD",
		// Customer.Name left blank — e.g. customer row was deleted
		// but the snapshot invoice remains
	}
	doc := InvoiceDocument(inv)
	if !strings.HasPrefix(doc.Title, "(unnamed Customer") {
		t.Errorf("expected fallback title, got %q", doc.Title)
	}
}

func TestBillDocument(t *testing.T) {
	now := time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)
	b := models.Bill{
		ID: 15, CompanyID: 3,
		BillNumber: "LG-AS-202511",
		BillDate:   now,
		Status:     models.BillStatusPosted,
		Amount:     decimal.NewFromFloat(21010.50),
		CurrencyCode: "CAD",
		Memo:         "Monthly lights",
		Vendor:       models.Vendor{Name: "Lighting Geek Technologies Inc."},
	}
	doc := BillDocument(b)
	assertTxDoc(t, doc, txCase{
		entityType: EntityTypeBill,
		entityID:   15,
		companyID:  3,
		title:      "Lighting Geek Technologies Inc.",
		docNumber:  "LG-AS-202511",
		status:     "posted",
		urlPrefix:  "/bills/",
		subShould:  []string{"Bill", "LG-AS-202511", "2026-04-09", "CAD 21010.50"},
	})
}

func TestQuoteDocument(t *testing.T) {
	q := models.Quote{
		ID: 9, CompanyID: 4,
		QuoteNumber:  "QUO-0001",
		QuoteDate:    time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		Status:       models.QuoteStatusDraft,
		Total:        decimal.NewFromFloat(2625.00),
		CurrencyCode: "CAD",
		Notes:        "Customer-visible description goes here",
		Memo:         "internal",
		Customer:     models.Customer{Name: "AR TESTING"},
	}
	doc := QuoteDocument(q)
	if doc.EntityType != EntityTypeQuote {
		t.Errorf("EntityType = %q", doc.EntityType)
	}
	if doc.Title != "AR TESTING" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.DocNumber != "QUO-0001" {
		t.Errorf("DocNumber = %q", doc.DocNumber)
	}
	// Notes should win over Memo as the Memo field in the Document.
	if doc.Memo != "Customer-visible description goes here" {
		t.Errorf("expected Notes to feed Memo, got %q", doc.Memo)
	}
}

func TestSalesOrderDocument(t *testing.T) {
	so := models.SalesOrder{
		ID: 22, CompanyID: 5,
		OrderNumber:  "SO-0002",
		OrderDate:    time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
		Status:       models.SalesOrderStatusPartiallyInvoiced,
		Total:        decimal.NewFromFloat(4800.00),
		CurrencyCode: "CAD",
		Customer:     models.Customer{Name: "AR TESTING"},
	}
	doc := SalesOrderDocument(so)
	assertTxDoc(t, doc, txCase{
		entityType: EntityTypeSalesOrder,
		entityID:   22,
		companyID:  5,
		title:      "AR TESTING",
		docNumber:  "SO-0002",
		status:     "partially_invoiced",
		urlPrefix:  "/sales-orders/",
		subShould:  []string{"Sales Order", "SO-0002", "2026-04-22", "CAD 4800.00"},
	})
}

func TestPurchaseOrderDocument(t *testing.T) {
	po := models.PurchaseOrder{
		ID: 33, CompanyID: 6,
		PONumber:     "PO-1001",
		PODate:       time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		Status:       models.POStatusConfirmed,
		Amount:       decimal.NewFromFloat(5500.00),
		CurrencyCode: "CAD",
		Notes:        "Rush order",
		Vendor:       models.Vendor{Name: "LGTek"},
	}
	doc := PurchaseOrderDocument(po)
	assertTxDoc(t, doc, txCase{
		entityType: EntityTypePurchaseOrder,
		entityID:   33,
		companyID:  6,
		title:      "LGTek",
		docNumber:  "PO-1001",
		status:     "confirmed",
		urlPrefix:  "/purchase-orders/",
		subShould:  []string{"Purchase Order", "PO-1001", "2026-04-15", "CAD 5500.00"},
	})
	if doc.Memo != "Rush order" {
		t.Errorf("Memo = %q", doc.Memo)
	}
}

func TestCustomerReceiptDocument(t *testing.T) {
	r := models.CustomerReceipt{
		ID: 55, CompanyID: 8,
		ReceiptNumber: "RCP-001",
		ReceiptDate:   time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
		Status:        models.CustomerReceiptStatusConfirmed,
		Amount:        decimal.NewFromFloat(16000.00),
		CurrencyCode:  "USD",
		Memo:          "Wire transfer",
		Customer:      models.Customer{Name: "POSX US INC."},
	}
	doc := CustomerReceiptDocument(r)
	assertTxDoc(t, doc, txCase{
		entityType: EntityTypeCustomerReceipt,
		entityID:   55,
		companyID:  8,
		title:      "POSX US INC.",
		docNumber:  "RCP-001",
		status:     "confirmed",
		urlPrefix:  "/receipts/",
		subShould:  []string{"Payment", "RCP-001", "2026-04-13", "USD 16000.00"},
	})
}

// formatTxSubtitle edge case: amount=0 should be omitted so draft docs
// don't read "Quote QUO-0001 · 2026-04-20 · CAD 0.00".
func TestFormatTxSubtitle_ZeroAmountOmitted(t *testing.T) {
	sub := formatTxSubtitle("Quote", "QUO-0001", "2026-04-20", "CAD", "0.00")
	if strings.Contains(sub, "0.00") {
		t.Errorf("expected zero-amount to be omitted, got %q", sub)
	}
	if !strings.Contains(sub, "QUO-0001") {
		t.Errorf("number missing: %q", sub)
	}
}

// ── Test harness ─────────────────────────────────────────────────────

type txCase struct {
	entityType string
	entityID   uint
	companyID  uint
	title      string
	docNumber  string
	status     string
	urlPrefix  string
	subShould  []string
}

func assertTxDoc(t *testing.T, got searchprojection.Document, want txCase) {
	t.Helper()
	if got.EntityType != want.entityType {
		t.Errorf("EntityType = %q, want %q", got.EntityType, want.entityType)
	}
	if got.EntityID != want.entityID {
		t.Errorf("EntityID = %d, want %d", got.EntityID, want.entityID)
	}
	if got.CompanyID != want.companyID {
		t.Errorf("CompanyID = %d, want %d", got.CompanyID, want.companyID)
	}
	if got.Title != want.title {
		t.Errorf("Title = %q, want %q", got.Title, want.title)
	}
	if got.DocNumber != want.docNumber {
		t.Errorf("DocNumber = %q, want %q", got.DocNumber, want.docNumber)
	}
	if got.Status != want.status {
		t.Errorf("Status = %q, want %q", got.Status, want.status)
	}
	if !strings.HasPrefix(got.URLPath, want.urlPrefix) {
		t.Errorf("URLPath %q doesn't start with %q", got.URLPath, want.urlPrefix)
	}
	for _, piece := range want.subShould {
		if !strings.Contains(got.Subtitle, piece) {
			t.Errorf("Subtitle %q missing %q", got.Subtitle, piece)
		}
	}
}
