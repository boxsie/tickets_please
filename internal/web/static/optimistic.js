// optimistic.js — makes the ticket-detail page feel instant (#85).
//
// Progressive enhancement: every behaviour here intercepts a normal <form>
// submit and falls back to the plain POST (full page reload) when fetch or the
// hooks are unavailable. Both flows lean on the existing SSE stream (#82/#83)
// for the authoritative update — this only paints the local tab ahead of the
// round-trip.
//
//   form[data-optimistic-comment]  → render a "sending…" placeholder row +
//       clear the textarea, then POST with an Idempotency-Key. The server's
//       SSE echo removes the placeholder (by id) and appends the canonical
//       row, so there's exactly one row. A failed POST turns the placeholder
//       red with a Retry.
//   form[data-optimistic-move]     → close the dialog immediately + a
//       "Moving…" toast, then POST with an Idempotency-Key. The SSE TicketMoved
//       patch morphs the status badge/actions. A failed POST reopens the dialog
//       with an error toast.
(function () {
  function cid() {
    if (window.crypto && crypto.randomUUID) return crypto.randomUUID();
    return 'c' + Date.now() + '-' + Math.floor(Math.random() * 1e9);
  }

  // postForm sends the form url-encoded (so the existing form handlers + CSRF
  // wrapper read it unchanged) with the client id as an Idempotency-Key.
  function postForm(form, id) {
    var data = new URLSearchParams(new FormData(form));
    return fetch(form.action, {
      method: 'POST',
      headers: { 'Idempotency-Key': id },
      body: data,
      credentials: 'same-origin',
    });
  }

  function toast(message, kind) {
    var region = document.getElementById('ticket-toasts');
    if (!region) return null;
    var el = document.createElement('div');
    el.className = 'toast' + (kind === 'error' ? ' toast-error' : '');
    el.setAttribute('role', 'status');
    el.textContent = message;
    region.appendChild(el);
    return el;
  }

  function wireComment(form) {
    form.addEventListener('submit', function (e) {
      if (!window.fetch) return; // no-JS-ish fallback: native POST
      var ta = form.querySelector('textarea[name="body"]');
      var body = ta ? ta.value.trim() : '';
      e.preventDefault();
      if (!form.reportValidity() || !body) return;

      var id = cid();
      var list = document.querySelector(form.getAttribute('data-comments-list') || '#comments-list');
      var pending = null;
      if (list) {
        var empty = list.querySelector('.comments-empty');
        if (empty) empty.remove();
        pending = document.createElement('li');
        pending.id = 'comment-pending-' + id;
        pending.className = 'comment-row comment-pending';
        pending.setAttribute('aria-busy', 'true');
        var meta = document.createElement('div');
        meta.className = 'comment-meta';
        var who = document.createElement('span');
        who.className = 'comment-author';
        who.textContent = 'you';
        var flag = document.createElement('span');
        flag.className = 'comment-pending-flag';
        flag.textContent = 'sending…';
        meta.appendChild(who);
        meta.appendChild(flag);
        var bodyEl = document.createElement('div');
        bodyEl.className = 'comment-body';
        bodyEl.textContent = body;
        pending.appendChild(meta);
        pending.appendChild(bodyEl);
        list.appendChild(pending);
      }
      if (ta) ta.value = '';

      postForm(form, id)
        .then(function (resp) {
          if (!resp.ok && pending) markCommentError(pending, form, body);
          // success: the SSE echo removes #comment-pending-<id> and appends
          // the canonical row — nothing more to do here.
        })
        .catch(function () {
          if (pending) markCommentError(pending, form, body);
        });
    });
  }

  function markCommentError(row, form, body) {
    row.classList.remove('comment-pending');
    row.classList.add('comment-error');
    row.removeAttribute('aria-busy');
    var flag = row.querySelector('.comment-pending-flag');
    if (flag) flag.textContent = 'failed to send — ';
    var retry = document.createElement('button');
    retry.type = 'button';
    retry.className = 'btn btn-sm comment-retry';
    retry.textContent = 'Retry';
    retry.addEventListener('click', function () {
      row.remove();
      var ta = form.querySelector('textarea[name="body"]');
      if (ta) ta.value = body;
      if (form.requestSubmit) form.requestSubmit();
      else {
        var btn = form.querySelector('button[type="submit"]');
        if (btn) btn.click();
      }
    });
    (flag || row).appendChild(retry);
  }

  function wireMove(form) {
    form.addEventListener('submit', function (e) {
      if (!window.fetch) return;
      e.preventDefault();
      if (!form.reportValidity()) return;

      var id = cid();
      var dlg = document.getElementById(form.getAttribute('data-dialog-id') || '');
      var sel = form.querySelector('[name="target_column"]');
      var col = sel ? sel.value : '';
      if (dlg && dlg.close) dlg.close();
      var t = toast('Moving' + (col ? ' to ' + col : '') + '…');

      postForm(form, id)
        .then(function (resp) {
          if (resp.ok) return; // SSE morphs the badge + adds the confirm toast
          if (t) t.remove();
          resp.text().then(function (msg) {
            toast(msg || 'Move failed', 'error');
          });
          if (dlg && dlg.showModal) dlg.showModal();
        })
        .catch(function () {
          if (t) t.remove();
          toast('Move failed — network error', 'error');
          if (dlg && dlg.showModal) dlg.showModal();
        });
    });
  }

  function wire() {
    document.querySelectorAll('form[data-optimistic-comment]').forEach(wireComment);
    document.querySelectorAll('form[data-optimistic-move]').forEach(wireMove);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wire);
  } else {
    wire();
  }
})();
