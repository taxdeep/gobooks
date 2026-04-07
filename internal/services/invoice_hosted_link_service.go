// 遵循project_guide.md
package services

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gobooks/internal/models"
	"gorm.io/gorm"
)

// ErrNoActiveLink is returned when a company+invoice has no active hosted link.
var ErrNoActiveLink = errors.New("no active hosted link for this invoice")

// ErrInvalidHostedToken is returned for any token that cannot grant access:
// not found, revoked, or expired. Callers must not distinguish between these
// cases to avoid information leakage to unauthenticated callers.
var ErrInvalidHostedToken = errors.New("invalid or inaccessible hosted invoice link")

// ErrActiveLinkExists is returned by CreateHostedLink when an active link already exists.
var ErrActiveLinkExists = errors.New("an active hosted link already exists for this invoice; revoke it first or use RegenerateHostedLink")

// generateHostedToken generates a cryptographically random token suitable for
// hosted invoice share links.
//
// Returns:
//   - plaintext: base64url-encoded 32-byte random value (43 chars, 256-bit entropy)
//   - hash:      hex-encoded sha256(plaintext) (64 chars) — the value stored in the DB
func generateHostedToken() (plaintext, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("hosted token generation failed: %w", err)
	}
	plaintext = base64.RawURLEncoding.EncodeToString(b)
	hash = hashHostedToken(plaintext)
	return plaintext, hash, nil
}

// hashHostedToken returns hex-encoded sha256(plaintext).
// This is the canonical lookup key stored in the DB.
func hashHostedToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// CreateHostedLink generates and stores a new active hosted link for the given invoice.
//
// Returns the plaintext token — the only time it is visible. The caller is
// responsible for displaying it to the user once and discarding it.
// Returns ErrActiveLinkExists if an active link already exists.
// Company isolation is enforced: returns an error if the invoice does not
// belong to companyID.
func CreateHostedLink(db *gorm.DB, companyID, invoiceID uint, createdBy *uint) (plaintext string, link *models.InvoiceHostedLink, err error) {
	// Company isolation guard.
	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&inv).Error; err != nil {
		return "", nil, fmt.Errorf("invoice not found in company: %w", err)
	}

	// One active link per invoice — service-layer enforcement.
	var existing models.InvoiceHostedLink
	if db.Where("invoice_id = ? AND status = ?", invoiceID, models.InvoiceHostedLinkStatusActive).
		First(&existing).Error == nil {
		return "", nil, ErrActiveLinkExists
	}

	plaintext, hash, err := generateHostedToken()
	if err != nil {
		return "", nil, err
	}

	newLink := &models.InvoiceHostedLink{
		CompanyID: companyID,
		InvoiceID: invoiceID,
		TokenHash: hash,
		Status:    models.InvoiceHostedLinkStatusActive,
		CreatedBy: createdBy,
	}
	if err := db.Create(newLink).Error; err != nil {
		return "", nil, fmt.Errorf("create hosted link: %w", err)
	}
	return plaintext, newLink, nil
}

// RevokeHostedLink sets the active link for company+invoice to revoked.
// Returns ErrNoActiveLink when no active link exists.
func RevokeHostedLink(db *gorm.DB, companyID, invoiceID uint) error {
	now := time.Now()
	result := db.Model(&models.InvoiceHostedLink{}).
		Where("invoice_id = ? AND company_id = ? AND status = ?",
			invoiceID, companyID, models.InvoiceHostedLinkStatusActive).
		Updates(map[string]any{
			"status":     models.InvoiceHostedLinkStatusRevoked,
			"revoked_at": now,
			"updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("revoke hosted link: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNoActiveLink
	}
	return nil
}

// RegenerateHostedLink atomically revokes the current active link (if any) and
// creates a new one. Returns the new plaintext token.
//
// This is the safe rotation path: the old token stops working the moment the
// transaction commits, before the new token is shown to the user.
func RegenerateHostedLink(db *gorm.DB, companyID, invoiceID uint, createdBy *uint) (plaintext string, link *models.InvoiceHostedLink, err error) {
	// Company isolation guard (outside transaction — fail fast).
	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&inv).Error; err != nil {
		return "", nil, fmt.Errorf("invoice not found in company: %w", err)
	}

	plaintext, hash, err := generateHostedToken()
	if err != nil {
		return "", nil, err
	}

	var newLink *models.InvoiceHostedLink
	txErr := db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		// Revoke existing active link (no error if none exists — rotation always succeeds).
		tx.Model(&models.InvoiceHostedLink{}).
			Where("invoice_id = ? AND company_id = ? AND status = ?",
				invoiceID, companyID, models.InvoiceHostedLinkStatusActive).
			Updates(map[string]any{
				"status":     models.InvoiceHostedLinkStatusRevoked,
				"revoked_at": now,
				"updated_at": now,
			})

		newLink = &models.InvoiceHostedLink{
			CompanyID: companyID,
			InvoiceID: invoiceID,
			TokenHash: hash,
			Status:    models.InvoiceHostedLinkStatusActive,
			CreatedBy: createdBy,
		}
		return tx.Create(newLink).Error
	})
	if txErr != nil {
		return "", nil, fmt.Errorf("regenerate hosted link: %w", txErr)
	}
	return plaintext, newLink, nil
}

// GetActiveHostedLink returns the active hosted link for a company+invoice.
// Returns ErrNoActiveLink when none exists.
func GetActiveHostedLink(db *gorm.DB, companyID, invoiceID uint) (*models.InvoiceHostedLink, error) {
	var link models.InvoiceHostedLink
	err := db.Where("invoice_id = ? AND company_id = ? AND status = ?",
		invoiceID, companyID, models.InvoiceHostedLinkStatusActive).
		First(&link).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoActiveLink
		}
		return nil, err
	}
	return &link, nil
}

// ValidateHostedToken looks up a token and returns the link when it is valid
// and currently accessible (active, not expired).
//
// On any failure — not found, revoked, or expired — returns ErrInvalidHostedToken.
// Callers MUST NOT distinguish between failure reasons to avoid leaking
// information about whether a given invoice or link exists.
func ValidateHostedToken(db *gorm.DB, plaintext string) (*models.InvoiceHostedLink, error) {
	if plaintext == "" {
		return nil, ErrInvalidHostedToken
	}
	hash := hashHostedToken(plaintext)

	var link models.InvoiceHostedLink
	if err := db.Where("token_hash = ?", hash).First(&link).Error; err != nil {
		// Not found — return generic error (do not leak "not found" vs "revoked").
		return nil, ErrInvalidHostedToken
	}
	if !link.IsAccessible() {
		// Revoked or expired — same generic error.
		return nil, ErrInvalidHostedToken
	}
	return &link, nil
}

// RecordHostedLinkView increments ViewCount and updates LastViewedAt.
// Failures are best-effort: log callers should warn but not surface to the user.
func RecordHostedLinkView(db *gorm.DB, linkID uint) error {
	now := time.Now()
	return db.Model(&models.InvoiceHostedLink{}).
		Where("id = ?", linkID).
		Updates(map[string]any{
			"last_viewed_at": now,
			"view_count":     gorm.Expr("view_count + 1"),
			"updated_at":     now,
		}).Error
}
