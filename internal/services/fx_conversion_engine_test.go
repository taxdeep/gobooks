package services

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestConvertJournalLineAmounts_IdentityConversion(t *testing.T) {
	result, err := ConvertJournalLineAmounts([]FXLineAmounts{
		{TxDebit: decimal.RequireFromString("100.00")},
		{TxCredit: decimal.RequireFromString("100.00")},
	}, decimal.NewFromInt(1))
	if err != nil {
		t.Fatalf("ConvertJournalLineAmounts: %v", err)
	}
	if !result.Lines[0].Debit.Equal(decimal.RequireFromString("100.00")) {
		t.Fatalf("expected debit 100.00, got %s", result.Lines[0].Debit)
	}
	if !result.Totals.BaseDebitTotal.Equal(result.Totals.BaseCreditTotal) {
		t.Fatal("identity conversion should stay balanced")
	}
}

func TestConvertJournalLineAmounts_ForeignPrecisionAndBankersRounding(t *testing.T) {
	result, err := ConvertJournalLineAmounts([]FXLineAmounts{
		{TxDebit: decimal.RequireFromString("1.00")},
		{TxCredit: decimal.RequireFromString("1.00")},
	}, decimal.RequireFromString("1.005"))
	if err != nil {
		t.Fatalf("ConvertJournalLineAmounts: %v", err)
	}
	if !result.Lines[0].Debit.Equal(decimal.RequireFromString("1.00")) {
		t.Fatalf("expected banker's rounded 1.00, got %s", result.Lines[0].Debit)
	}
	if !result.Lines[1].Credit.Equal(decimal.RequireFromString("1.00")) {
		t.Fatalf("expected banker's rounded 1.00, got %s", result.Lines[1].Credit)
	}
}

func TestConvertJournalLineAmounts_BaseImbalanceAbsorbedByAnchor(t *testing.T) {
	// 3 lines: USD 0.01 + USD 0.01 (debits) vs USD 0.02 (credit) at rate 1.5.
	// Per-line: 0.01 × 1.5 = 0.015 → rounds to 0.02 each (banker's rounding).
	// Naive total: debit 0.02+0.02=0.04, credit 0.02 → residual +0.02.
	// Anchor pattern should absorb the residual into the last debit line.
	result, err := ConvertJournalLineAmounts([]FXLineAmounts{
		{TxDebit: decimal.RequireFromString("0.01")},
		{TxDebit: decimal.RequireFromString("0.01")},
		{TxCredit: decimal.RequireFromString("0.02")},
	}, decimal.RequireFromString("1.5"))
	if err != nil {
		t.Fatalf("expected anchor absorption, got error: %v", err)
	}
	if !result.Totals.BaseDebitTotal.Equal(result.Totals.BaseCreditTotal) {
		t.Fatalf("base totals should balance after anchor absorption: debit=%s credit=%s",
			result.Totals.BaseDebitTotal, result.Totals.BaseCreditTotal)
	}
}

func TestConvertJournalLineAmounts_ReturnsLineLevelValuesForReversalFriendliness(t *testing.T) {
	result, err := ConvertJournalLineAmounts([]FXLineAmounts{
		{TxDebit: decimal.RequireFromString("12.34")},
		{TxCredit: decimal.RequireFromString("12.34")},
	}, decimal.RequireFromString("1.25000000"))
	if err != nil {
		t.Fatalf("ConvertJournalLineAmounts: %v", err)
	}
	if len(result.Lines) != 2 {
		t.Fatalf("expected 2 converted lines, got %d", len(result.Lines))
	}
	if !result.Lines[0].TxDebit.Equal(decimal.RequireFromString("12.34")) || !result.Lines[1].TxCredit.Equal(decimal.RequireFromString("12.34")) {
		t.Fatal("expected tx amounts to be preserved per line for reversal")
	}
	if !result.Lines[0].Debit.Equal(decimal.RequireFromString("15.42")) || !result.Lines[1].Credit.Equal(decimal.RequireFromString("15.42")) {
		t.Fatal("expected derived base values to stay on each line for reversal friendliness")
	}
}
