// 遵循project_guide.md
package search_engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/glebarez/go-sqlite"

	"balanciz/ent"
	"balanciz/ent/migrate"
	"balanciz/internal/searchprojection"
)

// newTestClient spins up an in-memory sqlite-backed ent.Client with the
// search_documents schema created. Sqlite doesn't support pg_trgm /
// tsvector, but the EntEngine's ranking logic uses only case-insensitive
// ContainsFold / EqualFold predicates, which work identically across
// dialects — so unit tests run correctness checks without a Postgres
// fixture. Opens the driver directly (not via enttest) because
// enttest.Open hard-codes the "sqlite3" driver name, which conflicts
// with balanciz' pure-Go glebarez driver registered as "sqlite".
func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	dsn := "file:searchengine_" + t.Name() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(drv))
	if err := client.Schema.Create(context.Background(), migrate.WithDropIndex(true)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func seedDoc(t *testing.T, c *ent.Client, companyID uint, entityType string, entityID uint, title, number string) {
	t.Helper()
	_, err := c.SearchDocument.Create().
		SetCompanyID(companyID).
		SetEntityType(entityType).
		SetEntityID(entityID).
		SetDocNumber(number).
		SetTitle(title).
		SetTitleNative(strings.ToLower(title)).
		SetSubtitle("sub").
		SetNillableDocDate(ptrTime(time.Now())).
		SetURLPath("/" + entityType + "/" + uintKey(entityID)).
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// Phase 4's read-path defence in depth: search always filters on
// CompanyID. This test seeds two companies' data and confirms company 2
// rows never surface for a company 1 search.
func TestEntEngine_CompanyIsolation(t *testing.T) {
	c := newTestClient(t)
	defer c.Close()

	seedDoc(t, c, 1, "customer", 1, "Alice Co-1", "")
	seedDoc(t, c, 2, "customer", 2, "Bob Co-2", "")

	e, err := NewEntEngine(c, searchprojection.AsciiNormalizer{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.Search(context.Background(), SearchRequest{CompanyID: 1, Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, cnd := range resp.Candidates {
		if strings.Contains(cnd.Primary, "Co-2") {
			t.Errorf("company 2 row leaked into company 1 results: %+v", cnd)
		}
	}
}

func TestEntEngine_EmptyQueryReturnsRecent(t *testing.T) {
	c := newTestClient(t)
	defer c.Close()
	seedDoc(t, c, 1, "customer", 1, "Alice", "")
	seedDoc(t, c, 1, "invoice", 1, "POSX US INC.", "INV-1")

	e, _ := NewEntEngine(c, searchprojection.AsciiNormalizer{})
	resp, err := e.Search(context.Background(), SearchRequest{CompanyID: 1, Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != "recent" {
		t.Errorf("Source = %q, want recent", resp.Source)
	}
	if len(resp.Candidates) == 0 {
		t.Error("expected non-empty recent list")
	}
}

func TestEntEngine_TitleMatch(t *testing.T) {
	c := newTestClient(t)
	defer c.Close()
	seedDoc(t, c, 1, "customer", 1, "Lighting Geek Technologies Inc.", "")
	seedDoc(t, c, 1, "customer", 2, "Unrelated Biz", "")

	e, _ := NewEntEngine(c, searchprojection.AsciiNormalizer{})
	resp, err := e.Search(context.Background(), SearchRequest{CompanyID: 1, Query: "light"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) == 0 {
		t.Fatal("expected match on 'light'")
	}
	// First candidate should be the Lighting match — single row wins tier 2.
	if !strings.Contains(resp.Candidates[0].Primary, "Lighting") {
		t.Errorf("unexpected top match: %+v", resp.Candidates[0])
	}
}

func TestEntEngine_DocNumberExactMatch(t *testing.T) {
	c := newTestClient(t)
	defer c.Close()
	seedDoc(t, c, 1, "invoice", 1, "POSX US INC.", "INV-202604")
	seedDoc(t, c, 1, "invoice", 2, "Another Cust", "INV-999")

	e, _ := NewEntEngine(c, searchprojection.AsciiNormalizer{})
	// Exact match on doc number should surface the right invoice.
	resp, err := e.Search(context.Background(), SearchRequest{CompanyID: 1, Query: "INV-202604"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cnd := range resp.Candidates {
		if cnd.Payload != nil && cnd.Payload["doc_num"] == "INV-202604" {
			found = true
		}
	}
	if !found {
		t.Errorf("exact doc number match missing from results: %+v", resp.Candidates)
	}
}

func TestEntEngine_GroupingByEntityType(t *testing.T) {
	c := newTestClient(t)
	defer c.Close()
	seedDoc(t, c, 1, "invoice", 1, "Acme", "INV-1")
	seedDoc(t, c, 1, "customer", 2, "Acme Corp", "")
	seedDoc(t, c, 1, "product_service", 3, "Acme widget", "SKU-1")

	e, _ := NewEntEngine(c, searchprojection.AsciiNormalizer{})
	resp, err := e.Search(context.Background(), SearchRequest{CompanyID: 1, Query: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	// Collect groups encountered (in order).
	var groups []string
	seen := make(map[string]bool)
	for _, cnd := range resp.Candidates {
		if !seen[cnd.GroupKey] {
			seen[cnd.GroupKey] = true
			groups = append(groups, cnd.GroupKey)
		}
	}
	if len(groups) < 2 {
		t.Errorf("expected multiple group families, got %+v", groups)
	}
	// Transactions should appear before contacts in display order.
	posTx, posContacts := -1, -1
	for i, g := range groups {
		if g == GroupTransactions && posTx == -1 {
			posTx = i
		}
		if g == GroupContacts && posContacts == -1 {
			posContacts = i
		}
	}
	if posTx != -1 && posContacts != -1 && posTx > posContacts {
		t.Errorf("transactions (pos %d) should come before contacts (pos %d)", posTx, posContacts)
	}
}

func TestEntEngine_RefusesZeroCompanyID(t *testing.T) {
	c := newTestClient(t)
	defer c.Close()
	e, _ := NewEntEngine(c, searchprojection.AsciiNormalizer{})
	if _, err := e.Search(context.Background(), SearchRequest{CompanyID: 0, Query: "x"}); err == nil {
		t.Error("expected error for zero CompanyID")
	}
}
