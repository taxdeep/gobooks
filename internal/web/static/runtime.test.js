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

function testInvoiceEditorInitializesWithSingleBlankLine() {
  const context = loadBrowserScript('invoice_editor.js', {});
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
      description: '',
      qty: '1',
      unit_price: '0.00',
      tax_code_id: '',
      line_net: '0.00',
      line_tax: '0.00',
      error: '',
      locked: false,
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
      description: '',
      task_id: '',
      is_billable: false,
      amount: '0.00',
      tax_code_id: '',
      line_net: '0.00',
      line_tax: '0.00',
      error: '',
    },
  );
}

async function testVendorSmartPickerRequestCarriesContext() {
  const fetchCalls = [];
  const gobooksFetch = async (url, options) => {
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
    fetch: gobooksFetch,
    window: {
      gobooksFetch,
      crypto: {
        randomUUID() {
          return 'vendor-request-001';
        },
      },
    },
  });

  const hiddenInput = { name: '' };
  const picker = context.gobooksSmartPicker();
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
  };

  picker.init();
  await picker._fetch('');

  assert.equal(hiddenInput.name, 'vendor_id');
  assert.equal(fetchCalls.length, 1);

  const requestURL = new URL(fetchCalls[0].url, 'https://example.test');
  assert.equal(requestURL.pathname, '/api/smart-picker/search');
  assert.equal(requestURL.searchParams.get('entity'), 'vendor');
  assert.equal(requestURL.searchParams.get('context'), 'expense_form_vendor');
  assert.equal(fetchCalls[0].options.method, 'GET');
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

  context.window.gobooksDateFilterInput.bindInput(input);

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
    context.window.gobooksDateFilterInput.normalizeDateInput('2026-01-04'),
    '2026-01-04',
  );
  assert.equal(
    context.window.gobooksDateFilterInput.normalizeDateInput('20260104'),
    '2026-01-04',
  );
  assert.ok(documentListeners.DOMContentLoaded, 'expected DOMContentLoaded hook to be registered');
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
      name: 'vendor smart picker search request includes expense_form_vendor context',
      fn: testVendorSmartPickerRequestCarriesContext,
    },
    {
      name: 'bill date filter input strips letters and normalizes 8-digit dates',
      fn: testBillDateFilterInputSanitizesAndNormalizes,
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
