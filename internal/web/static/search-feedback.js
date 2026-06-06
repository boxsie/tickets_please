// search-feedback.js — tiny enhancement for the inline 👍/👎 search-hit rating
// widget. The widget works without JS (the 👍/👎 controls are real <form>s and
// the 👎 reason box is a native <details>); htmx handles the in-place swap.
//
// This adds just the one behaviour native markup can't express: pressing Escape
// while a 👎 reason box is open collapses it. Delegated keydown on the document,
// so it covers htmx-swapped widgets with no re-wiring.
(function () {
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Escape') return;
    var open = document.querySelectorAll('details.hit-dislike[open]');
    if (!open.length) return;
    open.forEach(function (d) { d.removeAttribute('open'); });
  });
})();
