// copy.js — progressive-enhancement clipboard helper.
//
// Any element carrying a `data-copy` attribute becomes a copy button: clicking
// it writes the attribute's value to the clipboard and flashes "Copied" for a
// beat. Delegated off the document so SSE-injected content works without
// re-wiring. No-ops silently where the Clipboard API is unavailable (insecure
// origin / old browser) — the visible <code> still lets the user select+copy.
(function () {
  "use strict";

  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-copy]");
    if (!btn) return;

    var text = btn.getAttribute("data-copy");
    if (!navigator.clipboard || !text) return;

    navigator.clipboard.writeText(text).then(function () {
      if (btn.dataset.copyFlashing) return;
      var original = btn.textContent;
      btn.dataset.copyFlashing = "1";
      btn.textContent = "Copied";
      btn.classList.add("copied");
      setTimeout(function () {
        btn.textContent = original;
        btn.classList.remove("copied");
        delete btn.dataset.copyFlashing;
      }, 1200);
    });
  });
})();
