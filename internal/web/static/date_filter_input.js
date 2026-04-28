(function () {
  function sanitizeDateInput(value) {
    const src = String(value || '');
    let out = '';
    let dashCount = 0;
    let lastWasDash = false;

    for (const ch of src) {
      if (/[0-9]/.test(ch)) {
        if (out.length >= 10) break;
        out += ch;
        lastWasDash = false;
        continue;
      }
      if (ch === '-' && dashCount < 2 && out.length > 0 && !lastWasDash && out.length < 10) {
        out += ch;
        dashCount += 1;
        lastWasDash = true;
      }
    }
    return out;
  }

  function normalizeDateInput(value) {
    const sanitized = sanitizeDateInput(value);
    const digits = sanitized.replace(/\D/g, '');
    if (digits.length === 8) {
      return `${digits.slice(0, 4)}-${digits.slice(4, 6)}-${digits.slice(6, 8)}`;
    }
    return sanitized;
  }

  function handleInput(event) {
    if (!event || !event.target) return;
    event.target.value = sanitizeDateInput(event.target.value);
  }

  function handleBlur(event) {
    if (!event || !event.target) return;
    event.target.value = normalizeDateInput(event.target.value);
  }

  function bindSubmit(form) {
    if (!form || form.__gbDateFilterSubmitBound) return;
    form.__gbDateFilterSubmitBound = true;
    form.addEventListener('submit', function () {
      const inputs = typeof form.querySelectorAll === 'function'
        ? form.querySelectorAll('[data-date-filter-input]')
        : [];
      for (const input of inputs) {
        input.value = normalizeDateInput(input.value);
      }
    });
  }

  function bindInput(input) {
    if (!input || input.__gbDateFilterBound) return;
    input.__gbDateFilterBound = true;
    input.addEventListener('input', handleInput);
    input.addEventListener('blur', handleBlur);
    input.addEventListener('change', handleBlur);
    bindSubmit(input.form);
  }

  function bindAll(root) {
    if (!root || typeof root.querySelectorAll !== 'function') return;
    const inputs = root.querySelectorAll('[data-date-filter-input]');
    for (const input of inputs) {
      bindInput(input);
    }
  }

  const api = {
    sanitizeDateInput,
    normalizeDateInput,
    bindInput,
    bindAll,
  };

  if (typeof window !== 'undefined') {
    window.balancizDateFilterInput = api;
  }

  if (typeof document !== 'undefined' && typeof document.addEventListener === 'function') {
    document.addEventListener('DOMContentLoaded', function () {
      bindAll(document);
    });
  }
})();
