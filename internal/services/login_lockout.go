// 遵循project_guide.md
package services

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

const (
	UserPasswordFailureLimit  = 5
	UserTemporaryLockDuration = 30 * time.Minute
	UserLockoutWindow         = 24 * time.Hour
	UserDailyLockLimit        = 3

	userLoginLockReasonTooManyFailures = "too_many_failed_login_blocks"
)

type UserLoginLockoutState struct {
	Locked            bool
	Permanent         bool
	LockedUntil       *time.Time
	RetryAfter        time.Duration
	FailedAttempts    int
	RemainingAttempts int
	LockCount         int
}

func CheckUserLoginLockout(db *gorm.DB, user *models.User, now time.Time) (UserLoginLockoutState, error) {
	state := UserLoginLockoutState{}
	if user == nil {
		return state, nil
	}
	now = now.UTC()

	if user.PermanentlyLockedAt != nil {
		state.Locked = true
		state.Permanent = true
		state.LockCount = user.LoginLockCount
		return state, nil
	}

	if user.LoginLockedUntil == nil {
		return state, nil
	}

	lockedUntil := user.LoginLockedUntil.UTC()
	if now.Before(lockedUntil) {
		state.Locked = true
		state.LockedUntil = &lockedUntil
		state.RetryAfter = lockedUntil.Sub(now)
		state.LockCount = user.LoginLockCount
		return state, nil
	}

	if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"failed_login_attempts": 0,
		"login_locked_until":    nil,
	}).Error; err != nil {
		return state, err
	}
	user.FailedLoginAttempts = 0
	user.LoginLockedUntil = nil
	return state, nil
}

func RecordUserPasswordFailure(db *gorm.DB, user *models.User, now time.Time) (UserLoginLockoutState, error) {
	state := UserLoginLockoutState{}
	if user == nil {
		return state, nil
	}
	now = now.UTC()

	if pre, err := CheckUserLoginLockout(db, user, now); err != nil || pre.Locked {
		return pre, err
	}

	attempts := user.FailedLoginAttempts + 1
	if attempts < UserPasswordFailureLimit {
		if err := db.Model(&models.User{}).Where("id = ?", user.ID).
			Update("failed_login_attempts", attempts).Error; err != nil {
			return state, err
		}
		user.FailedLoginAttempts = attempts
		state.FailedAttempts = attempts
		state.RemainingAttempts = UserPasswordFailureLimit - attempts
		state.LockCount = user.LoginLockCount
		return state, nil
	}

	windowStart := now
	lockCount := 0
	if user.LoginLockWindowStartedAt != nil && now.Sub(user.LoginLockWindowStartedAt.UTC()) < UserLockoutWindow {
		windowStart = user.LoginLockWindowStartedAt.UTC()
		lockCount = user.LoginLockCount
	}
	lockCount++

	updates := map[string]any{
		"failed_login_attempts":        0,
		"login_lock_window_started_at": windowStart,
		"login_lock_count":             lockCount,
	}

	state.Locked = true
	state.LockCount = lockCount
	user.FailedLoginAttempts = 0
	user.LoginLockWindowStartedAt = &windowStart
	user.LoginLockCount = lockCount

	if lockCount >= UserDailyLockLimit {
		updates["login_locked_until"] = nil
		updates["permanently_locked_at"] = now
		updates["login_lock_reason"] = userLoginLockReasonTooManyFailures
		state.Permanent = true
		user.LoginLockedUntil = nil
		user.PermanentlyLockedAt = &now
		user.LoginLockReason = userLoginLockReasonTooManyFailures
	} else {
		lockedUntil := now.Add(UserTemporaryLockDuration)
		updates["login_locked_until"] = lockedUntil
		state.LockedUntil = &lockedUntil
		state.RetryAfter = UserTemporaryLockDuration
		user.LoginLockedUntil = &lockedUntil
	}

	if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		return UserLoginLockoutState{}, err
	}
	return state, nil
}

func RecordUserPasswordSuccess(db *gorm.DB, user *models.User) error {
	if user == nil {
		return nil
	}
	if user.FailedLoginAttempts == 0 && user.LoginLockedUntil == nil {
		return nil
	}
	if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"failed_login_attempts": 0,
		"login_locked_until":    nil,
	}).Error; err != nil {
		return err
	}
	user.FailedLoginAttempts = 0
	user.LoginLockedUntil = nil
	return nil
}

func UnlockUserLogin(db *gorm.DB, userID uuid.UUID) error {
	if userID == uuid.Nil {
		return fmt.Errorf("user id is required")
	}
	return db.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		"failed_login_attempts":        0,
		"login_locked_until":           nil,
		"login_lock_window_started_at": nil,
		"login_lock_count":             0,
		"permanently_locked_at":        nil,
		"login_lock_reason":            "",
	}).Error
}
