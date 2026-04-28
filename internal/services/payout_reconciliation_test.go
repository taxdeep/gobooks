// 遵循project_guide.md
package services

// payout_reconciliation_test.go — Batch 18: Payout ↔ bank entry matching tests.
//
// Uses the same payoutTestDB / seedPayoutBase / seedOneSettlement helpers from
// gateway_payout_service_test.go (same package).

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// ── Test DB helper ────────────────────────────────────────────────────────────

func reconTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := payoutTestDB(t)
	if err := db.AutoMigrate(
		&models.BankEntry{},
		&models.PayoutReconciliation{},
		&models.AuditLog{},
		// Batch 19: component table needed by ComputeGatewayPayoutExpectedNet
		// which is now called inside MatchGatewayPayoutToBankEntry.
		&models.GatewayPayoutComponent{},
	); err != nil {
		t.Fatalf("migrate recon tables: %v", err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

// makeGatewayPayout creates and posts a GatewayPayout using the existing service
// (which requires at least one GatewaySettlement).  Returns the payout.
func makeGatewayPayout(t *testing.T, db *gorm.DB, base payoutBase, net decimal.Decimal) *models.GatewayPayout {
	t.Helper()
	settlement := seedOneSettlement(t, db, base, net)
	inp := CreateGatewayPayoutInput{
		CompanyID:        base.companyID,
		GatewayAccountID: base.gatewayID,
		ProviderPayoutID: "po_test_" + net.StringFixed(0) + "_" + time.Now().Format("150405.000000"),
		PayoutDate:       time.Now(),
		FeeAmount:        decimal.Zero,
		BankAccountID:    base.bankID,
		SettlementIDs:    []uint{settlement.ID},
	}
	result, err := CreateGatewayPayout(db, inp)
	if err != nil {
		t.Fatalf("makeGatewayPayout: %v", err)
	}
	return result.Payout
}

// makeBankEntry creates a BankEntry with the given amount and bank account.
// currency defaults to "" (base currency) when empty.
func makeBankEntry(t *testing.T, db *gorm.DB, companyID, bankAccountID uint, amount decimal.Decimal) *models.BankEntry {
	t.Helper()
	return makeBankEntryWithCurrency(t, db, companyID, bankAccountID, amount, "CAD")
}

// makeBankEntryWithCurrency creates a BankEntry with explicit currency code.
func makeBankEntryWithCurrency(t *testing.T, db *gorm.DB, companyID, bankAccountID uint, amount decimal.Decimal, currency string) *models.BankEntry {
	t.Helper()
	entry, err := CreateBankEntry(db, CreateBankEntryInput{
		CompanyID:     companyID,
		BankAccountID: bankAccountID,
		EntryDate:     time.Now(),
		Amount:        amount,
		CurrencyCode:  currency,
		Description:   "test bank deposit",
	})
	if err != nil {
		t.Fatalf("makeBankEntry: %v", err)
	}
	return entry
}

// assertNoJEAdded asserts that no new JE was created beyond the baseline count.
func assertNoJEAdded(t *testing.T, db *gorm.DB, companyID uint, baselineCount int64) {
	t.Helper()
	var count int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", companyID).Count(&count)
	if count != baselineCount {
		t.Errorf("expected %d journal entries, got %d (match must not create JEs)", baselineCount, count)
	}
}

// jeCount returns the current count of JEs for a company.
func jeCount(db *gorm.DB, companyID uint) int64 {
	var count int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", companyID).Count(&count)
	return count
}

// ── A. Happy path ─────────────────────────────────────────────────────────────

// TestPayoutRecon_HappyPath creates a payout and a matching bank entry, matches
// them, and asserts the reconciliation record is correct and no JE is created.
func TestPayoutRecon_HappyPath(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)

	baseline := jeCount(db, base.companyID)

	err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test@example.com")
	if err != nil {
		t.Fatalf("MatchGatewayPayoutToBankEntry: %v", err)
	}

	// PayoutReconciliation record must exist.
	rec, err := GetPayoutReconciliation(db, base.companyID, payout.ID)
	if err != nil || rec == nil {
		t.Fatalf("expected reconciliation record, got nil (err=%v)", err)
	}
	if rec.GatewayPayoutID != payout.ID {
		t.Errorf("GatewayPayoutID: want %d got %d", payout.ID, rec.GatewayPayoutID)
	}
	if rec.BankEntryID != entry.ID {
		t.Errorf("BankEntryID: want %d got %d", entry.ID, rec.BankEntryID)
	}
	if rec.Actor != "test@example.com" {
		t.Errorf("Actor: want test@example.com got %s", rec.Actor)
	}
	if rec.MatchedAt.IsZero() {
		t.Error("MatchedAt must be set")
	}

	// No new JE must be created.
	assertNoJEAdded(t, db, base.companyID, baseline)

	// Payout should appear in matched list, not in unmatched.
	unmatched, _ := ListUnmatchedGatewayPayouts(db, base.companyID)
	for _, p := range unmatched {
		if p.ID == payout.ID {
			t.Error("payout still in unmatched list after reconciliation")
		}
	}
	matched, _ := ListMatchedPayoutReconciliations(db, base.companyID)
	found := false
	for _, r := range matched {
		if r.GatewayPayoutID == payout.ID {
			found = true
		}
	}
	if !found {
		t.Error("payout not found in matched list")
	}
}

// TestPayoutRecon_BankEntryUnmatchedAfterMatch verifies the bank entry no longer
// appears in the unmatched list after matching.
func TestPayoutRecon_BankEntryUnmatchedAfterMatch(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)

	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test"); err != nil {
		t.Fatalf("match: %v", err)
	}

	unmatched, _ := ListUnmatchedBankEntries(db, base.companyID)
	for _, e := range unmatched {
		if e.ID == entry.ID {
			t.Error("bank entry still in unmatched list after reconciliation")
		}
	}
}

// ── B. Reject paths ───────────────────────────────────────────────────────────

// TestPayoutRecon_AmountMismatch rejects when payout net ≠ bank entry amount.
func TestPayoutRecon_AmountMismatch(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(99)) // wrong amount

	err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test")
	if err == nil {
		t.Fatal("expected amount mismatch error, got nil")
	}

	// No reconciliation record.
	rec, _ := GetPayoutReconciliation(db, base.companyID, payout.ID)
	if rec != nil {
		t.Error("reconciliation record must not exist after rejected match")
	}
}

// TestPayoutRecon_AccountMismatch rejects when payout bank account ≠ entry bank account.
func TestPayoutRecon_AccountMismatch(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	// Create a second bank account.
	otherBank := models.Account{
		CompanyID: base.companyID, Code: "1001", Name: "Other Bank",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
	}
	db.Create(&otherBank)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base.companyID, otherBank.ID, payout.NetAmount) // different account

	err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test")
	if err == nil {
		t.Fatal("expected account mismatch error, got nil")
	}

	rec, _ := GetPayoutReconciliation(db, base.companyID, payout.ID)
	if rec != nil {
		t.Error("reconciliation record must not exist after rejected match")
	}
}

// TestPayoutRecon_CrossCompanyPayout rejects a payout that belongs to a different company.
func TestPayoutRecon_CrossCompanyPayout(t *testing.T) {
	db := reconTestDB(t)
	base1 := seedPayoutBase(t, db)
	base2 := seedPayoutBase(t, db) // second company

	payout := makeGatewayPayout(t, db, base1, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base2.companyID, base2.bankID, payout.NetAmount)

	// Try to match payout from company1 using company2's context.
	err := MatchGatewayPayoutToBankEntry(db, base2.companyID, payout.ID, entry.ID, "test")
	if err == nil {
		t.Fatal("expected cross-company error, got nil")
	}
}

// TestPayoutRecon_CrossCompanyBankEntry rejects a bank entry from a different company.
func TestPayoutRecon_CrossCompanyBankEntry(t *testing.T) {
	db := reconTestDB(t)
	base1 := seedPayoutBase(t, db)
	base2 := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base1, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base2.companyID, base2.bankID, payout.NetAmount)

	// Try to match payout from company1 with bank entry from company2.
	err := MatchGatewayPayoutToBankEntry(db, base1.companyID, payout.ID, entry.ID, "test")
	if err == nil {
		t.Fatal("expected cross-company bank entry error, got nil")
	}
}

// TestPayoutRecon_PayoutAlreadyMatched rejects a second match on an already matched payout.
func TestPayoutRecon_PayoutAlreadyMatched(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry1 := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)
	entry2 := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount) // second entry

	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry1.ID, "test"); err != nil {
		t.Fatalf("first match: %v", err)
	}

	// Second attempt: same payout, different bank entry.
	err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry2.ID, "test")
	if err == nil {
		t.Fatal("expected ErrReconPayoutAlreadyMatched, got nil")
	}

	// Only one reconciliation record.
	var count int64
	db.Model(&models.PayoutReconciliation{}).
		Where("company_id = ? AND gateway_payout_id = ?", base.companyID, payout.ID).
		Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 reconciliation record, got %d", count)
	}
}

// TestPayoutRecon_BankEntryAlreadyMatched rejects matching a bank entry that is
// already matched to a different payout.
func TestPayoutRecon_BankEntryAlreadyMatched(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout1 := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	payout2 := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(100))

	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout1.ID, entry.ID, "test"); err != nil {
		t.Fatalf("first match: %v", err)
	}

	// Second payout tries to match the same bank entry.
	err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout2.ID, entry.ID, "test")
	if err == nil {
		t.Fatal("expected ErrReconBankEntryAlreadyMatched, got nil")
	}
}

// TestPayoutRecon_PayoutNotFound rejects a non-existent payout ID.
func TestPayoutRecon_PayoutNotFound(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	entry := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(100))

	err := MatchGatewayPayoutToBankEntry(db, base.companyID, 99999, entry.ID, "test")
	if err == nil {
		t.Fatal("expected ErrReconPayoutNotFound, got nil")
	}
}

// TestPayoutRecon_BankEntryNotFound rejects a non-existent bank entry ID.
func TestPayoutRecon_BankEntryNotFound(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, 99999, "test")
	if err == nil {
		t.Fatal("expected ErrReconBankEntryNotFound, got nil")
	}
}

// ── C. Idempotency / concurrent ───────────────────────────────────────────────

// TestPayoutRecon_DuplicateSubmitBlocked ensures that a duplicate (payout, entry)
// submission — simulating browser double-submit — results in exactly one record.
func TestPayoutRecon_DuplicateSubmitBlocked(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)

	// First submit.
	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test"); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	// Second submit (same pair).
	err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test")
	if err == nil {
		t.Fatal("expected error on duplicate submit, got nil")
	}

	var count int64
	db.Model(&models.PayoutReconciliation{}).
		Where("company_id = ? AND gateway_payout_id = ?", base.companyID, payout.ID).
		Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 record after duplicate submit, got %d", count)
	}
}

// TestPayoutRecon_ConcurrentMatchSafe verifies that two concurrent goroutines
// racing to match the same payout can only produce one reconciliation record.
func TestPayoutRecon_ConcurrentMatchSafe(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry1 := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)
	entry2 := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry1.ID, "g0")
	}()
	go func() {
		defer wg.Done()
		errs[1] = MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry2.ID, "g1")
	}()
	wg.Wait()

	successes := 0
	for _, e := range errs {
		if e == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 success, got %d (errs: %v)", successes, errs)
	}

	var count int64
	db.Model(&models.PayoutReconciliation{}).
		Where("company_id = ? AND gateway_payout_id = ?", base.companyID, payout.ID).
		Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 reconciliation record after concurrent match, got %d", count)
	}
}

// ── D. Bank entry CRUD ────────────────────────────────────────────────────────

func TestPayoutRecon_ClassifyUniqueConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "postgres payout constraint",
			err: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "uq_payout_recon_payout",
			},
			want: ErrReconPayoutAlreadyMatched,
		},
		{
			name: "postgres bank entry constraint",
			err: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "uq_payout_recon_bank_entry",
			},
			want: ErrReconBankEntryAlreadyMatched,
		},
		{
			name: "sqlite payout column",
			err:  errors.New("constraint failed: UNIQUE constraint failed: payout_reconciliations.gateway_payout_id (2067)"),
			want: ErrReconPayoutAlreadyMatched,
		},
		{
			name: "sqlite bank entry column",
			err:  errors.New("constraint failed: UNIQUE constraint failed: payout_reconciliations.bank_entry_id (2067)"),
			want: ErrReconBankEntryAlreadyMatched,
		},
		{
			name: "ambiguous unique error stays generic",
			err:  errors.New("constraint failed: UNIQUE constraint failed: payout_reconciliations.company_id (2067)"),
			want: nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := classifyPayoutReconUniqueConflict(tc.err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("classifyPayoutReconUniqueConflict() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBankEntry_Create_ValidAccount creates a bank entry and verifies it persists.
func TestBankEntry_Create_ValidAccount(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	entry, err := CreateBankEntry(db, CreateBankEntryInput{
		CompanyID:     base.companyID,
		BankAccountID: base.bankID,
		EntryDate:     time.Now(),
		Amount:        decimal.NewFromInt(250),
		CurrencyCode:  "",
		Description:   "wire transfer",
	})
	if err != nil {
		t.Fatalf("CreateBankEntry: %v", err)
	}
	if entry.ID == 0 {
		t.Error("entry ID must be set")
	}

	loaded, err := GetBankEntry(db, base.companyID, entry.ID)
	if err != nil || loaded == nil {
		t.Fatalf("GetBankEntry: %v", err)
	}
	if !loaded.Amount.Equal(decimal.NewFromInt(250)) {
		t.Errorf("amount: want 250 got %s", loaded.Amount)
	}
}

// TestBankEntry_Create_InvalidAmount rejects zero and negative amounts.
func TestBankEntry_Create_InvalidAmount(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	_, err := CreateBankEntry(db, CreateBankEntryInput{
		CompanyID: base.companyID, BankAccountID: base.bankID,
		EntryDate: time.Now(), Amount: decimal.Zero,
	})
	if err == nil {
		t.Fatal("expected error for zero amount, got nil")
	}
}

// TestBankEntry_Create_NonExistentAccount rejects an unknown bank account ID.
func TestBankEntry_Create_NonExistentAccount(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	_, err := CreateBankEntry(db, CreateBankEntryInput{
		CompanyID: base.companyID, BankAccountID: 99999,
		EntryDate: time.Now(), Amount: decimal.NewFromInt(100),
	})
	if err == nil {
		t.Fatal("expected ErrReconBankAccountInvalid, got nil")
	}
}

// ── E. Regression / no pollution ──────────────────────────────────────────────

// TestPayoutRecon_GatewayPayoutMainChainUnchanged ensures that the existing
// GatewayPayout + JE chain is untouched after reconciliation.
func TestPayoutRecon_GatewayPayoutMainChainUnchanged(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	jeBaseline := jeCount(db, base.companyID)
	entry := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)

	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test"); err != nil {
		t.Fatalf("match: %v", err)
	}

	// JE count must not have changed.
	assertNoJEAdded(t, db, base.companyID, jeBaseline)

	// GatewayPayout record must be unchanged (no extra fields modified).
	var reloaded models.GatewayPayout
	db.First(&reloaded, payout.ID)
	if reloaded.JournalEntryID == nil {
		t.Error("GatewayPayout.JournalEntryID must remain set")
	}
	if !reloaded.NetAmount.Equal(payout.NetAmount) {
		t.Errorf("GatewayPayout.NetAmount changed unexpectedly")
	}
}

// TestPayoutRecon_ListUnmatchedAndCandidate verifies list helpers return correct results.
func TestPayoutRecon_ListUnmatchedAndCandidate(t *testing.T) {
	db := reconTestDB(t)
	base := seedPayoutBase(t, db)

	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)

	// Before match: both appear in unmatched lists.
	unPayouts, _ := ListUnmatchedGatewayPayouts(db, base.companyID)
	foundPayout := false
	for _, p := range unPayouts {
		if p.ID == payout.ID {
			foundPayout = true
		}
	}
	if !foundPayout {
		t.Error("payout must appear in unmatched list before matching")
	}

	candidates, _ := ListCandidateBankEntries(db, base.companyID, payout)
	foundEntry := false
	for _, e := range candidates {
		if e.ID == entry.ID {
			foundEntry = true
		}
	}
	if !foundEntry {
		t.Error("bank entry must appear in candidate list before matching")
	}

	// After match: both gone from unmatched / candidate.
	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test"); err != nil {
		t.Fatalf("match: %v", err)
	}
	candidates2, _ := ListCandidateBankEntries(db, base.companyID, payout)
	for _, e := range candidates2 {
		if e.ID == entry.ID {
			t.Error("matched bank entry must not appear in candidate list")
		}
	}
}
