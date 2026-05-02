package web

import (
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

func TestReportsHubHighlightsCorePackage(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Reports Hub Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	resp := performRequest(t, app, "/reports", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Reports hub, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		"Core Report Package",
		"Profit &amp; Loss (Income Statement)",
		"Balance Sheet",
		"Cash Flow Summary",
		"Trial Balance",
		"General Ledger",
		"Journal Entries",
		"Interactive",
		"Drill-through",
		"/reports/income-statement/export.csv",
		"/reports/balance-sheet/export.csv",
		"/reports/trial-balance/export.csv",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Reports hub to contain %q, got %q", want, body)
		}
	}
}

func TestReportsHubShowsFavouriteWithCapabilities(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Reports Hub Fav Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	if err := db.AutoMigrate(&models.ReportFavourite{}); err != nil {
		t.Fatal(err)
	}

	starred, err := services.ToggleReportFavourite(db, user.ID, companyID, "balance-sheet")
	if err != nil {
		t.Fatal(err)
	}
	if !starred {
		t.Fatal("expected balance-sheet to be starred")
	}

	resp := performRequest(t, app, "/reports", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Reports hub, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		"Favourites",
		"Balance Sheet",
		"As-of",
		"CSV",
		"Remove from favourites",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Reports hub favourite view to contain %q, got %q", want, body)
		}
	}
}
