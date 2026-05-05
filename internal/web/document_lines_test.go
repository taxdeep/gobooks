package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
)

func TestParseDocumentLinesSkipsDefaultBlankRows(t *testing.T) {
	app := fiber.New()
	var got []documentLine
	app.Post("/", func(c *fiber.Ctx) error {
		got = parseDocumentLines(c)
		return nil
	})

	form := url.Values{}
	form.Set("line_qty_0", "1")
	form.Set("line_price_0", "0.00")
	form.Set("line_product_service_id_1", "7")
	form.Set("line_description_1", "Test Parts")
	form.Set("line_qty_1", "1")
	form.Set("line_price_1", "12.00")

	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := app.Test(req); err != nil {
		t.Fatalf("submit form: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("lines: got %d want 1", len(got))
	}
	if got[0].ProductServiceID == nil || *got[0].ProductServiceID != 7 {
		t.Fatalf("product id: got %v want 7", got[0].ProductServiceID)
	}
	if !got[0].UnitPrice.Equal(decimal.RequireFromString("12.00")) {
		t.Fatalf("unit price: got %s want 12.00", got[0].UnitPrice)
	}
}
