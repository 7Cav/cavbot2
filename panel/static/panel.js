/* Panel script (spec #343, ADR 0013): the tag pickers. One plain file the
   binary serves; no framework, no build step. A page that never runs it
   posts each picker's tags as rendered, and the add and remove controls do
   nothing.

   A picker is the element carrying data-picker. Its tags list holds one
   [data-tag] per selected item, each with the hidden input the form posts.
   Its search list holds every candidate as a [data-option] row, rendered at
   load and hidden until the add control opens it. A row is shown when no
   tag carries its ID and its label contains the typed text. Choosing a row
   clones the picker's <template> into a tag; removing a tag re-offers the
   row, when the row exists: an unavailable moderator role has none. */
(function () {
  'use strict';

  var pickers = 0;

  function setUp(root) {
    var single = root.hasAttribute('data-single');
    var tags = root.querySelector('[data-tags]');
    var add = root.querySelector('[data-add]');
    var search = root.querySelector('[data-search]');
    var input = root.querySelector('[data-search-input]');
    var list = root.querySelector('[data-list]');
    var empty = root.querySelector('[data-empty]');
    var none = root.querySelector('[data-none]');
    var blank = root.querySelector('[data-tag-template]');
    var rows = Array.from(list.querySelectorAll('[data-option]'));
    var prefix = 'picker' + (++pickers) + '-';
    // active is the row the arrow keys point at, an index into the rows
    // shown by the last refresh.
    var active = 0;
    var shown = [];

    rows.forEach(function (row, i) { row.id = prefix + i; });
    list.id = prefix + 'list';
    add.setAttribute('aria-controls', list.id);
    input.setAttribute('aria-controls', list.id);

    function selectedIDs() {
      var ids = {};
      tags.querySelectorAll('[data-tag]').forEach(function (tag) {
        ids[tag.getAttribute('data-tag')] = true;
      });
      return ids;
    }

    // refresh hides the rows a tag holds or the typed text rules out, and
    // marks the active row for the keyboard and the screen reader.
    function refresh() {
      var ids = selectedIDs();
      var q = input.value.trim().toLowerCase();
      shown = [];
      rows.forEach(function (row) {
        var label = row.getAttribute('data-label').toLowerCase();
        var hide = ids[row.getAttribute('data-option')] === true || (q !== '' && label.indexOf(q) === -1);
        row.hidden = hide;
        row.classList.remove('is-active');
        row.setAttribute('aria-selected', 'false');
        if (!hide) { shown.push(row); }
      });
      // Nothing shown is either no match for the typed text, or nothing
      // left to add.
      empty.hidden = shown.length > 0 || q === '';
      none.hidden = shown.length > 0 || q !== '';
      if (active >= shown.length) { active = shown.length - 1; }
      if (active < 0) { active = 0; }
      if (shown.length) {
        shown[active].classList.add('is-active');
        shown[active].setAttribute('aria-selected', 'true');
        input.setAttribute('aria-activedescendant', shown[active].id);
      } else {
        input.removeAttribute('aria-activedescendant');
      }
    }

    function open() {
      search.hidden = false;
      add.setAttribute('aria-expanded', 'true');
      input.value = '';
      active = 0;
      refresh();
      input.focus();
    }

    function close() {
      search.hidden = true;
      add.setAttribute('aria-expanded', 'false');
    }

    // choose makes a tag for the row from the picker's template and hides
    // the row. A single picker drops its earlier tag first.
    function choose(row) {
      var tag = blank.content.firstElementChild.cloneNode(true);
      var colour = row.getAttribute('data-colour');
      var label = row.getAttribute('data-label');
      var dot = tag.querySelector('.dot');
      tag.setAttribute('data-tag', row.getAttribute('data-option'));
      tag.querySelector('input').value = row.getAttribute('data-option');
      tag.querySelector('[data-label]').textContent = label;
      tag.querySelector('[data-remove]').setAttribute('aria-label', 'Remove ' + label);
      if (dot && colour) {
        dot.classList.remove('dot--none');
        dot.style.background = colour;
      }
      if (single) {
        tags.querySelectorAll('[data-tag]').forEach(function (old) { old.remove(); });
      }
      tags.appendChild(tag);
      if (single) {
        close();
        add.focus();
        return;
      }
      // A click leaves focus on the row, and hiding a focused row drops
      // focus out of the picker, which would close the search. Focus the
      // input first, then hide the row.
      input.focus();
      input.value = '';
      refresh();
    }

    add.addEventListener('click', function () {
      if (search.hidden) { open(); } else { close(); }
    });

    input.addEventListener('input', function () {
      active = 0;
      refresh();
    });

    // move points the arrow keys at the row delta places away, within the
    // shown rows, and scrolls it into the list's view.
    function move(delta) {
      active = Math.min(Math.max(active + delta, 0), shown.length - 1);
      refresh();
      if (shown[active]) { shown[active].scrollIntoView({ block: 'nearest' }); }
    }

    input.addEventListener('keydown', function (e) {
      switch (e.key) {
        case 'ArrowDown':
          e.preventDefault();
          move(1);
          break;
        case 'ArrowUp':
          e.preventDefault();
          move(-1);
          break;
        case 'Enter':
          // Enter in a form's text input submits the form. Here it chooses.
          e.preventDefault();
          if (shown[active]) { choose(shown[active]); }
          break;
        case 'Escape':
          e.preventDefault();
          close();
          add.focus();
          break;
      }
    });

    list.addEventListener('click', function (e) {
      var row = e.target.closest('[data-option]');
      if (row && !row.hidden) { choose(row); }
    });

    tags.addEventListener('click', function (e) {
      var remove = e.target.closest('[data-remove]');
      if (!remove) { return; }
      remove.closest('[data-tag]').remove();
      refresh();
      close();
      add.focus();
    });

    // Focus leaving the picker closes the search. A click on a row moves
    // focus to the row, inside the picker, so the click still lands.
    root.addEventListener('focusout', function (e) {
      if (!search.hidden && !root.contains(e.relatedTarget)) { close(); }
    });
  }

  document.querySelectorAll('[data-picker]').forEach(setUp);
})();

/* Inline note edit on the Foxhole page (spec #434, ADR 0013). A row's Edit
   link opens the note form in the row's note cell: the same form the link
   opens above the list when script is off, cloned from the page's
   <template>. Enter saves, posting the form in the background; Escape and
   Cancel put the cell back. A saved note swaps in the row's note cell, the
   change log and the save's result from the page the save answers with, so
   nothing else on the page reloads. A save that took the member off the
   page, as an empty note on a member with no role does, drops the row and
   swaps in the list's counts instead. A refused save swaps in the form the
   server sends back, with its reason and the text typed. */
(function () {
  'use strict';

  var template = document.querySelector('[data-note-template]');
  if (!template) { return; }

  // mount puts a note form in the cell and wires its keys and buttons.
  // restore puts back what the cell showed before the form opened.
  function mount(cell, form, restore) {
    var input = form.querySelector('[data-note-input]');
    var cancel = form.querySelector('[data-field="cancel_note"]');
    // A form cloned into a row carries the hint ID the page's own form
    // uses. Each row's form gets its own, so its input names its own hint
    // and a screen reader reads the warning out.
    var hint = form.querySelector('[data-field="note-hint"]');
    if (hint) {
      hint.id = 'note-hint-' + cell.closest('[data-member]').getAttribute('data-member');
      input.setAttribute('aria-describedby', hint.id);
    }
    cell.replaceChildren(form);
    form.addEventListener('submit', function (e) {
      e.preventDefault();
      save(cell, form, restore);
    });
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') {
        e.preventDefault();
        restore();
      }
    });
    cancel.addEventListener('click', function (e) {
      e.preventDefault();
      restore();
    });
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
  }

  function open(cell) {
    var shown = Array.from(cell.childNodes);
    var note = cell.getAttribute('data-note');
    var form = template.content.firstElementChild.cloneNode(true);
    form.querySelector('[name="member"]').value = cell.closest('[data-member]').getAttribute('data-member');
    form.querySelector('[name="loaded"]').value = note;
    form.querySelector('[name="note"]').value = note;
    mount(cell, form, function () {
      cell.replaceChildren.apply(cell, shown);
      var link = cell.querySelector('[data-field="edit_note"]');
      if (link) { link.focus(); }
    });
  }

  // show puts a note in the cell as the server would render it: the note
  // text, the loaded note the next form starts from, and the link's label.
  function show(cell, note) {
    cell.setAttribute('data-note', note);
    cell.querySelector('[data-field="note"]').textContent = note;
    cell.querySelector('[data-field="edit_note"]').textContent = note ? 'Edit' : 'Add a note';
  }

  // swap replaces this page's element matching the selector with the one
  // the answer carries, when both have it.
  function swap(doc, selector) {
    var fresh = doc.querySelector(selector);
    var mine = document.querySelector(selector);
    if (fresh && mine) { mine.replaceWith(document.adoptNode(fresh)); }
  }

  // announce puts the answer's save result in this page's result line. The
  // line is a live region, so it keeps its element and changes its contents
  // only, and a screen reader reads the new result out. It copies the
  // answer's result, leaving the answer whole for the checks after it.
  function announce(doc) {
    var fresh = doc.querySelector('[data-note-result]');
    var mine = document.querySelector('[data-note-result]');
    if (!fresh || !mine) { return; }
    mine.replaceChildren.apply(mine, Array.from(fresh.childNodes).map(function (n) {
      return document.importNode(n, true);
    }));
  }

  // takeOff drops a row the save took off the page, swaps in the answer's
  // filter counts, and the answer's empty list when it was the last row.
  // Focus moves to a neighbouring row's Edit link, or the search box.
  function takeOff(row, doc) {
    var body = row.parentNode;
    var view = row.closest('[data-field]').getAttribute('data-field');
    var next = row.nextElementSibling || row.previousElementSibling;
    row.remove();
    swap(doc, '[data-filters]');
    var link = next && next.querySelector('[data-field="edit_note"]');
    if (link) {
      link.focus();
      return;
    }
    var empty = doc.querySelector('[data-field="' + view + '"] tbody');
    if (empty) { body.replaceWith(document.adoptNode(empty)); }
    var search = document.querySelector('[data-field="search"] [type="search"]');
    if (search) { search.focus(); }
  }

  // failed tells the manager in the form that the save didn't reach the
  // panel, and leaves the form open with their text.
  function failed(form) {
    var line = form.querySelector('[data-note-failed]') || document.createElement('div');
    line.setAttribute('data-note-failed', '');
    line.className = 'note fx-noterefusal';
    line.textContent = 'The note could not be saved. Reload the page and try again.';
    form.prepend(line);
    form.querySelector('[type="submit"]').disabled = false;
  }

  function save(cell, form, restore) {
    var input = form.querySelector('[data-note-input]');
    var loaded = form.querySelector('[name="loaded"]').value;
    if (input.value.trim() === loaded && !form.querySelector('[data-error]')) {
      restore();
      return;
    }
    var id = cell.closest('[data-member]').getAttribute('data-member');
    form.querySelector('[type="submit"]').disabled = true;
    fetch(form.action, { method: 'POST', body: new URLSearchParams(new FormData(form)), credentials: 'same-origin' })
      .then(function (res) {
        return res.text().then(function (text) {
          return { ok: res.ok, doc: new DOMParser().parseFromString(text, 'text/html') };
        });
      })
      .then(function (answer) {
        if (!answer.ok) {
          var refused = answer.doc.querySelector('main [data-field="note-form"]');
          if (refused) {
            // The refused form loads the note as it stands now, which may
            // be another manager's. Cancel shows that note, not the one the
            // row loaded.
            var current = refused.querySelector('[name="loaded"]').value;
            mount(cell, document.adoptNode(refused), function () {
              restore();
              show(cell, current);
            });
          } else {
            failed(form);
          }
          return;
        }
        swap(answer.doc, '[data-field="changes"]');
        announce(answer.doc);
        var fresh = answer.doc.querySelector('[data-member="' + CSS.escape(id) + '"] [data-note-cell]');
        if (fresh) {
          cell.replaceWith(document.adoptNode(fresh));
          return;
        }
        if (answer.doc.querySelector('[data-cleared="' + CSS.escape(id) + '"]')) {
          takeOff(cell.closest('[data-member]'), answer.doc);
          return;
        }
        // The view the save returns to no longer lists the row, as when
        // the search matched the old note: show the saved note in place.
        restore();
        show(cell, input.value.trim());
      })
      .catch(function () { failed(form); });
  }

  document.addEventListener('click', function (e) {
    var link = e.target.closest('[data-field="edit_note"]');
    var cell = link && link.closest('[data-note-cell]');
    if (!cell) { return; }
    e.preventDefault();
    open(cell);
  });
})();

/* The Foxhole page's selection bar (spec #434, ADR 0013): how many rows are
   ticked, and its buttons off while none is. The checkboxes belong to the
   bar's form through their form attribute, so without script the bar posts
   whatever is ticked, and it shows no count. */
(function () {
  'use strict';

  var bar = document.querySelector('[data-field="selection"]');
  if (!bar) { return; }
  var count = bar.querySelector('[data-selection-count]');
  var selected = count.querySelector('[data-field="selected"]');
  var buttons = bar.querySelectorAll('[data-needs-selection]');

  function refresh() {
    var n = document.querySelectorAll('[data-select]:checked').length;
    selected.textContent = n;
    buttons.forEach(function (b) { b.disabled = n === 0; });
  }

  document.addEventListener('change', function (e) {
    if (e.target.matches('[data-select]')) { refresh(); }
  });
  // Back and forward can bring the page back with boxes still ticked.
  window.addEventListener('pageshow', refresh);
  count.hidden = false;
  refresh();
})();
