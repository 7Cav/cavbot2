/* PROTOTYPE script for the Foxhole page variants (#417). Without it every
   form still posts, the filters are links, and the switcher arrows are
   links: the page stays safe, as ADR 0013 asks. With it, search and filters
   act in place, selection counts update, and notes edit in place. */
(function () {
  'use strict';

  function typing(el) {
    return el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));
  }

  // The switcher's arrow keys.
  document.addEventListener('keydown', function (e) {
    if (typing(document.activeElement) || e.metaKey || e.ctrlKey || e.altKey) { return; }
    var a = null;
    if (e.key === 'ArrowLeft') { a = document.querySelector('[data-proto-prev]'); }
    if (e.key === 'ArrowRight') { a = document.querySelector('[data-proto-next]'); }
    if (a) { e.preventDefault(); location.href = a.href; }
  });

  document.querySelectorAll('[data-fx-search], [data-fx-all], [data-fx-allwrap], [data-fx-sample]').forEach(function (el) {
    el.hidden = false;
  });

  var params = new URLSearchParams(location.search);
  var filter = params.get('filter') || '';

  function setUp(scope) {
    var rows = Array.from(scope.querySelectorAll('[data-fx-row]'));
    var picks = Array.from(scope.querySelectorAll('[data-fx-pick]'));
    var all = scope.querySelector('[data-fx-all]');
    var search = scope.querySelector('[data-fx-search]');
    var empty = scope.querySelector('[data-fx-empty]');
    var bulkpane = scope.querySelector('[data-fx-bulkpane]');
    var panebody = scope.querySelector('[data-fx-panebody]');
    var picked = scope.querySelector('[data-fx-picked]');
    var pickhint = scope.querySelector('[data-fx-pickhint]');

    function shown(row) { return !row.hidden; }

    function apply() {
      var q = search ? search.value.trim().toLowerCase() : '';
      var n = 0;
      rows.forEach(function (row) {
        var tags = ' ' + row.getAttribute('data-tags') + ' ';
        var ok = (filter === '' || tags.indexOf(' ' + filter + ' ') !== -1) &&
          (q === '' || row.getAttribute('data-search').indexOf(q) !== -1);
        row.hidden = !ok;
        if (ok) { n++; }
      });
      if (empty) { empty.hidden = n > 0; }
      count();
    }

    function count() {
      var on = picks.filter(function (p) { return p.checked; });
      scope.querySelectorAll('[data-fx-count]').forEach(function (c) { c.textContent = on.length; });
      scope.querySelectorAll('[data-fx-needs]').forEach(function (b) { b.disabled = on.length === 0; });
      if (all) {
        var vis = picks.filter(function (p) { return shown(p.closest('[data-fx-row]')); });
        var visOn = vis.filter(function (p) { return p.checked; });
        all.checked = vis.length > 0 && visOn.length === vis.length;
        all.indeterminate = visOn.length > 0 && visOn.length < vis.length;
      }
      if (bulkpane) {
        bulkpane.hidden = on.length === 0;
        if (panebody) { panebody.hidden = on.length > 0; }
        picked.innerHTML = '';
        on.forEach(function (p) {
          var li = document.createElement('li');
          li.textContent = p.getAttribute('data-name');
          picked.appendChild(li);
        });
        pickhint.hidden = on.length > 0;
      }
    }

    picks.forEach(function (p) { p.addEventListener('change', count); });
    if (all) {
      all.addEventListener('change', function () {
        picks.forEach(function (p) {
          if (shown(p.closest('[data-fx-row]'))) { p.checked = all.checked; }
        });
        count();
      });
    }
    if (search) { search.addEventListener('input', apply); }
    scope.querySelectorAll('[data-fx-filter]').forEach(function (a) {
      a.addEventListener('click', function (e) {
        e.preventDefault();
        filter = a.getAttribute('data-fx-filter');
        scope.querySelectorAll('[data-fx-filter]').forEach(function (b) { b.classList.toggle('is-on', b === a); });
        var u = new URL(location.href);
        if (filter) { u.searchParams.set('filter', filter); } else { u.searchParams.delete('filter'); }
        history.replaceState(null, '', u);
        apply();
      });
    });
    apply();
  }

  document.querySelectorAll('[data-fx-scope]').forEach(setUp);

  // Variant A: a note edits in place, and Save posts the note form.
  var retInput = document.querySelector('input[name=ret]');
  document.querySelectorAll('[data-fx-edit]').forEach(function (a) {
    a.addEventListener('click', function (e) {
      e.preventDefault();
      var cell = a.closest('.fx-note');
      var text = cell.querySelector('.fx-note-text');
      var current = text.querySelector('.off') ? '' : text.textContent;
      var form = document.createElement('form');
      form.method = 'post';
      form.action = '/notes';
      form.className = 'fx-note-form';
      form.innerHTML = '<input type="hidden" name="ret"><input type="hidden" name="id">' +
        '<textarea name="note" rows="2" maxlength="500"></textarea>' +
        '<button type="submit" class="btn btn--small btn--cta">Save</button> ' +
        '<button type="button" class="btn btn--small" data-cancel>Cancel</button>';
      form.querySelector('[name=ret]').value = retInput ? retInput.value : '';
      form.querySelector('[name=id]').value = a.getAttribute('data-fx-edit');
      var ta = form.querySelector('textarea');
      ta.value = current;
      var saved = Array.from(cell.childNodes);
      cell.innerHTML = '';
      cell.appendChild(form);
      ta.focus();
      function cancel() {
        cell.innerHTML = '';
        saved.forEach(function (n) { cell.appendChild(n); });
        a.focus();
      }
      form.querySelector('[data-cancel]').addEventListener('click', cancel);
      ta.addEventListener('keydown', function (k) {
        if (k.key === 'Escape') { k.preventDefault(); cancel(); }
        if (k.key === 'Enter' && !k.shiftKey) { k.preventDefault(); form.submit(); }
      });
    });
  });

  // Variant B: a changed note input is marked, and Save notes counts them.
  var dirty = false;
  document.querySelectorAll('[data-fx-noteinput]').forEach(function (input) {
    input.addEventListener('input', function () {
      input.classList.toggle('is-changed', input.value !== input.defaultValue);
      var form = input.form;
      var n = form.querySelectorAll('.fx-note-input.is-changed').length;
      var btn = form.querySelector('[data-fx-savenotes]');
      btn.textContent = n === 0 ? 'Save notes' : (n === 1 ? 'Save 1 note' : 'Save ' + n + ' notes');
      dirty = document.querySelector('.fx-note-input.is-changed') !== null;
    });
  });
  document.querySelectorAll('form').forEach(function (f) {
    f.addEventListener('submit', function () { dirty = false; });
  });
  window.addEventListener('beforeunload', function (e) {
    if (dirty) { e.preventDefault(); }
  });

  // Prototype only: fill the paste box with lines that exercise each result.
  document.querySelectorAll('[data-fx-sample]').forEach(function (b) {
    b.addEventListener('click', function () {
      var ta = b.closest('form').querySelector('[data-fx-lines]');
      ta.value = document.body.querySelector('[data-fx-sampletext]').getAttribute('data-fx-sampletext');
      ta.focus();
    });
  });
})();
