// 遵循project_guide.md
package web

import (
	"github.com/gofiber/fiber/v2"
)

func (s *Server) registerRoutes(app *fiber.App) {
	// 静态资源（Tailwind 输出）
	app.Static("/static", "internal/web/static")

	// ── 基础路由 ────────────────────────────────────────────────────────────────
	app.Get("/", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleDashboard)

	// ── 首次启动向导 ─────────────────────────────────────────────────────────────
	app.Get("/setup/bootstrap", s.handleBootstrapForm)
	app.Post("/setup/bootstrap", s.handleBootstrapSubmit)
	app.Get("/setup", s.LoadSession(), s.RequireAuth(), s.handleSetupForm)
	app.Post("/setup", s.LoadSession(), s.RequireAuth(), s.handleSetupSubmit)

	// ── 认证（邮箱 + 密码）───────────────────────────────────────────────────────
	app.Get("/login", s.handleLoginForm)
	app.Post("/login", s.handleLoginPost)
	app.Post("/logout", s.handleLogoutPost)

	// ── 公司选择（多公司成员）────────────────────────────────────────────────────
	app.Get("/select-company", s.LoadSession(), s.RequireAuth(), s.handleSelectCompanyGet)
	app.Post("/select-company", s.LoadSession(), s.RequireAuth(), s.handleSelectCompanyPost)

	// ── 设置：公司档案 ───────────────────────────────────────────────────────────
	// GET 页面对所有成员开放；POST 变更需要 manage_settings（owner / admin）
	app.Get("/settings/company/profile", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleCompanyProfileForm)
	app.Post("/settings/company/profile", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleCompanyProfileSubmit)
	// Logo upload (settings permission) and protected serve (any member, prevents hotlinking).
	app.Post("/settings/company/profile/logo", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleCompanyLogoUpload)
	app.Get("/company/logo", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleCompanyLogoServe)
	app.Get("/settings/company/templates", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleCompanyTemplatesGet)
	app.Get("/settings/company/sales-tax", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleCompanySalesTaxGet)
	app.Post("/settings/company/sales-tax", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleTaxCodeCreate)
	app.Post("/settings/company/sales-tax/update", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleTaxCodeUpdate)
	app.Post("/settings/company/sales-tax/deactivate", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleTaxCodeDeactivate)
	app.Get("/settings/company/numbering", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleNumberingSettingsGet)
	app.Post("/settings/company/numbering", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleNumberingSettingsPost)
	app.Get("/settings/company/payment-terms", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handlePaymentTermsGet)
	app.Post("/settings/company/payment-terms", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handlePaymentTermCreate)
	app.Post("/settings/company/payment-terms/update", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handlePaymentTermUpdate)
	app.Post("/settings/company/payment-terms/set-default", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handlePaymentTermSetDefault)
	app.Post("/settings/company/payment-terms/toggle", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handlePaymentTermToggle)
	app.Post("/settings/company/payment-terms/delete", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handlePaymentTermDelete)
	app.Get("/settings/company", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleCompanyHub)
	app.Post("/settings/company", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleCompanyProfileSubmit)
	// ── 设置：多币种 ─────────────────────────────────────────────────────────
	app.Get("/settings/company/currency", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleCompanyCurrencyGet)
	app.Post("/settings/company/currency/enable", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleCompanyCurrencyEnableMulti)
	app.Post("/settings/company/currency/add", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleCompanyCurrencyAdd)
	app.Get("/settings/company/exchange-rates", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleExchangeRatesGet)
	app.Post("/settings/company/exchange-rates", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleExchangeRatesAdd)
	app.Post("/settings/company/exchange-rates/delete", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleExchangeRatesDelete)

	// ── 设置：通知（SMTP / SMS）────────────────────────────────────────────────
	app.Get("/settings/company/notifications", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleCompanyNotificationsGet)
	app.Post("/settings/company/notifications", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleCompanyNotificationsPost)
	app.Post("/settings/company/notifications/test-email", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleCompanyNotificationsTestEmail)
	app.Post("/settings/company/notifications/test-sms", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleCompanyNotificationsTestSMS)

	// ── 设置：安全 ──────────────────────────────────────────────────────────────
	app.Get("/settings/company/security", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleCompanySecurityGet)
	app.Post("/settings/company/security", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleCompanySecurityPost)

	// ── 设置：销售渠道集成 ──────────────────────────────────────────────────────
	app.Get("/settings/channels", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleChannelAccounts)
	app.Post("/settings/channels", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleChannelAccountCreate)
	app.Post("/settings/channels/delete", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleChannelAccountDelete)
	app.Get("/settings/channels/mappings", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleChannelMappings)
	app.Post("/settings/channels/mappings", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleChannelMappingCreate)
	app.Get("/settings/channels/orders", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleChannelOrders)
	app.Post("/settings/channels/orders", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleChannelOrderCreate)
	app.Get("/settings/channels/orders/:id", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleChannelOrderDetail)
	app.Post("/settings/channels/orders/:id/convert", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceCreate), s.handleChannelOrderConvert)
	app.Get("/settings/channels/accounting", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleAccountingMappings)
	app.Post("/settings/channels/accounting", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleAccountingMappingSave)
	app.Get("/settings/channels/settlements", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleSettlements)
	app.Post("/settings/channels/settlements", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleSettlementCreate)
	app.Get("/settings/channels/settlements/:id", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleSettlementDetail)
	app.Post("/settings/channels/settlements/:id/post", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleSettlementPost)
	app.Post("/settings/channels/settlements/:id/record-payout", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleSettlementRecordPayout)
	app.Post("/settings/channels/settlements/:id/reverse-fee", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleSettlementReverseFee)
	app.Post("/settings/channels/settlements/:id/reverse-payout", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleSettlementReversePayout)

	// ── 设置：支付网关 ──────────────────────────────────────────────────────────
	app.Get("/settings/payment-gateways", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handlePaymentGateways)
	app.Post("/settings/payment-gateways", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handlePaymentGatewayCreate)
	app.Get("/settings/payment-gateways/mappings", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handlePaymentMappings)
	app.Post("/settings/payment-gateways/mappings", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handlePaymentMappingSave)
	app.Get("/settings/payment-gateways/requests", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handlePaymentRequests)
	app.Post("/settings/payment-gateways/requests", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handlePaymentRequestCreate)
	app.Get("/settings/payment-gateways/transactions", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handlePaymentTransactions)
	app.Post("/settings/payment-gateways/transactions", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handlePaymentTransactionCreate)
	app.Post("/settings/payment-gateways/transactions/:id/post", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handlePaymentTransactionPost)
	app.Post("/settings/payment-gateways/transactions/:id/apply", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handlePaymentTransactionApply)
	app.Post("/settings/payment-gateways/transactions/:id/apply-refund", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handlePaymentTransactionApplyRefund)
	app.Post("/settings/payment-gateways/transactions/:id/unapply", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handlePaymentTransactionUnapply)

	// 向后兼容：旧编号 URL（POST 转发；GET 重定向到新路径）
	app.Post("/settings/numbering", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleNumberingSettingsPost)
	app.Get("/settings/numbering", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), func(c *fiber.Ctx) error {
		return c.Redirect("/settings/company/numbering", fiber.StatusSeeOther)
	})

	// ── 设置：AI Connect（owner / admin 专属）───────────────────────────────────
	app.Get("/settings/ai-connect", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleAIConnectGet)
	app.Post("/settings/ai-connect", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleAIConnectPost)
	app.Post("/settings/ai-connect/test", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleAIConnectTestPost)

	// ── 设置：成员管理 ───────────────────────────────────────────────────────────
	// GET 对所有成员开放（viewer 可查看成员列表，但 UI 隐藏邀请表单）
	// POST 邀请需要 manage_members（owner / admin）
	app.Get("/settings/members", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleMembersGet)
	app.Post("/settings/members/invite", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionMemberManage), s.handleMembersInvitePost)

	// ── 设置：审计日志（需 view_audit_log 权限；AP / viewer 无权访问）──────────────
	app.Get("/settings/audit-log", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionAuditView), s.handleAuditLog)

	// ── 科目表（会计科目）────────────────────────────────────────────────────────
	// 查看对所有成员开放；变更科目表属于管理操作，需要 manage_settings（owner / admin）
	app.Get("/accounts", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleAccounts)
	app.Post("/accounts", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionAccountCreate), s.handleAccountCreate)
	app.Post("/accounts/update", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionAccountUpdate), s.handleAccountUpdate)
	app.Post("/accounts/inactive", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionAccountDelete), s.handleAccountInactive)

	// AI 科目推荐接口（辅助 UI，不变更数据，仅需成员资格）
	app.Post("/accounts/suggestions", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleAccountSuggestions)
	app.Post("/api/ai/recommend/account", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleAIRecommendAccount)
	app.Post("/api/accounts/recommendations", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleAccountRecommendations)

	// ── 日记账 ──────────────────────────────────────────────────────────────────
	// 新建 / 冲销属于 AR 操作，bookkeeper 及以上可执行（ar_access）
	app.Get("/journal-entry", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleJournalEntryForm)
	app.Post("/journal-entry", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleJournalEntryPost)
	app.Get("/journal-entry/list", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleJournalEntryList)
	app.Post("/journal-entry/:id/reverse", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleJournalEntryReverse)

	// ── 发票 ─────────────────────────────────────────────────────────────────────
	// 创建 / 编辑需要 ar_access（bookkeeper 及以上）
	// 过账 / 冲销 / 发行需要 approve_transactions（accountant 及以上）
	app.Get("/invoices", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleInvoices)
	app.Post("/invoices", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceCreate), s.handleInvoiceCreate)
	app.Get("/invoices/new", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceCreate), s.handleInvoiceNew)
	app.Get("/invoices/:id", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleInvoiceDetail)
	app.Get("/invoices/:id/edit", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handleInvoiceEdit)
	app.Post("/invoices/save-draft", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceCreate), s.handleInvoiceSaveDraft)

	// Invoice preview & PDF
	app.Get("/invoices/:id/preview", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleInvoicePreview)
	app.Get("/invoices/:id/pdf", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleInvoicePDF)

	// Invoice lifecycle management (issue → send → mark paid / void)
	app.Post("/invoices/:id/issue", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceApprove), s.handleInvoiceIssue)
	app.Post("/invoices/:id/send", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handleInvoiceSend)
	app.Post("/invoices/:id/send-email", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handleInvoiceSendEmail)
	app.Get("/invoices/:id/email-history", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handleGetInvoiceEmailHistory)
	app.Post("/invoices/:id/mark-paid", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handleInvoiceMarkPaid)
	app.Post("/invoices/:id/request-payment", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handleInvoiceRequestPayment)
	app.Get("/invoices/:id/receive-payment", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handleInvoiceReceivePaymentForm)
	app.Post("/invoices/:id/receive-payment", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceUpdate), s.handleInvoiceReceivePaymentSubmit)
	app.Post("/invoices/:id/post", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceApprove), s.handleInvoicePost)
	app.Post("/invoices/:id/void", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceApprove), s.handleInvoiceVoid)
	app.Post("/invoices/:id/delete", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceDelete), s.handleInvoiceDelete)

	// Invoice templates (settings)
	app.Get("/settings/invoice-templates", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleInvoiceTemplatesList)
	app.Get("/settings/invoice-templates/:id", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleInvoiceTemplateGet)
	app.Post("/settings/invoice-templates", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleInvoiceTemplateCreate)
	app.Post("/settings/invoice-templates/:id", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleInvoiceTemplateUpdate)
	app.Post("/settings/invoice-templates/:id/delete", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleInvoiceTemplateDelete)

	// API endpoints for templates
	app.Get("/api/invoice-templates/default", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleGetDefaultInvoiceTemplate)

	// ── 账单 ─────────────────────────────────────────────────────────────────────
	// 查看列表对所有成员开放；创建 / 编辑需要 ap_access（ap 及以上）
	app.Get("/bills", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleBills)
	app.Get("/bills/new", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionBillCreate), s.handleBillNew)
	app.Get("/bills/:id/edit", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionBillUpdate), s.handleBillEdit)
	app.Post("/bills/save-draft", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionBillCreate), s.handleBillSaveDraft)
	app.Post("/bills/:id/post", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionBillUpdate), s.handleBillPost)
	app.Post("/bills/:id/void", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionBillUpdate), s.handleBillVoid)

	// ── 报表（需 view_reports 权限；AP 角色无权访问）─────────────────────────────
	app.Get("/reports", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleReportsHub)
	app.Get("/reports/trial-balance", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleTrialBalance)
	app.Get("/reports/income-statement", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleIncomeStatement)
	app.Get("/reports/balance-sheet", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleBalanceSheet)
	app.Get("/reports/journal-entries", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleJournalEntryReport)
	app.Get("/reports/sales-tax", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleSalesTaxReport)
	app.Get("/reports/clearing", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleClearingReport)

	// ── CSV Exports ──────────────────────────────────────────────────────────
	app.Get("/export/clearing-summary.csv", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleExportClearingSummary)
	app.Get("/export/clearing-movements.csv", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleExportClearingMovements)
	app.Get("/export/settlements.csv", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleExportSettlementsList)
	app.Get("/export/settlements/:id/lines.csv", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleExportSettlementLines)
	app.Get("/export/channel-orders.csv", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleExportChannelOrders)
	app.Get("/export/channel-orders/:id/lines.csv", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionReportView), s.handleExportChannelOrderLines)

	// ── 票据模板管理 ──────────────────────────────────────────────────────────────
	// 模板管理界面
	app.Get("/settings/invoice-templates/manage", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsView), s.handleInvoiceTemplatesSettings)

	// ── 产品与服务目录 ────────────────────────────────────────────────────────────
	// 属于公司主数据，变更需要 manage_settings（owner / admin）
	// 查看对所有成员开放（入口已移至 Settings > Company 页面）
	app.Get("/products-services", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleProductServices)
	app.Post("/products-services", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleProductServiceCreate)
	app.Post("/products-services/update", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleProductServiceUpdate)
	app.Post("/products-services/inactive", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleProductServiceInactive)
	app.Get("/products-services/:id/ledger", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleInventoryLedger)
	app.Post("/products-services/opening", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleInventoryOpening)
	app.Post("/products-services/adjustment", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionSettingsUpdate), s.handleInventoryAdjustment)

	// ── 往来单位（客户 / 供应商）────────────────────────────────────────────────
	// 联系人数据对所有运营角色开放（不在现有 action 定义范围内，仅需成员资格）
	app.Get("/customers", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleCustomers)
	app.Get("/customers/new", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceCreate), s.handleCustomerNew)
	app.Post("/customers", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceCreate), s.handleCustomerCreate)
	app.Post("/customers/update", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceCreate), s.handleCustomerUpdate)
	app.Get("/vendors", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.handleVendors)
	app.Post("/vendors", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionBillCreate), s.handleVendorCreate)

	// ── 用户档案（仅需登录；不依赖公司成员资格，所有已认证用户均可访问）───────────────
	// NOTE: ResolveActiveCompany is intentionally NOT used here — it returns 403
	// for users with no membership and redirects multi-company users to /select-company,
	// both of which would block legitimate profile access. hasCompany is derived
	// directly from the session in handleProfileGet.
	app.Get("/profile", s.LoadSession(), s.RequireAuth(), s.handleProfileGet)
	app.Post("/profile/request-email-change", s.LoadSession(), s.RequireAuth(), s.handleRequestEmailChange)
	app.Post("/profile/verify-email-change", s.LoadSession(), s.RequireAuth(), s.handleVerifyEmailChange)
	app.Post("/profile/request-password-change", s.LoadSession(), s.RequireAuth(), s.handleRequestPasswordChange)
	app.Post("/profile/verify-password-change", s.LoadSession(), s.RequireAuth(), s.handleVerifyPasswordChange)

	// ── 银行操作 ─────────────────────────────────────────────────────────────────
	// 银行对账 / 收款归入 AR 操作（ar_access，bookkeeper 及以上）
	// 付款归入 AP 操作（ap_access，ap 及以上）
	app.Get("/banking/reconcile", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleBankReconcileForm)
	app.Post("/banking/reconcile", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleBankReconcileSubmit)
	app.Post("/banking/reconcile/void", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleVoidReconciliation)
	app.Post("/banking/reconcile/save-progress", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleBankReconcileSaveProgress)
	// Auto-match engine: suggest → accept/reject (membership only; no accounting changes)
	app.Post("/banking/reconcile/auto-match", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleAutoMatch)
	app.Post("/banking/reconcile/suggest/accept", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleAcceptSuggestion)
	app.Post("/banking/reconcile/suggest/reject", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionJournalCreate), s.handleRejectSuggestion)
	app.Get("/banking/receive-payment", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceCreate), s.handleReceivePaymentForm)
	app.Post("/banking/receive-payment", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionInvoiceCreate), s.handleReceivePaymentSubmit)
	app.Get("/banking/pay-bills", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionBillPay), s.handlePayBillsForm)
	app.Post("/banking/pay-bills", s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), s.RequirePermission(ActionBillPay), s.handlePayBillsSubmit)
}
