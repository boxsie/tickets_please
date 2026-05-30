// dialogs.js — canonical open/close driver for native <dialog class="modal">
// elements. Extracted from the inline <script> the legacy ticket detail page
// shipped so every templ-rendered page using the Modal component shares one
// copy. The Modal component's data-attribute API is preserved verbatim:
//
//   <button data-dialog="dlg-id">          opens #dlg-id via showModal()
//   <button data-dialog-close="dlg-id">    closes #dlg-id via close()
//   click on the backdrop of <dialog.modal> closes it
//
// Loaded from internal/web/components/layout/layout.templ as a <script defer>
// in <head>. The DOMContentLoaded gate ensures the queryselectors find the
// triggers even when the script lands before the body's parsed.
(function () {
  function wire() {
    document.querySelectorAll('[data-dialog]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var dlg = document.getElementById(btn.getAttribute('data-dialog'));
        if (dlg && typeof dlg.showModal === 'function') dlg.showModal();
      });
    });
    document.querySelectorAll('[data-dialog-close]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var dlg = document.getElementById(btn.getAttribute('data-dialog-close'));
        if (dlg) dlg.close();
      });
    });
    document.querySelectorAll('dialog.modal').forEach(function (dlg) {
      dlg.addEventListener('click', function (e) {
        if (e.target === dlg) dlg.close();
      });
    });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wire);
  } else {
    wire();
  }
})();
