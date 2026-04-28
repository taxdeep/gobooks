// 遵循project_guide.md
package web

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

// maxLogoSize is the hard upper limit for uploaded logo files (2 MB).
const maxLogoSize = 2 * 1024 * 1024

// logoErrorMessage maps short error codes (used in redirect query params) to
// human-readable messages rendered on the profile page.
func logoErrorMessage(code string) string {
	switch code {
	case "required":
		return "Please select a file to upload."
	case "size":
		return fmt.Sprintf("File is too large. Maximum size is %d MB.", maxLogoSize/1024/1024)
	case "type":
		return "Only JPG and PNG files are accepted."
	default:
		return "Could not save the logo. Please try again."
	}
}

// handleCompanyLogoUpload handles POST /settings/company/profile/logo.
//
// Validation pipeline:
//  1. File must be present.
//  2. Size must be ≤ maxLogoSize (checked via Content-Length before reading).
//  3. Actual content type is detected from the first 512 bytes (magic bytes),
//     not the browser-supplied Content-Type header which can be spoofed.
//  4. Only image/jpeg and image/png are accepted.
//
// Storage: data/{companyID}/profile/logo.{ext}  (relative to working directory).
// Any previous logo file is removed before the new one is written.
func (s *Server) handleCompanyLogoUpload(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	redirect := func(errCode string) error {
		return c.Redirect("/settings/company/profile?logo_error="+errCode, fiber.StatusSeeOther)
	}

	fh, err := c.FormFile("logo")
	if err != nil || fh == nil {
		return redirect("required")
	}
	if fh.Size > maxLogoSize {
		return redirect("size")
	}

	f, err := fh.Open()
	if err != nil {
		return redirect("save")
	}
	defer f.Close()

	// Read first 512 bytes to detect the true content type via magic bytes.
	// This is the only trustworthy check — browser Content-Type can be spoofed.
	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return redirect("save")
	}
	ct := http.DetectContentType(header[:n])

	var ext string
	switch ct {
	case "image/jpeg":
		ext = "jpg"
	case "image/png":
		ext = "png"
	default:
		return redirect("type")
	}

	// Seek back to the beginning so we can copy the full file.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return redirect("save")
	}

	// Create the storage directory (idempotent).
	dir := filepath.Join("data", fmt.Sprintf("%d", companyID), "profile")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return redirect("save")
	}

	// Remove any pre-existing logo before writing the new one.
	// Only one logo file exists per company at any time.
	for _, old := range []string{"logo.jpg", "logo.png"} {
		_ = os.Remove(filepath.Join(dir, old))
	}

	dstPath := filepath.Join(dir, "logo."+ext)
	dst, err := os.Create(dstPath)
	if err != nil {
		return redirect("save")
	}

	if _, err := io.Copy(dst, f); err != nil {
		dst.Close()
		_ = os.Remove(dstPath)
		return redirect("save")
	}
	dst.Close()

	// Persist the relative path in the database.
	if err := s.DB.Model(&models.Company{}).
		Where("id = ?", companyID).
		Update("logo_path", dstPath).Error; err != nil {
		_ = os.Remove(dstPath)
		return redirect("save")
	}

	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "settings.company.logo.uploaded", "company", companyID, actor, map[string]any{
		"company_id": companyID,
		"format":     ext,
	}, &cid, &uid)

	return c.Redirect("/settings/company/profile?saved=1", fiber.StatusSeeOther)
}

// handleCompanyLogoServe handles GET /company/logo.
//
// Anti-hotlinking: this route sits behind RequireAuth + ResolveActiveCompany +
// RequireMembership middleware. Unauthenticated requests (e.g. <img> tags on
// external sites) receive a 302 to /login instead of the image.
//
// The response carries:
//   - Cache-Control: private (prevents shared/CDN caching)
//   - X-Content-Type-Options: nosniff
//   - Content-Disposition: inline (display in browser, not download)
//
// A path-traversal guard verifies that the stored logo_path resolves to the
// expected data/{companyID}/profile/ directory before any file is read.
func (s *Server) handleCompanyLogoServe(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.SendStatus(fiber.StatusNotFound)
	}

	var company models.Company
	if err := s.DB.Select("logo_path").
		Where("id = ? AND logo_path != ''", companyID).
		First(&company).Error; err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	// Path-traversal guard: resolve both paths to absolute form and confirm
	// the logo file lives inside the expected per-company directory.
	expectedDir, err := filepath.Abs(filepath.Join("data", fmt.Sprintf("%d", companyID), "profile"))
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	absLogo, err := filepath.Abs(company.LogoPath)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	// Use separator-terminated prefix to prevent /profile1 from matching /profile.
	if !strings.HasPrefix(absLogo, expectedDir+string(filepath.Separator)) &&
		absLogo != expectedDir {
		return c.SendStatus(fiber.StatusNotFound)
	}

	data, err := os.ReadFile(absLogo)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	// Re-verify the stored file's actual content type from magic bytes.
	// Rejects any file that was somehow written with a wrong extension.
	ct := http.DetectContentType(data)
	if ct != "image/jpeg" && ct != "image/png" {
		return c.SendStatus(fiber.StatusNotFound)
	}

	c.Set(fiber.HeaderContentType, ct)
	c.Set(fiber.HeaderCacheControl, "private, max-age=3600")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set(fiber.HeaderContentDisposition, "inline")
	return c.Send(data)
}
