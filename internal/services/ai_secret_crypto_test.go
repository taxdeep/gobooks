package services

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func TestAISecretEncryptionRoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	if err := ConfigureAISecretKey(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ConfigureAISecretKey("")
	})

	encrypted, err := encryptAISecret("super-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "super-secret-key" {
		t.Fatal("expected encrypted value to differ from plaintext")
	}
	if !strings.HasPrefix(encrypted, aiSecretPrefix) {
		t.Fatalf("expected encrypted value to have prefix %q", aiSecretPrefix)
	}

	decrypted, err := decryptAISecret(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != "super-secret-key" {
		t.Fatalf("expected decrypted secret to match original, got %q", decrypted)
	}
}

func TestEncryptAISecretRequiresConfiguredKey(t *testing.T) {
	if err := ConfigureAISecretKey(""); err != nil {
		t.Fatal(err)
	}

	if _, err := encryptAISecret("super-secret-key"); err == nil {
		t.Fatal("expected encryption to fail when AI secret key is not configured")
	}
}

func TestAIConnectionSettingsPersistEncryptedAPIKey(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	if err := ConfigureAISecretKey(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ConfigureAISecretKey("")
	})

	db, err := gorm.Open(sqlite.Open("file:ai_secret_settings?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.AIConnectionSettings{}); err != nil {
		t.Fatal(err)
	}

	if err := UpsertAIConnectionSettings(db, 42, models.AIProviderOpenAICompatible, "https://example.com/v1", "super-secret-key", "gpt-test", true, false); err != nil {
		t.Fatal(err)
	}

	var raw models.AIConnectionSettings
	if err := db.Where("company_id = ?", 42).First(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if raw.APIKey == "super-secret-key" {
		t.Fatal("expected stored API key to be encrypted at rest")
	}
	if !strings.HasPrefix(raw.APIKey, aiSecretPrefix) {
		t.Fatalf("expected stored API key to have encrypted prefix %q", aiSecretPrefix)
	}

	decrypted, err := LoadAIConnectionSettings(db, 42)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted.APIKey != "super-secret-key" {
		t.Fatalf("expected decrypted API key to match original, got %q", decrypted.APIKey)
	}
}
