// Chart of Accounts drawer: POST /api/accounts/recommendations (enhance=false rule-only, enhance=true optional AI).
// Assistive only—never auto-applies; save still runs full server validation. Hidden reco_* fields are analytics-only.
function balancizAccountDrawerSuggest() {
  return {
    mode: "create",
    codeLen: 4,
    companyId: 0,
    parentAccountId: 0,
    selectedRoot: "",
    selectedDetail: "",
    localErr: "",
    prefixErr: "",
    prefixMap: {},
    detailByRoot: {},
    multiCurrency: false,
    currencyDetails: [],
    sug: null,
    sugLoadingRule: false,
    sugLoadingAI: false,
    sugError: "",
    aiHint: "",
    // Per-field save-time source (manual | rule | ai); hidden inputs posted on create/edit.
    recoNameSource: "manual",
    recoCodeSource: "manual",
    recoGifiSource: "manual",
    anyLoading() {
      return this.sugLoadingRule || this.sugLoadingAI;
    },
    init() {
      const el = this.$el;
      this.mode = el.dataset.mode || "create";
      this.codeLen = parseInt(el.dataset.digitLen, 10) || 4;
      this.companyId = parseInt(el.dataset.companyId, 10) || 0;
      this.parentAccountId = parseInt(el.dataset.parentAccountId, 10) || 0;
      try {
        this.prefixMap = JSON.parse(el.dataset.prefixMap || "{}");
      } catch (e) {
        this.prefixMap = {};
      }
      try {
        this.detailByRoot = JSON.parse(el.dataset.detailByRoot || "{}");
      } catch (e) {
        this.detailByRoot = {};
      }
      this.multiCurrency = el.dataset.multiCurrency === "true";
      try {
        this.currencyDetails = JSON.parse(el.dataset.currencyDetails || "[]");
      } catch (e) {
        this.currencyDetails = [];
      }
      this.selectedRoot = el.dataset.initialRoot || "";
      this.selectedDetail = el.dataset.initialDetail || "";
    },
    detailList() {
      return this.detailByRoot[this.selectedRoot] || [];
    },
    onRootChange(val) {
      this.selectedRoot = val;
      this.selectedDetail = "";
      if (this.$refs.detailSelect) this.$refs.detailSelect.value = "";
      this.validate(this.$refs.codeInput?.value || "");
    },
    onDetailChange(val) {
      this.selectedDetail = val;
      this.validate(this.$refs.codeInput?.value || "");
    },
    currencyDetailApplies() {
      const detail = this.selectedDetail || this.$refs.detailSelect?.value || "";
      return this.multiCurrency && this.currencyDetails.includes(detail);
    },
    validate(v) {
      if (this.mode !== "create") return;
      const n = this.codeLen;
      if (!v) {
        this.localErr = "";
        this.prefixErr = "";
        return;
      }
      if (!/^\d+$/.test(v)) {
        this.localErr = "Digits only.";
        this.prefixErr = "";
        return;
      }
      if (v.length !== n) {
        this.localErr = "Use exactly " + n + " digits.";
        this.prefixErr = "";
        return;
      }
      if (v[0] === "0") {
        this.localErr = "Cannot start with 0.";
        this.prefixErr = "";
        return;
      }
      this.localErr = "";
      const rt = this.$refs.rootSelect?.value || "";
      const want = this.prefixMap[rt];
      if (rt && want && v[0] !== want) {
        this.prefixErr = "Code must start with " + want + " for the selected root type.";
      } else {
        this.prefixErr = "";
      }
    },
    existingAccountCodeForRequest() {
      if (this.mode !== "create") return "";
      return (this.$refs.codeInput?.value || "").trim();
    },
    requestPayload(enhance) {
      const root = this.$refs.rootSelect?.value;
      const detail = this.$refs.detailSelect?.value;
      const name = this.$refs.nameInput?.value || "";
      return {
        company_id: this.companyId,
        account_name: name,
        root_account_type: root,
        detail_account_type: detail,
        existing_account_code: this.existingAccountCodeForRequest(),
        parent_account_id: this.parentAccountId,
        code_length: this.codeLen,
        enhance: !!enhance,
      };
    },
    async fetchSuggest() {
      const root = this.$refs.rootSelect?.value;
      const detail = this.$refs.detailSelect?.value;
      if (!root) {
        this.sugError = "Select root type first.";
        this.sug = null;
        this.aiHint = "";
        return;
      }
      if (!detail) {
        this.sugError = "Select detail type first.";
        this.sug = null;
        this.aiHint = "";
        return;
      }
      this.sugError = "";
      this.aiHint = "";
      this.sugLoadingRule = true;
      this.sug = null;
      try {
        const res = await fetch("/api/accounts/recommendations", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          credentials: "same-origin",
          body: JSON.stringify(this.requestPayload(false)),
        });
        let data = {};
        try {
          data = await res.json();
        } catch (e) {
          data = {};
        }
        if (!res.ok) {
          this.sugError = (data && data.error) || "Could not load suggestions.";
          return;
        }
        if (data && data.source === undefined) data.source = "rule";
        this.sug = data;
      } catch (e) {
        this.sugError = "Could not load suggestions.";
      } finally {
        this.sugLoadingRule = false;
      }
    },
    async fetchSuggestAI() {
      const root = this.$refs.rootSelect?.value;
      const detail = this.$refs.detailSelect?.value;
      if (!root) {
        this.sugError = "Select root type first.";
        this.aiHint = "";
        return;
      }
      if (!detail) {
        this.sugError = "Select detail type first.";
        this.aiHint = "";
        return;
      }
      const prevSug = this.sug;
      this.sugError = "";
      this.aiHint = "";
      this.sugLoadingAI = true;
      try {
        const res = await fetch("/api/accounts/recommendations", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          credentials: "same-origin",
          body: JSON.stringify(this.requestPayload(true)),
        });
        let data = {};
        try {
          data = await res.json();
        } catch (e) {
          data = {};
        }
        if (!res.ok) {
          this.sugError = (data && data.error) || "AI suggestion could not be loaded.";
          this.aiHint = "";
          this.sug = prevSug;
          return;
        }
        if (data && data.source === undefined) data.source = "rule";
        this.sug = data;
        if (data.ai_unavailable) {
          this.aiHint =
            "AI Connect isn’t available or the AI step didn’t complete; showing validated rule-based values. Configure AI Connect in Settings if you want enhancements.";
        } else {
          this.aiHint = "";
        }
      } catch (e) {
        this.sugError = "AI suggestion could not be loaded.";
        this.aiHint = "";
        this.sug = prevSug;
      } finally {
        this.sugLoadingAI = false;
      }
    },
    sugSourceForApply() {
      const s = (this.sug && this.sug.source && String(this.sug.source).trim().toLowerCase()) || "";
      if (s === "ai") return "ai";
      if (s === "rule") return "rule";
      return "rule";
    },
    markRecoNameManual() {
      this.recoNameSource = "manual";
    },
    markRecoCodeManual() {
      this.recoCodeSource = "manual";
    },
    markRecoGifiManual() {
      this.recoGifiSource = "manual";
    },
    applySugCode() {
      if (this.mode !== "create" || !this.sug || !this.sug.suggested_account_code) return;
      this.recoCodeSource = this.sugSourceForApply();
      this.$refs.codeInput.value = this.sug.suggested_account_code;
      this.validate(this.sug.suggested_account_code);
    },
    applySugName() {
      if (!this.sug || !this.sug.suggested_account_name) return;
      this.recoNameSource = this.sugSourceForApply();
      this.$refs.nameInput.value = this.sug.suggested_account_name;
    },
    applySugGifi() {
      if (!this.sug || !this.sug.suggested_gifi_code) return;
      this.recoGifiSource = this.sugSourceForApply();
      this.$refs.gifiInput.value = this.sug.suggested_gifi_code;
    },
    sourceLabel() {
      if (!this.sug) return "";
      const s = (this.sug.source && String(this.sug.source).trim().toLowerCase()) || "";
      if (s === "ai") return "AI";
      if (s === "rule") return "Rule";
      if (!s) return "Rule";
      return s.charAt(0).toUpperCase() + s.slice(1);
    },
    confidenceLabel() {
      if (!this.sug || !this.sug.confidence) return "";
      const c = String(this.sug.confidence);
      return c.charAt(0).toUpperCase() + c.slice(1);
    },
    helperHint() {
      if (!this.selectedRoot) {
        return "Select root and detail type, then use Suggest or Suggest with AI. Nothing is applied until you choose Apply.";
      }
      if (!this.selectedDetail) {
        return "Select detail type, then use Suggest or Suggest with AI. Nothing is applied until you choose Apply.";
      }
      return "Rule-based suggestions, or optional AI enhancement (requires AI Connect). Nothing is applied until you choose Apply.";
    },
  };
}
