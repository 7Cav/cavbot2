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
