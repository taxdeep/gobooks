// 遵循project_guide.md
package services

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func favouritesTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:report_favs_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ReportFavourite{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestToggleReportFavourite_AddRemoveCycle locks the core toggle
// contract: clicking the star alternates between starred and
// un-starred without accumulating duplicate rows.
func TestToggleReportFavourite_AddRemoveCycle(t *testing.T) {
	db := favouritesTestDB(t)
	user := uuid.New()
	const company = uint(1)

	// First toggle → starred
	starred, err := ToggleReportFavourite(db, user, company, "balance-sheet")
	if err != nil {
		t.Fatal(err)
	}
	if !starred {
		t.Fatal("first toggle should mark report as starred")
	}

	favs, err := ListUserReportFavourites(db, user, company)
	if err != nil {
		t.Fatal(err)
	}
	if !favs["balance-sheet"] {
		t.Fatal("expected balance-sheet in favourites after first toggle")
	}

	// Second toggle → un-starred
	starred2, err := ToggleReportFavourite(db, user, company, "balance-sheet")
	if err != nil {
		t.Fatal(err)
	}
	if starred2 {
		t.Fatal("second toggle should un-star the report")
	}

	favs2, err := ListUserReportFavourites(db, user, company)
	if err != nil {
		t.Fatal(err)
	}
	if favs2["balance-sheet"] {
		t.Fatal("expected balance-sheet absent from favourites after second toggle")
	}
}

// TestToggleReportFavourite_RejectsUnknownKey verifies the validation
// guard — a typo from a malicious / buggy form post can't pollute
// the table with garbage keys.
func TestToggleReportFavourite_RejectsUnknownKey(t *testing.T) {
	db := favouritesTestDB(t)
	user := uuid.New()
	_, err := ToggleReportFavourite(db, user, 1, "made-up-report-key")
	if err != ErrUnknownReportKey {
		t.Fatalf("expected ErrUnknownReportKey, got %v", err)
	}

	favs, _ := ListUserReportFavourites(db, user, 1)
	if len(favs) != 0 {
		t.Errorf("expected zero favourites, got %v", favs)
	}
}

// TestListUserReportFavourites_ScopedPerCompany verifies tenant
// isolation: two companies for the same user keep independent
// favourite lists. Critical because favouriting "balance-sheet" for
// company A shouldn't fill the star for company B.
func TestListUserReportFavourites_ScopedPerCompany(t *testing.T) {
	db := favouritesTestDB(t)
	user := uuid.New()

	if _, err := ToggleReportFavourite(db, user, 1, "balance-sheet"); err != nil {
		t.Fatal(err)
	}
	if _, err := ToggleReportFavourite(db, user, 2, "income-statement"); err != nil {
		t.Fatal(err)
	}

	favsA, _ := ListUserReportFavourites(db, user, 1)
	if !favsA["balance-sheet"] || favsA["income-statement"] {
		t.Errorf("company 1 favourites = %v, want only balance-sheet", favsA)
	}
	favsB, _ := ListUserReportFavourites(db, user, 2)
	if favsB["balance-sheet"] || !favsB["income-statement"] {
		t.Errorf("company 2 favourites = %v, want only income-statement", favsB)
	}
}

// TestListUserReportFavourites_EmptyArgs returns an empty map (not
// nil, not error) for the pre-onboarding edge case where the user
// hasn't picked a company yet.
func TestListUserReportFavourites_EmptyArgs(t *testing.T) {
	db := favouritesTestDB(t)
	favs, err := ListUserReportFavourites(db, uuid.Nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if favs == nil {
		t.Error("expected non-nil empty map; nil would force every caller to nil-check")
	}
	if len(favs) != 0 {
		t.Errorf("expected empty map, got %v", favs)
	}
}
