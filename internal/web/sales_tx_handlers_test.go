package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func TestSalesTransactionsPageMountsReactIslandWithFallback(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Sales TX React Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	app := testRouteApp(t, db)
	resp := performRequest(t, app, "/sales-transactions", rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	for _, want := range []string{
		`id="sales-transactions-island"`,
		`data-gb-react="sales-transactions"`,
		`data-api-url="/api/sales-transactions`,
		`/static/react/sales_transactions.js?v=1`,
		`No transactions match your filters.`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected sales transactions page to contain %q", want)
		}
	}
}

func TestSalesTransactionsAPIReturnsCompanyScopedRows(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Sales TX API Co")
	otherCompanyID := seedCompany(t, db, "Sales TX API Other Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	customerID := seedValidationCustomer(t, db, companyID, "API Customer")
	otherCustomerID := seedValidationCustomer(t, db, otherCompanyID, "Other API Customer")
	seedSalesTxInvoice(t, db, companyID, customerID, "INV-API-001", "123.45")
	seedSalesTxInvoice(t, db, otherCompanyID, otherCustomerID, "INV-OTHER-001", "999.99")

	app := testRouteApp(t, db)
	resp := performRequest(t, app, "/api/sales-transactions?type=invoices&sort=amount&dir=desc", rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}

	var body struct {
		Rows []struct {
			Key          string `json:"key"`
			Type         string `json:"type"`
			Number       string `json:"number"`
			CustomerName string `json:"customer_name"`
			Amount       string `json:"amount"`
			DetailURL    string `json:"detail_url"`
		} `json:"rows"`
		Total     int    `json:"total"`
		RowsTotal string `json:"rows_total"`
		SortBy    string `json:"sort_by"`
		SortDir   string `json:"sort_dir"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	if body.Total != 1 || len(body.Rows) != 1 {
		t.Fatalf("expected one company-scoped row, got total=%d rows=%d", body.Total, len(body.Rows))
	}
	row := body.Rows[0]
	if row.Type != "invoice" || row.Number != "INV-API-001" || row.CustomerName != "API Customer" {
		t.Fatalf("unexpected row payload: %+v", row)
	}
	if row.Amount != "123.45" || body.RowsTotal != "123.45" {
		t.Fatalf("unexpected amount payload row=%q total=%q", row.Amount, body.RowsTotal)
	}
	if row.Key == "" || row.DetailURL == "" {
		t.Fatalf("expected stable key and detail URL, got %+v", row)
	}
	if body.SortBy != "amount" || body.SortDir != "desc" {
		t.Fatalf("expected sort echo amount/desc, got %s/%s", body.SortBy, body.SortDir)
	}
}

func seedSalesTxInvoice(t *testing.T, db *gorm.DB, companyID, customerID uint, number, amount string) {
	t.Helper()

	date := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	row := models.Invoice{
		CompanyID:      companyID,
		InvoiceNumber:  number,
		CustomerID:     customerID,
		InvoiceDate:    date,
		Status:         models.InvoiceStatusIssued,
		Amount:         decimal.RequireFromString(amount),
		BalanceDue:     decimal.RequireFromString(amount),
		CurrencyCode:   "CAD",
		ExchangeRate:   decimal.NewFromInt(1),
		AmountBase:     decimal.RequireFromString(amount),
		BalanceDueBase: decimal.RequireFromString(amount),
		Memo:           "API fixture",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}
