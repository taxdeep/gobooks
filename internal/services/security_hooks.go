// 遵循project_guide.md
package services

// Package-level integration hooks for future security rule enforcement.
//
// ── Design intent ───────────────────────────────────────────────────────────
//
// This file defines the integration points that the login flow (and future
// operations) will call to evaluate security rules and dispatch alerts.
// No heavy detection logic is built yet; every function below is either a
// no-op stub or a thin wrapper around LogSecurityEvent.
//
// ── Future event flow ────────────────────────────────────────────────────────
//
// 1. Login handler detects a successful login (or failed attempt).
// 2. It calls EvaluateLoginSecurity(db, ctx).
// 3. EvaluateLoginSecurity loads company + system security settings.
// 4. For each enabled rule it dispatches an alert via the notification provider
//    (email / SMS / both) using LoadCompanyNotificationSettings / LoadSystemNotificationSettings.
// 5. The event is recorded via LogSecurityEvent for the audit trail.
//
// ── What is intentionally deferred ─────────────────────────────────────────
//
// - IP reputation / geolocation comparison (needs persistent IP history)
// - Device fingerprint storage and comparison
// - Advanced failed-login heuristics (current release ships a basic recent-event throttle)
// - Real alert dispatch (SendTestEmail / SendTestSMS are stubs today)
//
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"time"

	"gobooks/internal/models"

	"gorm.io/gorm"
)

// LoginSecurityContext carries the information the login handler knows at the
// moment of authentication. Pass this to EvaluateLoginSecurity.
type LoginSecurityContext struct {
	CompanyID *uint  // nil for sysadmin logins
	UserID    string // UUID string or sysadmin ID
	UserEmail string
	IPAddress string
	UserAgent string
	Success   bool // false = failed login attempt
}

const (
	unusualIPAlertEventType             = "security.alert.unusual_ip_login"
	blockedLoginEventType               = "login.blocked"
	failedLoginWindow                   = 15 * time.Minute
	maxFailedLoginAttemptsPerIP   int64 = 10
	maxFailedLoginAttemptsPerUser int64 = 5
)

type LoginThrottleState struct {
	Blocked    bool
	RetryAfter time.Duration
}

// EvaluateLoginSecurity is the shared login-time security hook for business and
// sysadmin authentication flows. It always writes a raw success / failure event
// and, for the currently shipped unusual-IP rule, emits an alert event once the
// user has prior successful history from a different address.
func EvaluateLoginSecurity(db *gorm.DB, ctx LoginSecurityContext) {
	var userIDPtr *string
	if ctx.UserID != "" {
		userID := ctx.UserID
		userIDPtr = &userID
	}

	shouldAlert, channel := shouldTriggerUnusualIPAlert(db, ctx, userIDPtr)

	// Record the raw event regardless of detection results.
	// This gives us a base audit trail that detection logic can query later.
	_ = LogSecurityEvent(
		db,
		ctx.CompanyID,
		userIDPtr,
		loginEventType(ctx.Success),
		ctx.IPAddress,
		ctx.UserAgent,
		nil,
	)

	if !shouldAlert {
		return
	}

	metadata := marshalSecurityMetadata(map[string]any{
		"channel":    string(channel),
		"user_email": ctx.UserEmail,
	})
	_ = LogSecurityEvent(
		db,
		ctx.CompanyID,
		userIDPtr,
		unusualIPAlertEventType,
		ctx.IPAddress,
		ctx.UserAgent,
		metadata,
	)
}

// loginEventType returns the canonical event_type string for login outcomes.
func loginEventType(success bool) string {
	if success {
		return "login.success"
	}
	return "login.failed"
}

func CheckLoginThrottle(db *gorm.DB, _ *uint, userID *string, ipAddress string) (LoginThrottleState, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-failedLoginWindow)
	state := LoginThrottleState{}

	if ipAddress != "" {
		count, lastAttemptAt, err := recentFailedLoginCount(db, cutoff, func(q *gorm.DB) *gorm.DB {
			return q.Where("ip_address = ?", ipAddress)
		})
		if err != nil {
			return state, err
		}
		if count >= maxFailedLoginAttemptsPerIP {
			state.Blocked = true
			state.RetryAfter = loginThrottleRetryAfter(now, lastAttemptAt)
			return state, nil
		}
	}

	if userID != nil && *userID != "" {
		count, lastAttemptAt, err := recentFailedLoginCount(db, cutoff, func(q *gorm.DB) *gorm.DB {
			return q.Where("user_id = ?", *userID)
		})
		if err != nil {
			return state, err
		}
		if count >= maxFailedLoginAttemptsPerUser {
			state.Blocked = true
			state.RetryAfter = loginThrottleRetryAfter(now, lastAttemptAt)
			return state, nil
		}
	}

	return state, nil
}

func RecordBlockedLogin(db *gorm.DB, companyID *uint, userID *string, ipAddress, userAgent string) {
	_ = LogSecurityEvent(db, companyID, userID, blockedLoginEventType, ipAddress, userAgent, nil)
}

func recentFailedLoginCount(db *gorm.DB, cutoff time.Time, scope func(*gorm.DB) *gorm.DB) (int64, time.Time, error) {
	query := db.Model(&models.SecurityEvent{}).
		Where("event_type = ? AND created_at >= ?", loginEventType(false), cutoff)
	if scope != nil {
		query = scope(query)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, time.Time{}, err
	}
	if count == 0 {
		return 0, time.Time{}, nil
	}

	var event models.SecurityEvent
	if err := query.Select("created_at").Order("created_at desc").First(&event).Error; err != nil {
		return 0, time.Time{}, err
	}
	return count, event.CreatedAt, nil
}

func loginThrottleRetryAfter(now, lastAttemptAt time.Time) time.Duration {
	if lastAttemptAt.IsZero() {
		return failedLoginWindow
	}
	remaining := failedLoginWindow - now.Sub(lastAttemptAt)
	if remaining < time.Minute {
		return time.Minute
	}
	return remaining
}

func shouldTriggerUnusualIPAlert(db *gorm.DB, ctx LoginSecurityContext, userID *string) (bool, models.AlertChannel) {
	if !ctx.Success || userID == nil || *userID == "" || ctx.IPAddress == "" {
		return false, ""
	}

	enabled, channel, ok := unusualIPRuleState(db, ctx.CompanyID)
	if !ok || !enabled {
		return false, channel
	}

	base := db.Model(&models.SecurityEvent{}).
		Where("event_type = ? AND user_id = ?", loginEventType(true), *userID)
	if ctx.CompanyID != nil {
		base = base.Where("company_id = ?", *ctx.CompanyID)
	} else {
		base = base.Where("company_id IS NULL")
	}

	var priorSuccessCount int64
	if err := base.Count(&priorSuccessCount).Error; err != nil || priorSuccessCount == 0 {
		return false, channel
	}

	var sameIPCount int64
	if err := base.Where("ip_address = ?", ctx.IPAddress).Count(&sameIPCount).Error; err != nil {
		return false, channel
	}

	return sameIPCount == 0, channel
}

func unusualIPRuleState(db *gorm.DB, companyID *uint) (enabled bool, channel models.AlertChannel, ok bool) {
	sys, err := LoadSystemSecuritySettings(db)
	if err != nil {
		return false, "", false
	}
	channel = models.AlertChannelEmail

	if companyID == nil {
		return sys.UnusualIPLoginAlertDefaultEnabled, channel, true
	}
	if !sys.UnusualIPLoginCompanyOverrideAllowed {
		return sys.UnusualIPLoginAlertDefaultEnabled, channel, true
	}

	row, err := LoadCompanySecuritySettings(db, *companyID)
	if err != nil {
		return false, "", false
	}
	if row.UnusualIPLoginAlertChannel != "" {
		channel = row.UnusualIPLoginAlertChannel
	}
	return row.UnusualIPLoginAlertEnabled, channel, true
}

func marshalSecurityMetadata(v any) *string {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}
