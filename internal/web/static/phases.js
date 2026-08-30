// phases.js — sticky collapse state for the phases-index <details> rows.
// Progressive enhancement: with JS off the rows still expand/collapse, they
// just don't remember across reloads. The open/closed state is persisted in
// localStorage keyed per project+phase so two projects with same-slug phases
// don't collide:
//
//   tp:phase-open:{project_id}:{phase_id}  =>  "1" (open) | "0" (closed)
//
// Skipped entirely when the page is in wave-focus mode (?wave= / ?phase= in the
// URL): there the server force-opens the relevant rows and persisting that
// transient state would leak into normal browsing.
//
// Loaded from internal/web/components/layout/layout.templ as <script defer>.
(function () {
  function focusMode() {
    var s = window.location.search;
    return s.indexOf('wave=') !== -1 || s.indexOf('phase=') !== -1;
  }

  function key(projectID, phaseID) {
    return 'tp:phase-open:' + projectID + ':' + phaseID;
  }

  function wire() {
    var list = document.querySelector('.phase-list[data-project-id]');
    if (!list) return;
    var projectID = list.getAttribute('data-project-id');
    var rows = list.querySelectorAll('details.phase-row[data-phase-id]');
    var focused = focusMode();

    rows.forEach(function (row) {
      var phaseID = row.getAttribute('data-phase-id');
      var k = key(projectID, phaseID);

      if (!focused) {
        var saved = null;
        try { saved = window.localStorage.getItem(k); } catch (e) { /* private mode */ }
        if (saved === '1') row.open = true;
        else if (saved === '0') row.open = false;
      }

      row.addEventListener('toggle', function () {
        if (focusMode()) return; // don't persist server-forced focus state
        try { window.localStorage.setItem(k, row.open ? '1' : '0'); } catch (e) { /* ignore */ }
      });
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wire);
  } else {
    wire();
  }
})();
