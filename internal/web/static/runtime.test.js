const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { URL, URLSearchParams } = require('node:url');

function loadBrowserScript(filename, extraContext) {
  const source = fs.readFileSync(path.join(__dirname, filename), 'utf8');
  const context = {
    console,
    URLSearchParams,
    setTimeout,
    clearTimeout,
    ...extraContext,
  };
  context.window = context.window || {};
  vm.createContext(context);
  vm.runInContext(source, context, { filename });
  return context;
}

function loadBrowserScripts(filenames, extraContext) {
  const context = {
    console,
    Event: class {
      constructor(type) {
        this.type = type;
      }
    },
    URLSearchParams,
    setTimeout,
    clearTimeout,
    ...extraContext,
  };
  context.window = context.window || {};
  vm.createContext(context);
  for (const filename of filenames) {
    const source = fs.readFileSync(path.join(__dirname, filename), 'utf8');
    vm.runInContext(source, context, { filename });
  }
  return context;
}

function testInvoiceEditorInitializesWithSingleBlankLine() {
  const context = loadBrowserScripts(['doc_line_items.js', 'invoice_editor.js'], {});
  const editor = context.invoiceEditor();

  editor.$el = {
    dataset: {
      products: '[]',
      taxCodes: '[]',
      paymentTerms: '[]',
      contactTerms: '{}',
      initialTerms: '',
      initialDate: '2026-04-10',
      initialDueDate: '',
      initialLines: '[]',
      invoiceId: '0',
      taskReadonly: 'false',
    },
  };

  editor.init();

  assert.equal(editor.lines.length, 1);
  assert.deepEqual(
    JSON.parse(JSON.stringify(editor.lines[0])),
    {
      product_service_id: '',
      product_service_label: '',
      description: '',
      qty: '1',
      unit_price: '0.00',
      tax_code_id: '',
      line_net: '0.00',
      line_tax: '0.00',
      error: '',
      locked: false,
      _rowKey: 1,
    },
  );
}

function testBillEditorInitializesWithSingleBlankLine() {
  const context = loadBrowserScript('bill_editor.js', {});
  const editor = context.billEditor();

  editor.$el = {
    dataset: {
      accounts: '[]',
      taxCodes: '[]',
      tasks: '[]',
      paymentTerms: '[]',
      contactTerms: '{}',
      initialTerms: '',
      initialDate: '2026-04-10',
      initialDueDate: '',
      initialLines: '[]',
    },
  };

  editor.init();

  assert.equal(editor.lines.length, 1);
  assert.deepEqual(
    JSON.parse(JSON.stringify(editor.lines[0])),
    {
      expense_account_id: '',
      product_service_id: '',
      description: '',
      qty: '1',
      unit: '',
      unit_price: '0.00',
      task_id: '',
      is_billable: false,
      amount: '0.00',
      tax_code_id: '',
      line_net: '0.00',
      line_tax: '0.00',
      error: '',
      category_query: '',
      category_source: '',
      category_open: false,
      category_loading: false,
      category_failed: false,
      category_results: [],
      category_highlighted: -1,
      category_fetch_seq: 0,
    },
  );
}

async function testBillEditorVendorCurrencyTriggersExchangeRateLookup() {
  const fetchCalls = [];
  const balancizFetch = async (url, options) => {
    fetchCalls.push({ url, options });
    return {
      ok: true,
      async json() {
        return {
          exchange_rate: '1.37000000',
          exchange_rate_date: '2026-04-29',
          exchange_rate_source: 'provider_fetched',
          source_label: 'Provider fetched',
        };
      },
    };
  };
  const context = loadBrowserScript('bill_editor.js', {
    window: { balancizFetch },
  });
  const editor = context.billEditor();
  const currencyField = {
    value: '',
    options: [{ value: '' }, { value: 'USD' }],
  };

  editor.$el = {
    dataset: {
      accounts: '[]',
      taxCodes: '[]',
      tasks: '[]',
      paymentTerms: '[]',
      contactTerms: '{}',
      initialTerms: '',
      initialDate: '2026-04-29',
      initialDueDate: '',
      initialLines: '[]',
      baseCurrency: 'CAD',
      initialCurrency: '',
      initialExchangeRate: '',
    },
    querySelector(selector) {
      if (selector === 'select[name="currency_code"]') return currencyField;
      return null;
    },
  };

  editor.init();
  editor.onContactChange('7', { currency_code: 'USD' });
  await new Promise(resolve => setTimeout(resolve, 0));

  assert.equal(currencyField.value, 'USD');
  assert.equal(editor.currency, 'USD');
  assert.equal(editor.exchangeRate, '1.37000000');
  assert.equal(fetchCalls.length, 1);
  const requestURL = new URL(fetchCalls[0].url, 'https://example.test');
  assert.equal(requestURL.pathname, '/api/exchange-rate');
  assert.equal(requestURL.searchParams.get('transaction_currency_code'), 'USD');
  assert.equal(requestURL.searchParams.get('date'), '2026-04-29');
  assert.equal(requestURL.searchParams.get('allow_provider_fetch'), '1');
}

async function testVendorSmartPickerRequestCarriesContext() {
  const fetchCalls = [];
  const balancizFetch = async (url, options) => {
    fetchCalls.push({ url, options });
    return {
      ok: true,
      async json() {
        return {
          candidates: [],
          request_id: 'vendor-request-001',
          requires_backend_validation: true,
        };
      },
    };
  };

  const context = loadBrowserScript('smart_picker.js', {
    fetch: balancizFetch,
    window: {
      balancizFetch,
      crypto: {
        randomUUID() {
          return 'vendor-request-001';
        },
      },
    },
  });

  const hiddenInput = { name: '' };
  const picker = context.balancizSmartPicker();
  picker.$el = {
    dataset: {
      entity: 'vendor',
      context: 'expense_form_vendor',
      fieldName: 'vendor_id',
      limit: '20',
      required: 'false',
      createUrl: '',
      createLabel: 'Add new',
      placeholder: 'Search vendors...',
      value: '',
      selectedLabel: '',
      hasError: 'false',
    },
    querySelector(selector) {
      if (selector === 'input[type=hidden]') {
        return hiddenInput;
      }
      return null;
    },
    addEventListener() {},
  };

  picker.init();
  await picker._fetch('');

  assert.equal(hiddenInput.name, 'vendor_id');
  const searchCalls = fetchCalls.filter(call => String(call.url).startsWith('/api/smart-picker/search?'));
  assert.equal(searchCalls.length, 1);

  const requestURL = new URL(searchCalls[0].url, 'https://example.test');
  assert.equal(requestURL.pathname, '/api/smart-picker/search');
  assert.equal(requestURL.searchParams.get('entity'), 'vendor');
  assert.equal(requestURL.searchParams.get('context'), 'expense_form_vendor');
  assert.equal(searchCalls[0].options.method, 'GET');
}

function testDocTransactionEditorCounterpartyCurrencySync() {
  const context = loadBrowserScripts(['doc_line_items.js', 'doc_transaction_editor.js'], {});
  const editor = context.docTransactionEditor();

  const currencyEvents = [];
  const currencyField = {
    tagName: 'SELECT',
    value: 'CAD',
    options: [{ value: 'CAD' }, { value: 'USD' }],
    dispatchEvent(event) {
      currencyEvents.push(event.type);
    },
  };
  const selectedOption = { dataset: { currency: 'usd' } };
  editor.$el = {
    dataset: {
      products: '[]',
      taxCodes: '[]',
      initialLines: '[]',
    },
    querySelector(selector) {
      if (selector === '[name="currency_code"]') return currencyField;
      return null;
    },
  };

  editor.init();
  editor.onCounterpartySelectChange({ target: { selectedOptions: [selectedOption] } });

  assert.equal(currencyField.value, 'USD');
  assert.deepEqual(currencyEvents, ['change']);
}

function testDocItemPickerSelectCarriesItemAndAccountCodes() {
  const context = loadBrowserScript('doc_item_picker.js', {});
  const line = {};
  const events = [];
  const picker = context.balancizItemPicker(line, 2, { context: 'po_line_item' });
  picker.$watch = () => {};
  picker.$dispatch = (name, detail) => events.push({ name, detail });

  picker.init();
  const item = {
    id: '9',
    primary: 'Blue Pen',
    payload: {
      item_code: 'PEN-BLUE',
      expense_account_id: '4',
      account_code: '1300',
      account_name: 'Inventory',
    },
  };

  assert.equal(picker.itemCode(item), 'PEN-BLUE');
  picker.select(item);

  assert.equal(line.product_service_id, '9');
  assert.equal(line.product_service_label, 'Blue Pen');
  assert.equal(line.product_service_code, 'PEN-BLUE');
  assert.equal(line.expense_account_id, '4');
  assert.equal(line.account_code, '1300');
  assert.equal(line.account_name, 'Inventory');
  assert.equal(line.account_label, '1300 Inventory');
  assert.deepEqual(JSON.parse(JSON.stringify(events[0])), {
    name: 'balanciz-item-picker-select',
    detail: { idx: 2, id: '9', payload: item.payload },
  });

  picker.clear();
  assert.equal(line.product_service_id, '');
  assert.equal(line.product_service_code, '');
  assert.equal(line.expense_account_id, '');
  assert.equal(line.account_code, '');
  assert.equal(line.account_name, '');
  assert.equal(line.account_label, '');
}

function testLineAccountPickerSelectsAccountWhenItemIsBlank() {
  const context = loadBrowserScript('doc_item_picker.js', {});
  const line = {};
  const picker = context.balancizLineAccountPicker(line, 0, { context: 'po_line_account' });
  picker.$watch = () => {};

  picker.init();
  picker.select({
    id: '12',
    primary: 'Office Supplies',
    secondary: '6100',
    payload: { account_code: '6100', account_name: 'Office Supplies' },
  });

  assert.equal(line.expense_account_id, '12');
  assert.equal(line.account_code, '6100');
  assert.equal(line.account_name, 'Office Supplies');
  assert.equal(line.account_label, '6100 Office Supplies');
  assert.equal(picker.locked(), false);

  line.product_service_id = '9';
  assert.equal(picker.locked(), true);
  picker.clear();
  assert.equal(line.expense_account_id, '12');
}

async function testDocTransactionEditorLocksCounterpartyCurrencyWhenConfigured() {
  const fetchCalls = [];
  const balancizFetch = async (url, options) => {
    fetchCalls.push({ url, options });
    return {
      ok: true,
      async json() {
        return {
          exchange_rate: '1.37000000',
          exchange_rate_date: '2026-05-04',
          exchange_rate_source: 'provider_fetched',
          source_label: 'Provider fetched',
        };
      },
    };
  };
  const context = loadBrowserScripts(['doc_line_items.js', 'doc_transaction_editor.js'], {
    window: { balancizFetch },
  });
  const editor = context.docTransactionEditor();

  const currencyEvents = [];
  const hiddenCurrencyField = { type: 'hidden', value: '' };
  const currencyField = {
    tagName: 'SELECT',
    type: 'select-one',
    value: 'CAD',
    options: [{ value: 'CAD' }, { value: 'USD' }],
    dispatchEvent(event) {
      currencyEvents.push(event.type);
    },
  };
  const vendorField = {
    value: '7',
    selectedOptions: [{ dataset: { currency: 'usd' } }],
  };
  const exchangeRateField = { value: '1.000000' };
  const poDateField = { value: '2026-05-04' };

  editor.$el = {
    dataset: {
      products: '[]',
      taxCodes: '[]',
      initialLines: '[]',
      baseCurrency: 'CAD',
      lockCounterpartyCurrency: 'true',
    },
    querySelector(selector) {
      if (selector === '[data-counterparty-currency-source]') return vendorField;
      if (selector === '[name="currency_code"]') return currencyField;
      if (selector === '[name="exchange_rate"]') return exchangeRateField;
      if (selector === '[name="po_date"], [name="bill_date"], [name="invoice_date"], [name="quote_date"], [name="order_date"]') return poDateField;
      return null;
    },
    querySelectorAll(selector) {
      if (selector === '[name="currency_code"]') return [hiddenCurrencyField, currencyField];
      return [];
    },
  };

  editor.init();
  await new Promise(resolve => setTimeout(resolve, 0));

  assert.equal(currencyField.value, 'USD');
  assert.equal(editor.currencyCode, 'USD');
  assert.equal(editor.counterpartyCurrencyLocked, true);
  assert.equal(editor.exchangeRate, '1.37000000');
  assert.equal(fetchCalls.length >= 1, true);
  const requestURL = new URL(fetchCalls[0].url, 'https://example.test');
  assert.equal(requestURL.pathname, '/api/exchange-rate');
  assert.equal(requestURL.searchParams.get('transaction_currency_code'), 'USD');
  assert.equal(requestURL.searchParams.get('date'), '2026-05-04');
  assert.equal(requestURL.searchParams.get('allow_provider_fetch'), '1');
  assert.deepEqual(currencyEvents, ['change']);
}

async function testDocTransactionEditorHiddenCurrencyUsesVendorRateText() {
  const fetchCalls = [];
  const balancizFetch = async (url, options) => {
    fetchCalls.push({ url, options });
    return {
      ok: true,
      async json() {
        return {
          exchange_rate: '1.33330000',
          exchange_rate_date: '2026-05-04',
          exchange_rate_source: 'rate_table',
          source_label: 'Rate table',
        };
      },
    };
  };
  const context = loadBrowserScripts(['doc_line_items.js', 'doc_transaction_editor.js'], {
    window: { balancizFetch },
  });
  const editor = context.docTransactionEditor();
  const hiddenCurrencyField = { tagName: 'INPUT', type: 'hidden', value: '', dispatchEvent() {} };
  const exchangeRateField = { value: '1.000000' };
  const poDateField = { value: '2026-05-04' };

  editor.$el = {
    dataset: {
      products: '[]',
      taxCodes: '[]',
      initialLines: '[]',
      baseCurrency: 'CAD',
      lockCounterpartyCurrency: 'true',
    },
    querySelector(selector) {
      if (selector === '[data-counterparty-currency-source]') return null;
      if (selector === '[name="currency_code"]') return hiddenCurrencyField;
      if (selector === '[name="exchange_rate"]') return exchangeRateField;
      if (selector === '[name="po_date"], [name="bill_date"], [name="invoice_date"], [name="quote_date"], [name="order_date"]') return poDateField;
      return null;
    },
    querySelectorAll(selector) {
      if (selector === '[name="currency_code"]') return [hiddenCurrencyField];
      return [];
    },
  };

  editor.init();
  editor.onCounterpartySelectChange({
    target: {
      value: '7',
      selectedOptions: [{ dataset: { currency: 'usd' } }],
    },
  });
  await new Promise(resolve => setTimeout(resolve, 0));

  assert.equal(hiddenCurrencyField.value, 'USD');
  assert.equal(editor.currencyCode, 'USD');
  assert.equal(editor.currencyRateLeftLabel(), '1 USD');
  assert.equal(editor.exchangeRate, '1.33330000');
  assert.equal(fetchCalls.length, 1);
  const requestURL = new URL(fetchCalls[0].url, 'https://example.test');
  assert.equal(requestURL.pathname, '/api/exchange-rate');
  assert.equal(requestURL.searchParams.get('transaction_currency_code'), 'USD');
  assert.equal(requestURL.searchParams.get('date'), '2026-05-04');
}

async function testDocTransactionEditorUsesVendorCurrencyMapFallback() {
  const fetchCalls = [];
  const balancizFetch = async (url) => {
    fetchCalls.push(url);
    return {
      ok: true,
      async json() {
        return {
          exchange_rate: '1.33330000',
          exchange_rate_date: '2026-05-04',
          source_label: 'Rate table',
        };
      },
    };
  };
  const context = loadBrowserScripts(['doc_line_items.js', 'doc_transaction_editor.js'], {
    window: { balancizFetch },
  });
  const editor = context.docTransactionEditor();
  const hiddenCurrencyField = { tagName: 'INPUT', type: 'hidden', value: '', dispatchEvent() {} };
  const exchangeRateField = { value: '1.000000' };
  const poDateField = { value: '2026-05-04' };
  const vendorField = {
    value: '42',
    selectedOptions: [{ dataset: {} }],
    addEventListener() {},
  };

  editor.$el = {
    dataset: {
      products: '[]',
      taxCodes: '[]',
      initialLines: '[]',
      baseCurrency: 'CAD',
      initialCurrency: '',
      counterpartyCurrencies: '{"42":"USD"}',
      lockCounterpartyCurrency: 'true',
    },
    querySelector(selector) {
      if (selector === '[data-counterparty-currency-source]') return vendorField;
      if (selector === '[name="currency_code"]') return hiddenCurrencyField;
      if (selector === '[name="exchange_rate"]') return exchangeRateField;
      if (selector === '[name="po_date"], [name="bill_date"], [name="invoice_date"], [name="quote_date"], [name="order_date"]') return poDateField;
      return null;
    },
    querySelectorAll(selector) {
      if (selector === '[name="currency_code"]') return [hiddenCurrencyField];
      return [];
    },
  };

  editor.init();
  await new Promise(resolve => setTimeout(resolve, 0));

  assert.equal(hiddenCurrencyField.value, 'USD');
  assert.equal(editor.currencyCode, 'USD');
  assert.equal(editor.currencyRateLeftLabel(), '1 USD');
  assert.equal(editor.exchangeRate, '1.33330000');
  assert.equal(fetchCalls.length >= 1, true);
}

function testBillDateFilterInputSanitizesAndNormalizes() {
  const documentListeners = {};
  const context = loadBrowserScript('date_filter_input.js', {
    document: {
      addEventListener(name, handler) {
        documentListeners[name] = handler;
      },
      querySelectorAll() {
        return [];
      },
    },
    window: {},
  });

  const submitListeners = {};
  const inputListeners = {};
  const form = {
    addEventListener(name, handler) {
      submitListeners[name] = handler;
    },
    querySelectorAll() {
      return [input];
    },
  };
  const input = {
    value: '',
    form,
    addEventListener(name, handler) {
      inputListeners[name] = handler;
    },
  };

  context.window.balancizDateFilterInput.bindInput(input);

  input.value = 'Abc2026/01-04##';
  inputListeners.input({ target: input });
  assert.equal(input.value, '202601-04');

  inputListeners.blur({ target: input });
  assert.equal(input.value, '2026-01-04');

  input.value = 'ddd';
  inputListeners.input({ target: input });
  assert.equal(input.value, '');

  input.value = '20260131';
  submitListeners.submit();
  assert.equal(input.value, '2026-01-31');

  assert.equal(
    context.window.balancizDateFilterInput.normalizeDateInput('2026-01-04'),
    '2026-01-04',
  );
  assert.equal(
    context.window.balancizDateFilterInput.normalizeDateInput('20260104'),
    '2026-01-04',
  );
  assert.ok(documentListeners.DOMContentLoaded, 'expected DOMContentLoaded hook to be registered');
}

async function testJournalEntryFXEditorBaseModeAndForeignFetch() {
  const fetchCalls = [];
  const localStore = new Map();
  const context = loadBrowserScript('journal_entry_fx.js', {
    fetch: async (url) => {
      fetchCalls.push(url);
      return {
        ok: true,
        async json() {
          return {
            transaction_currency_code: 'USD',
            base_currency_code: 'CAD',
            exchange_rate: '1.37000000',
            exchange_rate_date: '2026-04-10',
            exchange_rate_source: 'provider_fetched',
            source_label: 'Latest',
            snapshot_id: 55,
            is_identity: false,
          };
        },
      };
    },
    crypto: {
      randomUUID() {
        return 'journal-line-1';
      },
    },
    localStorage: {
      getItem(key) {
        return localStore.has(key) ? localStore.get(key) : null;
      },
      setItem(key, value) {
        localStore.set(key, value);
      },
      removeItem(key) {
        localStore.delete(key);
      },
    },
    document: {
      getElementById(id) {
        if (id === 'balanciz-journal-accounts-data') {
          return { textContent: '[]' };
        }
        if (id === 'balanciz-journal-currency-options') {
          return { textContent: '["CAD","USD"]' };
        }
        return null;
      },
    },
    window: {
      location: { search: '' },
      confirm() {
        return true;
      },
      crypto: {
        randomUUID() {
          return 'journal-line-1';
        },
      },
    },
  });

  const editor = context.balancizJournalEntryDraft();
  editor.$el = {
    dataset: {
      companyId: '42',
      baseCurrency: 'CAD',
      defaultCurrency: 'CAD',
    },
  };

  editor.init();
  assert.equal(editor.showFXBlock, false);
  assert.equal(editor.fx.source, 'identity');
  assert.equal(editor.lines.length, 2);

  editor.header.transaction_currency_code = 'USD';
  editor.onCurrencyChange();
  await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(editor.showFXBlock, true);
  assert.equal(fetchCalls.length, 1);
  assert.equal(editor.fx.snapshot_id, '55');
  assert.equal(editor.fx.source, 'provider_fetched');
}

async function testJournalEntryCurrencyChangeConfirmation() {
  const confirmations = [];
  const localStore = new Map();
  const context = loadBrowserScript('journal_entry_fx.js', {
    fetch: async () => ({
      ok: true,
      async json() {
        return {
          transaction_currency_code: 'USD',
          base_currency_code: 'CAD',
          exchange_rate: '1.25000000',
          exchange_rate_date: '2026-04-10',
          exchange_rate_source: 'provider_fetched',
          source_label: 'Latest',
          snapshot_id: 77,
        };
      },
    }),
    crypto: {
      randomUUID() {
        return 'journal-line-2';
      },
    },
    localStorage: {
      getItem(key) {
        return localStore.has(key) ? localStore.get(key) : null;
      },
      setItem(key, value) {
        localStore.set(key, value);
      },
      removeItem(key) {
        localStore.delete(key);
      },
    },
    document: {
      getElementById(id) {
        if (id === 'balanciz-journal-accounts-data') {
          return { textContent: '[]' };
        }
        if (id === 'balanciz-journal-currency-options') {
          return { textContent: '["CAD","USD"]' };
        }
        return null;
      },
    },
    window: {
      location: { search: '' },
      confirm(message) {
        confirmations.push(message);
        return false;
      },
      crypto: {
        randomUUID() {
          return 'journal-line-2';
        },
      },
    },
  });

  const editor = context.balancizJournalEntryDraft();
  editor.$el = {
    dataset: {
      companyId: '77',
      baseCurrency: 'CAD',
      defaultCurrency: 'CAD',
    },
  };

  editor.init();
  editor.lines[0].account_id = '1';
  editor.lines[0].debit = '10.00';
  editor.recalc();

  editor.header.transaction_currency_code = 'USD';
  editor.onCurrencyChange();

  assert.equal(confirmations.length, 1);
  assert.equal(editor.header.transaction_currency_code, 'CAD');
  assert.equal(editor.lines[0].debit, '10.00');
}

async function testJournalEntryAccountPickerUsesSmartPickerContract() {
  const fetchCalls = [];
  const balancizFetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (String(url).startsWith('/api/smart-picker/search?')) {
      return {
        ok: true,
        async json() {
          return {
            request_id: 'je-account-request-1',
            requires_backend_validation: true,
            candidates: [
              { id: '10', primary: 'Cash', secondary: '1000' },
              { id: '20', primary: 'Sales Revenue', secondary: '4100' },
            ],
          };
        },
      };
    }
    return {
      ok: true,
      async json() {
        return {};
      },
    };
  };
  const localStore = new Map();
  const context = loadBrowserScript('journal_entry_fx.js', {
    localStorage: {
      getItem(key) {
        return localStore.has(key) ? localStore.get(key) : null;
      },
      setItem(key, value) {
        localStore.set(key, value);
      },
      removeItem(key) {
        localStore.delete(key);
      },
    },
    document: {
      getElementById(id) {
        if (id === 'balanciz-journal-accounts-data') {
          return { textContent: '[]' };
        }
        if (id === 'balanciz-journal-currency-options') {
          return { textContent: '["CAD"]' };
        }
        return null;
      },
    },
    window: {
      location: { search: '', pathname: '/journal-entry' },
      balancizFetch,
      crypto: {
        randomUUID() {
          return 'je-account-request-1';
        },
      },
    },
    crypto: {
      randomUUID() {
        return 'journal-line-3';
      },
    },
  });

  const editor = context.balancizJournalEntryDraft();
  editor.$el = {
    dataset: {
      companyId: '42',
      baseCurrency: 'CAD',
      defaultCurrency: 'CAD',
    },
  };

  editor.init();
  const line = editor.lines[0];
  line.acctQuery = 'cash';
  await editor.onAcctQueryInput(line);

  const searchCalls = fetchCalls.filter(call => String(call.url).startsWith('/api/smart-picker/search?'));
  assert.equal(searchCalls.length, 1);
  const requestURL = new URL(searchCalls[0].url, 'https://example.test');
  assert.equal(requestURL.pathname, '/api/smart-picker/search');
  assert.equal(requestURL.searchParams.get('entity'), 'account');
  assert.equal(requestURL.searchParams.get('context'), 'journal_entry_account');
  assert.equal(requestURL.searchParams.get('q'), 'cash');
  assert.equal(requestURL.searchParams.get('limit'), '20');
  assert.equal(line.acctItems.length, 2);
  assert.equal(line.acctItems[0].code, '1000');

  editor.selectAccount(line, line.acctItems[0], 0);
  assert.equal(line.account_id, '10');
  assert.equal(line.acctQuery, '1000 - Cash');

  const usageCalls = fetchCalls.filter(call => String(call.url) === '/api/smart-picker/usage');
  assert.equal(usageCalls.length, 1);
  const usagePayload = JSON.parse(usageCalls[0].options.body);
  assert.equal(usagePayload.entity, 'account');
  assert.equal(usagePayload.context, 'journal_entry_account');
  assert.equal(usagePayload.event_type, 'select');
  assert.equal(usagePayload.request_id, 'je-account-request-1');
}

async function run() {
  const tests = [
    {
      name: 'invoiceEditor init creates exactly one blank line for a new invoice',
      fn: testInvoiceEditorInitializesWithSingleBlankLine,
    },
    {
      name: 'billEditor init creates exactly one blank line for a new bill',
      fn: testBillEditorInitializesWithSingleBlankLine,
    },
    {
      name: 'bill editor vendor currency triggers exchange-rate lookup',
      fn: testBillEditorVendorCurrencyTriggersExchangeRateLookup,
    },
    {
      name: 'vendor smart picker search request includes expense_form_vendor context',
      fn: testVendorSmartPickerRequestCarriesContext,
    },
    {
      name: 'document transaction editor syncs currency from selected counterparty',
      fn: testDocTransactionEditorCounterpartyCurrencySync,
    },
    {
      name: 'document item picker carries item and account codes',
      fn: testDocItemPickerSelectCarriesItemAndAccountCodes,
    },
    {
      name: 'line account picker selects account when item is blank',
      fn: testLineAccountPickerSelectsAccountWhenItemIsBlank,
    },
    {
      name: 'document transaction editor locks configured counterparty currency',
      fn: testDocTransactionEditorLocksCounterpartyCurrencyWhenConfigured,
    },
    {
      name: 'document transaction editor uses hidden vendor currency rate text',
      fn: testDocTransactionEditorHiddenCurrencyUsesVendorRateText,
    },
    {
      name: 'document transaction editor uses vendor currency map fallback',
      fn: testDocTransactionEditorUsesVendorCurrencyMapFallback,
    },
    {
      name: 'bill date filter input strips letters and normalizes 8-digit dates',
      fn: testBillDateFilterInputSanitizesAndNormalizes,
    },
    {
      name: 'journal entry editor hides FX in base mode and fetches stored FX in foreign mode',
      fn: testJournalEntryFXEditorBaseModeAndForeignFetch,
    },
    {
      name: 'journal entry currency change confirmation restores prior currency on cancel',
      fn: testJournalEntryCurrencyChangeConfirmation,
    },
    {
      name: 'journal entry account picker uses SmartPicker search contract',
      fn: testJournalEntryAccountPickerUsesSmartPickerContract,
    },
  ];

  let failed = false;
  for (const tc of tests) {
    try {
      await tc.fn();
      console.log('PASS', tc.name);
    } catch (err) {
      failed = true;
      console.error('FAIL', tc.name);
      console.error(err);
    }
  }

  if (failed) {
    process.exitCode = 1;
    return;
  }
  console.log(`PASS ${tests.length} runtime checks`);
}

run();
