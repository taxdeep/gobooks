# Balanciz Invoice Module — 深度分析 & Implementation Plan

**日期**: 2026-03-30  
**范围**: 完整 Invoice 模块（从已有代码出发，非从零开始）  
**目标**: 正式产品级实现，深度集成 accounting system

---

## 📊 第一部分：现有系统分析

### 1.1 已有的核心基础设施

#### 强有力的会计引擎 ✅
- **PostingEngine**（`services/posting_engine.go`）
  - 协调所有 posting/voiding/reversal 操作
  - 无状态设计，可共享使用
  - 支持 company 隔离

- **完整 Posting Pipeline** （`services/invoice_post.go`）
  ```
  Document Load 
    → Validation (pre-flight)
    → Fragment Builder (per line + tax)
    → Journal Aggregation (by account + side)
    → Double-entry validation
    → Transaction (JE + Ledger)
    → Audit Log
  ```

- **Tax Engine**（`services/tax_engine.go`）
  - Per-line tax calculation
  - Sales tax payable segregation
  - Tax scope handling (purchase vs sales)
  - Compliant with accounting rules

- **Fragment Builder**（`services/fragment_builder.go`）
  - Pure functions (no DB dependency, fully testable)
  - `BuildInvoiceFragments()` → raw fragments
  - `BuildBillFragments()` → bill fragments
  - Account segregation logic

- **Journal Aggregation**（`services/journal_aggregate.go`）
  - Collapse fragments by account + side
  - Double-entry validation
  - Prevents fragmented JE

- **Ledger Projection**（`services/ledger.go`）
  - ProjectToLedger() function
  - Creates ledger entries from JE lines
  - Status tracking (active, voided, reversed)

#### 数据隔离 & 审计 ✅
- **Company Isolation**: 全表 company_id FK + uniqueness constraints
- **Audit Logging**: WriteAuditLog() with snapshot + actor tracking
- **Concurrency Control**: SELECT FOR UPDATE 防止 double-posting
- **Entity Numbering**: ENYYYY######## 系统（内部不可变）
- **Orphan Prevention**: Migration 000_phase1_precheck.sql

#### 销售流程 ✅
- **Invoice Model**（`models/sales_purchases.go`）
  - Status lifecycle: draft → sent → paid / voided
  - Terms support: Net 15/30/60, Due on receipt, Custom
  - Invoice lines (qty, unit_price, tax_code)
  - Journal entry linking

- **Customer Management**(`models/party.go`)
  - Customer records (company-scoped)
  - Email address
  - Payment terms default

#### 通知基础设施 ✅
- **SMTP Configuration**
  - `services/email_sender.go`: Real SMTP client
  - `models/notification_settings.go`: Company-level SMTP config
  - Encryption support (STARTTLS, SSL/TLS, None)
  - Authentication with decryption
  - Test/verify flow

- **Email Provider**(`services/email_provider.go`)
  - Config loading & validation
  - Credential decryption (crypto)

#### Web 层已有 ✅
- **Routes** (完整 CRUD)
  ```
  GET  /invoices                 → list with filtering
  POST /invoices                 → create (legacy simple form)
  GET  /invoices/new             → editor blank
  GET  /invoices/:id             → detail view
  GET  /invoices/:id/edit        → editor draft
  POST /invoices/save-draft      → save line items
  POST /invoices/:id/post        → post (triggers PostInvoice)
  POST /invoices/:id/void        → void (triggers VoidInvoice)
  ```

- **Handlers**
  - `invoices_handlers.go` (295 lines)
  - `invoice_editor_handlers.go`
  - Auth middleware + company isolation
  - Permissions checks (ActionInvoiceCreate, ActionInvoiceApprove)

---

### 1.2 已有的缺失项目

| 功能 | 状态 | 复杂度 | 优先级 |
|------|------|--------|--------|
| Invoice Templates | ❌ | 中 | 🔴 High |
| PDF Export | ❌ | 中 | 🔴 High |
| Email Send | ❌ | 低 | 🔴 High |
| Payment Tracking | ❌ | 中 | 🟡 Medium |
| Recurring Invoices | ❌ | 高 | 🟢 Low |
| Invoice Reminders | ❌ | 中 | 🟢 Low |
| Customer Portal | ❌ | 高 | 🟢 Low |
| Payment Links | ❌ | 高 | 🟢 Low |

---

## 🏗️ 第二部分：架构约束（来自 PROJECT_GUIDE.md）

### 核心规则（不可违反）

1. **Posting Pipeline 严格顺序**
   ```
   Document → Validation → Tax Calculation → Fragments 
   → Aggregation → Journal Entry → Ledger Entries
   ```

2. **Company 隔离强制**
   - 所有新对象必须有 `company_id`
   - FK constraints 必须存在
   - 跨 company query 必须 WHERE company_id = ?

3. **Posted Documents 不可删除**
   - Draft: 可物理删除
   - Posted: 只能 void（生成 reversal JE，保留痕迹）
   - Void 必须记录: voided_by, voided_at, void_reason

4. **Backend Authority**
   - 所有业务逻辑在后端
   - 前端只做输入 + 预览
   - API responses 包含完整真实数据，不信任客户端计算

5. **Template vs Data 分离**
   - Template 只控制**展示**
   - 业务真实数据来自 DB（不从 HTML blob）
   - Line amounts from invoice_lines，not from template

6. **审计追踪**
   - 主要操作记录: create, update, post, void, send, payment
   - Actor tracking (user email / IP)
   - Snapshot of changed fields

---

## 📋 第三部分：实现分阶段计划

### 推荐 MVP Scope: Phase 1 + Phase 2 + Phase 3 (+ Phase 4 Basic)

---

### **PHASE 1: Invoice Template System** (3-4 天)
`目标`: Company-scoped customizable invoice templates

#### 1.1 数据库迁移
```sql
-- 新建 invoices_templates 表
CREATE TABLE invoices_templates (
  id BIGSERIAL PRIMARY KEY,
  company_id BIGINT NOT NULL REFERENCES companies(id),
  entity_number VARCHAR(50) NOT NULL UNIQUE,
  template_name VARCHAR(255) NOT NULL,
  is_default BOOLEAN DEFAULT false,
  logo_image_id BIGINT, -- FK to file storage
  header_text TEXT,
  footer_text TEXT,
  payment_terms_display TEXT,
  include_payment_mode_section BOOLEAN DEFAULT true,
  include_bank_account_section BOOLEAN DEFAULT false,
  include_notes_section BOOLEAN DEFAULT true,
  status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ON invoices_templates(company_id, is_default) WHERE is_default = true;

-- 新建 invoices_template_line_items 表
CREATE TABLE invoices_template_line_items (
  id BIGSERIAL PRIMARY KEY,
  template_id BIGINT NOT NULL REFERENCES invoices_templates(id) ON DELETE CASCADE,
  column_name VARCHAR(100) NOT NULL,
  column_label VARCHAR(255) NOT NULL,
  is_visible BOOLEAN DEFAULT true,
  sort_order INT NOT NULL DEFAULT 0
);

-- 更新 invoices 表
ALTER TABLE invoices ADD COLUMN template_id BIGINT REFERENCES invoices_templates(id);
```

#### 1.2 Go Models
```go
// internal/models/invoice_template.go
type InvoiceTemplate struct {
  ID            uint      `gorm:"primaryKey"`
  CompanyID     uint      `gorm:"not null;uniqueIndex:idx_company_default;index"`
  EntityNumber  string    `gorm:"not null;uniqueIndex"`
  TemplateName  string    `gorm:"not null"`
  IsDefault     bool      `gorm:"default:false;uniqueIndex:idx_company_default"`
  LogoImageID   *uint
  HeaderText    string
  FooterText    string
  LineItems     []InvoiceTemplateLineItem
  Status        string    `gorm:"not null;default:'active'"` // active, inactive
  CreatedAt     time.Time
  UpdatedAt     time.Time
}

type InvoiceTemplateLineItem struct {
  ID           uint
  TemplateID   uint
  ColumnName   string  // description, qty, unit_price, line_tax, line_total
  ColumnLabel  string
  IsVisible    bool
  SortOrder    int
}
```

#### 1.3 Service Layer (`internal/services/invoice_template.go`)
```go
func CreateTemplate(db *gorm.DB, companyID uint, req *CreateTemplateRequest) (*InvoiceTemplate, error)
func LoadTemplate(db *gorm.DB, companyID, templateID uint) (*InvoiceTemplate, error)
func GetDefaultTemplate(db *gorm.DB, companyID uint) (*InvoiceTemplate, error)
func ListTemplates(db *gorm.DB, companyID uint) ([]InvoiceTemplate, error)
func UpdateTemplate(db *gorm.DB, companyID, templateID uint, req *UpdateTemplateRequest) error
func SetDefaultTemplate(db *gorm.DB, companyID, templateID uint) error
func DeactivateTemplate(db *gorm.DB, companyID, templateID uint) error
```

#### 1.4 Web Layer
```go
// Routes
GET  /settings/company/invoice-templates              → list templates
GET  /settings/company/invoice-templates/new          → create form
POST /settings/company/invoice-templates              → create
GET  /settings/company/invoice-templates/:id/edit     → edit form
POST /settings/company/invoice-templates/:id          → update
POST /settings/company/invoice-templates/:id/set-default → set default
POST /settings/company/invoice-templates/:id/deactivate → deactivate

// Handler 示例
func (s *Server) handleInvoiceTemplatesGet(c *fiber.Ctx) error {
  companyID, _ := ActiveCompanyIDFromCtx(c)
  templates, err := services.ListTemplates(s.DB, companyID)
  // render templates list
}
```

#### 1.5 UI/Templates
- HTMX-based template editor
- Line item visibility checkbox
- Default selection radio button
- Tailwind styling (consistent with existing)

#### 1.6 关键约束
- Template 只影响**显示**，不影响**数据**
- Line amounts 必须从 invoice_lines 读取
- Draft invoice 可更改 template
- Posted invoice 不改 JE，但改后续生成的 PDF

---

### **PHASE 2: PDF Export** (2-3 天)
`目标`: 生成可下载的 PDF invoice

#### 2.1 选择库
**推荐**: wkhtmltopdf (通过 Go wrapper)
- 安装: `choco install wkhtmltopdf` (Windows) / `apt-get install wkhtmltopdf` (Linux)
- Go wrapper: `github.com/SebastiaanKlippert/go-wkhtmltopdf`

#### 2.2 实现
```go
// internal/services/invoice_pdf.go

// PdfConfig holds rendering options
type PdfConfig struct {
  IncludeCompanyLogo bool
  IncludeQRCode      bool
  Orientation        string // Portrait, Landscape
}

// GenerateInvoicePDF renders invoice to PDF bytes
func GenerateInvoicePDF(
  db *gorm.DB, 
  companyID uint, 
  invoiceID uint, 
  templateID *uint,
  config *PdfConfig,
) ([]byte, error) {
  // 1. Load invoice with lines, customer, company
  // 2. Load template (or default)
  // 3. Render HTML via Go template
  // 4. Generate PDF via wkhtmltopdf
  // 5. Return []byte
}

// RenderInvoiceHTML 纯 Go template 渲染
func RenderInvoiceHTML(inv *models.Invoice, tmpl *InvoiceTemplate) (string, error)
```

#### 2.3 HTML Template (`internal/templates/invoice.html`)
```html
<!DOCTYPE html>
<html>
<head>
  <style>
    /* Tailwind-like CSS for invoice */
    body { font-family: Arial, sans-serif; margin: 20px; }
    .header { border-bottom: 2px solid #000; padding-bottom: 20px; }
    .invoice-number { font-weight: bold; font-size: 16px; }
    table { width: 100%; border-collapse: collapse; }
    td, th { border: 1px solid #ccc; padding: 8px; text-align: left; }
    .total-section { margin-top: 20px; text-align: right; }
  </style>
</head>
<body>
  <div class="header">
    <div class="company-logo">{{ .Company.Name }}</div>
    <div class="invoice-number">Invoice #{{ .Invoice.InvoiceNumber }}</div>
  </div>
  
  <table>
    <thead>
      <tr>
        <th>Description</th>
        <th>Qty</th>
        <th>Unit Price</th>
        <th>Tax</th>
        <th>Total</th>
      </tr>
    </thead>
    <tbody>
      {{ range .Invoice.Lines }}
      <tr>
        <td>{{ .Description }}</td>
        <td>{{ .Qty }}</td>
        <td>{{ .UnitPrice }}</td>
        <td>{{ .LineTax }}</td>
        <td>{{ .LineTotal }}</td>
      </tr>
      {{ end }}
    </tbody>
  </table>
  
  <div class="total-section">
    <div>Subtotal: {{ .Invoice.Subtotal }}</div>
    <div>Tax: {{ .Invoice.TaxTotal }}</div>
    <div class="invoice-total">Total: {{ .Invoice.Amount }}</div>
  </div>
  
  <div class="footer">
    {{ .Template.FooterText }}
  </div>
</body>
</html>
```

#### 2.4 Web Layer
```go
// Routes
GET /invoices/:id/pdf              → download PDF
GET /invoices/:id/preview-pdf      → preview HTML

// Handler
func (s *Server) handleInvoicePdfDownload(c *fiber.Ctx) error {
  companyID, _ := ActiveCompanyIDFromCtx(c)
  id, _ := parseID(c.Params("id"))
  
  pdf, err := services.GenerateInvoicePDF(s.DB, companyID, id, nil, &services.PdfConfig{})
  
  c.Set("Content-Type", "application/pdf")
  c.Set("Content-Disposition", "attachment; filename=invoice-"+invoiceNumber+".pdf")
  return c.Send(pdf)
}
```

#### 2.5 关键约束
- PDF 文件名包含 invoice number（不含 company name 防止泄露）
- PDF 生成必须读取 DB，不信任前端参数
- Posted invoice 显示 "POSTED" 水印
- 幂等性：相同 invoice + template → 相同 PDF

---

### **PHASE 3: Email Notification** (2-3 天)
`目标`: 通过 SMTP 发送 invoice 给客户

#### 3.1 数据库迁移
```sql
CREATE TABLE invoices_sent_log (
  id BIGSERIAL PRIMARY KEY,
  company_id BIGINT NOT NULL REFERENCES companies(id),
  invoice_id BIGINT NOT NULL REFERENCES invoices(id),
  recipient_email VARCHAR(255) NOT NULL,
  sent_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  delivery_status VARCHAR(20) NOT NULL CHECK (delivery_status IN ('pending', 'sent', 'failed', 'bounced')),
  smtp_error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE invoices_notification_settings (
  id BIGSERIAL PRIMARY KEY,
  company_id BIGINT NOT NULL UNIQUE REFERENCES companies(id),
  on_post_auto_send BOOLEAN DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### 3.2 Service Layer
```go
// internal/services/invoice_notification.go

type SendInvoiceRequest struct {
  InvoiceID     uint
  RecipientEmail string
  IncludePDF    bool
  CustomMessage string
}

func SendInvoiceViaEmail(
  db *gorm.DB,
  companyID uint,
  req *SendInvoiceRequest,
  actor string,
  userID *uuid.UUID,
) error {
  // 1. Check SMTP readiness via EffectiveSMTPForCompany(db, companyID)
  // 2. Load invoice + customer
  // 3. Generate PDF
  // 4. Build email (recipient: customer.Email or explicit)
  // 5. Send via SendEmail()
  // 6. Log to invoices_sent_log (sent / failed)
  // 7. Audit log (invoice.sent)
  // 8. Update invoice status: draft → sent (if not already)
}

func GetNotificationSettings(db *gorm.DB, companyID uint) (*NotificationSettings, error)
func UpdateNotificationSettings(db *gorm.DB, companyID uint, settings *NotificationSettings) error
```

#### 3.3 Web Layer
```go
// Routes
POST /invoices/:id/send                              → send email
GET  /settings/company/invoice-notifications          → settings page
POST /settings/company/invoice-notifications          → update settings

// Handler
func (s *Server) handleInvoiceSend(c *fiber.Ctx) error {
  user := UserFromCtx(c)
  companyID, _ := ActiveCompanyIDFromCtx(c)
  id, _ := parseID(c.Params("id"))
  
  customMsg := c.FormValue("custom_message")
  includeAttach := c.FormValue("include_pdf") == "1"
  
  err := services.SendInvoiceViaEmail(s.DB, companyID, &SendInvoiceRequest{
    InvoiceID:      id,
    IncludePDF:     includeAttach,
    CustomMessage:  customMsg,
  }, user.Email, &user.ID)
  
  if err != nil {
    // Error response
  }
  
  // Success: redirect + message
}
```

#### 3.4 Email Template
```plaintext
From: {{ Company.Email }}
To: {{ Customer.Email }}
Subject: Invoice #{{ Invoice.InvoiceNumber }} from {{ Company.Name }}

Dear {{ Customer.Name }},

Please find attached your invoice.

Invoice Number: {{ Invoice.InvoiceNumber }}
Invoice Date: {{ Invoice.InvoiceDate }}
Due Date: {{ Invoice.DueDate }}
Total: {{ Invoice.Amount }}

{{ CustomMessage }}

Thank you for your business!

{{ Company.Name }}
```

#### 3.5 关键约束
- Email 发送失败不回滚 invoice 状态
- Audit log 记录发送尝试 + 结果
- 同一 invoice 可多次发送（tracked in sent_log）
- SMTP not ready → 禁止发送（错误消息清晰）

---

### **PHASE 4: Payment Tracking** (2-3 天)
`目标`: 记录客户付款，更新状态

#### 4.1 数据库迁移
```sql
CREATE TABLE invoice_payments (
  id BIGSERIAL PRIMARY KEY,
  company_id BIGINT NOT NULL REFERENCES companies(id),
  invoice_id BIGINT NOT NULL REFERENCES invoices(id),
  amount_received NUMERIC(18,2) NOT NULL,
  payment_date DATE NOT NULL,
  payment_method VARCHAR(50) NOT NULL CHECK (payment_method IN ('bank_transfer', 'credit_card', 'cheque', 'cash', 'other')),
  reference VARCHAR(255),  -- cheque #, transaction ID, etc.
  notes TEXT,
  received_by_user_id BIGINT NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Update invoices table
ALTER TABLE invoices
  ADD COLUMN paid_amount NUMERIC(18,2) DEFAULT 0,
  ADD COLUMN last_payment_date DATE;
```

#### 4.2 Go Models
```go
type InvoicePayment struct {
  ID             uint
  CompanyID      uint
  InvoiceID      uint
  AmountReceived decimal.Decimal
  PaymentDate    time.Time
  PaymentMethod  string  // bank_transfer, credit_card, cheque, cash, other
  Reference      string
  Notes          string
  ReceivedByID   uuid.UUID
  ReceivedBy     *models.User
  CreatedAt      time.Time
  UpdateAt       time.Time
}
```

#### 4.3 Service Layer
```go
func RecordPayment(
  db *gorm.DB,
  companyID uint,
  invoiceID uint,
  amount decimal.Decimal,
  method string,
  paymentDate time.Time,
  reference string,
  actor string,
  userID *uuid.UUID,
) (*InvoicePayment, error)

func UpdateInvoiceStatus(db *gorm.DB, invoice *Invoice) error  // draft → sent → partial → paid / voided

func GetInvoicePayments(db *gorm.DB, companyID, invoiceID uint) ([]InvoicePayment, error)
```

#### 4.4 Web Layer
```go
// Routes
POST /invoices/:id/payments              → record payment
GET  /invoices/:id/payments              → list payments

// Handler
func (s *Server) handleRecordPayment(c *fiber.Ctx) error {
  user := UserFromCtx(c)
  companyID, _ := ActiveCompanyIDFromCtx(c)
  id, _ := parseID(c.Params("id"))
  
  amount := ParseDecimal(c.FormValue("amount"))
  method := c.FormValue("payment_method")
  paymentDate := ParseDate(c.FormValue("payment_date"))
  reference := c.FormValue("reference")
  
  payment, err := services.RecordPayment(
    s.DB, companyID, id, amount, method, paymentDate, reference, user.Email, &user.ID,
  )
  
  // Update invoice status
  services.UpdateInvoiceStatus(s.DB, &invoice)
  
  // Redirect with success
}
```

#### 4.5 关键约束
- Payment 独立 document（可删除，不影响 invoice）
- 部分付款支持（Partial status）
- 金额 ≥ 发票 total 时 → Paid status
- Audit log 记录所有 payments

---

## 🚀 实现顺序建议

```
Week 1 (Phase 1 + 2):
  Day 1: Phase 1 迁移 + Models
  Day 2: Phase 1 Services + Web layer
  Day 3: Phase 1 UI + Tests
  Day 4: Phase 2 PDF setup + HTML templates
  Day 5: Phase 2 Web handlers + Tests

Week 2 (Phase 3 + 4):
  Day 6: Phase 3 迁移 + Email builder
  Day 7: Phase 3 Web layer + Integration test
  Day 8: Phase 4 迁移 + Payment service
  Day 9: Phase 4 Web layer + Status update logic
  Day 10: 集成测试 + Performance 检查
```

---

## ⚠️ 风险点 & 缓解

| 风险 | 可能性 | 影响 | 缓解 |
|------|--------|------|------|
| PDF 生成超时（大量 invoice） | 中 | 性能降低 | 异步 + cache |
| SMTP 连接失败 | 中 | Email 不送 | Retry + fallback + audit |
| Double-posting bug | 低 | 重复 JE | SELECT FOR UPDATE (已有) |
| Payment 重复记录 | 低 | 数据混乱 | Unique index on (invoice, date, amount) |
| Template 改动影响历史 PDF | 低 | 数据不一致 | 用 template_id snapshot |

---

## ✅ 检查清单

### 在开始编码前
- [ ] 确认 wkhtmltopdf 环境
- [ ] Review PROJECT_GUIDE.md 约束
- [ ] Setup testing + mock SMTP
- [ ] Review existing posting tests

### Phase 1
- [ ] Migration 成功运行
- [ ] Template model + GORM tags
- [ ] Template service + error handling
- [ ] Web routes + auth
- [ ] UI (Tailwind-based editor)
- [ ] Unit tests (service layer)

### Phase 2
- [ ] wkhtmltopdf installed + working
- [ ] HTML template rendering
- [ ] PDF generation + caching
- [ ] Download vs preview endpoints
- [ ] Security (auth + company isolation)
- [ ] Performance test (batch)

### Phase 3
- [ ] SMTP test (via EffectiveSMTPForCompany)
- [ ] Email building (with PDF attach)
- [ ] Error handling + retry
- [ ] Audit logging + tracking
- [ ] Notification settings UI

### Phase 4
- [ ] Payment model + migration
- [ ] Status update logic (draft → sent → partial → paid)
- [ ] Web handlers + validation
- [ ] Invoice detail view 显示 payments list
- [ ] Tests

---

## 🎯 总结

**Balanciz Invoice 模块是可以在满足以下条件下完全实现的**:

1. ✅ **已有强大的 posting 引擎** → 不需重写 JE 逻辑
2. ✅ **已有 company 隔离** → 数据安全 OK
3. ✅ **已有 audit 基础** → 完整追踪可实现
4. ✅ **已有 SMTP 基础** → Email 功能快速集成
5. ✅ **已有业务流程** → Invoice draft/post/void 已经运作

**关键是新增功能（Template、PDF、Email）的正确集成**，不是解决基础问题。

**推荐 MVP: Phase 1-3 (9-13 天完成)**
- 正式产品质量
- 完整业务流程（创建 → 发送 → 支付）
- 深度集成 accounting system
- 可审计、可扩展

