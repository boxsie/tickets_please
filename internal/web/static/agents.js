// agents.js — progressive enhancement for the /agents index table.
//
// Three behaviours, all delegated/idempotent so they survive hx-swaps:
//   1. Filter box ([data-agents-filter]) narrows rows by name/model/key.
//   2. Sortable headers (th[data-sort] in [data-agents-table]) reorder rows;
//      click toggles asc/desc, numeric columns sort on each row's
//      [data-sort-val], text columns on cell textContent.
//   3. Whole-row click ([data-href] on a <tr>) navigates to the agent detail,
//      except when the click landed on a link or button (copy/key affordances).
//
// No framework — the table renders fully server-side; this is pure sugar.
(function () {
  "use strict";

  function applyFilter(table, q) {
    var tbody = table.tBodies[0];
    if (!tbody) return;
    var query = q.trim().toLowerCase();
    var shown = 0;
    Array.prototype.forEach.call(tbody.rows, function (tr) {
      var hay = tr.dataset.filter || tr.textContent.toLowerCase();
      var match = !query || hay.indexOf(query) !== -1;
      tr.hidden = !match;
      if (match) shown++;
    });
    var empty = table.parentNode.querySelector("[data-agents-empty]");
    if (empty) empty.hidden = shown !== 0;
  }

  function sortBy(table, colIndex, type, dir) {
    var tbody = table.tBodies[0];
    if (!tbody) return;
    var rows = Array.prototype.slice.call(tbody.rows);
    rows.sort(function (a, b) {
      var ca = a.cells[colIndex],
        cb = b.cells[colIndex];
      var va, vb;
      if (type === "num") {
        va = parseFloat((ca && ca.dataset.sortVal) || "0") || 0;
        vb = parseFloat((cb && cb.dataset.sortVal) || "0") || 0;
        return dir * (va - vb);
      }
      va = (ca ? ca.textContent : "").trim().toLowerCase();
      vb = (cb ? cb.textContent : "").trim().toLowerCase();
      return dir * va.localeCompare(vb);
    });
    rows.forEach(function (tr) {
      tbody.appendChild(tr);
    });
  }

  function wireTable(table) {
    if (table.dataset.agentsWired) return;
    table.dataset.agentsWired = "1";

    var headers = table.querySelectorAll("thead th[data-sort]");
    Array.prototype.forEach.call(headers, function (th, idx) {
      th.classList.add("sortable-th");
      th.addEventListener("click", function () {
        var asc = th.getAttribute("aria-sort") !== "ascending";
        Array.prototype.forEach.call(headers, function (h) {
          h.removeAttribute("aria-sort");
        });
        th.setAttribute("aria-sort", asc ? "ascending" : "descending");
        sortBy(table, idx, th.dataset.sort, asc ? 1 : -1);
      });
    });

    // Row-click navigation.
    table.addEventListener("click", function (e) {
      if (e.target.closest("a, button")) return;
      var tr = e.target.closest("tr[data-href]");
      if (!tr) return;
      window.location.href = tr.dataset.href;
    });
  }

  function wire() {
    var filter = document.querySelector("[data-agents-filter]");
    var table = document.querySelector("[data-agents-table]");
    if (table) wireTable(table);
    if (filter && table && !filter.dataset.agentsWired) {
      filter.dataset.agentsWired = "1";
      filter.addEventListener("input", function () {
        applyFilter(table, filter.value);
      });
    }
  }

  document.addEventListener("DOMContentLoaded", wire);
  // hx-swapped content (sidebar refresh etc.) may re-insert the table.
  document.addEventListener("htmx:afterSwap", wire);
  // Already-loaded case (script deferred but DOM ready).
  if (document.readyState !== "loading") wire();
})();
