package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"balanciz/internal/config"
	"balanciz/internal/models"
)

func testAdminDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:admin_security_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.SysadminUser{}, &models.SysadminSession{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAdminAccountChangePasswordRevokesExistingSessions(t *testing.T) {
	db := testAdminDB(t)
	s := &Server{DB: db, Cfg: config.Config{Env: "test"}}

	hash, err := bcrypt.GenerateFromPassword([]byte("current-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	user := &models.SysadminUser{
		Email:        "admin@example.com",
		PasswordHash: string(hash),
		IsActive:     true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.SysadminSession{
		SysadminUserID: user.ID,
		TokenHash:      "token-1",
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		CreatedAt:      time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.SysadminSession{
		SysadminUserID: user.ID,
		TokenHash:      "token-2",
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		CreatedAt:      time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	app.Post("/admin/account/change-password", func(c *fiber.Ctx) error {
		c.Locals(LocalsAdminUser, user)
		return s.handleAdminAccountChangePassword(c)
	})

	form := url.Values{}
	form.Set("current_password", "current-password")
	form.Set("new_password", "new-password-123")
	form.Set("confirm_password", "new-password-123")

	req := httptest.NewRequest(http.MethodPost, "/admin/account/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/admin/account?flash=password_changed" {
		t.Fatalf("expected redirect to account page, got %q", got)
	}

	var sessionCount int64
	if err := db.Model(&models.SysadminSession{}).Where("sysadmin_user_id = ?", user.ID).Count(&sessionCount).Error; err != nil {
		t.Fatal(err)
	}
	if sessionCount != 1 {
		t.Fatalf("expected exactly one fresh sysadmin session, got %d", sessionCount)
	}
}
