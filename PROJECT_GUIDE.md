Gobooks Project Guide v4（Final · 可执行统一版）

⚠️ 最高优先级约束（Supreme Authority）
所有代码 / schema / UI / AI 行为必须遵守本文件
如有冲突：以本文件为准

一、🎯 Product Definition（升级版）
核心一句话

Gobooks = 多公司隔离的强规则会计系统 + 控制层 + AI建议层

产品本质（升级）

Gobooks 是：

一个 多公司（multi-tenant）会计系统
一个 强约束 accounting engine（不是工具）
一个 AI 辅助理解系统（不是自动化系统）

二、🔒 Core Principles（最终版）
不可违反（硬约束）
Correctness > Flexibility
Backend Authority > Frontend
Structure > Convenience
Auditability > Performance
Company Isolation > Everything（新增最高原则）
AI = Suggestion Layer ONLY

三、🏢 Multi-Company Architecture（🔥正式纳入核心）
3.1 基本模型
一个 user → 多个 company
session 必须包含：
active_company_id
3.2 强制规则（必须实现）

所有核心数据表必须：

company_id NOT NULL
3.3 强制校验（所有写操作）
assert(document.company_id == session.company_id)
assert(account.company_id == session.company_id)
assert(tax.company_id == session.company_id)
3.4 禁止行为（硬性）

❌ 跨公司 Journal Entry
❌ 跨公司 Ledger
❌ 共享 COA / Customer / Vendor

3.5 UI 行为（新增）
顶部必须有：
当前公司
切换公司
切换公司 = 完整数据上下文切换
四、🧩 System Architecture（双层）
1️⃣ Business App
多公司业务系统
所有 accounting / reporting / reconciliation 在这里
2️⃣ SysAdmin（完全隔离）
独立登录系统
不参与业务写入
可控制：
company lifecycle
users
system mode
五、🔁 Posting Engine（不可破坏核心）
标准流程（唯一入口）
Document
→ Validation
→ Tax
→ Posting Fragments
→ Aggregation
→ Journal Entry
→ Ledger Entries
强制规则
❌ 禁止绕过 Posting Engine
❌ 禁止直接写 Ledger
✅ 所有 JE 必须来自 source
Journal Entry 必须字段
company_id
status
source_type
source_id
totals
状态机
draft → posted → voided / reversed
六、💾 数据与标识系统（增强）
Entity Number（系统ID）
ENYYYY########

规则：

全局唯一
immutable
backend生成
Display Number（业务）
可配置
可重复检测
不参与 identity
七、🧾 Chart of Accounts（结构锁死）
Account Type（不可扩展）
asset
liability
equity
revenue
cost_of_sales
expense
Code 强规则（必须 enforce）
Code	Type
1xxxx	asset
2xxxx	liability
3xxxx	equity
4xxxx	revenue
5xxxx	cost_of_sales
6xxxx	expense
删除规则
❌ delete
✅ inactive
COA Template（新增强化）
system default template
company 创建时自动生成
标记：is_system_default = true

八、💰 Tax Engine（强化一致性）
原则 Tax = line-level calculation → account-level aggregation

Sales
revenue → revenue
tax → tax payable
Purchase
类型	行为
recoverable	tax receivable
non-recoverable	expense

强制
如果遇到Tax 独立 posting 会有一个提醒。

九、📘 Journal Entry（最终约束）
必须
*按 account 聚合
*与 source 强关联

禁止
❌ JE 无 source
❌ source 改但 JE 不变

十、🧭 Sidebar（UI强约束 · 不可改）
Core
- Dashboard
- Journal Entry
- Invoices
- Bills

Sales & Get Paid
- Customers
- Receive Payment

Expense & Bills
- Vendors
- Pay Bills

Accounting
- Chart of Accounts
- Reconciliation
- Reports

Settings
禁止
❌ Contacts 分类
❌ Banking 模块
❌ Reports 出现在其他位置

十一、📊 UI / UX Design Principles（Wave参考整合）
风格
Clean
Stable
Business-first
核心体验
左侧导航为核心入口
Dashboard = 状态总览（非复杂BI）
Reports = 标准报表入口
多公司 UX（关键）
切换公司 → UI不变 / 数据全换
用户始终知道“当前在哪个公司”
十二、🔔 Notifications（系统依赖）
必须字段
config
test_status
last_tested_at
verification_ready
强制规则
SMTP 未验证 → 禁止发送验证码
config 更新 → 状态失效
十三、🔐 User Security（强化）
email change → 验证
password change → 验证

验证码：

6位
case-insensitive
单次有效
有效期限制
十四、🧮 Reconciliation（🔥核心控制层）
定义

Reconciliation = Accounting Control Layer

状态机
draft → in_progress → completed → reopened → cancelled
匹配能力（必须）
one-to-one
one-to-many
many-to-one
split
完成条件
difference == 0
UI（强制）
QuickBooks 风格
summary bar
inflow / outflow 分离
十五、🔁 Void Reconciliation（严格限制）
仅允许最后一个 completed
不删除
rollback match
必须字段
is_voided
voided_by
voided_at
void_reason
十六、🤖 AI Layer（重新定义 · 融合你今天思考）
本质

AI = 外部会计（Advisor），不是执行者

❌ 禁止
AI 改账
AI 自动 reconciliation
AI bypass validation
✅ 允许
suggestion
ranking
explanation
当前能力
JE 异常检测
tax 合理性提示
报表解释
🔥 未来（多公司 AI）

（重要：只在系统稳定后）

intercompany detection
due to / due from pairing
mismatch alert
consolidation assist
十七、🚧 Intercompany Strategy（明确阶段）
当前阶段（硬限制）

❌ 不允许 intercompany
❌ 不允许跨公司 transaction

未来阶段（解锁条件）

仅当满足：

Posting Engine 稳定
Reconciliation 成熟
Audit 完整

才允许：

intercompany JE link
group reporting
elimination entries
十八、🧠 AI Auto Match（Reconciliation AI）
三层结构
Rules → Scoring → AI Enhancement
Suggestion 实体（必须）
reconciliation_match_suggestions
suggestion_lines
用户行为
Accept → match
Reject → no change
必须
explainability
十九、🧠 Reconciliation Memory

用途：

学习历史
提升建议质量
限制
可解释
非黑盒
二十、📜 Audit（全系统）

必须记录：

match / unmatch
suggestion accept / reject
reconciliation finish
reconciliation void
auto match run
二十一、📦 Data Principles（最终铁律）
必须
company_id 隔离
entity_number immutable
backend authority
JE 可追溯
禁止
删除历史
AI 改账
绕过 validation
JE 脱离业务
🔚 最终总结（终极版）

Gobooks 是一个：

✔ 多公司隔离系统
✔ 强规则会计引擎（Posting + COA + JE）
✔ 控制层（Reconciliation + Audit）
✔ 业务层（Sales / Expense）
✔ AI建议层（非执行）
✔ 系统层（SysAdmin + Observability）