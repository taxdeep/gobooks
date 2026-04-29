// 遵循project_guide.md
package services

import (
	"net/url"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func TestRecordUserPasswordFailureLocksAndPermanentlyBlocks(t *testing.T) {
	db := newLoginLockoutTestDB(t)
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	user := createLoginLockoutTestUser(t, db)

	for i := 1; i < UserPasswordFailureLimit; i++ {
		state, err := RecordUserPasswordFailure(db, user, now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
		if state.Locked {
			t.Fatalf("failure %d unexpectedly locked account", i)
		}
		if state.RemainingAttempts != UserPasswordFailureLimit-i {
			t.Fatalf("failure %d remaining attempts: got %d", i, state.RemainingAttempts)
		}
	}

	state, err := RecordUserPasswordFailure(db, user, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("lock failure: %v", err)
	}
	if !state.Locked || state.Permanent || state.LockedUntil == nil {
		t.Fatalf("expected temporary lock, got %+v", state)
	}

	for lock := 2; lock <= UserDailyLockLimit; lock++ {
		expireLoginLockForTest(t, db, user.ID, now.Add(time.Duration(lock)*time.Hour))
		reloadLoginLockoutTestUser(t, db, user)
		if state, err := CheckUserLoginLockout(db, user, now.Add(time.Duration(lock)*time.Hour)); err != nil {
			t.Fatalf("clear expired lock %d: %v", lock, err)
		} else if state.Locked {
			t.Fatalf("lock %d should have expired", lock)
		}
		for i := 0; i < UserPasswordFailureLimit; i++ {
			state, err = RecordUserPasswordFailure(db, user, now.Add(time.Duration(lock)*time.Hour).Add(time.Duration(i)*time.Minute))
			if err != nil {
				t.Fatalf("lock %d failure %d: %v", lock, i+1, err)
			}
		}
	}

	if !state.Locked || !state.Permanent {
		t.Fatalf("expected permanent block after daily lock limit, got %+v", state)
	}
	reloadLoginLockoutTestUser(t, db, user)
	if user.PermanentlyLockedAt == nil {
		t.Fatal("expected permanently_locked_at to be stored")
	}
}

func TestUnlockUserLoginClearsLockoutState(t *testing.T) {
	db := newLoginLockoutTestDB(t)
	user := createLoginLockoutTestUser(t, db)
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	until := now.Add(UserTemporaryLockDuration)
	if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"failed_login_attempts":        4,
		"login_locked_until":           until,
		"login_lock_window_started_at": now,
		"login_lock_count":             2,
		"permanently_locked_at":        now,
		"login_lock_reason":            userLoginLockReasonTooManyFailures,
	}).Error; err != nil {
		t.Fatalf("seed lockout: %v", err)
	}

	if err := UnlockUserLogin(db, user.ID); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	reloadLoginLockoutTestUser(t, db, user)
	if user.FailedLoginAttempts != 0 || user.LoginLockedUntil != nil || user.LoginLockWindowStartedAt != nil ||
		user.LoginLockCount != 0 || user.PermanentlyLockedAt != nil || user.LoginLockReason != "" {
		t.Fatalf("lockout state not cleared: %+v", user)
	}
}

func newLoginLockoutTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+url.QueryEscape(t.Name())+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.UserPlan{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func createLoginLockoutTestUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	u := &models.User{
		ID:           uuid.New(),
		Email:        "lockout@example.com",
		PasswordHash: "not-used",
		DisplayName:  "Lockout Test",
		IsActive:     true,
		PlanID:       1,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func reloadLoginLockoutTestUser(t *testing.T, db *gorm.DB, user *models.User) {
	t.Helper()
	if err := db.First(user, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
}

func expireLoginLockForTest(t *testing.T, db *gorm.DB, userID uuid.UUID, at time.Time) {
	t.Helper()
	expiredAt := at.Add(-time.Minute)
	if err := db.Model(&models.User{}).Where("id = ?", userID).Update("login_locked_until", expiredAt).Error; err != nil {
		t.Fatalf("expire lock: %v", err)
	}
}
