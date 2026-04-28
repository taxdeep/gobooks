// 遵循project_guide.md
package pdf

import (
	"testing"

	"balanciz/internal/models"
)

func TestEveryDocumentTypeHasFields(t *testing.T) {
	for _, doc := range models.AllPDFDocumentTypes {
		fs := FieldsForDocType(doc)
		if len(fs) == 0 {
			t.Errorf("doc type %q has empty registry", doc)
		}
	}
}

func TestEveryDocumentTypeHasLineItems(t *testing.T) {
	// Phase 3 keeps line-row fields on the shared commonLineFields slice; if
	// a future doc type is line-less (e.g. expense check), this test will
	// remind us to introduce a no-line variant rather than silently inherit.
	for _, doc := range models.AllPDFDocumentTypes {
		fs := FieldsForDocType(doc)
		hasLine := false
		for _, f := range fs {
			if f.Scope == FieldScopeLine {
				hasLine = true
				break
			}
		}
		if !hasLine {
			t.Errorf("doc type %q has no line-scope fields", doc)
		}
	}
}

func TestEveryDocumentTypeHasCompanyLetterhead(t *testing.T) {
	for _, doc := range models.AllPDFDocumentTypes {
		_, ok := FieldByKey(doc, "company.name")
		if !ok {
			t.Errorf("doc type %q missing company.name field — letterhead would render empty", doc)
		}
		_, ok = FieldByKey(doc, "company.logo")
		if !ok {
			t.Errorf("doc type %q missing company.logo field", doc)
		}
	}
}

func TestFieldByKeyUnknownReturnsFalse(t *testing.T) {
	if _, ok := FieldByKey(models.PDFDocInvoice, "nonexistent.field"); ok {
		t.Fatal("expected unknown key lookup to return false")
	}
	if _, ok := FieldByKey("not_a_doc_type", "invoice.number"); ok {
		t.Fatal("expected unknown doc type lookup to return false")
	}
}

func TestSortFieldsByGroupPutsDocumentFirst(t *testing.T) {
	fs := SortFieldsByGroup(FieldsForDocType(models.PDFDocInvoice))
	if len(fs) == 0 {
		t.Fatal("registry empty")
	}
	if fs[0].Group != "Document" {
		t.Errorf("expected Document group first, got %q", fs[0].Group)
	}
	// Company group should be last.
	if fs[len(fs)-1].Group != "Company" {
		t.Errorf("expected Company group last, got %q", fs[len(fs)-1].Group)
	}
}

func TestInvoiceFieldsIncludeCustomerPONumber(t *testing.T) {
	// Phase 2 added Customer PO# to the AR chain — make sure the registry
	// exposes it so templates can render it.
	if _, ok := FieldByKey(models.PDFDocInvoice, "invoice.customer_po_number"); !ok {
		t.Fatal("Invoice registry must expose invoice.customer_po_number")
	}
	if _, ok := FieldByKey(models.PDFDocSalesOrder, "sales_order.customer_po_number"); !ok {
		t.Fatal("SalesOrder registry must expose sales_order.customer_po_number")
	}
	if _, ok := FieldByKey(models.PDFDocShipment, "shipment.customer_po_number"); !ok {
		t.Fatal("Shipment registry must expose shipment.customer_po_number (joined from SO)")
	}
}
