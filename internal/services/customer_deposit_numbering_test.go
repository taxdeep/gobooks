// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func testCustomerDepositNumberingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:dep_numbering_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.NumberingSetting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestSuggestNextCustomerDepositNumber_Default locks the default format —
// "DEP0001" on a fresh company with no saved rules. The user asked for this
// specific format at design time (2026-04-24).
func TestSuggestNextCustomerDepositNumber_Default(t *testing.T) {
	db := testCustomerDepositNumberingDB(t)
	got, err := SuggestNextCustomerDepositNumber(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "DEP0001" {
		t.Fatalf("first suggestion = %q, want %q", got, "DEP0001")
	}
}

// TestBumpCustomerDepositNextNumber_Sequential verifies Bump advances the
// counter so the next Suggest returns the next-in-sequence number. Guards
// against silent off-by-one / non-persistence regressions.
func TestBumpCustomerDepositNextNumber_Sequential(t *testing.T) {
	db := testCustomerDepositNumberingDB(t)
	companyID := uint(7)

	expected := []string{"DEP0001", "DEP0002", "DEP0003"}
	for i, want := range expected {
		got, err := SuggestNextCustomerDepositNumber(db, companyID)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("iter %d: suggestion = %q, want %q", i, got, want)
		}
		if err := BumpCustomerDepositNextNumberAfterCreate(db, companyID); err != nil {
			t.Fatalf("iter %d: bump: %v", i, err)
		}
	}
}

// TestSuggestNextCustomerDepositNumber_PerCompany — counter must be scoped
// to the company (so two companies can each issue DEP0001 as their first).
func TestSuggestNextCustomerDepositNumber_PerCompany(t *testing.T) {
	db := testCustomerDepositNumberingDB(t)

	gotA, err := SuggestNextCustomerDepositNumber(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := BumpCustomerDepositNextNumberAfterCreate(db, 10); err != nil {
		t.Fatal(err)
	}

	gotB, err := SuggestNextCustomerDepositNumber(db, 11)
	if err != nil {
		t.Fatal(err)
	}

	if gotA != "DEP0001" || gotB != "DEP0001" {
		t.Fatalf("company 10 = %q, company 11 = %q; want both DEP0001", gotA, gotB)
	}

	gotA2, err := SuggestNextCustomerDepositNumber(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if gotA2 != "DEP0002" {
		t.Fatalf("company 10 after bump = %q, want DEP0002", gotA2)
	}
}
