package web

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/search_engine"
)

func newGlobalSearchUsageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:global_search_usage_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.GlobalSearchEvent{}, &models.GlobalSearchTypeStat{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestApplyGlobalSearchUsageBoosts_AmountSearchLearnsJEPreference(t *testing.T) {
	db := newGlobalSearchUsageTestDB(t)
	userID := uuid.New()
	now := time.Now().UTC()
	if err := db.Create(&models.GlobalSearchTypeStat{
		CompanyID:          1,
		ScopeType:          models.SmartPickerScopeUser,
		UserID:             &userID,
		QueryKind:          models.GlobalSearchQueryKindAmount,
		SelectedEntityType: "journal_entry",
		SelectCount:        5,
		SelectCount30D:     5,
		WeightSource:       "behavior",
		LastSelectedAt:     &now,
		UpdatedAt:          now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	s := &Server{DB: db}
	got := s.applyGlobalSearchUsageBoosts(1, &userID, "11039.18", []search_engine.Candidate{
		{ID: "1", EntityType: "bill", Primary: "Bill"},
		{ID: "2", EntityType: "journal_entry", Primary: "Journal Entry"},
	})
	if got[0].EntityType != "journal_entry" {
		t.Fatalf("journal entry should be boosted first for learned amount searches, got %+v", got)
	}
}
