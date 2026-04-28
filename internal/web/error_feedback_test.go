package web

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

func testErrorFeedbackDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_error_feedback_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.User{},
		&models.CompanyMembership{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.NumberingSetting{},
		&models.CompanyNotificationSettings{},
		&models.SystemNotificationSettings{},
		&models.PaymentGatewayAccount{},
		&models.PaymentAccountingMapping{},
		&models.PaymentRequest{},
		&models.PaymentTransaction{},
		&models.ProductService{},
		&models.ItemComponent{},
		&models.ItemChannelMapping{},
		&models.SalesChannelAccount{},
		&models.ChannelAccountingMapping{},
		&models.ChannelOrder{},
		&models.ChannelOrderLine{},
		&models.ChannelSettlement{},
		&models.ChannelSettlementLine{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedErrorFeedbackUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()

	user := &models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("%s@example.com", t.Name()),
		PasswordHash: "not-used",
		DisplayName:  "Error Feedback Test",
		IsActive:     true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

func errorFeedbackApp(server *Server, user *models.User, companyID uint) *fiber.App {
	app := fiber.New()
	membership := &models.CompanyMembership{Role: models.CompanyRoleAdmin}
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsUser, user)
		c.Locals(LocalsActiveCompanyID, companyID)
		c.Locals(LocalsCompanyMembership, membership)
		return c.Next()
	})

	app.Get("/invoices", server.handleInvoices)
	app.Get("/invoices/:id", server.handleInvoiceDetail)
	app.Post("/invoices/:id/issue", server.handleInvoiceIssue)
	app.Post("/invoices/:id/request-payment", server.handleInvoiceRequestPayment)

	app.Get("/settings/payment-gateways", server.handlePaymentGateways)
	app.Get("/settings/payment-gateways/mappings", server.handlePaymentMappings)
	app.Get("/settings/payment-gateways/requests", server.handlePaymentRequests)
	app.Post("/settings/payment-gateways/requests", server.handlePaymentRequestCreate)
	app.Get("/settings/payment-gateways/transactions", server.handlePaymentTransactions)
	app.Post("/settings/payment-gateways/transactions", server.handlePaymentTransactionCreate)
	app.Post("/settings/payment-gateways/transactions/:id/post", server.handlePaymentTransactionPost)
	app.Post("/settings/payment-gateways/transactions/:id/apply", server.handlePaymentTransactionApply)
	app.Post("/settings/payment-gateways/transactions/:id/unapply", server.handlePaymentTransactionUnapply)
	app.Post("/settings/payment-gateways/transactions/:id/apply-refund", server.handlePaymentTransactionApplyRefund)

	app.Get("/products-services", server.handleProductServices)
	app.Post("/products-services/update", server.handleProductServiceUpdate)
	app.Post("/products-services/inactive", server.handleProductServiceInactive)

	app.Get("/settings/channels", server.handleChannelAccounts)
	app.Post("/settings/channels/delete", server.handleChannelAccountDelete)
	app.Get("/settings/channels/accounting", server.handleAccountingMappings)
	app.Post("/settings/channels/accounting", server.handleAccountingMappingSave)
	app.Get("/settings/channels/settlements/:id", server.handleSettlementDetail)
	app.Post("/settings/channels/settlements/:id/reverse-fee", server.handleSettlementReverseFee)
	app.Post("/settings/channels/settlements/:id/reverse-payout", server.handleSettlementReversePayout)
	app.Get("/settings/channels/mappings", server.handleChannelMappings)
	app.Post("/settings/channels/mappings", server.handleChannelMappingCreate)
	app.Get("/settings/channels/orders", server.handleChannelOrders)
	app.Post("/settings/channels/orders", server.handleChannelOrderCreate)
	app.Get("/settings/channels/orders/:id", server.handleChannelOrderDetail)
	app.Post("/settings/channels/orders/:id/convert", server.handleChannelOrderConvert)
	app.Get("/reports/clearing", server.handleClearingReport)

	return app
}

func TestInvoiceIssueFailureShowsSpecificReason(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Invoice Issue Error Co")
	customerID := seedValidationCustomer(t, db, companyID, "Issue Customer")
	invoice := models.Invoice{
		CompanyID:     companyID,
		CustomerID:    customerID,
		InvoiceNumber: "INV-ISSUE-ERR-001",
		InvoiceDate:   time.Now().UTC(),
		Status:        models.InvoiceStatusDraft,
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}

	app := errorFeedbackApp(server, user, companyID)
	resp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/invoices/%d/issue", invoice.ID), url.Values{}, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if !strings.Contains(location, fmt.Sprintf("/invoices/%d?error=", invoice.ID)) {
		t.Fatalf("expected redirect with error, got %q", location)
	}
	decodedLocation, err := url.QueryUnescape(location)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decodedLocation, "Could not issue invoice: invoice must have at least one line item") {
		t.Fatalf("expected specific issue reason in redirect, got %q", decodedLocation)
	}

	page := performRequest(t, app, location, "")
	body := readResponseBody(t, page)
	if !strings.Contains(body, "invoice must have at least one line item") {
		t.Fatalf("expected specific issue reason in page banner, got %q", body)
	}
	if strings.Contains(body, ">Could not issue invoice.<") {
		t.Fatalf("generic issue error should not be shown alone, got %q", body)
	}
}

func readResponseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestInvoiceRequestPaymentErrorShowsOnDetailPage(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Invoice Error Co")
	if err := db.Model(&models.Company{}).Where("id = ?", companyID).Update("base_currency_code", "USD").Error; err != nil {
		t.Fatal(err)
	}
	customerID := seedValidationCustomer(t, db, companyID, "Customer A")
	invoice := models.Invoice{
		CompanyID:     companyID,
		CustomerID:    customerID,
		InvoiceNumber: "INV-FX-001",
		InvoiceDate:   time.Now().UTC(),
		Status:        models.InvoiceStatusIssued,
		CurrencyCode:  "EUR",
		Amount:        decimal.RequireFromString("100.00"),
		BalanceDue:    decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}
	gateway := models.PaymentGatewayAccount{
		CompanyID: companyID, ProviderType: models.ProviderStripe,
		DisplayName: "Stripe", AuthStatus: "active", WebhookStatus: "ready", IsActive: true,
	}
	if err := db.Create(&gateway).Error; err != nil {
		t.Fatal(err)
	}

	app := errorFeedbackApp(server, user, companyID)
	resp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/invoices/%d/request-payment", invoice.ID), url.Values{
		"gateway_account_id": {fmt.Sprintf("%d", gateway.ID)},
	}, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if !strings.Contains(location, fmt.Sprintf("/invoices/%d?error=", invoice.ID)) {
		t.Fatalf("expected redirect with error, got %q", location)
	}

	page := performRequest(t, app, location, "")
	body := readResponseBody(t, page)
	if !strings.Contains(body, "foreign-currency invoices cannot use the payment gateway in this version") {
		t.Fatalf("expected request payment error banner, got %q", body)
	}
}

func TestPaymentGatewayTransactionFailuresShowErrorBanner(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Gateway Error Co")
	app := errorFeedbackApp(server, user, companyID)

	gateway := models.PaymentGatewayAccount{
		CompanyID: companyID, ProviderType: models.ProviderStripe,
		DisplayName: "Stripe", AuthStatus: "active", WebhookStatus: "ready", IsActive: true,
	}
	if err := db.Create(&gateway).Error; err != nil {
		t.Fatal(err)
	}

	postedJEID := uint(1)
	feeTxn := models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: gateway.ID, TransactionType: models.TxnTypeFee,
		Amount: decimal.RequireFromString("10.00"), CurrencyCode: "USD", Status: "completed",
		ExternalTxnRef: "txn-fee", RawPayload: datatypes.JSON("{}"),
	}
	applyTxn := models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: gateway.ID, TransactionType: models.TxnTypeCharge,
		Amount: decimal.RequireFromString("15.00"), CurrencyCode: "USD", Status: "completed",
		ExternalTxnRef: "txn-apply", RawPayload: datatypes.JSON("{}"),
	}
	unapplyTxn := models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: gateway.ID, TransactionType: models.TxnTypeCharge,
		Amount: decimal.RequireFromString("20.00"), CurrencyCode: "USD", Status: "completed",
		ExternalTxnRef: "txn-unapply", RawPayload: datatypes.JSON("{}"), PostedJournalEntryID: &postedJEID,
	}
	refundTxn := models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: gateway.ID, TransactionType: models.TxnTypeCharge,
		Amount: decimal.RequireFromString("25.00"), CurrencyCode: "USD", Status: "completed",
		ExternalTxnRef: "txn-refund", RawPayload: datatypes.JSON("{}"), PostedJournalEntryID: &postedJEID,
	}
	for _, txn := range []*models.PaymentTransaction{&feeTxn, &applyTxn, &unapplyTxn, &refundTxn} {
		if err := db.Create(txn).Error; err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name       string
		path       string
		wantInBody string
	}{
		{name: "post", path: fmt.Sprintf("/settings/payment-gateways/transactions/%d/post", feeTxn.ID), wantInBody: "clearing account not configured"},
		{name: "apply", path: fmt.Sprintf("/settings/payment-gateways/transactions/%d/apply", applyTxn.ID), wantInBody: "not yet posted"},
		{name: "unapply", path: fmt.Sprintf("/settings/payment-gateways/transactions/%d/unapply", unapplyTxn.ID), wantInBody: "not applied"},
		{name: "refund", path: fmt.Sprintf("/settings/payment-gateways/transactions/%d/apply-refund", refundTxn.ID), wantInBody: "only refund transactions can be refund-applied"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := performFormRequest(t, app, http.MethodPost, tc.path, nil, "")
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
			}
			location := resp.Header.Get("Location")
			if !strings.Contains(location, "/settings/payment-gateways/transactions?error=") {
				t.Fatalf("expected redirect with error, got %q", location)
			}

			page := performRequest(t, app, location, "")
			body := readResponseBody(t, page)
			if !strings.Contains(body, tc.wantInBody) {
				t.Fatalf("expected body to contain %q, got %q", tc.wantInBody, body)
			}
		})
	}
}

func TestPaymentGatewayPagesShowErrorBanners(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Gateway Page Error Co")
	app := errorFeedbackApp(server, user, companyID)

	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "gateways", path: "/settings/payment-gateways?error=provider+type+required", want: "provider type required"},
		{name: "mappings", path: "/settings/payment-gateways/mappings?error=gateway+account+is+required", want: "gateway account is required"},
		{name: "requests", path: "/settings/payment-gateways/requests?error=amount+must+be+a+valid+number", want: "amount must be a valid number"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := performRequest(t, app, tc.path, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
			}
			body := readResponseBody(t, resp)
			if !strings.Contains(body, tc.want) {
				t.Fatalf("expected body to contain %q, got %q", tc.want, body)
			}
		})
	}
}

func TestPaymentTransactionCreateValidationShowsErrorInsteadOfFalseSuccess(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Gateway Create Error Co")
	app := errorFeedbackApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/settings/payment-gateways/transactions", url.Values{
		"transaction_type": {"charge"},
		"amount":           {"10.00"},
		"currency_code":    {"USD"},
	}, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if got, want := location, "/settings/payment-gateways/transactions?error=gateway+account+is+required"; got != want {
		t.Fatalf("expected redirect to %q, got %q", want, got)
	}

	var count int64
	if err := db.Model(&models.PaymentTransaction{}).Where("company_id = ?", companyID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no transaction created, got %d", count)
	}

	page := performRequest(t, app, location, "")
	body := readResponseBody(t, page)
	if !strings.Contains(body, "gateway account is required") {
		t.Fatalf("expected create error banner, got %q", body)
	}
}

func TestChannelCreateAndConvertErrorsShowBanners(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Channel Error Co")
	app := errorFeedbackApp(server, user, companyID)

	channel := models.SalesChannelAccount{
		CompanyID: companyID, ChannelType: models.ChannelTypeAmazon,
		DisplayName: "Amazon", AuthStatus: models.ChannelAuthConnected, IsActive: true,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	item := models.ProductService{
		CompanyID: companyID, Name: "Widget", Type: models.ProductServiceTypeService, IsActive: true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	existing := models.ItemChannelMapping{
		CompanyID: companyID, ItemID: item.ID, ChannelAccountID: channel.ID,
		ChannelType: channel.ChannelType, ExternalSKU: "SKU-001", IsActive: true,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}

	mappingResp := performFormRequest(t, app, http.MethodPost, "/settings/channels/mappings", url.Values{
		"channel_account_id": {fmt.Sprintf("%d", channel.ID)},
		"item_id":            {fmt.Sprintf("%d", item.ID)},
		"external_sku":       {"SKU-001"},
	}, "")
	if mappingResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, mappingResp.StatusCode)
	}
	mappingLocation := mappingResp.Header.Get("Location")
	if !strings.Contains(mappingLocation, "/settings/channels/mappings?error=") {
		t.Fatalf("expected mapping error redirect, got %q", mappingLocation)
	}
	mappingPage := performRequest(t, app, mappingLocation, "")
	if body := readResponseBody(t, mappingPage); !strings.Contains(body, "an active mapping already exists") {
		t.Fatalf("expected mapping error banner, got %q", body)
	}

	orderErrorResp := performFormRequest(t, app, http.MethodPost, "/settings/channels/orders", url.Values{
		"external_order_id": {"ORDER-ERR"},
		"line_count":        {"0"},
	}, "")
	if orderErrorResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, orderErrorResp.StatusCode)
	}
	orderErrorLocation := orderErrorResp.Header.Get("Location")
	if got, want := orderErrorLocation, "/settings/channels/orders?error=channel+account+is+required"; got != want {
		t.Fatalf("expected redirect to %q, got %q", want, got)
	}
	orderErrorPage := performRequest(t, app, orderErrorLocation, "")
	if body := readResponseBody(t, orderErrorPage); !strings.Contains(body, "channel account is required") {
		t.Fatalf("expected order create error banner, got %q", body)
	}

	orderResp := performFormRequest(t, app, http.MethodPost, "/settings/channels/orders", url.Values{
		"channel_account_id": {fmt.Sprintf("%d", channel.ID)},
		"external_order_id":  {"ORDER-001"},
		"order_date":         {"2026-04-03"},
		"currency_code":      {"USD"},
		"line_count":         {"1"},
		"line_sku[0]":        {"SKU-NEW"},
		"line_qty[0]":        {"1"},
		"line_price[0]":      {"12.00"},
	}, "")
	if orderResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, orderResp.StatusCode)
	}
	if got, want := orderResp.Header.Get("Location"), "/settings/channels/orders?created=1"; got != want {
		t.Fatalf("expected redirect to %q, got %q", want, got)
	}

	var order models.ChannelOrder
	if err := db.Where("company_id = ? AND external_order_id = ?", companyID, "ORDER-001").First(&order).Error; err != nil {
		t.Fatal(err)
	}

	convertResp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/settings/channels/orders/%d/convert", order.ID), url.Values{}, "")
	if convertResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, convertResp.StatusCode)
	}
	convertLocation := convertResp.Header.Get("Location")
	if !strings.Contains(convertLocation, fmt.Sprintf("/settings/channels/orders/%d?error=", order.ID)) {
		t.Fatalf("expected convert error redirect, got %q", convertLocation)
	}
	convertPage := performRequest(t, app, convertLocation, "")
	if body := readResponseBody(t, convertPage); !strings.Contains(body, "customer and invoice number required") {
		t.Fatalf("expected convert error banner, got %q", body)
	}
}

func TestChannelOrderConvertServiceErrorIsEscapedAndDisplayed(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Channel Convert Service Error Co")
	app := errorFeedbackApp(server, user, companyID)

	channel := models.SalesChannelAccount{
		CompanyID: companyID, ChannelType: models.ChannelTypeAmazon,
		DisplayName: "Amazon", AuthStatus: models.ChannelAuthConnected, IsActive: true,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	customerID := seedValidationCustomer(t, db, companyID, "Convert Customer")
	convertedInvoiceID := uint(77)
	order := models.ChannelOrder{
		CompanyID: companyID, ChannelAccountID: channel.ID,
		ExternalOrderID: "ORDER-CONVERTED", OrderStatus: "imported",
		RawPayload: datatypes.JSON("{}"), ImportedAt: time.Now(),
		ConvertedInvoiceID: &convertedInvoiceID,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ChannelOrderLine{
		CompanyID: companyID, ChannelOrderID: order.ID,
		ExternalSKU: "SKU-1", Quantity: decimal.NewFromInt(1),
		MappingStatus: models.MappingStatusMappedExact,
		RawPayload:    datatypes.JSON("{}"),
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/settings/channels/orders/%d/convert", order.ID), url.Values{
		"customer_id":    {fmt.Sprintf("%d", customerID)},
		"invoice_number": {"INV-CONVERT-001"},
	}, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	wantLocation := fmt.Sprintf("/settings/channels/orders/%d?error=channel+order+has+already+been+converted+to+an+invoice", order.ID)
	if location != wantLocation {
		t.Fatalf("expected redirect to %q, got %q", wantLocation, location)
	}

	page := performRequest(t, app, location, "")
	body := readResponseBody(t, page)
	if !strings.Contains(body, "channel order has already been converted to an invoice") {
		t.Fatalf("expected convert service error banner, got %q", body)
	}
}

func TestChannelAccountDeleteFailureShowsBanner(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Channel Delete Error Co")
	app := errorFeedbackApp(server, user, companyID)

	channel := models.SalesChannelAccount{
		CompanyID: companyID, ChannelType: models.ChannelTypeAmazon,
		DisplayName: "Amazon Delete", AuthStatus: models.ChannelAuthConnected, IsActive: true,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	order := models.ChannelOrder{
		CompanyID: companyID, ChannelAccountID: channel.ID,
		ExternalOrderID: "ORDER-DELETE-BLOCK", OrderStatus: "imported",
		RawPayload: datatypes.JSON("{}"), ImportedAt: time.Now(),
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	resp := performFormRequest(t, app, http.MethodPost, "/settings/channels/delete", url.Values{
		"account_id": {fmt.Sprintf("%d", channel.ID)},
	}, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if got, want := location, "/settings/channels?error=cannot+delete+channel+account%3A+1+orders+exist"; got != want {
		t.Fatalf("expected redirect to %q, got %q", want, got)
	}

	page := performRequest(t, app, location, "")
	body := readResponseBody(t, page)
	if !strings.Contains(body, "cannot delete channel account: 1 orders exist") {
		t.Fatalf("expected delete error banner, got %q", body)
	}
}

func TestSettlementReversalFailuresShowErrorBanner(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Settlement Reverse Error Co")
	app := errorFeedbackApp(server, user, companyID)

	channel := models.SalesChannelAccount{
		CompanyID: companyID, ChannelType: models.ChannelTypeAmazon,
		DisplayName: "Amazon Reverse", AuthStatus: models.ChannelAuthConnected, IsActive: true,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}

	missingJEID := uint(999999)
	feeSettlement := models.ChannelSettlement{
		CompanyID: companyID, ChannelAccountID: channel.ID,
		ExternalSettlementID: "SET-FEE-ERR", CurrencyCode: "USD",
		RawPayload: datatypes.JSON("{}"), PostedJournalEntryID: &missingJEID,
	}
	if err := db.Create(&feeSettlement).Error; err != nil {
		t.Fatal(err)
	}

	payoutSettlement := models.ChannelSettlement{
		CompanyID: companyID, ChannelAccountID: channel.ID,
		ExternalSettlementID: "SET-PAYOUT-ERR", CurrencyCode: "USD",
		RawPayload: datatypes.JSON("{}"), PayoutJournalEntryID: &missingJEID,
	}
	if err := db.Create(&payoutSettlement).Error; err != nil {
		t.Fatal(err)
	}

	feeResp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/settings/channels/settlements/%d/reverse-fee", feeSettlement.ID), nil, "")
	if feeResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, feeResp.StatusCode)
	}
	feeLocation := feeResp.Header.Get("Location")
	if !strings.Contains(feeLocation, fmt.Sprintf("/settings/channels/settlements/%d?error=", feeSettlement.ID)) {
		t.Fatalf("expected fee reverse redirect with error, got %q", feeLocation)
	}
	if strings.Contains(feeLocation, "feereverseerr=1") {
		t.Fatalf("expected old fee reverse error flag to be removed, got %q", feeLocation)
	}
	feePage := performRequest(t, app, feeLocation, "")
	if body := readResponseBody(t, feePage); !strings.Contains(body, "reverse fee JE: record not found") {
		t.Fatalf("expected fee reverse error banner, got %q", body)
	}

	payoutResp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/settings/channels/settlements/%d/reverse-payout", payoutSettlement.ID), nil, "")
	if payoutResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, payoutResp.StatusCode)
	}
	payoutLocation := payoutResp.Header.Get("Location")
	if !strings.Contains(payoutLocation, fmt.Sprintf("/settings/channels/settlements/%d?error=", payoutSettlement.ID)) {
		t.Fatalf("expected payout reverse redirect with error, got %q", payoutLocation)
	}
	if strings.Contains(payoutLocation, "payoutreverseerr=1") {
		t.Fatalf("expected old payout reverse error flag to be removed, got %q", payoutLocation)
	}
	payoutPage := performRequest(t, app, payoutLocation, "")
	if body := readResponseBody(t, payoutPage); !strings.Contains(body, "reverse payout JE: record not found") {
		t.Fatalf("expected payout reverse error banner, got %q", body)
	}
}

func TestPaymentRequestDefaultStatusIsPending(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Payment Request Pending Co")
	app := errorFeedbackApp(server, user, companyID)

	gateway := models.PaymentGatewayAccount{
		CompanyID: companyID, ProviderType: models.ProviderStripe,
		DisplayName: "Stripe Pending", AuthStatus: "active", WebhookStatus: "not_configured", IsActive: true,
	}
	if err := db.Create(&gateway).Error; err != nil {
		t.Fatal(err)
	}

	resp := performFormRequest(t, app, http.MethodPost, "/settings/payment-gateways/requests", url.Values{
		"gateway_account_id": {fmt.Sprintf("%d", gateway.ID)},
		"amount":             {"42.50"},
		"currency_code":      {"USD"},
		"description":        {"Test request"},
	}, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	if got, want := resp.Header.Get("Location"), "/settings/payment-gateways/requests?created=1"; got != want {
		t.Fatalf("expected redirect to %q, got %q", want, got)
	}

	var req models.PaymentRequest
	if err := db.Where("company_id = ? AND gateway_account_id = ?", companyID, gateway.ID).First(&req).Error; err != nil {
		t.Fatal(err)
	}
	if req.Status != models.PaymentRequestPending {
		t.Fatalf("expected default payment request status %q, got %q", models.PaymentRequestPending, req.Status)
	}

	page := performRequest(t, app, "/settings/payment-gateways/requests", "")
	body := readResponseBody(t, page)
	if !strings.Contains(body, "Pending") {
		t.Fatalf("expected requests page to show pending status, got %q", body)
	}
}

func TestAccountingMappingSharedClearingAccountShowsBanner(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Channel Accounting Error Co")
	app := errorFeedbackApp(server, user, companyID)

	clearing := models.Account{
		CompanyID: companyID, Code: "1500", Name: "Clearing",
		RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true,
	}
	if err := db.Create(&clearing).Error; err != nil {
		t.Fatal(err)
	}
	firstChannel := models.SalesChannelAccount{
		CompanyID: companyID, ChannelType: models.ChannelTypeAmazon,
		DisplayName: "Amazon", AuthStatus: models.ChannelAuthConnected, IsActive: true,
	}
	secondChannel := models.SalesChannelAccount{
		CompanyID: companyID, ChannelType: models.ChannelTypeShopify,
		DisplayName: "Shopify", AuthStatus: models.ChannelAuthConnected, IsActive: true,
	}
	if err := db.Create(&firstChannel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&secondChannel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ChannelAccountingMapping{
		CompanyID: companyID, ChannelAccountID: firstChannel.ID, ClearingAccountID: &clearing.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performFormRequest(t, app, http.MethodPost, "/settings/channels/accounting", url.Values{
		"channel_account_id":  {fmt.Sprintf("%d", secondChannel.ID)},
		"clearing_account_id": {fmt.Sprintf("%d", clearing.ID)},
	}, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if !strings.Contains(location, "/settings/channels/accounting?error=") {
		t.Fatalf("expected save error redirect, got %q", location)
	}

	page := performRequest(t, app, location, "")
	body := readResponseBody(t, page)
	if !strings.Contains(body, "shared clearing accounts are not supported") {
		t.Fatalf("expected shared clearing banner, got %q", body)
	}
}

func TestClearingReportShowsSharedClearingWarning(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Clearing Warning Co")
	app := errorFeedbackApp(server, user, companyID)

	clearing := models.Account{
		CompanyID: companyID, Code: "1500", Name: "Clearing",
		RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true,
	}
	if err := db.Create(&clearing).Error; err != nil {
		t.Fatal(err)
	}
	firstChannel := models.SalesChannelAccount{
		CompanyID: companyID, ChannelType: models.ChannelTypeAmazon,
		DisplayName: "Amazon", AuthStatus: models.ChannelAuthConnected, IsActive: true,
	}
	secondChannel := models.SalesChannelAccount{
		CompanyID: companyID, ChannelType: models.ChannelTypeShopify,
		DisplayName: "Shopify", AuthStatus: models.ChannelAuthConnected, IsActive: true,
	}
	if err := db.Create(&firstChannel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&secondChannel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ChannelAccountingMapping{
		CompanyID: companyID, ChannelAccountID: firstChannel.ID, ClearingAccountID: &clearing.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ChannelAccountingMapping{
		CompanyID: companyID, ChannelAccountID: secondChannel.ID, ClearingAccountID: &clearing.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performRequest(t, app, "/reports/clearing", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "shared clearing accounts are not supported") {
		t.Fatalf("expected shared clearing warning, got %q", body)
	}
}

func TestSystemItemMutationsAreBlockedAndOrdinaryItemsStillWork(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "System Item Guard Co")
	app := errorFeedbackApp(server, user, companyID)

	revenueAccountID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)

	systemCode := "TASK_LABOR"
	systemItem := models.ProductService{
		CompanyID:        companyID,
		Name:             "Task",
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: revenueAccountID,
		IsActive:         true,
		IsSystem:         true,
		SystemCode:       &systemCode,
	}
	ordinaryItem := models.ProductService{
		CompanyID:        companyID,
		Name:             "Consulting",
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: revenueAccountID,
		IsActive:         true,
	}
	if err := db.Create(&systemItem).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&ordinaryItem).Error; err != nil {
		t.Fatal(err)
	}

	updateSystemResp := performFormRequest(t, app, http.MethodPost, "/products-services/update", url.Values{
		"item_id":            {fmt.Sprintf("%d", systemItem.ID)},
		"name":               {"Task"},
		"type":               {string(models.ProductServiceTypeNonInventory)},
		"structure_type":     {string(models.ItemStructureSingle)},
		"default_price":      {"0.00"},
		"revenue_account_id": {fmt.Sprintf("%d", revenueAccountID)},
	}, "")
	if updateSystemResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, updateSystemResp.StatusCode)
	}
	updateSystemBody := readResponseBody(t, updateSystemResp)
	if !strings.Contains(updateSystemBody, services.ErrSystemItemTypeImmutable.Error()) {
		t.Fatalf("expected system item type guard banner, got %q", updateSystemBody)
	}
	if err := db.First(&systemItem, systemItem.ID).Error; err != nil {
		t.Fatal(err)
	}
	if systemItem.Type != models.ProductServiceTypeService {
		t.Fatalf("expected system item type to remain %q, got %q", models.ProductServiceTypeService, systemItem.Type)
	}

	updateOrdinaryResp := performFormRequest(t, app, http.MethodPost, "/products-services/update", url.Values{
		"item_id":            {fmt.Sprintf("%d", ordinaryItem.ID)},
		"name":               {"Consulting"},
		"type":               {string(models.ProductServiceTypeNonInventory)},
		"structure_type":     {string(models.ItemStructureSingle)},
		"default_price":      {"0.00"},
		"revenue_account_id": {fmt.Sprintf("%d", revenueAccountID)},
	}, "")
	if updateOrdinaryResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, updateOrdinaryResp.StatusCode)
	}
	if got, want := updateOrdinaryResp.Header.Get("Location"), "/products-services?updated=1"; got != want {
		t.Fatalf("expected ordinary item redirect %q, got %q", want, got)
	}
	if err := db.First(&ordinaryItem, ordinaryItem.ID).Error; err != nil {
		t.Fatal(err)
	}
	if ordinaryItem.Type != models.ProductServiceTypeNonInventory {
		t.Fatalf("expected ordinary item type to change to %q, got %q", models.ProductServiceTypeNonInventory, ordinaryItem.Type)
	}

	inactivateSystemResp := performFormRequest(t, app, http.MethodPost, "/products-services/inactive", url.Values{
		"item_id": {fmt.Sprintf("%d", systemItem.ID)},
	}, "")
	if inactivateSystemResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, inactivateSystemResp.StatusCode)
	}
	if !strings.Contains(inactivateSystemResp.Header.Get("Location"), "/products-services?error=") {
		t.Fatalf("expected redirect with error, got %q", inactivateSystemResp.Header.Get("Location"))
	}
	systemPage := performRequest(t, app, inactivateSystemResp.Header.Get("Location"), "")
	systemBody := readResponseBody(t, systemPage)
	if !strings.Contains(systemBody, services.ErrSystemItemCannotBeInactivated.Error()) {
		t.Fatalf("expected system item inactive guard banner, got %q", systemBody)
	}
	if err := db.First(&systemItem, systemItem.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !systemItem.IsActive {
		t.Fatal("expected system item to remain active")
	}

	inactivateOrdinaryResp := performFormRequest(t, app, http.MethodPost, "/products-services/inactive", url.Values{
		"item_id": {fmt.Sprintf("%d", ordinaryItem.ID)},
	}, "")
	if inactivateOrdinaryResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, inactivateOrdinaryResp.StatusCode)
	}
	if got, want := inactivateOrdinaryResp.Header.Get("Location"), "/products-services?inactive=1"; got != want {
		t.Fatalf("expected ordinary inactive redirect %q, got %q", want, got)
	}
	if err := db.First(&ordinaryItem, ordinaryItem.ID).Error; err != nil {
		t.Fatal(err)
	}
	if ordinaryItem.IsActive {
		t.Fatal("expected ordinary item to become inactive")
	}
}
