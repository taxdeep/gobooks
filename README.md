## GoBooks (Go + Fiber + GORM + Templ)
# Gobooks

> A modern, structured accounting system designed for clarity, scalability, and automation.

---

## ✨ Overview

Gobooks is an accounting platform built with a strong foundation in **accounting correctness**, **clean structure**, and **automation-ready architecture**.

Unlike traditional tools that prioritize flexibility at the cost of consistency, Gobooks enforces **strict accounting rules** while providing intelligent assistance through **rule-based and AI-powered recommendations**.

---

## 🎯 Why Gobooks?

Most accounting software today suffers from:

- Inconsistent chart of accounts
- Weak validation and hidden accounting errors
- Overly flexible systems that lead to long-term chaos
- Poor auditability and unclear data flow

Gobooks solves this by focusing on:

> **Structure first. Automation second. AI last.**

---

## 🧠 Core Principles

- **Correctness > Flexibility**
- **Backend rules > Frontend assumptions**
- **Structured data > user randomness**
- **Auditability by design**
- **AI as assistant, never authority**

---

## 🏗️ Architecture

Gobooks is built as a **multi-tenant accounting platform** with two distinct layers:

### 1. Business App (per company)

- Chart of Accounts
- Journal Entries
- Invoices & Bills
- Customers & Vendors
- Reports
- Banking

### 2. SysAdmin (system-level)

- Company management
- User management
- System control (maintenance mode)
- Audit logs
- Runtime logs
- System metrics (CPU, memory, DB size, storage)

---

## 📊 Key Features

### 🔢 Structured Chart of Accounts
- Enforced account code rules
- Root type + detail type system
- Prefix validation (1xxxx → asset, etc.)
- No deletion — only inactive

---

### 🧾 Journal Entry Engine (Core)
- Debit/Credit enforcement
- Automatic validation
- **Account-level aggregation (clean journal output)**
- Fully traceable lifecycle (post / void / reverse)

---

### 💰 Sales Tax Engine
- Custom tax codes
- Recoverable / non-recoverable tax handling
- Line-level calculation
- Account-level posting aggregation
- Clean and audit-friendly journal output

---

### 🤖 Smart Recommendation System
- Rule-based suggestions (default)
- AI-enhanced suggestions (optional)
- Account name / code / GIFI recommendations
- Confidence scoring

> AI never overrides user input or accounting rules.

---

### 🧠 AI Connect (Optional)
- External AI provider integration
- Secure backend-only API key handling
- Used only for recommendations

---

### 🧾 Audit Log (Built-in)
- Tracks all critical actions
- Linked to entity_number
- Immutable accounting trail

---

### ⚙️ SysAdmin Console
- Full system-level control
- Multi-company management
- Runtime visibility
- Production-grade admin UI

---

### 📈 System Observability
- CPU usage
- Memory usage
- Database size
- Storage usage (attachment-ready)

---




**Version:** 0.0.2

GoBooks is a simple accounting web app focused on core bookkeeping workflows:

- Company Setup
- Chart of Accounts
- Journal Entry
- Invoices and Bills
- Banking (Reconcile / Receive Payment / Pay Bills)
- Reports (Trial Balance, Income Statement, Balance Sheet)
- Audit Log and Reverse Entry

The codebase follows the product guide and keeps implementation straightforward.

### 0.0.2 (summary)

- **Chart of accounts:** Root/detail classification, strict backend validation on create and edit (account code length, numeric rules, prefix vs root type, GIFI format, uniqueness). Optional safe normalization for account name and GIFI trim on save.
- **Recommendations:** Rule-based and optional AI-assisted suggestions via unified API; suggestions are assistive only and never bypass validation.
- **Analytics (lightweight):** Optional `field_recommendation_sources` JSON on accounts records client-reported manual vs rule vs AI apply for product analysis only (not audit-grade; spoofing acceptable for this phase).
- **Docs / ops:** Clarified trust model in code comments; see `PROJECT_GUIDE.md` for product behavior.

## Tech Stack

- Go
- Fiber
- GORM
- PostgreSQL
- Templ
- Tailwind CSS
- HTMX
- Alpine.js
- shopspring/decimal (money precision)

## License

**SPDX-License-Identifier: AGPL-3.0-only**

Gobooks is licensed under the **GNU Affero General Public License v3.0 (AGPL-3.0)**.
See [`LICENSE.md`](LICENSE.md) for copyright, summary, and links to the full text.

This means:

- You are free to use, modify, and distribute this software.
- If you run a modified version of Gobooks as a network service (SaaS),
  you must make the source code of your modifications available to users.

### Commercial Use

If you intend to use Gobooks in a commercial SaaS product
without complying with AGPL requirements, contact **TAXDEEP CORP.**
at [info@taxdeep.com](mailto:info@taxdeep.com) to obtain a commercial license.

### Trademark Notice

**GoBooks** is a trademark of **TAXDEEP CORP.** and may not be used in derivative
products without permission.

## Quick Start (Recommended): Docker

### 1) Prerequisites

- Docker Desktop installed and running

### 2) Run

From `d:\Coding\gobooks`:

```bash
docker compose up --build
```

### 3) Open App

- <http://localhost:6768>

On first run, you will see the Setup page and can create your company profile.

## Local Development (Without Docker)

### 1) Prerequisites

- Go 1.22+
- Node.js 18+
- PostgreSQL 14+

### 2) Configure environment

```bash
copy .env.example .env
```

Update `.env` with your local PostgreSQL values.

### 3) Install frontend dependencies

```bash
npm install
```

### 4) Build Tailwind CSS

```bash
npm run build:css
```

For development watch mode (optional, another terminal):

```bash
npm run dev:css
```

### 5) Run backend

```bash
go run ./cmd/gobooks
```

### 6) Open app

- <http://localhost:6768>

## Useful Commands

- Run app with Docker:
  - `docker compose up --build`
- Stop Docker stack:
  - `docker compose down`
- Run backend locally:
  - `go run ./cmd/gobooks`
- Build CSS once:
  - `npm run build:css`
- Watch CSS:
  - `npm run dev:css`

## Project Structure (High Level)

- `cmd/gobooks/` - app entrypoint
- `internal/version/` - release version string (`0.0.2`)
- `internal/config/` - environment config loading
- `internal/db/` - database connection + migration
- `internal/models/` - GORM models
- `internal/services/` - business logic (reports, payments, reverse, audit helpers)
- `internal/web/` - Fiber server, routes, handlers, templates
- `internal/web/templates/` - Templ components and pages

## Troubleshooting

- `go` command not found:
  - Install Go and add it to PATH.
- `docker` command not found:
  - Install Docker Desktop and restart terminal.
- Database connection error:
  - Check `.env` values (`DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`).
- Page has no styles:
  - Run `npm run build:css` (or `npm run dev:css` in development).

