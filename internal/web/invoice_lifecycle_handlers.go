// 遵循project_guide.md
package web

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gobooks/internal/services"
)

// handleInvoiceIssue transitions an invoice from draft to issued.
// POST /invoices/:id/issue
func (s *Server) handleInvoiceIssue(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	_, err = services.IssueInvoice(s.DB, companyID, invoiceID)
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), err.Error())
	}

	return redirectTo(c, fmt.Sprintf("/invoices/%d?issued=1", invoiceID))
}

// handleInvoiceSend transitions an invoice from issued to sent.
// POST /invoices/:id/send
func (s *Server) handleInvoiceSend(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	_, err = services.SendInvoice(s.DB, companyID, invoiceID)
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), err.Error())
	}

	return redirectTo(c, fmt.Sprintf("/invoices/%d?sent=1", invoiceID))
}

// handleInvoiceMarkPaid transitions an invoice to paid.
// POST /invoices/:id/mark-paid
func (s *Server) handleInvoiceMarkPaid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	_, err = services.MarkInvoicePaid(s.DB, companyID, invoiceID)
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), err.Error())
	}

	return redirectTo(c, fmt.Sprintf("/invoices/%d?paid=1", invoiceID))
}

// handleInvoiceVoid voids an invoice and creates a reversal JE.
// POST /invoices/:id/void
func (s *Server) handleInvoiceVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	user := UserFromCtx(c)
	var userID *uuid.UUID
	actor := "system"
	if user != nil {
		uid := user.ID
		userID = &uid
		if user.Email != "" {
			actor = user.Email
		}
	}

	if err := services.VoidInvoice(s.DB, companyID, invoiceID, actor, userID); err != nil {
		return c.Redirect(
			fmt.Sprintf("/invoices/%d?voiderror=%s", invoiceID, url.QueryEscape(err.Error())),
			fiber.StatusSeeOther,
		)
	}

	return redirectTo(c, fmt.Sprintf("/invoices/%d?voided=1", invoiceID))
}

// handleInvoicePost explicitly posts an invoice to accounting.
// POST /invoices/:id/post
func (s *Server) handleInvoicePost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	user := UserFromCtx(c)
	actor := "system"
	var uid *uuid.UUID
	if user != nil {
		u := user.ID
		uid = &u
		if user.Email != "" {
			actor = user.Email
		}
	}

	if err := services.PostInvoice(s.DB, companyID, invoiceID, actor, uid); err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), "Could not post: "+err.Error())
	}

	return redirectTo(c, fmt.Sprintf("/invoices/%d?issued=1", invoiceID))
}

// handleInvoiceDelete deletes a draft invoice.
// POST /invoices/:id/delete
func (s *Server) handleInvoiceDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	user := UserFromCtx(c)
	var userID *uuid.UUID
	actor := "system"
	if user != nil {
		uid := user.ID
		userID = &uid
		if user.Email != "" {
			actor = user.Email
		}
	}

	if err := services.DeleteInvoice(s.DB, companyID, invoiceID, actor, userID); err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), err.Error())
	}

	return redirectTo(c, "/invoices?deleted=1")
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func parseInvoiceID(c *fiber.Ctx) (uint, error) {
	idStr := c.Params("id")
	id64, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint(id64), nil
}

func redirectTo(c *fiber.Ctx, path string) error {
	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", path)
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect(path, fiber.StatusSeeOther)
}

func redirectErr(c *fiber.Ctx, path, errMsg string) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return c.Redirect(path+sep+"error="+url.QueryEscape(errMsg), fiber.StatusSeeOther)
}
