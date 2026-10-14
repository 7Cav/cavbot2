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
   row, when the row exists: an unavailable role has none. A picker with
   data-options-from renders no rows of its own and copies them from the
   <template> with that ID, so the Settings page's rank pickers share one
   list of the guild's roles. */
(function () {
  'use strict';

  var roots = document.querySelectorAll('[data-picker]');
  if (!roots.length) { return; }

  var pickers = 0;

  // A press is a mousedown and the mouseup that ends it, or the dragend
  // when the press drags something. pressed is the element the press began
  // on, and null between presses. afterPress, when set, runs as the press
  // ends and is called with that element. That comes before the click,
  // whose target the press has already fixed.
  var pressed = null;
  var afterPress = null;

  function endPress() {
    var target = pressed;
    var run = afterPress;
    pressed = null;
    afterPress = null;
    if (run) { run(target); }
  }

  document.addEventListener('mousedown', function (e) { pressed = e.target; }, true);
  document.addEventListener('mouseup', endPress, true);
  document.addEventListener('dragend', endPress, true);

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
    var shared = root.hasAttribute('data-options-from') &&
      document.getElementById(root.getAttribute('data-options-from'));
    if (shared) { list.appendChild(shared.content.cloneNode(true)); }
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
    // the row. A single picker drops its earlier tag first. The label goes
    // in each [data-label]: the one the tag shows, and the one in its
    // remove control's name, which the template already ends with the set
    // the picker edits.
    function choose(row) {
      var tag = blank.content.firstElementChild.cloneNode(true);
      var colour = row.getAttribute('data-colour');
      var label = row.getAttribute('data-label');
      var dot = tag.querySelector('.dot');
      tag.setAttribute('data-tag', row.getAttribute('data-option'));
      tag.querySelector('input').value = row.getAttribute('data-option');
      tag.querySelectorAll('[data-label]').forEach(function (el) { el.textContent = label; });
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
    // focus to the row, inside the picker, so the click still lands. A
    // press elsewhere moves focus on its mousedown. Closing then would
    // move what sits below the list up before the mouseup, and the press
    // would make no click (#682). So a press closes the search once it
    // ends, unless closing would move what the press was on. Then the
    // search stays open, so a second press there lands too.
    root.addEventListener('focusout', function (e) {
      if (search.hidden || root.contains(e.relatedTarget)) { return; }
      if (!pressed) {
        close();
        return;
      }
      afterPress = function (target) {
        // Hiding the search to measure can clamp the page's scroll, so the
        // scroll goes back too. Nothing paints between the hide and the
        // show.
        var x = window.scrollX;
        var y = window.scrollY;
        var top = target.getBoundingClientRect().top;
        search.hidden = true;
        var moved = target.getBoundingClientRect().top !== top;
        search.hidden = false;
        window.scrollTo(x, y);
        if (!moved) { close(); }
      };
    });
  }

  roots.forEach(setUp);
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
   server sends back, with its reason and the text typed. Any other answer
   that isn't OK and didn't come through a redirect swaps in a fresh form
   with the text typed and the sentence the answer carries. An answer with
   no sentence, or none at all, gets a line saying the panel didn't confirm
   the save.

   A save from a panel session that has ended answers with the sign-in
   page. The row then says whether the note saved, beside a Sign in again
   button. The button keeps the notes typed in the page's rows in the
   tab's session storage, and the Foxhole page the sign-in comes back to
   opens them again. Without script the save shows the sign-in page, and
   the typed note is lost. */
(function () {
  'use strict';

  var template = document.querySelector('[data-note-template]');
  if (!template) { return; }

  // memberOf is the ID of the member whose row holds the cell.
  function memberOf(cell) {
    return cell.closest('[data-member]').getAttribute('data-member');
  }

  // ownIDs gives each ID under root the member's suffix, and the note box's
  // description with them. A form in a row carries the IDs the page's own
  // note form uses: the hint's, and on a refused save the refusal's. A form
  // with a line the script put at its top carries the line's too. Each
  // member's form gets its own, so forms open at once share none, and its
  // note box names its own line and hint.
  function ownIDs(root, member) {
    var input = root.querySelector('[data-note-input]');
    root.querySelectorAll('[id]').forEach(function (el) { el.id += '-' + member; });
    var described = input.getAttribute('aria-describedby');
    if (described) {
      input.setAttribute('aria-describedby', described.split(' ').map(function (id) {
        return id + '-' + member;
      }).join(' '));
    }
  }

  // mount puts a note form in the cell, wires its keys and buttons, and
  // returns its note box. restore puts back what the cell showed before
  // the form opened.
  function mount(cell, form, restore) {
    var input = form.querySelector('[data-note-input]');
    var cancel = form.querySelector('[data-field="cancel_note"]');
    ownIDs(form, memberOf(cell));
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
    return input;
  }

  // focusBox focuses a note box with the caret after its text. A screen
  // reader reads out the box's description as focus lands.
  function focusBox(input) {
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
  }

  // restorer is what Cancel calls for a form about to open in the cell. It
  // puts back what the cell shows now and focuses the cell's Edit link.
  function restorer(cell) {
    var shown = Array.from(cell.childNodes);
    return function () {
      cell.replaceChildren.apply(cell, shown);
      var link = cell.querySelector('[data-field="edit_note"]');
      if (link) { link.focus(); }
    };
  }

  // fill is a fresh note form from the page's template, holding values, an
  // object keyed by field name. A field values leaves out keeps the
  // template's value, as the view's search and filter do.
  function fill(values) {
    var form = template.content.firstElementChild.cloneNode(true);
    Object.keys(values).forEach(function (name) {
      form.querySelector('[name="' + name + '"]').value = values[name];
    });
    return form;
  }

  function open(cell) {
    var note = cell.getAttribute('data-note');
    var form = fill({ member: memberOf(cell), loaded: note, note: note });
    focusBox(mount(cell, form, restorer(cell)));
  }

  // show puts a note in the cell as the server would render it: the note
  // text, the loaded note the next form starts from, and the link's label
  // and the name naming the member, read from the row.
  function show(cell, note) {
    var link = cell.querySelector('[data-field="edit_note"]');
    var member = cell.closest('[data-member]').querySelector('[data-field="display_name"]').textContent;
    cell.setAttribute('data-note', note);
    cell.querySelector('[data-field="note"]').textContent = note;
    link.textContent = note ? 'Edit' : 'Add a note';
    link.setAttribute('aria-label', (note ? 'Edit the note on ' : 'Add a note on ') + member);
  }

  // answerOf reads a response the script fetched: whether it was OK,
  // whether it came through a redirect, and its page.
  function answerOf(res) {
    return res.text().then(function (text) {
      return { ok: res.ok, redirected: res.redirected, doc: new DOMParser().parseFromString(text, 'text/html') };
    });
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

  // unconfirmed is what the row says when no answer came, or one that
  // carries no page, as the plain-text answer to a failure inside the
  // panel. Either can come after the note was stored, so the line refuses
  // nothing and takes the neutral stripe.
  var unconfirmed = 'The panel didn\'t confirm this save. Reload the page to see whether the note saved.';

  // sentenceOf is the sentence a page carries when it answered a save in
  // place and isn't the Foxhole page, as the no-access page and the error
  // pages do. It is null for an answer with no page.
  function sentenceOf(doc) {
    var line = doc.querySelector('main [data-field="no-access"], main[data-failure] p');
    return line && line.textContent.trim();
  }

  // The lines a row shows when the panel session has ended. No answer
  // carries a sentence that fits, so the script owns them. Each refuses
  // nothing, so each takes the neutral stripe.
  var endedUnsaved = 'Your session ended, so this note was not saved. Sign in again and it will be here to save.';
  var endedSaved = 'The note saved, but your session has ended. Sign in again before your next change.';
  var notSavedYet = 'This note is not saved yet. Press Save to save it.';

  // noteLine is a note with the ID given, for the top of a note form. It
  // takes the red stripe only when refused says it refuses something.
  function noteLine(id, text, refused) {
    var el = document.createElement('div');
    el.id = id;
    el.className = refused ? 'note fx-noterefusal' : 'note note--neutral fx-noterefusal';
    el.textContent = text;
    return el;
  }

  // describe puts the line at the top of the form, where a refused note's
  // sentence sits, and names it first in the note box's description, so a
  // screen reader reads it out as focus lands in the box.
  function describe(form, el) {
    form.prepend(el);
    var input = form.querySelector('[data-note-input]');
    var described = input.getAttribute('aria-describedby');
    input.setAttribute('aria-describedby', described ? el.id + ' ' + described : el.id);
  }

  // posted is what the form posts, keyed by field name.
  function posted(form) {
    var values = {};
    new FormData(form).forEach(function (value, name) { values[name] = value; });
    return values;
  }

  // failed puts a fresh form in the cell holding what the manager posted,
  // with the answer's sentence where a refused note's sits, or the
  // unconfirmed line when the sentence is null. Save posts the note again,
  // and Cancel puts back what the cell showed before.
  function failed(cell, form, restore, sentence) {
    var fresh = fill(posted(form));
    describe(fresh, noteLine('note-failed', sentence || unconfirmed, Boolean(sentence)));
    focusBox(mount(cell, fresh, restore));
  }

  // startAddress starts a sign-in at the forum. The sign-in page's form
  // posts there, and a field named next carries the panel address the
  // sign-in comes back to.
  var startAddress = '/auth/start';

  // viewAddress is the address of the view this page shows, with its
  // search and filter. It is the note form's Cancel address without the
  // fragment that opens it at the list, so a sign-in that comes back to it
  // lands at the top of the page with nothing focused, and a kept note can
  // take focus.
  function viewAddress() {
    return template.content.querySelector('[data-field="cancel_note"]').getAttribute('href').split('#')[0];
  }

  // signedInAs is the forum user the rail names.
  function signedInAs() {
    return document.querySelector('.rail [data-field="username"]').textContent;
  }

  // keptKey names the entry in the tab's session storage that holds the
  // kept notes through the forum's sign-in. Closing the tab drops it.
  var keptKey = 'foxhole-kept-notes';

  // waiting is the notes the tab's storage holds for the forum user signed
  // in, as a kept note whose block didn't load leaves there.
  function waiting() {
    try {
      var kept = JSON.parse(sessionStorage.getItem(keptKey));
      return kept && kept.user === signedInAs() ? kept.notes : [];
    } catch (e) {
      return [];
    }
  }

  // store puts the notes in the tab's storage, with the forum user who
  // typed them. Without session storage the page goes on, and the notes
  // are lost, as without script.
  function store(notes) {
    try {
      sessionStorage.setItem(keptKey, JSON.stringify({ user: signedInAs(), notes: notes }));
    } catch (e) {
      // With no session storage, there is nowhere to keep them.
    }
  }

  // keep stores each note form open in a row whose text differs from the
  // note it loaded, after any notes still waiting in storage. The form of
  // the member named first goes first.
  function keep(first) {
    var notes = waiting();
    document.querySelectorAll('[data-note-cell] [data-field="note-form"]').forEach(function (form) {
      var values = posted(form);
      var note = { member: values.member, loaded: values.loaded, note: values.note };
      if (note.note.trim() === note.loaded) { return; }
      if (note.member === first) {
        notes.unshift(note);
      } else {
        notes.push(note);
      }
    });
    store(notes);
  }

  // signInAgain keeps the typed notes, then starts a sign-in that comes
  // back to this view.
  function signInAgain(first) {
    keep(first);
    var start = document.createElement('form');
    var next = document.createElement('input');
    start.method = 'post';
    start.action = startAddress;
    next.type = 'hidden';
    next.name = 'next';
    next.value = viewAddress();
    start.appendChild(next);
    document.body.appendChild(start);
    start.submit();
  }

  // signInRow is the Sign in again button, in a row of its own under a
  // line saying the session has ended. first is the member whose note
  // takes focus when the notes open again, null for none in particular.
  function signInRow(first) {
    var row = document.createElement('div');
    var button = document.createElement('button');
    row.className = 'fx-notesignin';
    button.type = 'button';
    button.className = 'btn btn--small btn--cta';
    button.setAttribute('data-field', 'sign_in_again');
    button.textContent = 'Sign in again';
    button.addEventListener('click', function () { signInAgain(first); });
    row.appendChild(button);
    return row;
  }

  // turnedAway is failed for a save the ended session turned away, so
  // nothing was stored. Its line says so, with the Sign in again button
  // under it.
  function turnedAway(cell, form, restore) {
    var fresh = fill(posted(form));
    var said = noteLine('note-ended', endedUnsaved);
    describe(fresh, said);
    said.after(signInRow(memberOf(cell)));
    focusBox(mount(cell, fresh, restore));
  }

  // savedEnded shows the note as saved in the cell, as a save that went
  // through does, for a save the session ended after. Under the note go
  // the line saying the session has ended and the Sign in again button,
  // in place of any an earlier save left there. The button takes focus
  // and names the line as its description, so a screen reader reads both
  // out.
  function savedEnded(cell, restore, note) {
    restore();
    show(cell, note);
    cell.querySelectorAll('[data-ended]').forEach(function (el) { el.remove(); });
    var said = noteLine('note-ended-' + memberOf(cell), endedSaved);
    var row = signInRow(null);
    var button = row.querySelector('button');
    said.setAttribute('data-ended', '');
    row.setAttribute('data-ended', '');
    button.setAttribute('aria-describedby', said.id);
    cell.append(said, row);
    button.focus();
  }

  // aboveList puts el where the page puts a note block, under whatever is
  // pending and over the action blocks and the list.
  function aboveList(el) {
    document.querySelector('main [data-field="actions"], main [data-notice="member-list"], main [data-field="changes"]').before(el);
  }

  // notLoaded is what the page says when a kept note's block didn't load.
  var notLoaded = 'A note you typed before signing in again didn\'t load.';

  // unopened puts a note whose block didn't load back in storage, and says
  // so above the list, once however many didn't. Its Reload the page
  // button keeps the notes typed in the rows too, so the next load opens
  // them all. The line is an alert, so a screen reader reads it out as it
  // appears.
  function unopened(note) {
    store(waiting().concat([note]));
    if (document.querySelector('[data-field="kept-unopened"]')) { return; }
    var said = document.createElement('div');
    var reload = document.createElement('button');
    said.className = 'note note--neutral';
    said.setAttribute('role', 'alert');
    said.setAttribute('data-field', 'kept-unopened');
    reload.type = 'button';
    reload.className = 'btn btn--small btn--cta';
    reload.textContent = 'Reload the page';
    reload.addEventListener('click', function () {
      keep(null);
      location.reload();
    });
    said.append(notLoaded + ' ', reload);
    aboveList(said);
  }

  // openAboveList opens a kept note in a "Note on ..." block above the
  // list, where the Edit link opens a note form without script, for a
  // member this view doesn't list. The block comes from this view's page
  // with the member's note form open, loaded in the background. Its Save
  // posts the page's way, without script.
  function openAboveList(note, first) {
    var url = new URL(viewAddress(), location.href);
    url.searchParams.set('note', note.member);
    fetch(url, { credentials: 'same-origin' })
      .then(answerOf)
      .then(function (answer) {
        var page = answer.ok && answer.doc.querySelector('main[data-page="foxhole"]');
        var block = page && page.querySelector('[data-field="note-block"]');
        if (!block) {
          unopened(note);
          return;
        }
        block = document.adoptNode(block);
        var form = block.querySelector('[data-field="note-form"]');
        form.querySelector('[name="loaded"]').value = note.loaded;
        form.querySelector('[name="note"]').value = note.note;
        describe(form, noteLine('note-kept', notSavedYet));
        ownIDs(block, note.member);
        aboveList(block);
        if (first) { focusBox(form.querySelector('[data-note-input]')); }
      })
      .catch(function () { unopened(note); });
  }

  // bringBack opens the notes kept before a sign-in, on the first Foxhole
  // page the tab loads after it, when the forum user who typed them is
  // signed in. That load drops them either way. Each opens in its row with
  // its typed text and the note its form loaded, so the server refuses as
  // stale a save over a note someone changed meanwhile. The first takes
  // focus.
  function bringBack() {
    var kept;
    try {
      kept = JSON.parse(sessionStorage.getItem(keptKey));
      sessionStorage.removeItem(keptKey);
    } catch (e) {
      return;
    }
    if (!kept || kept.user !== signedInAs()) { return; }
    kept.notes.forEach(function (note, i) {
      var cell = document.querySelector('[data-member="' + CSS.escape(note.member) + '"] [data-note-cell]');
      if (!cell) {
        openAboveList(note, i === 0);
        return;
      }
      var form = fill({ member: note.member, loaded: note.loaded, note: note.note });
      describe(form, noteLine('note-kept', notSavedYet));
      var input = mount(cell, form, restorer(cell));
      if (i === 0) { focusBox(input); }
    });
  }

  function save(cell, form, restore) {
    var input = form.querySelector('[data-note-input]');
    // A save of the note as it stands posts like any other, so the server
    // refuses it as unchanged, or as stale when the note changed under a
    // kept form, and the refusal shows in the form.
    var id = memberOf(cell);
    form.querySelector('[type="submit"]').disabled = true;
    fetch(form.action, { method: 'POST', body: new URLSearchParams(new FormData(form)), credentials: 'same-origin' })
      .then(answerOf)
      .then(function (answer) {
        // The panel sends a save from a panel session that has ended to
        // sign in. The sign-in page's form comes back to no page when the
        // session ended before the save, which then stored nothing. When the
        // session ended after the save went through, on the Foxhole page
        // the save redirects to, the form comes back to that page.
        var signin = answer.doc.querySelector('main form[action="' + startAddress + '"]');
        if (signin) {
          if (signin.querySelector('[name="next"]')) {
            savedEnded(cell, restore, input.value.trim());
          } else {
            turnedAway(cell, form, restore);
          }
          return;
        }
        // A save that went through redirects to the Foxhole page. When
        // that page fails to load, the answer holds none of what follows,
        // and the note shows as saved.
        if (!answer.ok && !answer.redirected) {
          var refused = answer.doc.querySelector('main [data-field="note-form"]');
          if (refused) {
            // The refused form loads the note as it stands now, which may
            // be another manager's. Cancel shows that note, not the one the
            // row loaded.
            var current = refused.querySelector('[name="loaded"]').value;
            focusBox(mount(cell, document.adoptNode(refused), function () {
              restore();
              show(cell, current);
            }));
          } else {
            failed(cell, form, restore, sentenceOf(answer.doc));
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
      .catch(function () { failed(cell, form, restore, null); });
  }

  document.addEventListener('click', function (e) {
    var link = e.target.closest('[data-field="edit_note"]');
    var cell = link && link.closest('[data-note-cell]');
    if (!cell) { return; }
    e.preventDefault();
    open(cell);
  });

  bringBack();
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

/* Carrying the Foxhole selection, issue #701 under ADR 0013. The selection
   lasts through a search, a filter link, a visit to the no-role view and
   back, and a Reload. Each control marked data-keeps-selection opens the
   page again in a view. When one is followed, the script gives its address
   a member value for each member selected, and the page that opens ticks
   them, with those the search or the filter hides in a group at the top of
   the holder list. In the holder list the selected members are the boxes
   ticked. A page with no selection bar, as the no-role view, has no boxes,
   so they are the members its own address names. The server renders each
   control naming no member, so without script the selection drops, and a
   box a manager unticked never comes back ticked. */
(function () {
  'use strict';

  function selection() {
    if (document.querySelector('[data-field="selection"]')) {
      return Array.from(document.querySelectorAll('[data-select]:checked')).map(function (box) { return box.value; });
    }
    return new URLSearchParams(location.search).getAll('member');
  }

  // carry is the address with the selection in place of the members it
  // names. It keeps the address's fragment, which names the list.
  function carry(address) {
    var url = new URL(address, location.href);
    url.searchParams.delete('member');
    selection().forEach(function (id) { url.searchParams.append('member', id); });
    return url.href;
  }

  // A link takes the selection as it is pressed, so a link opened in a new
  // tab carries it too.
  function press(e) {
    var link = e.target.closest('a[data-keeps-selection]');
    if (link) { link.href = carry(link.href); }
  }
  document.addEventListener('click', press);
  document.addEventListener('auxclick', press);

  // The search sends its form's fields as the address, and the script adds
  // one for each member selected. It first drops any it added on an earlier
  // submit, since Back can bring the page back with them.
  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (!form.matches('form[data-keeps-selection]')) { return; }
    form.querySelectorAll('[data-carried]').forEach(function (field) { field.remove(); });
    selection().forEach(function (id) {
      var field = document.createElement('input');
      field.type = 'hidden';
      field.name = 'member';
      field.value = id;
      field.setAttribute('data-carried', '');
      form.appendChild(field);
    });
  });
})();

/* The member list notice on the Foxhole page (spec #434, ADR 0013). While
   the member list is partial a notice stands in the holder list's place,
   with a Reload link to the page in the view it shows, or, on a page that
   answers a refused selection bar action, to the selection. The script
   loads that address in the background, where the page waits for the list
   on its way, and puts each answer's notice in place of this one, so the
   count and the retry move on. The Reload link keeps this page's address,
   since the link in each answer names that answer's view, which brings no
   selection back. An answer with no notice means the list has arrived:
   the page reloads, unless a text box on it holds text, which a reload
   would lose. The notice then says the list has arrived and links the
   reload. Without script the Reload link does the same by hand. */
(function () {
  'use strict';

  var notice = document.querySelector('[data-notice="member-list"]');
  if (!notice) { return; }
  // Loads start at least spacing apart, and a load starts at least a
  // second after the one before answered. A load the server held while the
  // list was on its way is followed almost at once. One the server answered
  // at once, for a list that can't arrive within its wait, is followed
  // spacing later, so an open tab through an outage loads the page a few
  // times a minute.
  var spacing = 10000;
  var started = 0;
  // address is the address this page's Reload link names.
  var address = notice.querySelector('[data-field="reload"]').getAttribute('href');

  function holdsText() {
    return Array.from(document.querySelectorAll('input[type="text"], input[type="search"], input:not([type]), textarea'))
      .some(function (box) { return box.value.trim() !== ''; });
  }

  function arrived() {
    if (!holdsText()) {
      location.replace(address);
      return;
    }
    var link = document.createElement('a');
    link.href = address;
    link.setAttribute('data-field', 'reload');
    link.textContent = 'Reload the page.';
    notice.setAttribute('data-list-status', 'arrived');
    notice.replaceChildren('The member list has arrived. ', link);
  }

  // refresh puts the answer's notice in this one. The notice is a live
  // region, so it keeps its element and changes its contents only, and
  // only when they changed, so a screen reader reads out news alone.
  function refresh(fresh) {
    if (fresh.textContent === notice.textContent) { return; }
    notice.setAttribute('data-list-status', fresh.getAttribute('data-list-status'));
    notice.replaceChildren.apply(notice, Array.from(fresh.childNodes).map(function (n) {
      return document.importNode(n, true);
    }));
    notice.querySelector('[data-field="reload"]').setAttribute('href', address);
  }

  function load() {
    started = Date.now();
    fetch(address, { credentials: 'same-origin' })
      .then(function (res) {
        return res.text().then(function (text) {
          return { ok: res.ok, doc: new DOMParser().parseFromString(text, 'text/html') };
        });
      })
      .then(function (answer) {
        // A sign-in page or an error page says nothing about the list:
        // try again later.
        var page = answer.ok && answer.doc.querySelector('main[data-page="foxhole"]');
        if (!page) {
          later();
          return;
        }
        var fresh = page.querySelector('[data-notice="member-list"]');
        if (!fresh) {
          arrived();
          return;
        }
        refresh(fresh);
        later();
      })
      .catch(later);
  }

  function later() {
    setTimeout(load, Math.max(1000, started + spacing - Date.now()));
  }

  later();
})();

/* The Foxhole page's progress block (spec #434, ADR 0013). While a Foxhole
   action runs, the block shows how far it had got when the page loaded, and
   its Reload link loads the page again in the view it shows. The script
   hides the link's sentence and loads that address in the background every
   few seconds instead. Each answer's count, time left, pause line and Stop
   go into this block in place, so a keyboard user on Stop stays there until
   someone presses it, and then moves to the line saying it's stopping. An
   answer showing the report means the action has ended: the report takes
   the block's place, with a link to reload the page, and the change log
   takes the answer's, which holds the ended report too. A status line only
   a screen reader reads says how it ended, since the report itself isn't a
   live region. The holder list, any note being typed and any selection
   stay as the page loaded them. */
(function () {
  'use strict';

  var progress = document.querySelector('[data-field="progress"]');
  if (!progress) { return; }
  // every is the wait from one answer to the next load. An answer that
  // isn't the Foxhole page, a sign-in page or an error page, waits backOff
  // instead, so an open tab through a forum outage loads it a few times a
  // minute.
  var every = 3000;
  var backOff = 10000;
  var address = progress.querySelector('[data-field="reload"]').getAttribute('href');
  // said is a live region from the start, so a screen reader reads out
  // what goes in it at the end.
  var said = document.createElement('div');
  said.setAttribute('role', 'status');
  said.className = 'visually-hidden';
  progress.before(said);

  // hideHint hides the block's no-script sentence, which says to reload.
  function hideHint(block) {
    block.querySelector('[data-field="reload-hint"]').hidden = true;
  }

  // update brings this block up to the answer's, for the same action. The
  // pause line comes and goes, and Stop gives way to the stopping line once
  // someone presses it.
  function update(fresh) {
    ['done', 'total', 'left'].forEach(function (name) {
      var mine = progress.querySelector('[data-field="' + name + '"]');
      var theirs = fresh.querySelector('[data-field="' + name + '"]');
      if (mine.textContent !== theirs.textContent) { mine.textContent = theirs.textContent; }
    });
    progress.querySelector('[data-field="left"]').setAttribute('data-seconds',
      fresh.querySelector('[data-field="left"]').getAttribute('data-seconds'));
    var paused = progress.querySelector('[data-field="paused"]');
    var pausedNow = fresh.querySelector('[data-field="paused"]');
    if (pausedNow && !paused) {
      progress.querySelector('[data-field="how-far"]').after(document.importNode(pausedNow, true));
    } else if (paused && !pausedNow) {
      paused.remove();
    }
    var stop = progress.querySelector('[data-field="stop"]');
    var stopping = fresh.querySelector('[data-field="stopping"]');
    if (stop && stopping) {
      var focused = stop.contains(document.activeElement);
      stopping = document.importNode(stopping, true);
      stopping.tabIndex = -1;
      stop.replaceWith(stopping);
      if (focused) { stopping.focus(); }
    }
  }

  // ended puts the answer's report in this block's place, with a link to
  // reload the page, and the answer's change log in this page's. Focus in
  // the block moves to the link.
  function ended(page, report) {
    var focused = progress.contains(document.activeElement);
    var line = document.createElement('div');
    var link = document.createElement('a');
    line.className = 'block-row';
    line.setAttribute('data-field', 'reload-changes');
    link.href = address;
    link.setAttribute('data-keeps-selection', '');
    link.textContent = 'Reload the page to see the changes';
    line.appendChild(link);
    report = document.adoptNode(report);
    var heading = report.querySelector('h2');
    heading.after(line);
    progress.replaceWith(report);
    var changes = page.querySelector('[data-field="changes"]');
    var mine = document.querySelector('[data-field="changes"]');
    if (changes && mine) { mine.replaceWith(document.adoptNode(changes)); }
    said.textContent = heading.textContent + '. ' + link.textContent + '.';
    if (focused) { link.focus(); }
  }

  function load() {
    fetch(address, { credentials: 'same-origin' })
      .then(function (res) {
        return res.text().then(function (text) {
          return { ok: res.ok, doc: new DOMParser().parseFromString(text, 'text/html') };
        });
      })
      .then(function (answer) {
        var page = answer.ok && answer.doc.querySelector('main[data-page="foxhole"]');
        var report = page && page.querySelector('[data-field="report"]');
        var fresh = page && page.querySelector('[data-field="progress"]');
        if (report) {
          ended(page, report);
          return;
        }
        if (!fresh) {
          setTimeout(load, backOff);
          return;
        }
        if (fresh.getAttribute('data-report') === progress.getAttribute('data-report')) {
          update(fresh);
        } else {
          // Another action started after this one ended, between two loads.
          fresh = document.adoptNode(fresh);
          hideHint(fresh);
          progress.replaceWith(fresh);
          progress = fresh;
        }
        setTimeout(load, every);
      })
      .catch(function () { setTimeout(load, backOff); });
  }

  hideHint(progress);
  setTimeout(load, every);
})();

/* Landing on a block (ADR 0013). A control that opens a block below the top
   of the page, as each Foxhole preview's opener, each way to a Foxhole list,
   a Foxhole row's note link and a hub's Edit do, names the block's heading
   in its address's fragment. So does each Hubs form's action, and the
   Foxhole selection bar's and note form's, since a refused save answers at
   that address. The heading is marked tabindex="-1", so the browser
   scrolls to it and focuses it, with or without script, and a keyboard or
   screen reader user starts at the block rather than at the top of the
   page. The script focuses it too, on load and when the fragment changes,
   for a browser that only scrolls. Cancel on a Retry pressed in a Foxhole
   change log entry names that entry's summary line instead, which takes
   focus with no mark. */
(function () {
  'use strict';

  function land() {
    var target = location.hash.length > 1 && document.getElementById(location.hash.slice(1));
    if (target && target.matches('[tabindex="-1"], summary')) { target.focus({ preventScroll: true }); }
  }

  land();
  window.addEventListener('hashchange', land);
})();
