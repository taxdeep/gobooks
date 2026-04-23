// pdf_template_editor.js — Alpine component for the Phase 3 (G7) PDF
// template visual editor.
// v=1
//
// Owns a mutable copy of the template's schema (page + theme + blocks),
// exposes per-block CRUD methods, and persists via POSTs to the
// pdf_template edit handlers.
//
// Live preview: the editor builds the current schema_json on each
// "Refresh" click, POSTs to /pdf-templates/preview-html, and dumps the
// returned HTML into the iframe via srcdoc. HTML preview (not PDF) keeps
// the round-trip under ~50ms — chromedp is reserved for actual document
// rendering.
function gobooksTemplateEditor() {
  return {
    // ── Init-only config (read in init() from data-* attrs) ────────────────
    docType:         "",
    templateID:      0,
    systemReadOnly:  false,
    fields:          [],   // [{key,label,type,group,scope}]

    // ── Mutable editor state ───────────────────────────────────────────────
    meta: { name: "", description: "" },
    schema: {
      version: 1,
      page:   { size: "Letter", orientation: "portrait", margins: [40,40,40,40] },
      theme:  { accent_color: "#0066cc", text_color: "#1a1a1a", muted_color: "#6b7280", font_family: "Inter", font_size_pt: 11, line_height: "1.4" },
      blocks: [],
    },
    expanded:    {},        // {idx: bool}
    saving:      false,
    saveAsOpen:  false,
    _blockKeySeq: 0,

    init() {
      const el = this.$el;
      this.docType        = el.dataset.docType   || "";
      this.templateID     = parseInt(el.dataset.templateId, 10) || 0;
      this.systemReadOnly = el.dataset.systemReadonly === "true";
      try { this.fields = JSON.parse(el.dataset.fields || "[]"); }
      catch (_) { this.fields = []; }

      this.meta.name        = el.dataset.initialName        || "";
      this.meta.description = el.dataset.initialDescription || "";

      // Initial schema comes from data-initial-schema (pretty JSON).
      const raw = el.dataset.initialSchema || "";
      if (raw) {
        try {
          const parsed = JSON.parse(raw);
          if (parsed && typeof parsed === "object") {
            this.schema = this._fillDefaults(parsed);
          }
        } catch (_) { /* leave defaults */ }
      }
      // Stable per-block keys for x-for so reorder doesn't reuse stale DOM.
      this.schema.blocks.forEach(b => b._key = ++this._blockKeySeq);
      this.schema.blocks.forEach((_, i) => { this.expanded[i] = i === 0; });

      // First preview render after initial paint settles.
      this.$nextTick(() => this.refreshPreview());
    },

    // ── Schema-shape helpers ───────────────────────────────────────────────
    _fillDefaults(s) {
      s.version = s.version || 1;
      s.page    = Object.assign({ size:"Letter", orientation:"portrait", margins:[40,40,40,40] }, s.page || {});
      if (!Array.isArray(s.page.margins) || s.page.margins.length !== 4) {
        s.page.margins = [40,40,40,40];
      }
      s.theme   = Object.assign({ accent_color:"#0066cc", text_color:"#1a1a1a", muted_color:"#6b7280", font_family:"Inter", font_size_pt:11, line_height:"1.4" }, s.theme || {});
      s.blocks  = Array.isArray(s.blocks) ? s.blocks : [];
      // Each block.config is a pre-parsed object after JSON.parse — but the
      // server-side struct stores it as base64-encoded raw JSON. Detect strings
      // and parse them; otherwise leave as-is.
      s.blocks.forEach(b => {
        if (typeof b.config === "string") {
          try { b.config = JSON.parse(b.config); } catch (_) { b.config = {}; }
        }
        if (b.config === null || typeof b.config !== "object") b.config = {};
      });
      return s;
    },

    // ── Field-picker helpers (dropdowns) ───────────────────────────────────
    fieldGroups() {
      // Group doc-scope fields by .group; line-scope fields are filtered out
      // (those only belong inside lines_table.columns).
      const groups = {};
      for (const f of this.fields) {
        if (f.scope === "line") continue;
        if (!groups[f.group]) groups[f.group] = [];
        groups[f.group].push(f);
      }
      return Object.entries(groups).map(([name, items]) => ({ name, items }));
    },

    lineFields() {
      return this.fields.filter(f => f.scope === "line");
    },

    moneyFields() {
      // Used by the totals block — only money-typed doc fields.
      return this.fields.filter(f => f.type === "money" && f.scope === "document");
    },

    // ── Block-list management ──────────────────────────────────────────────
    blockTitle(blk) {
      switch (blk.type) {
        case "header":      return "Header";
        case "two_col":     return "Two-column band";
        case "lines_table": return "Line items";
        case "totals":      return "Totals";
        case "text":        return blk.config?.title ? "Text — " + blk.config.title : "Text";
        case "spacer":      return "Spacer (" + (blk.config?.height_pt || 16) + "pt)";
      }
      return blk.type;
    },

    toggleExpand(idx) { this.expanded[idx] = !this.expanded[idx]; },

    addBlock(type) {
      if (this.systemReadOnly) return;
      const blk = {
        id: "blk_" + Date.now().toString(36),
        _key: ++this._blockKeySeq,
        type: type,
        visible: true,
        config: this._defaultConfigFor(type),
      };
      this.schema.blocks.push(blk);
      this.expanded[this.schema.blocks.length - 1] = true;
    },

    _defaultConfigFor(type) {
      switch (type) {
        case "header":      return { left: [], right: [] };
        case "two_col":     return { left_title: "", left: [], right_title: "", right: [] };
        case "lines_table": return { columns: [], empty_rows_hint: 0, show_product_sku: false };
        case "totals":      return { rows: [], show_grand_total_emphasis: true };
        case "text":        return { title: "", body: "", align: "", italic: false, bold: false };
        case "spacer":      return { height_pt: 16 };
      }
      return {};
    },

    moveBlock(idx, delta) {
      if (this.systemReadOnly) return;
      const tgt = idx + delta;
      if (tgt < 0 || tgt >= this.schema.blocks.length) return;
      const arr = this.schema.blocks;
      [arr[idx], arr[tgt]] = [arr[tgt], arr[idx]];
      // Swap expanded state too.
      const a = this.expanded[idx], b = this.expanded[tgt];
      this.expanded[idx] = b; this.expanded[tgt] = a;
    },

    removeBlock(idx) {
      if (this.systemReadOnly) return;
      if (!confirm("Delete this block?")) return;
      this.schema.blocks.splice(idx, 1);
    },

    // ── FieldRef array helpers (for header / two_col left+right slots) ─────
    addFieldRef(blkIdx, slot) {
      if (this.systemReadOnly) return;
      const blk = this.schema.blocks[blkIdx];
      if (!blk) return;
      if (!Array.isArray(blk.config[slot])) blk.config[slot] = [];
      blk.config[slot].push({ type: "field", field: "", value: "", label: "", format: "", hide_when_empty: false, emphasis_level: 0 });
    },

    removeFieldRef(blkIdx, slot, refIdx) {
      if (this.systemReadOnly) return;
      const arr = this.schema.blocks[blkIdx]?.config?.[slot];
      if (Array.isArray(arr)) arr.splice(refIdx, 1);
    },

    // ── LinesTable column helpers ──────────────────────────────────────────
    addColumn(blkIdx) {
      if (this.systemReadOnly) return;
      const blk = this.schema.blocks[blkIdx];
      if (!blk) return;
      if (!Array.isArray(blk.config.columns)) blk.config.columns = [];
      blk.config.columns.push({ field: "lines.description", label_override: "", width_pct: 0, align: "" });
    },

    removeColumn(blkIdx, colIdx) {
      if (this.systemReadOnly) return;
      const cols = this.schema.blocks[blkIdx]?.config?.columns;
      if (Array.isArray(cols)) cols.splice(colIdx, 1);
    },

    // ── Totals row helpers ─────────────────────────────────────────────────
    addTotalRow(blkIdx) {
      if (this.systemReadOnly) return;
      const blk = this.schema.blocks[blkIdx];
      if (!blk) return;
      if (!Array.isArray(blk.config.rows)) blk.config.rows = [];
      blk.config.rows.push({ field: "", label_override: "" });
    },

    removeTotalRow(blkIdx, rowIdx) {
      if (this.systemReadOnly) return;
      const rows = this.schema.blocks[blkIdx]?.config?.rows;
      if (Array.isArray(rows)) rows.splice(rowIdx, 1);
    },

    // ── Save / Save as ─────────────────────────────────────────────────────
    serializeSchema() {
      // Strip non-persisted helper fields (_key) before serialising. Server-
      // side ParseSchema ignores unknown fields, but keeping the JSON clean
      // makes diffs friendlier.
      const cleaned = JSON.parse(JSON.stringify(this.schema));
      cleaned.blocks.forEach(b => { delete b._key; });
      return JSON.stringify(cleaned);
    },

    async save() {
      if (this.systemReadOnly || this.saving) return;
      this.saving = true;
      try {
        const form = new FormData();
        form.set("name",        this.meta.name);
        form.set("description", this.meta.description);
        form.set("schema_json", this.serializeSchema());
        const resp = await (window.gobooksFetch || fetch)("/pdf-templates/" + this.templateID + "/save-schema", {
          method: "POST",
          body:   form,
        });
        if (resp.redirected) {
          window.location.href = resp.url;
          return;
        }
        if (!resp.ok) {
          const text = await resp.text();
          alert("Save failed: " + text);
        }
      } catch (e) {
        alert("Save failed: " + e.message);
      } finally {
        this.saving = false;
      }
    },

    openSaveAs() {
      this.saveAsOpen = true;
    },

    // ── Live preview ───────────────────────────────────────────────────────
    async refreshPreview() {
      const iframe = this.$refs.preview;
      if (!iframe) return;
      try {
        const resp = await (window.gobooksFetch || fetch)("/pdf-templates/preview-html", {
          method:  "POST",
          headers: { "Content-Type": "application/json" },
          body:    JSON.stringify({ doc_type: this.docType, schema_json: this.serializeSchema() }),
        });
        if (!resp.ok) {
          const text = await resp.text();
          iframe.srcdoc = "<pre style='padding:1rem;color:#b91c1c'>Preview failed: " + text.replace(/[<>&]/g, c => ({"<":"&lt;",">":"&gt;","&":"&amp;"}[c])) + "</pre>";
          return;
        }
        const html = await resp.text();
        iframe.srcdoc = html;
      } catch (e) {
        iframe.srcdoc = "<pre style='padding:1rem;color:#b91c1c'>Preview error: " + e.message + "</pre>";
      }
    },
  };
}
