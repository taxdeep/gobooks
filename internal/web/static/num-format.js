// GoBooks number formatter (num-format.js v1)
//
// Reformats raw server-side decimal values (e.g. "1234.56") into the user's
// preferred display format. The format is injected server-side as the
// data-numfmt attribute on <body>.
//
// Supported formats:
//   comma_dot   → 1,234.56  (North American — default)
//   dot_comma   → 1.234,56  (Continental European)
//   space_comma → 1 234,56  (French / Nordic; non-breaking space)
//   space_dot   → 1 234.56  (Swiss / ISO; non-breaking space)
//
// The script walks all text nodes in <body>, skipping form controls and code
// elements. Any text node whose trimmed content matches /^-?\d+\.\d{2}$/ is
// reformatted in-place. This covers all values produced by Money() /
// StringFixed(2) without requiring template changes.
(function () {
  'use strict';

  var fmt = (document.body && document.body.getAttribute('data-numfmt')) || 'comma_dot';

  // Fast path: nothing to do only if we could guarantee no thousands separators
  // are needed — but since Money() outputs bare "1234.56", we always reformat.

  var RE = /^(-?)(\d+)\.(\d{2})$/;
  var NBSP = '\u00A0'; // non-breaking space for space_* formats

  function addThousands(s, sep) {
    return s.replace(/\B(?=(\d{3})+(?!\d))/g, sep);
  }

  function reformat(text) {
    var t = text.trim();
    var m = t.match(RE);
    if (!m) return null;
    var sign = m[1], whole = m[2], cents = m[3];
    switch (fmt) {
      case 'dot_comma':
        return sign + addThousands(whole, '.') + ',' + cents;
      case 'space_comma':
        return sign + addThousands(whole, NBSP) + ',' + cents;
      case 'space_dot':
        return sign + addThousands(whole, NBSP) + '.' + cents;
      default: // comma_dot
        return sign + addThousands(whole, ',') + '.' + cents;
    }
  }

  // Tags whose text content must never be touched.
  var SKIP = /^(SCRIPT|STYLE|INPUT|TEXTAREA|SELECT|OPTION|CODE|PRE|BUTTON)$/;

  function walk(node) {
    if (node.nodeType === 3) { // TEXT_NODE
      var r = reformat(node.textContent);
      if (r !== null) node.textContent = r;
      return;
    }
    if (node.nodeType === 1 && SKIP.test(node.tagName)) return;
    var child = node.firstChild;
    while (child) {
      var next = child.nextSibling;
      walk(child);
      child = next;
    }
  }

  function run() {
    if (document.body) walk(document.body);
  }

  // defer scripts run at readyState "interactive" (after HTML parse, before
  // DOMContentLoaded), so document.body is available and the else branch fires.
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', run);
  } else {
    run();
  }
})();
