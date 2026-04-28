// 遵循project_guide.md
package services

// fragment_builder_test.go — pure unit tests for BuildInvoiceFragments and
// BuildBillFragments (fragment_builder.go).
//
// These tests have no database dependency. They construct model instances in
// memory, call the pure fragment-building functions, and assert on the
// resulting []PostingFragment — both before and after AggregateJournalLines.
//
// Coverage:
//
//   Invoice:
//     • Multiple lines with different revenue accounts → each gets its own credit.
//     • Two lines pointing to the same revenue account → merge after aggregation.
//     • Two lines with the same tax code → tax credits merge after aggregation.
//     • Missing ProductService → error returned.
//
//   Bill:
//     • Full-recovery tax → ITC debit kept separate; expense debit = net only.
//     • Non-recoverable tax → no ITC line; full tax embedded in expense debit.
//     • Partial-recovery tax → ITC debit = recoverable portion; expense = net + non-rec.
//     • Missing ExpenseAccountID → error returned.

import (
	"testing"

	"balanciz/internal/models"
)

// ── Invoice helpers ───────────────────────────────────────────────────────────

// ps builds an in-memory ProductService with a revenue account for fragment tests.
func ps(companyID, revenueAcctID uint) *models.ProductService {
	return &models.ProductService{
		CompanyID:        companyID,
		RevenueAccountID: revenueAcctID,
		IsActive:         true,
		RevenueAccount:   models.Account{ID: revenueAcctID, CompanyID: companyID},
	}
}

// invLine builds an InvoiceLine with a preloaded ProductService and optional TaxCode.
// When tc is provided, LineTax is computed as net × tc.Rate (banker's rounding, 2 dp),
// matching what the draft-save handler stores.  BuildInvoiceFragments uses the stored
// LineTax as the single tax truth (Method A), so tests must set it to get tax fragments.
func invLine(svc *models.ProductService, net string, tc *models.TaxCode) models.InvoiceLine {
	l := models.InvoiceLine{
		ProductServiceID: &svc.ID,
		ProductService:   svc,
		LineNet:          d(net),
		Description:      "line",
	}
	if tc != nil {
		l.TaxCodeID = &tc.ID
		l.TaxCode = tc
		// Simulate what handleInvoiceSaveDraft stores.
		l.LineTax = d(net).Mul(tc.Rate).RoundBank(2)
	}
	return l
}

// ── Invoice — multiple lines, different revenue accounts ──────────────────────

func TestBuildInvoiceFragments_MultiLine_DifferentRevenueAccounts(t *testing.T) {
	// Two lines posting to distinct revenue accounts: 4000 and 4100.
	// Expected raw fragments: 1 AR debit + 2 revenue credits.
	// After aggregation: still 3 lines (different accounts → no merge).
	svc1 := ps(1, 4000)
	svc2 := ps(1, 4100)
	inv := models.Invoice{
		Amount: d("1000.00"),
		Lines: []models.InvoiceLine{
			invLine(svc1, "600.00", nil),
			invLine(svc2, "400.00", nil),
		},
	}

	frags, err := BuildInvoiceFragments(inv, 1100)
	if err != nil {
		t.Fatalf("BuildInvoiceFragments: %v", err)
	}
	if len(frags) != 3 {
		t.Fatalf("want 3 raw fragments (1 AR + 2 revenue), got %d", len(frags))
	}

	// AR debit must equal the invoice gross total.
	var arFrag *PostingFragment
	for i := range frags {
		if frags[i].AccountID == 1100 {
			arFrag = &frags[i]
		}
	}
	if arFrag == nil || !arFrag.Debit.Equal(d("1000.00")) {
		t.Fatalf("AR debit fragment: %+v", arFrag)
	}

	agg, err := AggregateJournalLines(frags)
	if err != nil {
		t.Fatalf("AggregateJournalLines: %v", err)
	}
	// Different revenue accounts → no merge → still 3 lines.
	if len(agg) != 3 {
		t.Fatalf("want 3 aggregated lines, got %d: %+v", len(agg), agg)
	}

	// Double-entry balance.
	if !sumPostingDebits(agg).Equal(sumPostingCredits(agg)) {
		t.Fatalf("imbalance: debits=%s credits=%s",
			sumPostingDebits(agg), sumPostingCredits(agg))
	}
}

// ── Invoice — same revenue account merges ─────────────────────────────────────

func TestBuildInvoiceFragments_SameRevenueAccountMerges(t *testing.T) {
	// Two lines both posting to revenue account 4000.
	// Raw: 1 AR debit + 2 revenue credits (same account).
	// After aggregation: 1 AR + 1 merged revenue = 2 lines.
	svc := ps(1, 4000)
	inv := models.Invoice{
		Amount: d("1000.00"),
		Lines: []models.InvoiceLine{
			invLine(svc, "600.00", nil),
			invLine(svc, "400.00", nil),
		},
	}

	frags, err := BuildInvoiceFragments(inv, 1100)
	if err != nil {
		t.Fatalf("BuildInvoiceFragments: %v", err)
	}

	agg, err := AggregateJournalLines(frags)
	if err != nil {
		t.Fatalf("AggregateJournalLines: %v", err)
	}
	if len(agg) != 2 {
		t.Fatalf("want 2 lines (AR + merged revenue), got %d: %+v", len(agg), agg)
	}

	// Merged revenue credit must equal the sum of both line nets.
	var rev *PostingFragment
	for i := range agg {
		if agg[i].AccountID == 4000 {
			rev = &agg[i]
		}
	}
	if rev == nil || !rev.Credit.Equal(d("1000.00")) {
		t.Fatalf("merged revenue credit: %+v", rev)
	}
}

// ── Invoice — same tax payable account merges ─────────────────────────────────

func TestBuildInvoiceFragments_SameTaxAccountMerges(t *testing.T) {
	// Two lines, same revenue account + same 5% GST code.
	// net = $500 per line, tax = $25 per line.
	// Raw: 1 AR + 2 revenue + 2 tax = 5 fragments.
	// After aggregation: 1 AR + 1 merged revenue + 1 merged tax = 3 lines.
	gst := taxCode("0.05", models.TaxScopeSales, models.TaxRecoveryNone, "0", 2300, 0)
	gst.ID = 1 // needs non-zero so &gst.ID is a valid pointer
	svc := ps(1, 4000)

	inv := models.Invoice{
		Amount: d("1050.00"), // 2×$500 net + 2×$25 tax
		Lines: []models.InvoiceLine{
			invLine(svc, "500.00", &gst),
			invLine(svc, "500.00", &gst),
		},
	}

	frags, err := BuildInvoiceFragments(inv, 1100)
	if err != nil {
		t.Fatalf("BuildInvoiceFragments: %v", err)
	}
	if len(frags) != 5 {
		t.Fatalf("want 5 raw fragments, got %d: %+v", len(frags), frags)
	}

	agg, err := AggregateJournalLines(frags)
	if err != nil {
		t.Fatalf("AggregateJournalLines: %v", err)
	}
	if len(agg) != 3 {
		t.Fatalf("want 3 aggregated lines, got %d: %+v", len(agg), agg)
	}

	// Merged tax credit = 2 × $25 = $50.
	var taxFrag *PostingFragment
	for i := range agg {
		if agg[i].AccountID == 2300 {
			taxFrag = &agg[i]
		}
	}
	if taxFrag == nil || !taxFrag.Credit.Equal(d("50.00")) {
		t.Fatalf("merged tax credit: %+v", taxFrag)
	}

	// Balance check: total debits = total credits = $1050.
	if !sumPostingDebits(agg).Equal(d("1050.00")) {
		t.Fatalf("total debits = %s, want 1050.00", sumPostingDebits(agg))
	}
	if !sumPostingCredits(agg).Equal(d("1050.00")) {
		t.Fatalf("total credits = %s, want 1050.00", sumPostingCredits(agg))
	}
}

// ── Invoice — error: missing ProductService ───────────────────────────────────

func TestBuildInvoiceFragments_ErrorMissingProductService(t *testing.T) {
	inv := models.Invoice{
		Amount: d("100.00"),
		Lines: []models.InvoiceLine{
			{LineNet: d("100.00"), Description: "orphan line"},
			// ProductService intentionally nil
		},
	}
	_, err := BuildInvoiceFragments(inv, 1100)
	if err == nil {
		t.Fatal("expected error for missing ProductService, got nil")
	}
}

// ── Bill helpers ──────────────────────────────────────────────────────────────

// billLine builds a BillLine with the given expense account and optional TaxCode.
func billLine(expenseAcctID uint, net string, tc *models.TaxCode) models.BillLine {
	l := models.BillLine{
		ExpenseAccountID: &expenseAcctID,
		LineNet:          d(net),
		Description:      "expense",
	}
	if tc != nil {
		l.TaxCodeID = &tc.ID
		l.TaxCode = tc
	}
	return l
}

// ── Bill — full-recovery tax posts ITC debit separately ──────────────────────

func TestBuildBillFragments_FullRecoverableTax(t *testing.T) {
	// $1 000 net, 13% HST, full recovery.
	// Expected: DR Expense 1000, DR ITC 130, CR AP 1130.
	itcAcct := uint(1320)
	hst := taxCode("0.13", models.TaxScopePurchase, models.TaxRecoveryFull, "100", 0, itcAcct)
	hst.ID = 1

	bill := models.Bill{
		BillNumber: "BILL-001",
		Lines:      []models.BillLine{billLine(6100, "1000.00", &hst)},
	}

	frags, err := BuildBillFragments(bill, 2000)
	if err != nil {
		t.Fatalf("BuildBillFragments: %v", err)
	}
	// Raw: DR expense 1000, DR ITC 130, CR AP 1130 = 3 fragments.
	if len(frags) != 3 {
		t.Fatalf("want 3 fragments, got %d: %+v", len(frags), frags)
	}

	agg, err := AggregateJournalLines(frags)
	if err != nil {
		t.Fatalf("AggregateJournalLines: %v", err)
	}
	if len(agg) != 3 {
		t.Fatalf("want 3 aggregated lines, got %d: %+v", len(agg), agg)
	}

	var expFrag, itcFrag, apFrag *PostingFragment
	for i := range agg {
		switch agg[i].AccountID {
		case 6100:
			expFrag = &agg[i]
		case itcAcct:
			itcFrag = &agg[i]
		case 2000:
			apFrag = &agg[i]
		}
	}

	// Expense debit = net only (tax is fully recovered, not embedded).
	if expFrag == nil || !expFrag.Debit.Equal(d("1000.00")) {
		t.Fatalf("expense debit: %+v", expFrag)
	}
	// ITC debit = full tax amount.
	if itcFrag == nil || !itcFrag.Debit.Equal(d("130.00")) {
		t.Fatalf("ITC debit: %+v", itcFrag)
	}
	// AP credit = gross payable.
	if apFrag == nil || !apFrag.Credit.Equal(d("1130.00")) {
		t.Fatalf("AP credit: %+v", apFrag)
	}

	// Balance.
	if !sumPostingDebits(agg).Equal(sumPostingCredits(agg)) {
		t.Fatalf("imbalance: debits=%s credits=%s",
			sumPostingDebits(agg), sumPostingCredits(agg))
	}
}

// ── Bill — non-recoverable tax is embedded in expense debit ──────────────────

func TestBuildBillFragments_NonRecoverableTaxEmbeddedInExpense(t *testing.T) {
	// $1 000 net, 13% HST, no recovery.
	// Expected: DR Expense 1130 (net + full tax), CR AP 1130. No ITC line.
	hst := taxCode("0.13", models.TaxScopePurchase, models.TaxRecoveryNone, "0", 0, 0)
	hst.ID = 1

	bill := models.Bill{
		BillNumber: "BILL-002",
		Lines:      []models.BillLine{billLine(6100, "1000.00", &hst)},
	}

	frags, err := BuildBillFragments(bill, 2000)
	if err != nil {
		t.Fatalf("BuildBillFragments: %v", err)
	}
	// Raw: DR expense 1130, CR AP 1130 = 2 fragments (no ITC).
	if len(frags) != 2 {
		t.Fatalf("want 2 fragments (no ITC), got %d: %+v", len(frags), frags)
	}

	agg, err := AggregateJournalLines(frags)
	if err != nil {
		t.Fatalf("AggregateJournalLines: %v", err)
	}

	var expFrag, apFrag *PostingFragment
	for i := range agg {
		switch agg[i].AccountID {
		case 6100:
			expFrag = &agg[i]
		case 2000:
			apFrag = &agg[i]
		}
	}

	// Expense debit = net + full tax (non-recoverable embedded).
	if expFrag == nil || !expFrag.Debit.Equal(d("1130.00")) {
		t.Fatalf("expense debit (want 1130): %+v", expFrag)
	}
	if apFrag == nil || !apFrag.Credit.Equal(d("1130.00")) {
		t.Fatalf("AP credit: %+v", apFrag)
	}
}

// ── Bill — partial recovery splits tax across ITC and expense ─────────────────

func TestBuildBillFragments_PartialRecovery(t *testing.T) {
	// $1 000 net, 13% HST, 50% recoverable.
	// tax = 130; recoverable = 65, non-recoverable = 65.
	// Expected: DR Expense 1065 (1000 + 65), DR ITC 65, CR AP 1130.
	itcAcct := uint(1320)
	hst := taxCode("0.13", models.TaxScopePurchase, models.TaxRecoveryPartial, "50", 0, itcAcct)
	hst.ID = 1

	bill := models.Bill{
		BillNumber: "BILL-003",
		Lines:      []models.BillLine{billLine(6100, "1000.00", &hst)},
	}

	frags, err := BuildBillFragments(bill, 2000)
	if err != nil {
		t.Fatalf("BuildBillFragments: %v", err)
	}
	if len(frags) != 3 {
		t.Fatalf("want 3 fragments, got %d: %+v", len(frags), frags)
	}

	agg, err := AggregateJournalLines(frags)
	if err != nil {
		t.Fatalf("AggregateJournalLines: %v", err)
	}

	var expFrag, itcFrag, apFrag *PostingFragment
	for i := range agg {
		switch agg[i].AccountID {
		case 6100:
			expFrag = &agg[i]
		case itcAcct:
			itcFrag = &agg[i]
		case 2000:
			apFrag = &agg[i]
		}
	}

	if expFrag == nil || !expFrag.Debit.Equal(d("1065.00")) {
		t.Fatalf("expense debit (want 1065): %+v", expFrag)
	}
	if itcFrag == nil || !itcFrag.Debit.Equal(d("65.00")) {
		t.Fatalf("ITC debit (want 65): %+v", itcFrag)
	}
	if apFrag == nil || !apFrag.Credit.Equal(d("1130.00")) {
		t.Fatalf("AP credit: %+v", apFrag)
	}
}

// ── Bill — error: missing expense account ─────────────────────────────────────

func TestBuildBillFragments_ErrorMissingExpenseAccount(t *testing.T) {
	bill := models.Bill{
		BillNumber: "BILL-ERR",
		Lines: []models.BillLine{
			{LineNet: d("100.00"), Description: "no expense acct"},
			// ExpenseAccountID intentionally nil
		},
	}
	_, err := BuildBillFragments(bill, 2000)
	if err == nil {
		t.Fatal("expected error for missing ExpenseAccountID, got nil")
	}
}
