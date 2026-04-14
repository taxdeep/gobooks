// 遵循project_guide.md
package web

// ar_stub_handlers.go — Phase 1 AR module route stubs.
//
// These handlers return HTTP 501 Not Implemented as route placeholders.
// They establish the AR module URL structure without implementing any logic.
// Each handler will be replaced by a real implementation in the corresponding Phase:
//
//   Phase 2: Quote, SalesOrder
//   Phase 3: CustomerDeposit
//   Phase 4: CustomerReceipt, PaymentApplication
//   Phase 5: ARReturn, ARRefund
//   Phase 6: CustomerStatement, ARAging, WriteOff
//   Phase 7: Gateway interaction

import "github.com/gofiber/fiber/v2"

// ── Quote stubs (Phase 2) ─────────────────────────────────────────────────────

func (s *Server) handleQuoteList(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Quotes: not yet implemented (Phase 2)")
}

func (s *Server) handleQuoteNew(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("New Quote: not yet implemented (Phase 2)")
}

func (s *Server) handleQuoteDetail(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Quote Detail: not yet implemented (Phase 2)")
}

func (s *Server) handleQuoteCreate(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Create Quote: not yet implemented (Phase 2)")
}

func (s *Server) handleQuoteUpdate(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Update Quote: not yet implemented (Phase 2)")
}

func (s *Server) handleQuoteSend(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Send Quote: not yet implemented (Phase 2)")
}

func (s *Server) handleQuoteConvert(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Convert Quote: not yet implemented (Phase 2)")
}

// ── SalesOrder stubs (Phase 2) ────────────────────────────────────────────────

func (s *Server) handleSalesOrderList(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Sales Orders: not yet implemented (Phase 2)")
}

func (s *Server) handleSalesOrderNew(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("New Sales Order: not yet implemented (Phase 2)")
}

func (s *Server) handleSalesOrderDetail(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Sales Order Detail: not yet implemented (Phase 2)")
}

func (s *Server) handleSalesOrderCreate(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Create Sales Order: not yet implemented (Phase 2)")
}

func (s *Server) handleSalesOrderConfirm(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Confirm Sales Order: not yet implemented (Phase 2)")
}

func (s *Server) handleSalesOrderCancel(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Cancel Sales Order: not yet implemented (Phase 2)")
}

// ── CustomerDeposit stubs (Phase 3) ──────────────────────────────────────────

func (s *Server) handleDepositList(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Customer Deposits: not yet implemented (Phase 3)")
}

func (s *Server) handleDepositNew(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("New Deposit: not yet implemented (Phase 3)")
}

func (s *Server) handleDepositDetail(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Deposit Detail: not yet implemented (Phase 3)")
}

func (s *Server) handleDepositCreate(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Create Deposit: not yet implemented (Phase 3)")
}

func (s *Server) handleDepositPost(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Post Deposit: not yet implemented (Phase 3)")
}

func (s *Server) handleDepositApply(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Apply Deposit: not yet implemented (Phase 3)")
}

func (s *Server) handleDepositVoid(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Void Deposit: not yet implemented (Phase 3)")
}

// ── CustomerReceipt stubs (Phase 4) ──────────────────────────────────────────

func (s *Server) handleReceiptList(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Customer Receipts: not yet implemented (Phase 4)")
}

func (s *Server) handleReceiptNew(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("New Receipt: not yet implemented (Phase 4)")
}

func (s *Server) handleReceiptDetail(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Receipt Detail: not yet implemented (Phase 4)")
}

func (s *Server) handleReceiptCreate(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Create Receipt: not yet implemented (Phase 4)")
}

func (s *Server) handleReceiptConfirm(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Confirm Receipt: not yet implemented (Phase 4)")
}

func (s *Server) handleReceiptApply(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Apply Receipt: not yet implemented (Phase 4)")
}

func (s *Server) handleReceiptUnapply(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Unapply Receipt: not yet implemented (Phase 4)")
}

func (s *Server) handleReceiptReverse(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Reverse Receipt: not yet implemented (Phase 4)")
}

// ── ARReturn stubs (Phase 5) ──────────────────────────────────────────────────

func (s *Server) handleReturnList(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Returns: not yet implemented (Phase 5)")
}

func (s *Server) handleReturnNew(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("New Return: not yet implemented (Phase 5)")
}

func (s *Server) handleReturnDetail(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Return Detail: not yet implemented (Phase 5)")
}

func (s *Server) handleReturnCreate(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Create Return: not yet implemented (Phase 5)")
}

func (s *Server) handleReturnApprove(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Approve Return: not yet implemented (Phase 5)")
}

func (s *Server) handleReturnReject(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Reject Return: not yet implemented (Phase 5)")
}

// ── ARRefund stubs (Phase 5) ──────────────────────────────────────────────────

func (s *Server) handleRefundList(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Refunds: not yet implemented (Phase 5)")
}

func (s *Server) handleRefundNew(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("New Refund: not yet implemented (Phase 5)")
}

func (s *Server) handleRefundDetail(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Refund Detail: not yet implemented (Phase 5)")
}

func (s *Server) handleRefundCreate(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Create Refund: not yet implemented (Phase 5)")
}

func (s *Server) handleRefundPost(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Post Refund: not yet implemented (Phase 5)")
}

func (s *Server) handleRefundVoid(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).SendString("Void Refund: not yet implemented (Phase 5)")
}
