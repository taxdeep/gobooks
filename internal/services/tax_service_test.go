// 遵循project_guide.md
package services

import (
	"testing"

	"balanciz/internal/models"

	"github.com/shopspring/decimal"
)

// d is a test helper to build a decimal.Decimal from a string without error handling noise.
func d(s string) decimal.Decimal {
	v, _ := decimal.NewFromString(s)
	return v
}

// taxCode builds a minimal TaxCode for table-driven tests.
// salesAcct and purchaseAcct are GL account IDs (0 = not set).
func taxCode(rate string, scope models.TaxScope, mode models.TaxRecoveryMode, recoveryPct string, salesAcct uint, purchaseAcct uint) models.TaxCode {
	tc := models.TaxCode{
		Rate:              d(rate),
		Scope:             scope,
		RecoveryMode:      mode,
		RecoveryRate:      d(recoveryPct),
		SalesTaxAccountID: salesAcct,
	}
	if purchaseAcct != 0 {
		tc.PurchaseRecoverableAccountID = &purchaseAcct
	}
	return tc
}

// ── ComputeTax ───────────────────────────────────────────────────────────────

func TestComputeTax_FullRecovery(t *testing.T) {
	tc := taxCode("0.05", models.TaxScopeBoth, models.TaxRecoveryFull, "100", 10, 20)
	got := ComputeTax(d("1000.00"), tc)

	assertDecimal(t, "TaxAmount", "50.00", got.TaxAmount)
	assertDecimal(t, "RecoverableAmount", "50.00", got.RecoverableAmount)
	assertDecimal(t, "NonRecoverableAmount", "0.00", got.NonRecoverableAmount)
}

func TestComputeTax_PartialRecovery(t *testing.T) {
	// 13% rate, 50% recoverable → tax = 130, recoverable = 65, non-recoverable = 65
	tc := taxCode("0.13", models.TaxScopeBoth, models.TaxRecoveryPartial, "50", 10, 20)
	got := ComputeTax(d("1000.00"), tc)

	assertDecimal(t, "TaxAmount", "130.00", got.TaxAmount)
	assertDecimal(t, "RecoverableAmount", "65.00", got.RecoverableAmount)
	assertDecimal(t, "NonRecoverableAmount", "65.00", got.NonRecoverableAmount)
}

func TestComputeTax_NoRecovery(t *testing.T) {
	tc := taxCode("0.05", models.TaxScopeBoth, models.TaxRecoveryNone, "0", 10, 0)
	got := ComputeTax(d("1000.00"), tc)

	assertDecimal(t, "TaxAmount", "50.00", got.TaxAmount)
	assertDecimal(t, "RecoverableAmount", "0.00", got.RecoverableAmount)
	assertDecimal(t, "NonRecoverableAmount", "50.00", got.NonRecoverableAmount)
}

func TestComputeTax_ZeroAmount(t *testing.T) {
	tc := taxCode("0.05", models.TaxScopeBoth, models.TaxRecoveryFull, "100", 10, 20)
	got := ComputeTax(decimal.Zero, tc)

	assertDecimal(t, "TaxAmount", "0", got.TaxAmount)
	assertDecimal(t, "RecoverableAmount", "0", got.RecoverableAmount)
	assertDecimal(t, "NonRecoverableAmount", "0", got.NonRecoverableAmount)
}

func TestComputeTax_ZeroRate(t *testing.T) {
	tc := taxCode("0", models.TaxScopeBoth, models.TaxRecoveryFull, "100", 10, 20)
	got := ComputeTax(d("500.00"), tc)

	assertDecimal(t, "TaxAmount", "0", got.TaxAmount)
}

func TestComputeLineTax_matches_ComputeTax(t *testing.T) {
	tc := taxCode("0.13", models.TaxScopeBoth, models.TaxRecoveryPartial, "50", 10, 20)
	net := d("1000.00")
	tr := ComputeTax(net, tc)
	lt := ComputeLineTax(net, tc)
	if !lt.NetAmount.Equal(net) {
		t.Fatalf("NetAmount")
	}
	if !lt.TaxAmount.Equal(tr.TaxAmount) || !lt.RecoverableTaxAmount.Equal(tr.RecoverableAmount) || !lt.NonRecoverableTaxAmount.Equal(tr.NonRecoverableAmount) {
		t.Fatalf("line tax %+v vs tax result %+v", lt, tr)
	}
	if !lt.AsTaxResult().TaxAmount.Equal(tr.TaxAmount) {
		t.Fatal("AsTaxResult")
	}
}

func TestComputeTax_PartialRecovery_Rounding(t *testing.T) {
	// 5% on $33.33 = $1.6665 → rounded to $1.67; 60% of $1.67 = $1.002 → $1.00; non-rec = $0.67
	tc := taxCode("0.05", models.TaxScopeBoth, models.TaxRecoveryPartial, "60", 10, 20)
	got := ComputeTax(d("33.33"), tc)

	assertDecimal(t, "TaxAmount", "1.67", got.TaxAmount)
	assertDecimal(t, "RecoverableAmount", "1.00", got.RecoverableAmount)
	assertDecimal(t, "NonRecoverableAmount", "0.67", got.NonRecoverableAmount)

	// RecoverableAmount + NonRecoverableAmount must equal TaxAmount.
	sum := got.RecoverableAmount.Add(got.NonRecoverableAmount)
	if !sum.Equal(got.TaxAmount) {
		t.Errorf("recoverable + non-recoverable = %s, want %s", sum, got.TaxAmount)
	}
}

// ── SalesTaxPostingLine ──────────────────────────────────────────────────────

func TestSalesTaxPostingLine(t *testing.T) {
	tc := taxCode("0.05", models.TaxScopeSales, models.TaxRecoveryNone, "0", 99, 0)
	result := ComputeTax(d("200.00"), tc)
	line := SalesTaxPostingLine(result, tc)

	if line.AccountID != 99 {
		t.Errorf("AccountID = %d, want 99", line.AccountID)
	}
	assertDecimal(t, "Amount", "10.00", line.Amount)
}

// ── PurchaseTaxPostingLines ──────────────────────────────────────────────────

func TestPurchaseTaxPostingLines_FullRecovery(t *testing.T) {
	tc := taxCode("0.05", models.TaxScopePurchase, models.TaxRecoveryFull, "100", 10, 55)
	result := ComputeTax(d("1000.00"), tc)
	rec, nonRec := PurchaseTaxPostingLines(result, tc)

	// Full recovery: entire $50 goes to ITC receivable account.
	if rec.AccountID != 55 {
		t.Errorf("recoverable AccountID = %d, want 55", rec.AccountID)
	}
	assertDecimal(t, "recoverable Amount", "50.00", rec.Amount)

	// Non-recoverable amount is zero, but line is still returned.
	assertDecimal(t, "non-recoverable Amount", "0.00", nonRec.Amount)
}

func TestPurchaseTaxPostingLines_PartialRecovery(t *testing.T) {
	tc := taxCode("0.10", models.TaxScopePurchase, models.TaxRecoveryPartial, "40", 10, 55)
	result := ComputeTax(d("500.00"), tc)
	// tax = 50.00, recoverable = 40% = 20.00, non-recoverable = 30.00
	rec, nonRec := PurchaseTaxPostingLines(result, tc)

	if rec.AccountID != 55 {
		t.Errorf("recoverable AccountID = %d, want 55", rec.AccountID)
	}
	assertDecimal(t, "recoverable Amount", "20.00", rec.Amount)

	if nonRec.AccountID != 0 {
		t.Errorf("non-recoverable AccountID = %d, want 0 (caller routes to expense)", nonRec.AccountID)
	}
	assertDecimal(t, "non-recoverable Amount", "30.00", nonRec.Amount)
}

func TestPurchaseTaxPostingLines_NoRecovery(t *testing.T) {
	tc := taxCode("0.08", models.TaxScopePurchase, models.TaxRecoveryNone, "0", 10, 0)
	result := ComputeTax(d("250.00"), tc)
	// tax = 20.00, recoverable = 0, non-recoverable = 20.00
	rec, nonRec := PurchaseTaxPostingLines(result, tc)

	// No recoverable account set: AccountID must be 0 and Amount must be zero.
	if rec.AccountID != 0 {
		t.Errorf("recoverable AccountID = %d, want 0", rec.AccountID)
	}
	assertDecimal(t, "recoverable Amount", "0.00", rec.Amount)

	if nonRec.AccountID != 0 {
		t.Errorf("non-recoverable AccountID = %d, want 0", nonRec.AccountID)
	}
	assertDecimal(t, "non-recoverable Amount", "20.00", nonRec.Amount)
}

func TestPurchaseTaxPostingLines_NoRecoverableAccount(t *testing.T) {
	// RecoveryMode is full but no PurchaseRecoverableAccountID set:
	// recoverable line should have AccountID=0 (caller must handle).
	tc := taxCode("0.05", models.TaxScopePurchase, models.TaxRecoveryFull, "100", 10, 0)
	result := ComputeTax(d("200.00"), tc)
	rec, _ := PurchaseTaxPostingLines(result, tc)

	if rec.AccountID != 0 {
		t.Errorf("AccountID = %d, want 0 when no recoverable account configured", rec.AccountID)
	}
}

// ── CalculateTax (engine integration) ───────────────────────────────────────

func TestCalculateTax_SalesScope(t *testing.T) {
	tc := taxCode("0.05", models.TaxScopeSales, models.TaxRecoveryNone, "0", 77, 0)
	results := CalculateTax(d("100.00"), tc)

	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].SalesTaxAccountID != 77 {
		t.Errorf("SalesTaxAccountID = %d, want 77", results[0].SalesTaxAccountID)
	}
	assertDecimal(t, "TaxAmount", "5.00", results[0].TaxAmount)
}

func TestCalculateTax_PurchaseOnlyScope_ReturnsNil(t *testing.T) {
	// Sales-path caller (CalculateTax) must skip purchase-only codes.
	tc := taxCode("0.05", models.TaxScopePurchase, models.TaxRecoveryFull, "100", 10, 20)
	results := CalculateTax(d("100.00"), tc)

	if results != nil {
		t.Errorf("expected nil for purchase-only code, got %v", results)
	}
}

func TestCalculateTax_BothScope(t *testing.T) {
	tc := taxCode("0.13", models.TaxScopeBoth, models.TaxRecoveryNone, "0", 88, 0)
	results := CalculateTax(d("1000.00"), tc)

	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	assertDecimal(t, "TaxAmount", "130.00", results[0].TaxAmount)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func assertDecimal(t *testing.T, label, want string, got decimal.Decimal) {
	t.Helper()
	wantD, _ := decimal.NewFromString(want)
	if !got.Equal(wantD) {
		t.Errorf("%s: got %s, want %s", label, got.StringFixed(2), wantD.StringFixed(2))
	}
}
