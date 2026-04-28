package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"balanciz/internal/models"
)

func TestSelectCompanyPostRecordsSmartPickerUsage(t *testing.T) {
	db := testRouteDB(t)
	companyA := seedCompany(t, db, "Active Co")
	companyB := seedCompany(t, db, "Switch Target Co")
	user, rawToken := seedUserSession(t, db, &companyA)
	seedMembership(t, db, user.ID, companyA)
	seedMembership(t, db, user.ID, companyB)

	app := testRouteApp(t, db)
	form := url.Values{}
	form.Set("company_id", strconv.FormatUint(uint64(companyB), 10))
	req := httptest.NewRequest(http.MethodPost, "/select-company", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(CSRFHeaderName, "csrf-company-switch")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf-company-switch", Path: "/"})
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect after switch, got %d", resp.StatusCode)
	}

	var event models.SmartPickerEvent
	if err := db.Where("company_id = ? AND user_id = ? AND context = ? AND entity_type = ? AND selected_entity_id = ?",
		companyB, user.ID, "company.switcher", "company", companyB).
		First(&event).Error; err != nil {
		t.Fatalf("expected company switch smart picker event, got %v", err)
	}

	var stat models.SmartPickerUsageStat
	if err := db.Where("company_id = ? AND scope_type = ? AND user_id = ? AND context = ? AND entity_type = ? AND entity_id = ?",
		companyB, models.SmartPickerScopeUser, user.ID, "company.switcher", "company", companyB).
		First(&stat).Error; err != nil {
		t.Fatalf("expected company switch user usage stat, got %v", err)
	}
	if stat.SelectCount != 1 {
		t.Fatalf("expected select count 1, got %d", stat.SelectCount)
	}
}

func TestCompaniesSearchIsLimitedToCurrentUserMemberships(t *testing.T) {
	db := testRouteDB(t)
	visible := seedCompany(t, db, "Taxdeep Corp.")
	otherVisible := seedCompany(t, db, "Carote Kitchenware")
	containedMatch := seedCompany(t, db, "Alpha Taxdeep Services")
	hidden := seedCompany(t, db, "Taxdeep Hidden")
	user, rawToken := seedUserSession(t, db, &visible)
	seedMembership(t, db, user.ID, visible)
	seedMembership(t, db, user.ID, otherVisible)
	seedMembership(t, db, user.ID, containedMatch)
	otherUser := models.User{
		ID:           uuid.New(),
		Email:        "hidden-" + strings.ReplaceAll(t.Name(), "/", "-") + "@example.com",
		PasswordHash: "not-used",
		DisplayName:  "Hidden User",
		IsActive:     true,
	}
	if err := db.Create(&otherUser).Error; err != nil {
		t.Fatal(err)
	}
	seedMembership(t, db, otherUser.ID, hidden)

	app := testRouteApp(t, db)
	resp := performRequest(t, app, "/companies?q=Taxdeep", rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	listStart := strings.Index(html, `id="companies-list"`)
	if listStart == -1 {
		t.Fatalf("expected companies-list in response, got %q", html)
	}
	listHTML := html[listStart:]
	if !strings.Contains(listHTML, "Taxdeep Corp.") {
		t.Fatalf("expected visible company in search results, got %q", listHTML)
	}
	if strings.Contains(listHTML, "Carote Kitchenware") {
		t.Fatalf("expected search results to filter non-matching user company: %q", listHTML)
	}
	firstPrefix := strings.Index(listHTML, "Taxdeep Corp.")
	firstContained := strings.Index(listHTML, "Alpha Taxdeep Services")
	if firstPrefix == -1 || firstContained == -1 || firstPrefix > firstContained {
		t.Fatalf("expected prefix company match first, got %q", listHTML)
	}
	if strings.Contains(listHTML, "Taxdeep Hidden") {
		t.Fatalf("search leaked another user's company: %q", listHTML)
	}
	if !strings.Contains(html, `hx-trigger="input changed delay:200ms, search"`) {
		t.Fatalf("expected company search input to auto-match while typing, got %q", html)
	}
}

func TestSidebarCompanySwitcherUsesTopEightUserCompanySelections(t *testing.T) {
	db := testRouteDB(t)
	activeID := seedCompany(t, db, "Zulu Active")
	user, _ := seedUserSession(t, db, &activeID)
	seedMembership(t, db, user.ID, activeID)

	ids := []uint{activeID}
	for _, name := range []string{
		"Alpha Co", "Beta Co", "Charlie Co", "Delta Co", "Echo Co",
		"Foxtrot Co", "Golf Co", "Hotel Co", "India Co", "Juliet Co",
	} {
		id := seedCompany(t, db, name)
		seedMembership(t, db, user.ID, id)
		ids = append(ids, id)
	}

	now := time.Now().UTC()
	if err := db.Create(&models.SmartPickerUsageStat{
		CompanyID:      ids[4],
		ScopeType:      models.SmartPickerScopeUser,
		UserID:         &user.ID,
		Context:        "company.switcher",
		EntityType:     "company",
		EntityID:       ids[4],
		SelectCount:    12,
		LastSelectedAt: &now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	s := &Server{DB: db}
	data := s.buildSidebarData(&user, activeID)
	if len(data.SwitcherRows) != 8 {
		t.Fatalf("expected top 8 company rows, got %d: %+v", len(data.SwitcherRows), data.SwitcherRows)
	}
	if data.SwitcherRows[0].CompanyIDStr != strconv.FormatUint(uint64(ids[4]), 10) {
		t.Fatalf("expected most-used company first, got %+v", data.SwitcherRows)
	}
	foundActive := false
	for _, row := range data.SwitcherRows {
		if row.CompanyIDStr == strconv.FormatUint(uint64(activeID), 10) {
			foundActive = true
			break
		}
	}
	if !foundActive {
		t.Fatalf("expected active company to stay visible in limited switcher rows: %+v", data.SwitcherRows)
	}
}
