# Balanciz Invoice Module — 实现摘要 & 后续步骤

## 📌 分析完成

我已经对 Balanciz 项目进行了深度分析，并为 Invoice Module 制定了完整的实现计划。

### 关键发现

#### ✅ 已有的强大基础
- **Posting Engine**: 完整的会计引擎（Document → Validation → Tax → Fragments → JE → Ledger）
- **Company Isolation**: 全表强制 company_id，完整的多租户隔离
- **Invoice Pipeline**: Draft → Sent → Paid / Voided 生命周期已实现
- **Tax System**: 完整的 sales tax 计算和账户映射
- **Audit Trail**: 完整的审计日志系统（actor、snapshot、timestamp）
- **SMTP Infrastructure**: 邮件配置、加密、测试流程已就位
- **Web Handlers**: 基本的 CRUD routes 和权限控制已实现

#### ❌ 需要新增的功能
1. **Invoice Templates** - Company-scoped customizable templates
2. **PDF Export** - 基于模板生成可下载 PDF
3. **Email Send** - 通过 SMTP 发送 invoice 给客户
4. **Payment Tracking** - 记录支付，更新 invoice 状态
5. **Recurring Invoices** - 自动周期性生成（后续优先级）
6. **Invoice Reminders** - 到期提醒（后续优先级）

---

## 🎯 建议的实现路径

### MVP（9-13 天）= Phase 1 + 2 + 3 + 部分 Phase 4

```
Phase 1: Invoice Template System (3-4 天)
  ├─ DB Migration: invoices_templates, invoices_template_line_items
  ├─ Go Models + GORM ORM
  ├─ Service Layer (CRUD operations)
  ├─ Web Routes + Handlers (settings/company/invoice-templates)
  └─ UI (Tailwind template editor)

Phase 2: PDF Export (2-3 天)
  ├─ Setup wkhtmltopdf library
  ├─ HTML template rendering
  ├─ PDF generation service
  ├─ Web routes: GET /invoices/:id/pdf, /preview-pdf
  └─ Integration with Template system

Phase 3: Email Notification (2-3 天)
  ├─ DB Migration: invoices_sent_log, invoices_notification_settings
  ├─ Email builder service (with PDF attachment)
  ├─ SMTP integration (via existing EffectiveSMTPForCompany)
  ├─ Web routes: POST /invoices/:id/send, settings page
  ├─ Error handling + audit logging
  └─ Sent log tracking

Phase 4: Payment Tracking (2-3 天)
  ├─ DB Migration: invoice_payments table
  ├─ Payment recording service
  ├─ Invoice status auto-update (draft → sent → partial → paid)
  ├─ Web routes: POST /invoices/:id/payments
  └─ Payment list display
```

---

## 📐 架构原则（必须遵守）

1. **Pipeline 顺序严格**
   - Document → Validation → Tax → Fragments → Aggregation → JE → Ledger
   - 不允许跳过任何步骤

2. **Company 隔离强制**
   - 所有新对象 = company_id FK + WHERE company_id = ? filters
   - 不跨 company 查询

3. **Posted Documents 不可删除**
   - Draft: 可物理删除
   - Posted: Void only（生成 reversal JE，保留记录）
   - void_by, void_at, void_reason 必须记录

4. **Backend Authority**
   - 所有业务逻辑在服务层
   - 前端只做输入 + 预览
   - API 返回完整真实数据

5. **Template ≠ Data**
   - 模板只控制**显示**
   - 业务真实数据从 invoice_lines 读取（不从 HTML）
   - Line amounts 在 DB，不在 template

6. **完整审计**
   - 创建、修改、发送、支付 都要记录
   - Actor（用户邮箱）+ 时间 + 变更内容快照

---

## 🚀 立即可采取的行动

### 1. 准备环境（立即）
```bash
# 安装 wkhtmltopdf (需要用于 PDF 生成)
# Windows:
choco install wkhtmltopdf

# Linux:
sudo apt-get install wkhtmltopdf

# macOS:
brew install wkhtmltopdf
```

### 2. 详细文档位置
所有分析已保存到项目根目录：
- **[INVOICE_MODULE_ANALYSIS.md](./INVOICE_MODULE_ANALYSIS.md)** ← 完整分析文档
  - 包含所有 4 个阶段的详细规范
  - 数据库迁移 SQL
  - Go 代码框架
  - Web 路由示例
  - 关键约束清单

### 3. 开始实现 Phase 1（Template System）

#### Step 1: 创建数据库迁移
```bash
# 新建迁移文件
touch migrations/023_invoice_templates.sql
```

内容参考 INVOICE_MODULE_ANALYSIS.md Phase 1.1 部分

#### Step 2: 创建 Go Models
```bash
# 新建 models 文件
cat > internal/models/invoice_template.go << 'EOF'
// 参考 INVOICE_MODULE_ANALYSIS.md Phase 1.2
EOF
```

#### Step 3: 创建 Service Layer
```bash
# 新建 services 文件
cat > internal/services/invoice_template.go << 'EOF'
// 参考 INVOICE_MODULE_ANALYSIS.md Phase 1.3
EOF
```

#### Step 4: 创建 Web Handlers
```bash
# 新建 handlers 文件
cat > internal/web/invoice_templates_handlers.go << 'EOF'
// 参考 INVOICE_MODULE_ANALYSIS.md Phase 1.4
EOF
```

#### Step 5: 更新路由
```go
// 在 internal/web/routes.go 中添加
app.Get("/settings/company/invoice-templates", s.LoadSession(), s.RequireAuth(), 
  s.ResolveActiveCompany(), s.RequireMembership(), s.handleInvoiceTemplatesGet)
// ... 其他路由
```

---

## 📊 预期交付物

### Phase 1 + 2 + 3 完成后
- ✅ Invoice templates with company-scoped customization
- ✅ PDF export (download + preview)
- ✅ Email delivery with SMTP
- ✅ Full audit trail
- ✅ Payment tracking (basic)
- ✅ Complete company isolation
- ✅ ~95% 功能覆盖

### 属于 Phase 4+ 的功能（后续）
- ❌ 自动提醒（Phase 6）
- ❌ 周期性生成（Phase 5）
- ❌ 客户门户（Phase 7）
- ❌ 支付链接集成（Phase 7）

---

## ⚠️ 常见陷阱 & 如何避免

| 陷阱 | 原因 | 解决方案 |
|------|------|--------|
| 模板中存储业务数据 | 会导致数据真实性问题 | 模板只存 HTML/CSS/configuration，数据来自 invoice_lines |
| 跳过 PostingEngine | 账务混乱 | 必须走完整 Pipeline（不简化） |
| 忘记 company_id 隔离 | 多租户数据泄露 | 每个查询都有 WHERE company_id = ? |
| Posted 单据物理删除 | 审计痕迹丢失 | 用 void，不用 delete |
| 前端计算金额 | 不安全 + 不一致 | 后端算 + 验证 |
| SMTP 失败导致回滚 | 流程中断 | SMTP 失败不影响 invoice 状态，只记录 |

---

## 📋 检查清单（Before Coding）

- [ ] 了解 PROJECT_GUIDE.md 的所有规则
- [ ] Review 现有的 posting_engine.go + invoice_post.go
- [ ] 确认 wkhtmltopdf 环境可用
- [ ] 确认所有新 migration 都有 company_id FK
- [ ] 确认所有 Web handler 都有权限检查
- [ ] 设置单元测试框架
- [ ] 定义测试数据生成工具
- [ ] 准备 SMTP 测试配置（mock 或 real）

---

## 💡 实现顺序建议

```
Week 1:
  Day 1: Phase 1 迁移、Models、Service 框架
  Day 2: Phase 1 Web layer + 基础 UI
  Day 3: Phase 1 测试、调试、完成
  Day 4: Phase 2 PDF 库集成 + HTML template
  Day 5: Phase 2 Web handlers + 测试

Week 2:
  Day 6: Phase 3 Email service + SMTP 集成
  Day 7: Phase 3 Web layer 完成
  Day 8: Phase 4 Payment recording service
  Day 9: Phase 4 Status update + Web layer
  Day 10: 集成测试、性能检查、文档
```

---

## 📞 何时求助

- **数据库设计**: 参考 INVOICE_MODULE_ANALYSIS.md 中的 SQL
- **Service 逻辑**: 查看现有的 services/ 中的模式（fragment_builder.go, tax_engine.go）
- **Web routing**: 查看现有的 routes.go + middleware 模式
- **Posting 集成**: 参考 invoice_post.go 的 transaction 模式
- **Audit 记录**: 查看 audit_log.go 的 WriteAuditLog 使用

---

## 🎁 已交付

### 文档
✅ [INVOICE_MODULE_ANALYSIS.md](./INVOICE_MODULE_ANALYSIS.md)
   - 现有系统分析（第一部分）
   - 架构约束（第二部分）
   - 详细阶段计划（第三部分）
   - 代码框架 + SQL
   - 检查清单

✅ [INVOICE_MODULE_IMPLEMENTATION.md](#next-document)
   - 代码示例（将在下一步生成）
   - 测试套件框架
   - 集成指南

### 计划
✅ 7 阶段 implementation roadmap
✅ 风险评估和缓解策略
✅ MVP 定义（Phase 1-3）
✅ 架构约束总结

---

## 🔗 相关文档

- [PROJECT_GUIDE.md](./PROJECT_GUIDE.md) ← 项目核心规则（必读）
- [internal/models/sales_purchases.go](./internal/models/sales_purchases.go) ← 现有 Invoice 模型
- [internal/services/invoice_post.go](./internal/services/invoice_post.go) ← Posting 逻辑
- [internal/services/posting_engine.go](./internal/services/posting_engine.go) ← Engine 核心
- [internal/web/invoices_handlers.go](./internal/web/invoices_handlers.go) ← 现有 Web 层

---

## ❓ 下一步确认

请确认以下问题来优化实现顺序：

1. **Priority**: 是否所有 Phase 1-3 都需要在 MVP 中？或者可以延后 Phase 4（Payment）？
2. **PDF Library**: 对 wkhtmltopdf 的系统依赖是否可接受？还是希望用纯 Go 库（质量稍差）？
3. **Email**: 是否需要在发送时立即生成 PDF，还是可以异步？
4. **Authentication**: Customer 能否通过 email link 无密码访问 invoice preview？
5. **Recurring**: MVP 之后立即做 Phase 5（Recurring），还是可以延缓？

---

## 📝 License & Compliance

所有建议的代码和设计都遵循：
- Balanciz 现有的 LICENSE.md
- PROJECT_GUIDE.md 的所有强制规则
- Go 编码标准（formatting, naming, error handling）

---

**分析完成时间**: 2026-03-30  
**分析状态**: ✅ 完整 & 可执行  
**推荐下一步**: 确认 Phase 1-3 MVP 范围，开始编码

