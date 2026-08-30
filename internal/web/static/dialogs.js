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
// in <head>.
//
// Open/close is wired by event DELEGATION on the document, not per-element
// listeners. That matters because the ticket-detail action cluster
// (#ticket-actions, which carries the Archive/Unarchive trigger) is
// re-rendered live via SSE element patches — a per-element listener bound at
// load would be lost the moment the morph replaced the button. A single
// document-level listener keeps every current and future trigger working,
// including SSE-injected ones, with no re-wiring.
(function () {
  document.addEventListener('click', function (e) {
    var t = e.target;
    if (!t || !t.closest) return;
    var opener = t.closest('[data-dialog]');
    if (opener) {
      var dlg = document.getElementById(opener.getAttribute('data-dialog'));
      if (dlg && typeof dlg.showModal === 'function') dlg.showModal();
      return;
    }
    var closer = t.closest('[data-dialog-close]');
    if (closer) {
      var cdlg = document.getElementById(closer.getAttribute('data-dialog-close'));
      if (cdlg) cdlg.close();
      return;
    }
    // Backdrop click: the event target is the <dialog> itself only when the
    // click landed on the backdrop area, not on .modal-body content.
    if (t.matches && t.matches('dialog.modal')) {
      t.close();
    }
  });
})();
