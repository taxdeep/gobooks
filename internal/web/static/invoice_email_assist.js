function balancizEmailAssist() {
  return {
    invoiceId: 0,
    emailAssist: { loading: false, visible: false, suggestion: "", error: "", empty: false },
    bodyMode: "manual",
    _assistSeq: 0,

    init() {
      this.invoiceId = parseInt(this.$el.dataset.invoiceId, 10) || 0;
      this._syncBodyMode();
    },

    _bodyField() {
      return this.$refs.emailBody || null;
    },

    _attachField() {
      return this.$refs.attachPDF || null;
    },

    _defaultAttachBody() {
      return this.$refs.bodyDefaultAttachPDF ? this.$refs.bodyDefaultAttachPDF.value : "";
    },

    _defaultNoPDFBody() {
      return this.$refs.bodyDefaultNoPDF ? this.$refs.bodyDefaultNoPDF.value : "";
    },

    _syncBodyMode() {
      const bodyField = this._bodyField();
      if (!bodyField) return;
      const current = bodyField.value;
      const attachDefault = this._defaultAttachBody();
      const noPDFDefault = this._defaultNoPDFBody();
      if (attachDefault !== "" || noPDFDefault !== "") {
        if (current === attachDefault) {
          this.bodyMode = "default_attach_pdf";
          return;
        }
        if (current === noPDFDefault) {
          this.bodyMode = "default_no_pdf";
          return;
        }
      }
      if (this.emailAssist.suggestion && current === this.emailAssist.suggestion) {
        this.bodyMode = "ai_applied";
        return;
      }
      this.bodyMode = "manual";
    },

    onBodyEdited() {
      this._syncBodyMode();
    },

    onAttachPDFToggle() {
      const bodyField = this._bodyField();
      const attachField = this._attachField();
      if (!bodyField || !attachField) return;

      if (this.bodyMode !== "default_attach_pdf" && this.bodyMode !== "default_no_pdf") {
        return;
      }

      bodyField.value = attachField.checked ? this._defaultAttachBody() : this._defaultNoPDFBody();
      this.bodyMode = attachField.checked ? "default_attach_pdf" : "default_no_pdf";
    },

    async aiEmailAssist() {
      if (!this.invoiceId || this.emailAssist.loading || this.emailAssist.visible) return;

      const seq = ++this._assistSeq;
      this.emailAssist.loading = true;
      this.emailAssist.visible = true;
      this.emailAssist.suggestion = "";
      this.emailAssist.error = "";
      this.emailAssist.empty = false;

      const fetchFn = window.balancizFetch || fetch;
      try {
        const resp = await fetchFn("/api/ai/invoice-email-assist", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ invoice_id: this.invoiceId }),
        });
        const data = await resp.json();
        if (seq !== this._assistSeq) return;

        if (!resp.ok) {
          this.emailAssist.error = data.error || "AI draft unavailable.";
          return;
        }

        this.emailAssist.suggestion = (data.suggestion || "").trim();
        this.emailAssist.empty = this.emailAssist.suggestion === "";
      } catch (_) {
        if (seq !== this._assistSeq) return;
        this.emailAssist.error = "Request failed. Please try again.";
      } finally {
        if (seq === this._assistSeq) this.emailAssist.loading = false;
      }
    },

    applyEmailSuggestion() {
      const bodyField = this._bodyField();
      if (!bodyField || !this.emailAssist.suggestion) return;

      bodyField.value = this.emailAssist.suggestion;
      this.bodyMode = "ai_applied";
      this.emailAssist.visible = false;
      this.emailAssist.empty = false;
    },

    dismissEmailAssist() {
      this.emailAssist.visible = false;
      this.emailAssist.suggestion = "";
      this.emailAssist.error = "";
      this.emailAssist.empty = false;
    },
  };
}
