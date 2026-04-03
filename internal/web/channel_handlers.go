// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── Channel Accounts ─────────────────────────────────────────────────────────

func (s *Server) handleChannelAccounts(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	accounts, _ := services.ListChannelAccounts(s.DB, companyID)

	vm := pages.ChannelAccountsVM{
		HasCompany: true,
		Accounts:   accounts,
		Created:    c.Query("created") == "1",
		Updated:    c.Query("updated") == "1",
		Deleted:    c.Query("deleted") == "1",
	}
	return pages.ChannelAccounts(vm).Render(c.Context(), c)
}

func (s *Server) handleChannelAccountCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	channelType := strings.TrimSpace(c.FormValue("channel_type"))
	displayName := strings.TrimSpace(c.FormValue("display_name"))
	region := strings.TrimSpace(c.FormValue("region"))
	extRef := strings.TrimSpace(c.FormValue("external_account_ref"))

	if channelType == "" || displayName == "" {
		accounts, _ := services.ListChannelAccounts(s.DB, companyID)
		return pages.ChannelAccounts(pages.ChannelAccountsVM{
			HasCompany: true, Accounts: accounts,
			FormError: "Channel type and display name are required.",
		}).Render(c.Context(), c)
	}

	acct := models.SalesChannelAccount{
		CompanyID:   companyID,
		ChannelType: models.ChannelType(channelType),
		DisplayName: displayName,
		Region:      region,
		AuthStatus:  models.ChannelAuthPending,
		IsActive:    true,
	}
	if extRef != "" {
		acct.ExternalAccountRef = &extRef
	}

	if err := services.CreateChannelAccount(s.DB, &acct); err != nil {
		accounts, _ := services.ListChannelAccounts(s.DB, companyID)
		return pages.ChannelAccounts(pages.ChannelAccountsVM{
			HasCompany: true, Accounts: accounts,
			FormError: "Could not create channel account. Please try again.",
		}).Render(c.Context(), c)
	}
	return c.Redirect("/settings/channels?created=1", fiber.StatusSeeOther)
}

func (s *Server) handleChannelAccountDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.FormValue("account_id"))
	id64, _ := strconv.ParseUint(idRaw, 10, 64)
	if id64 > 0 {
		if err := services.DeleteChannelAccount(s.DB, companyID, uint(id64)); err != nil {
			return c.Redirect("/settings/channels?deleteerror=1", fiber.StatusSeeOther)
		}
	}
	return c.Redirect("/settings/channels?deleted=1", fiber.StatusSeeOther)
}

// ── Item Channel Mappings ────────────────────────────────────────────────────

func (s *Server) handleChannelMappings(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	mappings, _ := services.ListItemMappings(s.DB, companyID)
	accounts, _ := services.ListChannelAccounts(s.DB, companyID)

	var items []models.ProductService
	s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name ASC").Find(&items)

	vm := pages.ChannelMappingsVM{
		HasCompany: true,
		Mappings:   mappings,
		Accounts:   accounts,
		Items:      items,
		Created:    c.Query("created") == "1",
	}
	return pages.ChannelMappings(vm).Render(c.Context(), c)
}

func (s *Server) handleChannelMappingCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	acctIDRaw := strings.TrimSpace(c.FormValue("channel_account_id"))
	itemIDRaw := strings.TrimSpace(c.FormValue("item_id"))
	externalSKU := strings.TrimSpace(c.FormValue("external_sku"))
	marketplaceID := strings.TrimSpace(c.FormValue("marketplace_id"))
	asin := strings.TrimSpace(c.FormValue("asin"))

	acctID, _ := strconv.ParseUint(acctIDRaw, 10, 64)
	itemID, _ := strconv.ParseUint(itemIDRaw, 10, 64)

	if acctID == 0 || itemID == 0 || externalSKU == "" {
		return c.Redirect("/settings/channels/mappings", fiber.StatusSeeOther)
	}

	// Load channel account to get type.
	acct, err := services.GetChannelAccount(s.DB, companyID, uint(acctID))
	if err != nil {
		return c.Redirect("/settings/channels/mappings", fiber.StatusSeeOther)
	}

	m := models.ItemChannelMapping{
		CompanyID:        companyID,
		ItemID:           uint(itemID),
		ChannelAccountID: uint(acctID),
		ChannelType:      acct.ChannelType,
		ExternalSKU:      externalSKU,
		IsActive:         true,
	}
	if marketplaceID != "" {
		m.MarketplaceID = &marketplaceID
	}
	if asin != "" {
		m.ASIN = &asin
	}

	services.CreateItemMapping(s.DB, &m)
	return c.Redirect("/settings/channels/mappings?created=1", fiber.StatusSeeOther)
}

// ── Channel Orders ───────────────────────────────────────────────────────────

func (s *Server) handleChannelOrders(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	summaries, _ := services.ListChannelOrderSummaries(s.DB, companyID, 50)
	accounts, _ := services.ListChannelAccounts(s.DB, companyID)

	vm := pages.ChannelOrdersVM{
		HasCompany: true,
		Orders:     summaries,
		Accounts:   accounts,
		Created:    c.Query("created") == "1",
	}
	return pages.ChannelOrders(vm).Render(c.Context(), c)
}

func (s *Server) handleChannelOrderCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	acctIDRaw := strings.TrimSpace(c.FormValue("channel_account_id"))
	extOrderID := strings.TrimSpace(c.FormValue("external_order_id"))
	orderDateRaw := strings.TrimSpace(c.FormValue("order_date"))
	currencyCode := strings.TrimSpace(c.FormValue("currency_code"))
	lineCountRaw := strings.TrimSpace(c.FormValue("line_count"))

	acctID, _ := strconv.ParseUint(acctIDRaw, 10, 64)
	if acctID == 0 {
		return c.Redirect("/settings/channels/orders", fiber.StatusSeeOther)
	}

	orderDate := time.Now()
	if d, err := time.Parse("2006-01-02", orderDateRaw); err == nil {
		orderDate = d
	}

	order := models.ChannelOrder{
		CompanyID:        companyID,
		ChannelAccountID: uint(acctID),
		ExternalOrderID:  extOrderID,
		OrderDate:        &orderDate,
		OrderStatus:      "imported",
		CurrencyCode:     currencyCode,
		RawPayload:       datatypes.JSON("{}"),
	}

	lineCount, _ := strconv.Atoi(lineCountRaw)
	var lines []models.ChannelOrderLine
	for i := 0; i < lineCount; i++ {
		sku := strings.TrimSpace(c.FormValue(strings.Replace("line_sku[%d]", "%d", strconv.Itoa(i), 1)))
		qtyRaw := strings.TrimSpace(c.FormValue(strings.Replace("line_qty[%d]", "%d", strconv.Itoa(i), 1)))
		priceRaw := strings.TrimSpace(c.FormValue(strings.Replace("line_price[%d]", "%d", strconv.Itoa(i), 1)))

		if sku == "" {
			continue
		}

		qty, _ := decimal.NewFromString(qtyRaw)
		price, _ := decimal.NewFromString(priceRaw)

		lines = append(lines, models.ChannelOrderLine{
			ExternalSKU: sku,
			Quantity:    qty,
			ItemPrice:   &price,
			RawPayload:  datatypes.JSON("{}"),
		})
	}

	services.CreateChannelOrderWithLines(s.DB, &order, lines)
	return c.Redirect("/settings/channels/orders?created=1", fiber.StatusSeeOther)
}

func (s *Server) handleChannelOrderDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id64 == 0 {
		return c.Redirect("/settings/channels/orders", fiber.StatusSeeOther)
	}

	order, err := services.GetChannelOrder(s.DB, companyID, uint(id64))
	if err != nil {
		return c.Redirect("/settings/channels/orders", fiber.StatusSeeOther)
	}

	orderLines, _ := services.GetChannelOrderLines(s.DB, companyID, order.ID)

	// Check conversion eligibility.
	convertErr := services.ValidateChannelOrderConvertible(s.DB, companyID, order.ID)

	var customers []models.Customer
	if convertErr == nil {
		s.DB.Where("company_id = ?", companyID).Order("name ASC").Find(&customers)
	}

	// Suggest next invoice number.
	nextNo, _ := services.SuggestNextInvoiceNumber(s.DB, companyID)

	vm := pages.ChannelOrderDetailVM{
		HasCompany:         true,
		Order:              *order,
		Lines:              orderLines,
		IsConvertible:      convertErr == nil,
		ConvertedInvoiceID: order.ConvertedInvoiceID,
		Customers:          customers,
		InvoiceNumber:      nextNo,
		Converted:          c.Query("converted") == "1",
	}
	if convertErr != nil {
		vm.ConvertibleError = convertErr.Error()
	}

	return pages.ChannelOrderDetail(vm).Render(c.Context(), c)
}

func (s *Server) handleChannelOrderConvert(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id64 == 0 {
		return c.Redirect("/settings/channels/orders", fiber.StatusSeeOther)
	}
	orderID := uint(id64)

	customerIDRaw := strings.TrimSpace(c.FormValue("customer_id"))
	invoiceNumber := strings.TrimSpace(c.FormValue("invoice_number"))

	custID, _ := strconv.ParseUint(customerIDRaw, 10, 64)
	if custID == 0 || invoiceNumber == "" {
		return c.Redirect("/settings/channels/orders/"+c.Params("id")+"?error=customer+and+invoice+number+required", fiber.StatusSeeOther)
	}

	result, err := services.ConvertChannelOrderToDraftInvoice(s.DB, services.ConvertOptions{
		CompanyID:      companyID,
		ChannelOrderID: orderID,
		CustomerID:     uint(custID),
		InvoiceNumber:  invoiceNumber,
		InvoiceDate:    time.Now(),
	})
	if err != nil {
		return c.Redirect("/settings/channels/orders/"+c.Params("id")+"?error="+err.Error(), fiber.StatusSeeOther)
	}

	_ = result
	return c.Redirect("/settings/channels/orders/"+c.Params("id")+"?converted=1", fiber.StatusSeeOther)
}
