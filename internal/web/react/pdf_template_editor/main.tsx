import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import grapesjs from "grapesjs";
import "grapesjs/dist/css/grapes.min.css";

declare global {
  interface Window {
    gobooksFetch?: (url: string, options?: RequestInit) => Promise<Response>;
  }
}

type Field = {
  key: string;
  label: string;
  type: string;
  group: string;
  scope: "document" | "line" | string;
};

type FieldRef = {
  type?: "field" | "literal" | "image" | string;
  field?: string;
  value?: string;
  label?: string;
  format?: string;
  hide_when_empty?: boolean;
  emphasis_level?: number;
};

type Block = {
  id: string;
  type: string;
  visible: boolean;
  config: Record<string, unknown>;
};

type Schema = {
  version: number;
  page: {
    size: string;
    orientation: string;
    margins: number[];
  };
  theme: {
    accent_color: string;
    font_family: string;
    font_size_pt: number;
    line_height: string;
    text_color: string;
    muted_color: string;
  };
  blocks: Block[];
};

type RootConfig = {
  docType: string;
  docTypeLabel: string;
  templateID: string;
  saveUrl: string;
  saveAsUrl: string;
  previewUrl: string;
  previewPageUrl: string;
  templatesUrl: string;
  systemReadOnly: boolean;
  initialName: string;
  initialDescription: string;
  fields: Field[];
  schema: Schema;
};

type SelectionState = {
  component: any;
  id: string;
  type: string;
  visible: boolean;
  config: Record<string, unknown>;
};

const blockTypes = ["header", "two_col", "lines_table", "totals", "text", "spacer"];

const blockLabels: Record<string, string> = {
  header: "Header",
  two_col: "Two-column band",
  lines_table: "Line items table",
  totals: "Totals block",
  text: "Text block",
  spacer: "Spacer"
};

const defaultSchema: Schema = {
  version: 1,
  page: { size: "Letter", orientation: "portrait", margins: [40, 40, 40, 40] },
  theme: {
    accent_color: "#0066cc",
    font_family: "Inter",
    font_size_pt: 11,
    line_height: "1.4",
    text_color: "#1a1a1a",
    muted_color: "#6b7280"
  },
  blocks: []
};

function escapeHTML(value: unknown): string {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function escapeAttr(value: unknown): string {
  return escapeHTML(value);
}

function parseJSON<T>(value: string | undefined, fallback: T): T {
  if (!value) return fallback;
  try {
    return JSON.parse(value) as T;
  } catch {
    return fallback;
  }
}

function normalizeSchema(raw: Partial<Schema>): Schema {
  const page = { ...defaultSchema.page, ...(raw.page || {}) };
  if (!Array.isArray(page.margins) || page.margins.length !== 4) {
    page.margins = [...defaultSchema.page.margins];
  }
  const theme = { ...defaultSchema.theme, ...(raw.theme || {}) };
  const blocks = Array.isArray(raw.blocks)
    ? raw.blocks.map((block, index) => ({
        id: block.id || `blk_${Date.now().toString(36)}_${index}`,
        type: block.type || "text",
        visible: block.visible !== false,
        config: normalizeConfig(block.config)
      }))
    : [];
  return { version: raw.version || 1, page, theme, blocks };
}

function normalizeConfig(config: unknown): Record<string, unknown> {
  if (typeof config === "string") {
    return parseJSON<Record<string, unknown>>(config, {});
  }
  if (config && typeof config === "object" && !Array.isArray(config)) {
    return config as Record<string, unknown>;
  }
  return {};
}

function parseRoot(root: HTMLElement): RootConfig {
  return {
    docType: root.dataset.docType || "",
    docTypeLabel: root.dataset.docTypeLabel || "PDF",
    templateID: root.dataset.templateId || "",
    saveUrl: root.dataset.saveUrl || "",
    saveAsUrl: root.dataset.saveAsUrl || "",
    previewUrl: root.dataset.previewUrl || "/pdf-templates/preview-html",
    previewPageUrl: root.dataset.previewPageUrl || "",
    templatesUrl: root.dataset.templatesUrl || "/pdf-templates",
    systemReadOnly: root.dataset.systemReadonly === "true",
    initialName: root.dataset.initialName || "",
    initialDescription: root.dataset.initialDescription || "",
    fields: parseJSON<Field[]>(root.dataset.fields, []),
    schema: normalizeSchema(parseJSON<Partial<Schema>>(root.dataset.initialSchema, defaultSchema))
  };
}

function fieldLabel(fields: Field[], key: string | undefined): string {
  if (!key) return "";
  return fields.find((field) => field.key === key)?.label || key;
}

function asFieldRefs(value: unknown): FieldRef[] {
  return Array.isArray(value) ? (value as FieldRef[]) : [];
}

function asColumns(value: unknown): Array<Record<string, unknown>> {
  return Array.isArray(value) ? (value as Array<Record<string, unknown>>) : [];
}

function asBool(value: unknown, fallback = false): boolean {
  if (typeof value === "boolean") return value;
  return fallback;
}

function asNumber(value: unknown, fallback = 0): number {
  const n = Number(value);
  return Number.isFinite(n) ? n : fallback;
}

function configAttr(config: Record<string, unknown>): string {
  return encodeURIComponent(JSON.stringify(config));
}

function readConfigAttr(value: string | undefined): Record<string, unknown> {
  if (!value) return {};
  try {
    return normalizeConfig(JSON.parse(decodeURIComponent(value)));
  } catch {
    return {};
  }
}

function defaultConfigFor(type: string, fields: Field[]): Record<string, unknown> {
  const docFields = fields.filter((field) => field.scope !== "line");
  const lineFields = fields.filter((field) => field.scope === "line");
  const moneyFields = fields.filter((field) => field.scope === "document" && field.type === "money");
  switch (type) {
    case "header":
      return {
        left: docFields.slice(0, 2).map((field) => ({ type: "field", field: field.key, label: "" })),
        right: docFields.slice(2, 5).map((field) => ({ type: "field", field: field.key, label: "" }))
      };
    case "two_col":
      return {
        left_title: "Bill To",
        left: docFields.slice(0, 3).map((field) => ({ type: "field", field: field.key, label: field.label })),
        right_title: "Details",
        right: docFields.slice(3, 6).map((field) => ({ type: "field", field: field.key, label: field.label }))
      };
    case "lines_table":
      return {
        columns: lineFields.slice(0, 5).map((field) => ({ field: field.key, label_override: "", width_pct: 0, align: "" })),
        empty_rows_hint: 0,
        show_product_sku: false
      };
    case "totals":
      return { rows: moneyFields.slice(-4).map((field) => ({ field: field.key, label_override: "" })), show_grand_total_emphasis: true };
    case "spacer":
      return { height_pt: 16 };
    default:
      return { title: "Notes", body: "Edit this text block.", align: "", italic: false, bold: false };
  }
}

function renderFieldRefs(fields: Field[], refs: FieldRef[]) {
  if (refs.length === 0) return `<div class="gb-muted">No fields selected</div>`;
  return refs
    .map((ref) => {
      const label = ref.label || (ref.type === "literal" ? "" : fieldLabel(fields, ref.field));
      const value = ref.type === "literal" ? ref.value || "Literal text" : `{{${ref.field || "field"}}}`;
      const strong = ref.emphasis_level && ref.emphasis_level > 0;
      return `<div class="${strong ? "gb-field gb-field-strong" : "gb-field"}"><span>${escapeHTML(label)}</span><b>${escapeHTML(value)}</b></div>`;
    })
    .join("");
}

function renderBlockInner(type: string, config: Record<string, unknown>, fields: Field[]) {
  switch (type) {
    case "header":
      return `
        <div class="gb-block-heading">Header</div>
        <div class="gb-two-grid">
          <div>${renderFieldRefs(fields, asFieldRefs(config.left))}</div>
          <div class="gb-right">${renderFieldRefs(fields, asFieldRefs(config.right))}</div>
        </div>`;
    case "two_col":
      return `
        <div class="gb-two-grid">
          <div><div class="gb-block-heading">${escapeHTML(config.left_title || "Left")}</div>${renderFieldRefs(fields, asFieldRefs(config.left))}</div>
          <div><div class="gb-block-heading">${escapeHTML(config.right_title || "Right")}</div>${renderFieldRefs(fields, asFieldRefs(config.right))}</div>
        </div>`;
    case "lines_table": {
      const cols = asColumns(config.columns);
      const headers = cols.length > 0 ? cols.map((col) => `<th>${escapeHTML(col.label_override || fieldLabel(fields, String(col.field || "")))}</th>`).join("") : `<th>Line items</th>`;
      const cells = cols.length > 0 ? cols.map((col) => `<td>{{${escapeHTML(col.field || "field")}}}</td>`).join("") : `<td>No columns selected</td>`;
      return `<div class="gb-block-heading">Line Items</div><table class="gb-table"><thead><tr>${headers}</tr></thead><tbody><tr>${cells}</tr><tr>${cells}</tr></tbody></table>`;
    }
    case "totals": {
      const rows = asColumns(config.rows);
      const html = rows.length > 0
        ? rows.map((row) => `<div><span>${escapeHTML(row.label_override || fieldLabel(fields, String(row.field || "")))}</span><b>{{${escapeHTML(row.field || "total")}}}</b></div>`).join("")
        : `<div><span>Total</span><b>{{total}}</b></div>`;
      return `<div class="gb-totals">${html}</div>`;
    }
    case "spacer":
      return `<div class="gb-spacer" style="height:${Math.max(8, Math.min(160, asNumber(config.height_pt, 16)))}px">Spacer ${asNumber(config.height_pt, 16)}pt</div>`;
    default:
      return `<div class="gb-text-block ${config.bold ? "gb-bold" : ""} ${config.italic ? "gb-italic" : ""} gb-align-${escapeAttr(config.align || "left")}"><div class="gb-block-heading">${escapeHTML(config.title || "Text")}</div><p>${escapeHTML(config.body || "Text block")}</p></div>`;
  }
}

function renderBlock(block: Block, fields: Field[]) {
  const hidden = block.visible === false;
  return `<section
    data-gb-block-type="${escapeAttr(block.type)}"
    data-gb-block-id="${escapeAttr(block.id)}"
    data-gb-visible="${hidden ? "false" : "true"}"
    data-gb-config="${escapeAttr(configAttr(block.config))}"
    class="gb-doc-block ${hidden ? "gb-doc-block-hidden" : ""}"
  >${renderBlockInner(block.type, block.config, fields)}</section>`;
}

function editorCSS(schema: Schema) {
  return `
    body { background:#f8fafc; color:${schema.theme.text_color}; font-family:${schema.theme.font_family}, Arial, sans-serif; font-size:${schema.theme.font_size_pt}pt; line-height:${schema.theme.line_height}; }
    .gb-doc-block { border:1px solid #d8dee8; border-radius:8px; margin:12px; padding:14px; background:#fff; box-shadow:0 1px 2px rgba(15,23,42,.06); }
    .gb-doc-block:hover { outline:2px solid ${schema.theme.accent_color}; outline-offset:1px; }
    .gb-doc-block-hidden { opacity:.45; border-style:dashed; }
    .gb-block-heading { color:${schema.theme.accent_color}; font-weight:700; text-transform:uppercase; letter-spacing:.04em; margin-bottom:8px; }
    .gb-two-grid { display:grid; grid-template-columns:1fr 1fr; gap:24px; }
    .gb-right { text-align:right; }
    .gb-field { display:flex; justify-content:space-between; gap:12px; margin:3px 0; color:${schema.theme.muted_color}; }
    .gb-field b { color:${schema.theme.text_color}; }
    .gb-field-strong b { font-size:1.2em; }
    .gb-muted { color:${schema.theme.muted_color}; font-style:italic; }
    .gb-table { width:100%; border-collapse:collapse; }
    .gb-table th { color:${schema.theme.accent_color}; border-bottom:2px solid ${schema.theme.accent_color}; text-align:left; padding:6px; font-size:.9em; }
    .gb-table td { border-bottom:1px solid #e5e7eb; padding:8px 6px; color:${schema.theme.muted_color}; }
    .gb-totals { margin-left:auto; width:40%; }
    .gb-totals div { display:flex; justify-content:space-between; gap:12px; border-bottom:1px solid #e5e7eb; padding:5px 0; }
    .gb-spacer { display:flex; align-items:center; justify-content:center; border:1px dashed #cbd5e1; color:${schema.theme.muted_color}; background:#f8fafc; }
    .gb-bold { font-weight:700; }
    .gb-italic { font-style:italic; }
    .gb-align-center { text-align:center; }
    .gb-align-right { text-align:right; }
  `;
}

function findBlockComponent(component: any): any | null {
  let current = component;
  while (current && typeof current.getAttributes === "function") {
    if (current.getAttributes()["data-gb-block-type"]) return current;
    current = typeof current.parent === "function" ? current.parent() : null;
  }
  return null;
}

function componentToBlock(component: any): Block | null {
  const attrs = component?.getAttributes?.() || {};
  const type = attrs["data-gb-block-type"];
  if (!type) return null;
  return {
    id: attrs["data-gb-block-id"] || `blk_${Date.now().toString(36)}`,
    type,
    visible: attrs["data-gb-visible"] !== "false",
    config: readConfigAttr(attrs["data-gb-config"])
  };
}

function setComponentBlock(component: any, block: Block, fields: Field[]) {
  component.setAttributes({
    ...(component.getAttributes?.() || {}),
    "data-gb-block-type": block.type,
    "data-gb-block-id": block.id,
    "data-gb-visible": block.visible ? "true" : "false",
    "data-gb-config": configAttr(block.config)
  });
  component.setClass(["gb-doc-block", block.visible ? "" : "gb-doc-block-hidden"].filter(Boolean));
  component.components(renderBlockInner(block.type, block.config, fields));
}

function classNames(...parts: Array<string | false | null | undefined>) {
  return parts.filter(Boolean).join(" ");
}

function Button({ children, onClick, disabled, tone = "secondary", type = "button" }: {
  children: React.ReactNode;
  onClick?: () => void;
  disabled?: boolean;
  tone?: "primary" | "secondary" | "danger";
  type?: "button" | "submit";
}) {
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={classNames(
        "rounded-md px-3 py-2 text-body font-semibold disabled:cursor-not-allowed disabled:opacity-50",
        tone === "primary" && "bg-primary text-onPrimary hover:bg-primary-hover",
        tone === "secondary" && "border border-border-input text-text hover:bg-background",
        tone === "danger" && "border border-border-danger text-danger-hover hover:bg-danger-soft"
      )}
    >
      {children}
    </button>
  );
}

function FieldSelect({ fields, value, onChange, scope }: { fields: Field[]; value?: string; onChange: (value: string) => void; scope?: "document" | "line" }) {
  const filtered = fields.filter((field) => !scope || field.scope === scope);
  return (
    <select value={value || ""} onChange={(event) => onChange(event.target.value)} className="w-full rounded-md border border-border-input bg-surface px-2 py-1.5 text-body outline-none focus:ring-2 focus:ring-primary-focus">
      <option value="">- Select field -</option>
      {filtered.map((field) => (
        <option key={field.key} value={field.key}>{field.label}</option>
      ))}
    </select>
  );
}

function TextInput({ value, onChange, placeholder = "" }: { value?: unknown; onChange: (value: string) => void; placeholder?: string }) {
  return <input type="text" value={String(value ?? "")} placeholder={placeholder} onChange={(event) => onChange(event.target.value)} className="w-full rounded-md border border-border-input bg-surface px-2 py-1.5 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus" />;
}

function NumberInput({ value, onChange, min = 0, max = 200 }: { value?: unknown; onChange: (value: number) => void; min?: number; max?: number }) {
  return <input type="number" min={min} max={max} value={asNumber(value, 0)} onChange={(event) => onChange(Number(event.target.value))} className="w-full rounded-md border border-border-input bg-surface px-2 py-1.5 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus" />;
}

function PDFTemplateEditor({ config }: { config: RootConfig }) {
  const editorHostRef = useRef<HTMLDivElement | null>(null);
  const previewRef = useRef<HTMLIFrameElement | null>(null);
  const editorRef = useRef<any>(null);
  const [name, setName] = useState(config.initialName);
  const [description, setDescription] = useState(config.initialDescription);
  const [page, setPage] = useState(config.schema.page);
  const [theme, setTheme] = useState(config.schema.theme);
  const [selection, setSelection] = useState<SelectionState | null>(null);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [saveAsOpen, setSaveAsOpen] = useState(false);
  const [saveAsName, setSaveAsName] = useState(`${config.initialName} copy`);

  const docFields = useMemo(() => config.fields.filter((field) => field.scope !== "line"), [config.fields]);
  const lineFields = useMemo(() => config.fields.filter((field) => field.scope === "line"), [config.fields]);
  const moneyFields = useMemo(() => config.fields.filter((field) => field.scope === "document" && field.type === "money"), [config.fields]);

  const currentShellSchema = (): Schema => ({
    version: 1,
    page: { ...page, margins: page.margins.map((value) => Number(value) || 0) },
    theme: { ...theme, font_size_pt: Number(theme.font_size_pt) || 11 },
    blocks: []
  });

  const selectFromComponent = (component: any) => {
    const blockComponent = findBlockComponent(component);
    const block = componentToBlock(blockComponent);
    if (!block || !blockComponent) {
      setSelection(null);
      return;
    }
    setSelection({ component: blockComponent, id: block.id, type: block.type, visible: block.visible, config: block.config });
  };

  useEffect(() => {
    if (!editorHostRef.current || editorRef.current) return;
    const initialHTML = config.schema.blocks.map((block) => renderBlock(block, config.fields)).join("");
    const editor = grapesjs.init({
      container: editorHostRef.current,
      height: "720px",
      storageManager: false,
      fromElement: false,
      components: initialHTML || renderBlock({ id: "blk_empty_intro", type: "text", visible: true, config: { title: "Start here", body: "Add blocks from the left panel." } }, config.fields),
      style: editorCSS(config.schema),
      panels: { defaults: [] },
      selectorManager: { componentFirst: true },
      canvas: {
        styles: [],
        scripts: []
      }
    });
    editorRef.current = editor;
    editor.on("component:selected", (component: any) => selectFromComponent(component));
    editor.on("component:update", (component: any) => selectFromComponent(component));
    setTimeout(() => refreshPreview(), 100);
    return () => {
      editor.destroy();
      editorRef.current = null;
    };
  }, []);

  useEffect(() => {
    const editor = editorRef.current;
    if (!editor) return;
    editor.setStyle(editorCSS(currentShellSchema()));
  }, [page, theme]);

  const serializeSchema = (): string => {
    const editor = editorRef.current;
    const wrapper = editor?.getWrapper?.();
    const blocks: Block[] = [];
    wrapper?.components?.().forEach((component: any) => {
      const block = componentToBlock(component);
      if (block) blocks.push(block);
    });
    return JSON.stringify({ ...currentShellSchema(), blocks });
  };

  const refreshPreview = async () => {
    if (!previewRef.current) return;
    setError("");
    try {
      const response = await (window.gobooksFetch || fetch)(config.previewUrl, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ doc_type: config.docType, schema_json: serializeSchema() })
      });
      const text = await response.text();
      if (!response.ok) {
        previewRef.current.srcdoc = `<pre style="padding:16px;color:#b91c1c;white-space:pre-wrap">Preview failed: ${escapeHTML(text)}</pre>`;
        return;
      }
      previewRef.current.srcdoc = text;
    } catch (err) {
      previewRef.current.srcdoc = `<pre style="padding:16px;color:#b91c1c">Preview error: ${escapeHTML(err instanceof Error ? err.message : String(err))}</pre>`;
    }
  };

  const addBlock = (type: string) => {
    if (config.systemReadOnly) return;
    const block: Block = {
      id: `blk_${Date.now().toString(36)}`,
      type,
      visible: true,
      config: defaultConfigFor(type, config.fields)
    };
    const added = editorRef.current?.addComponents(renderBlock(block, config.fields));
    const component = Array.isArray(added) ? added[0] : added;
    if (component) {
      editorRef.current.select(component);
      selectFromComponent(component);
    }
  };

  const updateSelection = (updater: (block: Block) => Block) => {
    if (!selection || config.systemReadOnly) return;
    const current: Block = { id: selection.id, type: selection.type, visible: selection.visible, config: selection.config };
    const next = updater(current);
    setComponentBlock(selection.component, next, config.fields);
    setSelection({ ...selection, id: next.id, type: next.type, visible: next.visible, config: next.config });
  };

  const save = async () => {
    if (config.systemReadOnly || saving) return;
    setSaving(true);
    setError("");
    setMessage("");
    try {
      const form = new FormData();
      form.set("name", name);
      form.set("description", description);
      form.set("schema_json", serializeSchema());
      const response = await (window.gobooksFetch || fetch)(config.saveUrl, {
        method: "POST",
        credentials: "same-origin",
        body: form
      });
      if (response.redirected) {
        window.location.href = response.url;
        return;
      }
      if (!response.ok) throw new Error(await response.text());
      setMessage("Saved.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Save failed.");
    } finally {
      setSaving(false);
    }
  };

  const submitSaveAs = () => {
    if (!saveAsName.trim()) {
      setError("New template name is required.");
      return;
    }
    const form = document.createElement("form");
    form.method = "POST";
    form.action = config.saveAsUrl;
    const nameInput = document.createElement("input");
    nameInput.type = "hidden";
    nameInput.name = "new_name";
    nameInput.value = saveAsName.trim();
    const schemaInput = document.createElement("input");
    schemaInput.type = "hidden";
    schemaInput.name = "schema_json";
    schemaInput.value = serializeSchema();
    form.append(nameInput, schemaInput);
    document.body.append(form);
    form.submit();
  };

  const removeSelection = () => {
    if (!selection || config.systemReadOnly) return;
    selection.component.remove();
    setSelection(null);
  };

  return (
    <div className="max-w-[100%] space-y-4" data-react-pdf-template-editor-ready="true">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-title font-semibold">{config.docTypeLabel} Template - Edit</h1>
          <p className="mt-1 text-text-muted2">
            React + GrapesJS visual editor. Backend still owns schema validation, sample rendering, and PDF generation.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <a href={config.templatesUrl} className="rounded-md border border-border-input px-3 py-2 text-body font-medium text-text-muted3 hover:bg-background hover:text-text">Back</a>
          {config.previewPageUrl ? <a href={config.previewPageUrl} target="_blank" rel="noreferrer" className="rounded-md border border-border-input px-3 py-2 text-body font-medium text-text hover:bg-background">Open PDF preview</a> : null}
          <Button onClick={() => setSaveAsOpen(true)}>Save as...</Button>
          <Button onClick={save} disabled={config.systemReadOnly || saving} tone="primary">{saving ? "Saving..." : "Save"}</Button>
        </div>
      </div>

      {config.systemReadOnly ? (
        <div className="rounded-md border border-warning-border bg-warning-soft px-4 py-3 text-body text-warning-hover">
          This is a system template. Save is disabled; use Save as to create a company-owned editable copy.
        </div>
      ) : null}
      {message ? <div className="rounded-md border border-success-border bg-success-soft px-4 py-3 text-body text-success-hover">{message}</div> : null}
      {error ? <div className="rounded-md border border-border-danger bg-danger-soft px-4 py-3 text-body text-danger-hover">{error}</div> : null}

      <section className="grid grid-cols-1 gap-4 2xl:grid-cols-[280px_minmax(0,1fr)_360px]">
        <aside className="space-y-4 rounded-lg border border-border bg-surface p-4 shadow-sm">
          <div>
            <h2 className="text-section font-semibold text-text">Template</h2>
            <div className="mt-3 space-y-3">
              <label className="block text-small font-semibold uppercase tracking-wider text-text-muted">
                Name
                <input value={name} disabled={config.systemReadOnly} onChange={(event) => setName(event.target.value)} className="mt-1 block w-full rounded-md border border-border-input bg-surface px-2 py-1.5 text-body normal-case tracking-normal text-text outline-none focus:ring-2 focus:ring-primary-focus disabled:opacity-60" />
              </label>
              <label className="block text-small font-semibold uppercase tracking-wider text-text-muted">
                Description
                <input value={description} disabled={config.systemReadOnly} onChange={(event) => setDescription(event.target.value)} className="mt-1 block w-full rounded-md border border-border-input bg-surface px-2 py-1.5 text-body normal-case tracking-normal text-text outline-none focus:ring-2 focus:ring-primary-focus disabled:opacity-60" />
              </label>
            </div>
          </div>

          <div className="border-t border-border pt-4">
            <h2 className="text-section font-semibold text-text">Page</h2>
            <div className="mt-3 grid grid-cols-2 gap-2">
              <select value={page.size} onChange={(event) => setPage({ ...page, size: event.target.value })} className="rounded-md border border-border-input bg-surface px-2 py-1.5 text-body">
                <option value="Letter">Letter</option>
                <option value="A4">A4</option>
              </select>
              <select value={page.orientation} onChange={(event) => setPage({ ...page, orientation: event.target.value })} className="rounded-md border border-border-input bg-surface px-2 py-1.5 text-body">
                <option value="portrait">Portrait</option>
                <option value="landscape">Landscape</option>
              </select>
              {["Top", "Right", "Bottom", "Left"].map((label, index) => (
                <label key={label} className="text-small text-text-muted2">
                  {label}
                  <input type="number" value={page.margins[index] || 0} onChange={(event) => {
                    const margins = [...page.margins];
                    margins[index] = Number(event.target.value) || 0;
                    setPage({ ...page, margins });
                  }} className="mt-1 w-full rounded-md border border-border-input bg-surface px-2 py-1 text-body text-text" />
                </label>
              ))}
            </div>
          </div>

          <div className="border-t border-border pt-4">
            <h2 className="text-section font-semibold text-text">Theme</h2>
            <div className="mt-3 space-y-2">
              <label className="grid grid-cols-[80px_1fr] items-center gap-2 text-small text-text-muted2">
                Accent
                <input type="color" value={theme.accent_color} onChange={(event) => setTheme({ ...theme, accent_color: event.target.value })} className="h-8 w-full rounded border border-border-input bg-transparent" />
              </label>
              <label className="grid grid-cols-[80px_1fr] items-center gap-2 text-small text-text-muted2">
                Text
                <input type="color" value={theme.text_color} onChange={(event) => setTheme({ ...theme, text_color: event.target.value })} className="h-8 w-full rounded border border-border-input bg-transparent" />
              </label>
              <label className="grid grid-cols-[80px_1fr] items-center gap-2 text-small text-text-muted2">
                Font
                <select value={theme.font_family} onChange={(event) => setTheme({ ...theme, font_family: event.target.value })} className="rounded-md border border-border-input bg-surface px-2 py-1.5 text-body text-text">
                  <option value="Inter">Inter</option>
                  <option value="Helvetica">Helvetica</option>
                  <option value="Roboto">Roboto</option>
                  <option value="Times">Times</option>
                  <option value="Georgia">Georgia</option>
                </select>
              </label>
            </div>
          </div>

          <div className="border-t border-border pt-4">
            <h2 className="text-section font-semibold text-text">Blocks</h2>
            <div className="mt-3 grid grid-cols-1 gap-2">
              {blockTypes.map((type) => (
                <button key={type} type="button" disabled={config.systemReadOnly} onClick={() => addBlock(type)} className="rounded-md border border-border-input px-3 py-2 text-left text-body font-medium text-text hover:bg-background disabled:opacity-50">
                  + {blockLabels[type]}
                </button>
              ))}
            </div>
          </div>
        </aside>

        <main className="overflow-hidden rounded-lg border border-border bg-surface shadow-sm">
          <div className="flex items-center justify-between border-b border-border px-4 py-3">
            <div>
              <h2 className="text-section font-semibold text-text">Canvas</h2>
              <p className="text-small text-text-muted2">Select blocks to edit them on the right. Drag blocks to reorder.</p>
            </div>
            <Button onClick={refreshPreview}>Refresh preview</Button>
          </div>
          <div className="gobooks-grapes-host" ref={editorHostRef} />
        </main>

        <aside className="space-y-4">
          <Inspector
            selection={selection}
            fields={config.fields}
            docFields={docFields}
            lineFields={lineFields}
            moneyFields={moneyFields}
            readonly={config.systemReadOnly}
            updateSelection={updateSelection}
            removeSelection={removeSelection}
          />
          <div className="rounded-lg border border-border bg-surface p-4 shadow-sm">
            <div className="flex items-center justify-between gap-3">
              <div>
                <h2 className="text-section font-semibold text-text">Live Preview</h2>
                <p className="text-small text-text-muted2">Rendered by Go backend with sample data.</p>
              </div>
              <Button onClick={refreshPreview}>Refresh</Button>
            </div>
            <iframe ref={previewRef} className="mt-3 h-[520px] w-full rounded-md border border-border bg-white" sandbox="allow-same-origin" />
          </div>
        </aside>
      </section>

      {saveAsOpen ? (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={() => setSaveAsOpen(false)}>
          <div className="w-full max-w-md rounded-lg border border-border bg-surface p-6 shadow-xl" onClick={(event) => event.stopPropagation()}>
            <h2 className="text-section font-semibold text-text">Save as new template</h2>
            <p className="mt-2 text-body text-text-muted2">Creates a company-owned copy with the current canvas state.</p>
            <label className="mt-4 block text-small font-semibold uppercase tracking-wider text-text-muted">
              New template name
              <input value={saveAsName} onChange={(event) => setSaveAsName(event.target.value)} className="mt-2 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-body normal-case tracking-normal text-text outline-none focus:ring-2 focus:ring-primary-focus" />
            </label>
            <div className="mt-5 flex justify-end gap-2">
              <Button onClick={() => setSaveAsOpen(false)}>Cancel</Button>
              <Button onClick={submitSaveAs} tone="primary">Create</Button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function Inspector({
  selection,
  fields,
  docFields,
  lineFields,
  moneyFields,
  readonly,
  updateSelection,
  removeSelection
}: {
  selection: SelectionState | null;
  fields: Field[];
  docFields: Field[];
  lineFields: Field[];
  moneyFields: Field[];
  readonly: boolean;
  updateSelection: (updater: (block: Block) => Block) => void;
  removeSelection: () => void;
}) {
  if (!selection) {
    return (
      <div className="rounded-lg border border-border bg-surface p-4 text-body text-text-muted2 shadow-sm">
        Select a block on the canvas to edit its fields, labels, visibility, or spacing.
      </div>
    );
  }

  const setConfig = (nextConfig: Record<string, unknown>) => updateSelection((block) => ({ ...block, config: nextConfig }));
  const patchConfig = (patch: Record<string, unknown>) => setConfig({ ...selection.config, ...patch });

  return (
    <div className="rounded-lg border border-border bg-surface p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-section font-semibold text-text">{blockLabels[selection.type] || selection.type}</h2>
          <p className="text-small text-text-muted2">Block settings</p>
        </div>
        <label className="inline-flex items-center gap-2 text-small text-text-muted2">
          <input
            type="checkbox"
            checked={selection.visible}
            disabled={readonly}
            onChange={(event) => updateSelection((block) => ({ ...block, visible: event.target.checked }))}
          />
          Visible
        </label>
      </div>

      <div className="mt-4 space-y-4">
        {selection.type === "header" ? (
          <FieldRefEditor title="Left slot" slot="left" fields={fields} docFields={docFields} config={selection.config} setConfig={setConfig} />
        ) : null}
        {selection.type === "header" ? (
          <FieldRefEditor title="Right slot" slot="right" fields={fields} docFields={docFields} config={selection.config} setConfig={setConfig} />
        ) : null}
        {selection.type === "two_col" ? (
          <>
            <label className="block text-small font-semibold uppercase tracking-wider text-text-muted">Left title<TextInput value={selection.config.left_title} onChange={(value) => patchConfig({ left_title: value })} /></label>
            <FieldRefEditor title="Left fields" slot="left" fields={fields} docFields={docFields} config={selection.config} setConfig={setConfig} />
            <label className="block text-small font-semibold uppercase tracking-wider text-text-muted">Right title<TextInput value={selection.config.right_title} onChange={(value) => patchConfig({ right_title: value })} /></label>
            <FieldRefEditor title="Right fields" slot="right" fields={fields} docFields={docFields} config={selection.config} setConfig={setConfig} />
          </>
        ) : null}
        {selection.type === "lines_table" ? <LinesTableEditor fields={lineFields} config={selection.config} setConfig={setConfig} /> : null}
        {selection.type === "totals" ? <TotalsEditor fields={moneyFields} config={selection.config} setConfig={setConfig} /> : null}
        {selection.type === "text" ? <TextBlockEditor config={selection.config} patchConfig={patchConfig} /> : null}
        {selection.type === "spacer" ? (
          <label className="block text-small font-semibold uppercase tracking-wider text-text-muted">Height pt<NumberInput value={selection.config.height_pt} onChange={(value) => patchConfig({ height_pt: value })} min={2} max={200} /></label>
        ) : null}
      </div>

      <div className="mt-5 border-t border-border pt-4">
        <Button onClick={removeSelection} disabled={readonly} tone="danger">Delete block</Button>
      </div>
    </div>
  );
}

function FieldRefEditor({ title, slot, fields, docFields, config, setConfig }: {
  title: string;
  slot: "left" | "right";
  fields: Field[];
  docFields: Field[];
  config: Record<string, unknown>;
  setConfig: (config: Record<string, unknown>) => void;
}) {
  const refs = asFieldRefs(config[slot]);
  const updateRefs = (next: FieldRef[]) => setConfig({ ...config, [slot]: next });
  return (
    <div>
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-small font-semibold uppercase tracking-wider text-text-muted">{title}</h3>
        <button type="button" onClick={() => updateRefs([...refs, { type: "field", field: docFields[0]?.key || "", label: "" }])} className="text-small text-primary hover:underline">+ Add</button>
      </div>
      <div className="mt-2 space-y-2">
        {refs.map((ref, index) => (
          <div key={index} className="space-y-2 rounded-md border border-border-subtle p-2">
            <div className="grid grid-cols-[92px_1fr] gap-2">
              <select value={ref.type || "field"} onChange={(event) => {
                const next = [...refs];
                next[index] = { ...ref, type: event.target.value };
                updateRefs(next);
              }} className="rounded-md border border-border-input bg-surface px-2 py-1.5 text-body">
                <option value="field">Field</option>
                <option value="literal">Literal</option>
                <option value="image">Image</option>
              </select>
              {ref.type === "literal" ? (
                <TextInput value={ref.value} onChange={(value) => {
                  const next = [...refs];
                  next[index] = { ...ref, value };
                  updateRefs(next);
                }} />
              ) : (
                <FieldSelect fields={fields} scope="document" value={ref.field} onChange={(value) => {
                  const next = [...refs];
                  next[index] = { ...ref, field: value };
                  updateRefs(next);
                }} />
              )}
            </div>
            <div className="grid grid-cols-[1fr_auto] gap-2">
              <TextInput value={ref.label} placeholder="Label" onChange={(value) => {
                const next = [...refs];
                next[index] = { ...ref, label: value };
                updateRefs(next);
              }} />
              <button type="button" onClick={() => updateRefs(refs.filter((_, i) => i !== index))} className="rounded-md px-2 text-danger-hover hover:bg-danger-soft">Delete</button>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function LinesTableEditor({ fields, config, setConfig }: { fields: Field[]; config: Record<string, unknown>; setConfig: (config: Record<string, unknown>) => void }) {
  const columns = asColumns(config.columns);
  const updateColumns = (next: Array<Record<string, unknown>>) => setConfig({ ...config, columns: next });
  return (
    <div>
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-small font-semibold uppercase tracking-wider text-text-muted">Columns</h3>
        <button type="button" onClick={() => updateColumns([...columns, { field: fields[0]?.key || "", label_override: "", width_pct: 0, align: "" }])} className="text-small text-primary hover:underline">+ Add</button>
      </div>
      <div className="mt-2 space-y-2">
        {columns.map((col, index) => (
          <div key={index} className="space-y-2 rounded-md border border-border-subtle p-2">
            <FieldSelect fields={fields} scope="line" value={String(col.field || "")} onChange={(value) => {
              const next = [...columns];
              next[index] = { ...col, field: value };
              updateColumns(next);
            }} />
            <div className="grid grid-cols-[1fr_80px_88px_auto] gap-2">
              <TextInput value={col.label_override} placeholder="Label override" onChange={(value) => {
                const next = [...columns];
                next[index] = { ...col, label_override: value };
                updateColumns(next);
              }} />
              <NumberInput value={col.width_pct} min={0} max={100} onChange={(value) => {
                const next = [...columns];
                next[index] = { ...col, width_pct: value };
                updateColumns(next);
              }} />
              <select value={String(col.align || "")} onChange={(event) => {
                const next = [...columns];
                next[index] = { ...col, align: event.target.value };
                updateColumns(next);
              }} className="rounded-md border border-border-input bg-surface px-2 py-1.5 text-body">
                <option value="">auto</option>
                <option value="left">left</option>
                <option value="right">right</option>
                <option value="center">center</option>
              </select>
              <button type="button" onClick={() => updateColumns(columns.filter((_, i) => i !== index))} className="rounded-md px-2 text-danger-hover hover:bg-danger-soft">Delete</button>
            </div>
          </div>
        ))}
      </div>
      <label className="mt-3 inline-flex items-center gap-2 text-body text-text">
        <input type="checkbox" checked={asBool(config.show_product_sku)} onChange={(event) => setConfig({ ...config, show_product_sku: event.target.checked })} />
        Prepend SKU to item name
      </label>
    </div>
  );
}

function TotalsEditor({ fields, config, setConfig }: { fields: Field[]; config: Record<string, unknown>; setConfig: (config: Record<string, unknown>) => void }) {
  const rows = asColumns(config.rows);
  const updateRows = (next: Array<Record<string, unknown>>) => setConfig({ ...config, rows: next });
  return (
    <div>
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-small font-semibold uppercase tracking-wider text-text-muted">Rows</h3>
        <button type="button" onClick={() => updateRows([...rows, { field: fields[0]?.key || "", label_override: "" }])} className="text-small text-primary hover:underline">+ Add</button>
      </div>
      <div className="mt-2 space-y-2">
        {rows.map((row, index) => (
          <div key={index} className="grid grid-cols-[1fr_1fr_auto] gap-2 rounded-md border border-border-subtle p-2">
            <FieldSelect fields={fields} scope="document" value={String(row.field || "")} onChange={(value) => {
              const next = [...rows];
              next[index] = { ...row, field: value };
              updateRows(next);
            }} />
            <TextInput value={row.label_override} placeholder="Label override" onChange={(value) => {
              const next = [...rows];
              next[index] = { ...row, label_override: value };
              updateRows(next);
            }} />
            <button type="button" onClick={() => updateRows(rows.filter((_, i) => i !== index))} className="rounded-md px-2 text-danger-hover hover:bg-danger-soft">Delete</button>
          </div>
        ))}
      </div>
      <label className="mt-3 inline-flex items-center gap-2 text-body text-text">
        <input type="checkbox" checked={asBool(config.show_grand_total_emphasis, true)} onChange={(event) => setConfig({ ...config, show_grand_total_emphasis: event.target.checked })} />
        Emphasize final row
      </label>
    </div>
  );
}

function TextBlockEditor({ config, patchConfig }: { config: Record<string, unknown>; patchConfig: (patch: Record<string, unknown>) => void }) {
  return (
    <div className="space-y-3">
      <label className="block text-small font-semibold uppercase tracking-wider text-text-muted">Title<TextInput value={config.title} onChange={(value) => patchConfig({ title: value })} /></label>
      <label className="block text-small font-semibold uppercase tracking-wider text-text-muted">
        Body
        <textarea value={String(config.body || "")} onChange={(event) => patchConfig({ body: event.target.value })} rows={5} className="mt-1 block w-full rounded-md border border-border-input bg-surface px-2 py-1.5 text-body normal-case tracking-normal text-text outline-none focus:ring-2 focus:ring-primary-focus" />
      </label>
      <div className="grid grid-cols-3 gap-2">
        <select value={String(config.align || "")} onChange={(event) => patchConfig({ align: event.target.value })} className="rounded-md border border-border-input bg-surface px-2 py-1.5 text-body">
          <option value="">left</option>
          <option value="center">center</option>
          <option value="right">right</option>
        </select>
        <label className="inline-flex items-center gap-2 text-body text-text"><input type="checkbox" checked={asBool(config.bold)} onChange={(event) => patchConfig({ bold: event.target.checked })} /> Bold</label>
        <label className="inline-flex items-center gap-2 text-body text-text"><input type="checkbox" checked={asBool(config.italic)} onChange={(event) => patchConfig({ italic: event.target.checked })} /> Italic</label>
      </div>
    </div>
  );
}

document.querySelectorAll<HTMLElement>('[data-gb-react="pdf-template-editor"]').forEach((root) => {
  createRoot(root).render(<PDFTemplateEditor config={parseRoot(root)} />);
});

export {};
