package services

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestNormalizeSalesTxSort(t *testing.T) {
	cases := []struct {
		sortBy  string
		sortDir string
		wantBy  string
		wantDir string
	}{
		{"", "", SalesTxSortDate, SalesTxSortDesc},
		{"amount", "", SalesTxSortAmount, SalesTxSortDesc},
		{"customer", "", SalesTxSortCustomer, SalesTxSortAsc},
		{"status", "desc", SalesTxSortStatus, SalesTxSortDesc},
		{"bogus", "asc", SalesTxSortDate, SalesTxSortDesc},
	}
	for _, tc := range cases {
		gotBy, gotDir := NormalizeSalesTxSort(tc.sortBy, tc.sortDir)
		if gotBy != tc.wantBy || gotDir != tc.wantDir {
			t.Fatalf("NormalizeSalesTxSort(%q,%q) = (%q,%q), want (%q,%q)",
				tc.sortBy, tc.sortDir, gotBy, gotDir, tc.wantBy, tc.wantDir)
		}
	}
}

func TestNormalizeSalesTxType(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "all"},
		{"all", "all"},
		{"invoices", SalesTxTypeInvoice},
		{"quotes", SalesTxTypeQuote},
		{"sales_orders", SalesTxTypeSalesOrder},
		{"payments", SalesTxTypePayment},
		{"credit_memos", SalesTxTypeCreditNote},
		{"returns", SalesTxTypeReturn},
		{"unbilled", SalesTxPseudoUnbilled},
		{"recently_paid", SalesTxPseudoRecentlyPaid},
		{"unknown", "unknown"},
	}
	for _, tc := range cases {
		if got := NormalizeSalesTxType(tc.raw); got != tc.want {
			t.Fatalf("NormalizeSalesTxType(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSortSalesTransactions(t *testing.T) {
	rows := []SalesTxRow{
		sortTxRow(1, SalesTxTypeInvoice, "2026-01-03", "INV-2", "Zulu", "issued", "50.00"),
		sortTxRow(2, SalesTxTypeQuote, "2026-01-01", "QUO-1", "Alpha", "draft", "200.00"),
		sortTxRow(3, SalesTxTypeInvoice, "2026-01-02", "INV-1", "Beta", "voided", "10.00"),
	}

	sortSalesTransactions(rows, SalesTxSortDate, SalesTxSortAsc)
	assertSalesTxOrder(t, rows, 2, 3, 1)

	sortSalesTransactions(rows, SalesTxSortAmount, SalesTxSortDesc)
	assertSalesTxOrder(t, rows, 2, 1, 3)

	sortSalesTransactions(rows, SalesTxSortType, SalesTxSortAsc)
	assertSalesTxOrder(t, rows, 1, 3, 2)

	sortSalesTransactions(rows, SalesTxSortNumber, SalesTxSortAsc)
	assertSalesTxOrder(t, rows, 3, 1, 2)

	sortSalesTransactions(rows, SalesTxSortCustomer, SalesTxSortAsc)
	assertSalesTxOrder(t, rows, 2, 3, 1)

	sortSalesTransactions(rows, SalesTxSortStatus, SalesTxSortAsc)
	assertSalesTxOrder(t, rows, 2, 1, 3)
}

func TestSortSalesTransactionsFallbackStable(t *testing.T) {
	rows := []SalesTxRow{
		sortTxRow(1, SalesTxTypeInvoice, "2026-01-01", "B", "Same", "issued", "10.00"),
		sortTxRow(3, SalesTxTypeInvoice, "2026-01-03", "A", "Same", "issued", "10.00"),
		sortTxRow(2, SalesTxTypeInvoice, "2026-01-02", "C", "Same", "issued", "10.00"),
	}

	sortSalesTransactions(rows, SalesTxSortAmount, SalesTxSortAsc)
	assertSalesTxOrder(t, rows, 3, 2, 1)
}

func sortTxRow(id uint, typ, date, number, customer, status, amount string) SalesTxRow {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	return SalesTxRow{
		ID:           id,
		Type:         typ,
		Date:         d,
		Number:       number,
		CustomerName: customer,
		Status:       status,
		Amount:       decimal.RequireFromString(amount),
	}
}

func assertSalesTxOrder(t *testing.T, rows []SalesTxRow, ids ...uint) {
	t.Helper()
	if len(rows) != len(ids) {
		t.Fatalf("row count = %d, want %d", len(rows), len(ids))
	}
	for i, want := range ids {
		if rows[i].ID != want {
			t.Fatalf("row %d id = %d, want %d; rows=%v", i, rows[i].ID, want, ids)
		}
	}
}
