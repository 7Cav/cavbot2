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
   list of the guild's roles. A picker sends a change event each time its
   tags change, and takes a panel:select event carrying the IDs it is to
   hold, as the unsaved-edit block brings an edit back (issue #775). An ID
   with no row, as an unavailable role has none, comes back only while a tag
   still holds it. */
(function () {
  'use strict';

  var roots = document.querySelectorAll('[data-picker]');
  if (!roots.length) { return; }

  var pickers = 0;

  // A press is a mousedown and the mouseup that ends it, or the dragend
  // when the press drags something. A contextmenu ends a press too. A
  // browser that opens its menu at the mousedown, as Chrome on Linux does,
  // lets the menu take the mouseup (#718). pressed is the element the
  // press began on, and null between presses. rightPress is whether the
  // right button made the press, and false between presses. afterPress,
  // when set, runs as the press ends and is called with that element, and
  // with whether a contextmenu ended it. That comes before the click, whose
  // target the press has already fixed. A right-click makes no click, so
  // afterPress waits for the next press to end instead. On macOS a
  // Control+click opens the menu too, but Chrome and Safari report its
  // mousedown as the left button. Only the contextmenu shows that the
  // press was a right-click (#771).
  var pressed = null;
  var rightPress = false;
  var afterPress = null;

  function endPress(e) {
    var target = pressed;
    var run = afterPress;
    if (!target) { return; }
    pressed = null;
    if (rightPress) {
      rightPress = false;
      return;
    }
    afterPress = null;
    if (run) { run(target, e.type === 'contextmenu'); }
  }

  document.addEventListener('mousedown', function (e) {
    pressed = e.target;
    rightPress = e.button === 2;
  }, true);
  document.addEventListener('mouseup', endPress, true);
  document.addEventListener('dragend', endPress, true);
  document.addEventListener('contextmenu', endPress, true);

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

    // changed tells whoever listens, as the unsaved-edit block does, that
    // the tags changed.
    function changed() {
      root.dispatchEvent(new Event('change', { bubbles: true }));
    }

    // tagFor makes a tag for the row from the picker's template and adds
    // it. A single picker drops its earlier tag first. The label goes in
    // each [data-label]: the one the tag shows, and the one in its remove
    // control's name, which the template already ends with the set the
    // picker edits.
    function tagFor(row) {
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
    }

    // choose makes a tag for the row and hides the row.
    function choose(row) {
      tagFor(row);
      changed();
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

    // The list's tabindex keeps it out of the Tab order, but it also lets a
    // press on the list's scrollbar focus it. There typing filters nothing
    // and the arrow keys only scroll, so focus goes back to the search box.
    // Only focus from a press goes back. If the list were ever a Tab stop
    // again, sending Tab's focus back would trap Tab in the search box. A
    // press on a row focuses the row, not the list, so the row's click
    // still chooses it (#717).
    list.addEventListener('focus', function () {
      if (pressed) { input.focus(); }
    });

    tags.addEventListener('click', function (e) {
      var remove = e.target.closest('[data-remove]');
      if (!remove) { return; }
      remove.closest('[data-tag]').remove();
      changed();
      refresh();
      close();
      add.focus();
    });

    // A panel:select makes the tags the IDs it carries, without moving
    // focus.
    root.addEventListener('panel:select', function (e) {
      var ids = e.detail;
      tags.querySelectorAll('[data-tag]').forEach(function (tag) {
        if (ids.indexOf(tag.getAttribute('data-tag')) === -1) { tag.remove(); }
      });
      var held = selectedIDs();
      ids.forEach(function (id) {
        var row = rows.filter(function (r) { return r.getAttribute('data-option') === id; })[0];
        if (!held[id] && row) { tagFor(row); }
      });
      refresh();
    });

    // Focus leaving the picker closes the search. A click on a row moves
    // focus to the row, inside the picker, so the click still lands. A
    // press elsewhere moves focus on its mousedown. Closing then would
    // move what sits below the list up before the mouseup, and the press
    // would make no click (#682). So a press closes the search once it
    // ends, unless closing would move what the press was on. Then the
    // search stays open, so a second press there lands too. A right-click
    // makes no click, so nothing has to stay under the pointer. It closes
    // the search at once, unless it began inside the picker, as on a tag.
    // Then the search stays open through the menu. The next left press
    // closes it as any left press would (#718), and a right-click outside
    // the picker closes it at once (#804, below). A Control+click on macOS
    // goes by the same rule. Its mousedown reports the left button, so the
    // rule waits for its contextmenu (#771).
    root.addEventListener('focusout', function (e) {
      if (search.hidden || root.contains(e.relatedTarget)) { return; }
      if (!pressed || (rightPress && !root.contains(pressed))) {
        close();
        return;
      }
      afterPress = function closeAfter(target, byMenu) {
        if (byMenu) {
          if (root.contains(target)) {
            afterPress = closeAfter;
          } else {
            close();
          }
          return;
        }
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

    // A right-click outside the picker closes the search at once, whatever
    // press came before it. Focus may have left the picker already. A
    // right-click on a tag moves it out, and so does a left press that kept
    // the search open, and then no focusout comes. The contextmenu comes as
    // the menu opens. That is after the mousedown on Linux and macOS and
    // after the mouseup on Windows, and a Control+click on macOS sends one
    // too (#804). A close the picker left waiting stays waiting. Run once
    // the search is closed, it moves nothing and closes the search again.
    document.addEventListener('contextmenu', function (e) {
      if (!root.contains(e.target)) { close(); }
    }, true);
  }

  roots.forEach(setUp);
})();

/* A reload never sends a form again, ADR 0019 (issues #770 and #775). A
   page the panel renders in answer to a form names on <main> the address
   that shows it as it stands. The script puts that address in the tab in
   place of the form's history entry as the page loads. It adds no entry,
   so one Back still leaves the page. A reload, a Back or a Forward that
   loads the page again then loads that address by GET: it sends nothing
   and shows no refusal note. The forum's 502 names the landing, and the
   unsaved-edit block puts the page the form was sent from in its place
   (issue #803). Without script the tab keeps the form's address, and a
   reload sends the form again after the browser asks. */
(function () {
  'use strict';

  var named = document.querySelector('main[data-address]');
  if (named) {
    history.replaceState(history.state, '', named.getAttribute('data-address'));
  }
})();

/* Unsaved edits, issue #775 under ADR 0019 (GLOSSARY.md, Unsaved edit).
   What a panel user typed into a form and has not saved comes back marked
   not saved yet when the page loads again. Each form whose edit is kept
   carries data-edit, naming the edit, and data-posted when the server
   rendered it from what a save posted, or what an address carries, rather
   than from the store.

   The tab's session storage keeps each edit under the forum user signed
   in, from the moment its form is sent. On a page that shows a form posted
   back, as a refused save's does, the edit is what the form shows. As the
   user types there, or in a form that came back, the edit follows. It
   keeps the version and the loaded values the page carried, so the panel
   still refuses a save of it over a newer save as stale.

   A reload, a Back or a Forward brings each edit back into its form on the
   page. So does the page a failed save's Try again opens, the page a
   sign-in returns to when a save, a press or a reload met that sign-in, and
   the page the Foxhole member list notice reloads. Once the forum answers again
   after its 502 met a form, that page's reload and its Try again open the
   page the form was sent from, which brings the edit back too (issue
   #803). The form shows a line saying the edit is not saved yet. Its link
   opens the form as stored, from the form's data-stored address or else
   the page again, and drops the edit. Any other load of a page that shows
   the form as stored drops the edit, a save's own included. So does a page
   a save redirects to that doesn't show its form. Closing the tab drops
   every edit. Another forum user signed in on the tab never sees one.
   Without session storage, or without script, the page goes on and the
   edit is lost. */
var unsavedEdits = (function () {
  'use strict';

  // storageKey names the entry in the tab's session storage that holds the
  // edits. It holds the forum user they belong to and each edit by name,
  // with the values its form posts, the names of its form's fields and the
  // address it was typed at. It also holds the edit whose form was sent
  // last, the address of the page any form was sent from, the press it
  // carried and that press once a sign-in shows the session turned it
  // away, each until the next signed-in page loads, and whether the next
  // page brings the edits back.
  var storageKey = 'panel-unsaved-edits';

  // signedInAs is the forum user the rail names, empty on a page with no
  // panel session, as the sign-in page is.
  var signedInAs = (function () {
    var name = document.querySelector('.rail [data-field="username"]');
    return name ? name.textContent : '';
  })();

  function read() {
    try {
      return JSON.parse(sessionStorage.getItem(storageKey)) || {};
    } catch (e) {
      return {};
    }
  }

  var state = read();
  if (!state.edits) { state.edits = {}; }
  if (signedInAs && state.user !== signedInAs) {
    state = { user: signedInAs, edits: {} };
  }

  function write() {
    try {
      sessionStorage.setItem(storageKey, JSON.stringify(state));
    } catch (e) {
      // With no session storage, there is nowhere to keep the edits.
    }
  }

  // here is the address the tab shows.
  function here() {
    return location.pathname + location.search + location.hash;
  }

  // shows says whether the tab shows the page at address, whatever block
  // it opens at.
  function shows(address) {
    var at = new URL(address, location.href);
    return at.pathname + at.search === location.pathname + location.search;
  }

  // navigation is how the tab came to load this page: navigate, reload or
  // back_forward.
  var navigation = (function () {
    var entries = window.performance && performance.getEntriesByType ? performance.getEntriesByType('navigation') : [];
    return entries.length ? entries[0].type : 'navigate';
  })();
  var again = navigation === 'reload' || navigation === 'back_forward';

  // restoring says whether this page brings the edits back.
  var restoring = signedInAs !== '' && (again || state.restore === true);

  // valuesOf is what the form posts now, in order, as name and value
  // pairs.
  function valuesOf(form) {
    var values = [];
    new FormData(form).forEach(function (value, name) { values.push([name, value]); });
    return values;
  }

  // namesOf is the name of every field the form has. A box left unticked
  // posts nothing, so its name alone says it was there.
  function namesOf(form) {
    var names = [];
    Array.from(form.elements).forEach(function (field) {
      if (field.name && names.indexOf(field.name) === -1) { names.push(field.name); }
    });
    return names;
  }

  // fill puts an edit back in the form's fields: each picker's tags, each
  // box's and button's check, and each other field's value. A field the
  // edit's form didn't have keeps what the page gave it.
  function fill(form, edit) {
    var given = {};
    var had = edit.names || [];
    edit.values.forEach(function (pair) { (given[pair[0]] = given[pair[0]] || []).push(pair[1]); });
    form.querySelectorAll('[data-picker]').forEach(function (picker) {
      picker.dispatchEvent(new CustomEvent('panel:select', { detail: given[picker.getAttribute('data-field')] || [] }));
    });
    var used = {};
    Array.from(form.elements).forEach(function (field) {
      if (!field.name || field.closest('[data-picker]')) { return; }
      if (field.type === 'checkbox' || field.type === 'radio') {
        if (had.indexOf(field.name) !== -1) { field.checked = (given[field.name] || []).indexOf(field.value) !== -1; }
        return;
      }
      if (!(field.name in given)) { return; }
      var i = used[field.name] || 0;
      used[field.name] = i + 1;
      if (i < given[field.name].length) { field.value = given[field.name][i]; }
    });
  }

  // keep puts what the form posts now in the tab as the edit named, typed
  // at address, the address the tab shows when none is given.
  function keep(name, form, address) {
    state.edits[name] = { values: valuesOf(form), names: namesOf(form), address: address || here() };
    write();
  }

  function drop(name) {
    delete state.edits[name];
    write();
  }

  // live is each form whose edit follows what the user types, by the name
  // of its edit.
  var live = {};

  function follow(name, form) {
    live[name] = form;
  }

  document.addEventListener('input', followTyping);
  document.addEventListener('change', followTyping);
  function followTyping(e) {
    var form = e.target.closest && e.target.closest('form[data-edit]');
    var name = form && form.getAttribute('data-edit');
    if (name && live[name] === form) { keep(name, form); }
  }

  // sentFrom is the address of the page a form is sent from, the path and
  // query the tab shows. It opens at the block the form's action names,
  // else at the heading of the block the form sits in when that heading
  // has an ID, else where the tab is.
  function sentFrom(form) {
    var action = new URL(form.getAttribute('action') || '', location.href);
    return location.pathname + location.search + (action.hash || headingFragment(form) || location.hash);
  }

  // Every form sent the page's way from a signed-in page notes the page it
  // was sent from, and the press it carries, the data-press of the button
  // pressed, or none. A form whose edit is kept keeps it from that moment.
  // One the script sends itself, as a Foxhole row's note form, cancels the
  // submit. A sign-in form, on a page with no panel session, notes nothing,
  // so a sign-in that takes a second try keeps both.
  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (e.defaultPrevented || !signedInAs) { return; }
    state.from = sentFrom(form);
    var press = e.submitter && e.submitter.getAttribute('data-press');
    if (press) {
      state.press = press;
    } else {
      delete state.press;
    }
    if (form.matches('form[data-edit]')) {
      var name = form.getAttribute('data-edit');
      keep(name, form);
      state.sent = name;
    }
    write();
  });

  // restoreNext makes the next page the tab loads bring the edits back.
  function restoreNext() {
    state.restore = true;
    write();
  }

  // loadAgain makes the press e of the link load its address when the tab
  // shows that page already, where the link would only scroll. A press
  // with a modifier key, as one that opens a new tab, goes the browser's
  // way.
  function loadAgain(e, link) {
    if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey || !shows(link.href)) { return; }
    e.preventDefault();
    history.replaceState(history.state, '', link.href);
    location.reload();
  }

  // Try again on a failed save's error page brings the edits back. One that
  // opens the page the tab shows loads it again.
  document.addEventListener('click', function (e) {
    var retry = e.target.closest('a[data-retry]');
    if (!retry) { return; }
    restoreNext();
    loadAgain(e, retry);
  });

  // headingOf is the heading of the block the form sits in.
  function headingOf(form) {
    var block = form.querySelector('h2') ? form : form.closest('section, .block');
    return block && block.querySelector('h2');
  }

  // headingFragment is the fragment that opens a page at the heading of
  // the block the form sits in, empty when that heading has no ID.
  function headingFragment(form) {
    var heading = headingOf(form);
    return heading && heading.id ? '#' + heading.id : '';
  }

  // describedFirst names the line first in the description of the
  // heading, so a screen reader reads it out as focus lands there.
  function describedFirst(heading, line) {
    var described = heading.getAttribute('aria-describedby');
    heading.setAttribute('aria-describedby', described ? line.id + ' ' + described : line.id);
  }

  // storedAddress is the address that opens the form as stored: the
  // form's data-stored, or this page, opened at the form's heading. A
  // reload of this page shows a form as stored once its edit is dropped.
  function storedAddress(form) {
    if (form.hasAttribute('data-stored')) { return form.getAttribute('data-stored'); }
    return location.pathname + location.search + headingFragment(form);
  }

  // storedLink is the link, labelled label, that opens the form of the
  // edit named as stored, at address, and drops the edit. The tab may show
  // that very address, where a link would only scroll, so the link loads
  // it again.
  function storedLink(name, address, label) {
    var link = document.createElement('a');
    link.href = address;
    link.setAttribute('data-field', 'stored');
    link.textContent = label;
    link.addEventListener('click', function (e) {
      drop(name);
      delete live[name];
      loadAgain(e, link);
    });
    return link;
  }

  // marked puts the line saying the edit is not saved yet at the top of
  // the form, where a refused save's note sits, and names it first in the
  // description of the block's heading. A screen reader reads it out as
  // focus lands on the heading. text is the line's sentence, which ends
  // with its link to the form as stored, labelled stored.
  function marked(form, name, text, stored) {
    var line = document.createElement('div');
    line.id = 'unsaved-' + name;
    line.className = 'note note--neutral';
    line.setAttribute('data-field', 'unsaved');
    line.append(text + ' ', storedLink(name, storedAddress(form), stored), '.');
    var heading = headingOf(form);
    if (heading && form.contains(heading)) {
      var row = document.createElement('div');
      row.className = 'block-row';
      row.appendChild(line);
      heading.after(row);
    } else {
      form.prepend(line);
    }
    if (heading) { describedFirst(heading, line); }
    return line;
  }

  // notSent is the data-press of the button pressed once the session had
  // ended, only on the first signed-in load after that press's sign-in
  // page, and null once its line is placed.
  var notSent = null;

  // markNotSent puts the line saying the press wasn't sent at the top of
  // the pressed button's row, when root holds the button, and names it
  // first in the description of the block's heading. The line names the
  // button by its label, since Approve and Clear approval share one form.
  function markNotSent(root) {
    var button = notSent && root.querySelector('[data-press="' + CSS.escape(notSent) + '"]');
    if (!button) { return; }
    notSent = null;
    var line = document.createElement('div');
    line.id = 'not-sent';
    line.className = 'note note--neutral';
    line.setAttribute('data-field', 'not-sent');
    line.textContent = 'Your session ended, so ' + button.textContent.replace(/\s+/g, ' ').trim() + ' was not sent. Press it again.';
    button.closest('.submit-row, form').prepend(line);
    var heading = headingOf(button.form);
    if (heading) { describedFirst(heading, line); }
  }

  // placed is each form on the page whose edit is kept, by the name of its
  // edit, and restored those of them that brought an edit back.
  var placed = {};
  var restored = [];

  if (signedInAs) {
    var answers = document.querySelector('main[data-address]') !== null;
    var sent = state.sent;
    var from = state.from;
    // A press the session turned away gets its line on the page it was sent
    // from, when the next signed-in page to load is that page.
    if (state.unsent && from && shows(from)) { notSent = state.unsent; }
    delete state.sent;
    delete state.from;
    delete state.press;
    delete state.unsent;
    delete state.restore;
    // The forum's 502 to a posted form names the landing, since the panel
    // answers it before learning which page the form was sent from. That page goes
    // in the tab in its place. On any of the forum's 502s, Try again opens
    // the page the tab shows, at its block, so the first reload or Try
    // again once the forum answers shows the form again.
    var outage = document.querySelector('main[data-failure="forum"]');
    var retry = outage && outage.querySelector('a[data-retry]');
    if (outage && answers && from) { history.replaceState(history.state, '', from); }
    if (retry) { retry.href = here(); }
    document.querySelectorAll('form[data-edit]').forEach(function (form) {
      var name = form.getAttribute('data-edit');
      if (placed[name]) { return; }
      placed[name] = form;
      var edit = state.edits[name];
      if (restoring && edit) {
        fill(form, edit);
        restored.push(form);
        keep(name, form);
        follow(name, form);
      } else if (form.hasAttribute('data-posted')) {
        keep(name, form);
        follow(name, form);
      } else {
        delete state.edits[name];
      }
    });
    // A save that went through redirects to a page that answers no form.
    // When that page doesn't show the form, the save's edit goes too.
    if (sent && !placed[sent] && !answers && !restoring) { delete state.edits[sent]; }
    write();
  }

  // The sign-in page a save, a press or a reload met. The panel sends a
  // form's visitor to sign in with no page to come back to. When the form
  // kept an edit, or its button was a press marked data-press, the sign-in
  // form names the page the form was sent from instead. The page the
  // sign-in comes back to brings the edits back. A sign-in the panel
  // names a page for is a page load's, so a press noted before it went
  // through, as a Re-add whose answer met the ended session does. Only a
  // sign-in with no page named holds a press the session turned away, and
  // a sign-in that takes a second try keeps it.
  var signin = document.querySelector('main form[action="/auth/start"]');
  if (!signedInAs && signin) {
    var named = signin.querySelector('input[name="next"]') !== null;
    if (state.press && !named) { state.unsent = state.press; }
    delete state.press;
    var sentBack = Boolean(state.from && (state.unsent || (state.sent && state.edits[state.sent])));
    if (sentBack && !named) {
      var next = document.createElement('input');
      next.type = 'hidden';
      next.name = 'next';
      next.value = state.from;
      signin.appendChild(next);
    }
    if (sentBack || again) {
      signin.addEventListener('submit', function () {
        delete state.sent;
        restoreNext();
      });
    }
    write();
  }

  return {
    restoring: restoring,
    // edits is each edit the tab keeps for the forum user signed in, by
    // name.
    edits: function () { return state.edits; },
    keep: keep,
    drop: drop,
    follow: follow,
    shows: shows,
    marked: marked,
    storedLink: storedLink,
    storedAddress: storedAddress,
    restoreNext: restoreNext,
    markNotSent: markNotSent,
    // placed reports whether a form on the page took the edit named as
    // the page loaded.
    placed: function (name) { return Boolean(placed[name]); },
    // restored reports whether the form brought an edit back as the page
    // loaded.
    restored: function (form) { return restored.indexOf(form) !== -1; }
  };
})();

/* The line on a form that brought an edit back, issue #775 under ADR 0019.
   Two Foxhole forms are left to their own blocks below: a note form's line
   names the note, and the paste box takes none, since its preview comes
   back in its place. */
(function () {
  'use strict';

  document.querySelectorAll('form[data-edit]').forEach(function (form) {
    if (!unsavedEdits.restored(form) || form.matches('[data-field="add-form"], [data-field="note-form"]')) { return; }
    unsavedEdits.marked(form, form.getAttribute('data-edit'), 'These changes are not saved yet. Press Save to keep them, or', 'show what is saved');
  });
})();

/* The line on a press that met an ended session, issue #802 under ADR 0013.
   A button marked data-press makes its change in one press, as a Confirm
   does. A press of one sent once the session had ended comes back from its
   sign-in to the page it was pressed on, and the line at the button says
   the press wasn't sent. Any other load of the page shows no line. The add
   preview loads in the background, and places its own line below. Without
   script the press's sign-in lands on the landing, with no line. */
(function () {
  'use strict';

  unsavedEdits.markNotSent(document);
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
   button. The button keeps the notes typed in the page's rows as unsaved
   edits (issue #775 under ADR 0019), and the Foxhole page the sign-in
   comes back to opens them again, marked not saved yet. Without script the
   save shows the sign-in page, and the typed note is lost.

   The note form above the list, the one the Edit link opens without
   script, posts the page's way, and its unsaved edit is kept as any form's
   is. A reload of a refused save there, or a Back that loads the page
   again, opens the form with the text, marked not saved yet. Saving it
   over a note someone changed meanwhile is refused as stale. */
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
  var notSavedYet = 'This note is not saved yet. Press Save to save it, or';

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

  // editOf is the name a member's note is kept under as an unsaved edit.
  function editOf(member) {
    return 'note-' + member;
  }

  // keep keeps each note form open in a row whose text differs from the
  // note it loaded as an unsaved edit typed at this view's address, where
  // the sign-in comes back to. The form of the member named first goes
  // before the rest, and any edit kept already, as one whose block didn't
  // load, before them all.
  function keep(first) {
    var forms = Array.from(document.querySelectorAll('[data-note-cell] [data-field="note-form"]')).filter(function (form) {
      var values = posted(form);
      return values.note.trim() !== values.loaded;
    });
    forms.sort(function (a, b) { return (posted(b).member === first) - (posted(a).member === first); });
    forms.forEach(function (form) {
      var name = editOf(posted(form).member);
      unsavedEdits.drop(name);
      unsavedEdits.keep(name, form, viewAddress());
    });
  }

  // signInAgain keeps the typed notes, then starts a sign-in that comes
  // back to this view, which opens them again.
  function signInAgain(first) {
    keep(first);
    unsavedEdits.restoreNext();
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

  // unopened says above the list that a note's block didn't load, once
  // however many didn't. The tab keeps the note. Its Reload the page
  // button keeps the notes typed in the rows too, so the next load opens
  // them all. The line is an alert, so a screen reader reads it out as it
  // appears.
  function unopened() {
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

  // keptLine is the line at the top of a note form that brought a note
  // back, saying it is not saved yet. It ends with a link that opens the
  // member's note form as stored, at stored, and drops the note.
  function keptLine(name, stored) {
    var el = noteLine('note-kept', notSavedYet);
    el.setAttribute('data-field', 'unsaved');
    el.append(' ', unsavedEdits.storedLink(name, stored, 'show the saved note'), '.');
    return el;
  }

  // openAboveList opens a kept note in a "Note on ..." block above the
  // list, where the Edit link opens a note form without script, for a
  // member this view doesn't list. The block comes from this view's page
  // with the member's note form open, loaded in the background. Its Save
  // posts the page's way, without script.
  function openAboveList(name, note, first) {
    var url = new URL(viewAddress(), location.href);
    url.searchParams.set('note', note.member);
    url.hash = 'note';
    fetch(url, { credentials: 'same-origin' })
      .then(answerOf)
      .then(function (answer) {
        var page = answer.ok && answer.doc.querySelector('main[data-page="foxhole"]');
        var block = page && page.querySelector('[data-field="note-block"]');
        if (!block) {
          unopened();
          return;
        }
        block = document.adoptNode(block);
        var form = block.querySelector('[data-field="note-form"]');
        form.querySelector('[name="loaded"]').value = note.loaded;
        form.querySelector('[name="note"]').value = note.note;
        describe(form, keptLine(name, url.href));
        ownIDs(block, note.member);
        aboveList(block);
        unsavedEdits.follow(name, form);
        if (first) { focusBox(form.querySelector('[data-note-input]')); }
      })
      .catch(unopened);
  }

  // noteOf reads a note brought back off the values its form posted.
  function noteOf(values) {
    var note = {};
    values.forEach(function (pair) { note[pair[0]] = pair[1]; });
    return note;
  }

  // bringBack opens the notes kept through a sign-in on the view the
  // sign-in comes back to, or on a reload of it, each with its typed text
  // and the note its form loaded, so the server refuses as stale a save
  // over a note someone changed meanwhile. A note whose form is the one
  // above the list came back into it as the page loaded, and takes its line
  // here. Each other opens in its row, or in a block above the list for a
  // member this view doesn't list. The first in a row takes focus. Cancel
  // in a row drops the note, and a save drops it once it goes through.
  function bringBack() {
    var block = document.querySelector('main [data-field="note-block"] [data-field="note-form"]');
    if (block && unsavedEdits.restored(block)) {
      var said = keptLine(block.getAttribute('data-edit'), unsavedEdits.storedAddress(block));
      describe(block, said);
      block.closest('[data-field="note-block"]').querySelector('h2').setAttribute('aria-describedby', said.id);
    }
    if (!unsavedEdits.restoring) { return; }
    var edits = unsavedEdits.edits();
    var opened = 0;
    Object.keys(edits).forEach(function (name) {
      if (name.indexOf('note-') !== 0 || unsavedEdits.placed(name) || !unsavedEdits.shows(edits[name].address)) { return; }
      var note = noteOf(edits[name].values);
      var first = opened++ === 0;
      var cell = document.querySelector('[data-member="' + CSS.escape(note.member) + '"] [data-note-cell]');
      if (!cell) {
        openAboveList(name, note, first);
        return;
      }
      var form = fill({ member: note.member, loaded: note.loaded, note: note.note });
      form.setAttribute('data-edit', name);
      describe(form, keptLine(name, cell.querySelector('[data-field="edit_note"]').href));
      var restore = restorer(cell);
      var input = mount(cell, form, function () {
        unsavedEdits.drop(name);
        restore();
      });
      unsavedEdits.follow(name, form);
      if (first) { focusBox(input); }
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
        unsavedEdits.drop(editOf(id));
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

/* Carrying the Foxhole selection, issues #701, #723 and #770 under ADR 0013
   and ADR 0019. The selection lasts through a search, a filter link, a
   visit to the no-role view and back, a Reload link marked
   data-keeps-selection, and a browser reload of the Foxhole page. Each
   control marked data-keeps-selection opens the page again in a view. When
   one is followed, the script gives its address a member value for each
   member selected, and the page that opens ticks them, with those the
   search or the filter hides in a group at the top of the holder list. In
   the holder list the selected members are the boxes ticked. A page with no
   selection bar, as the no-role view, has no boxes, so they are the members
   its own address names. On the Foxhole page, at /foxhole, the tab's
   address names the boxes ticked now, so a reload, or a Back that loads the
   page again, ticks them as the manager left them. A page that answers a
   sent form, as a refused Approve does, has the address at /foxhole that
   shows it in the tab by now (ADR 0019), so the boxes changed there go in
   that address too, and a reload loads the page as it stands, with the
   boxes the manager left ticked and no refusal note. The server renders
   each control naming no member, so without script the selection drops,
   and a box a manager unticked never comes back ticked. */
(function () {
  'use strict';

  // selection is the IDs of the members selected now.
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

  // atViewAddress says whether the tab is at the path the search opens,
  // /foxhole, where the address names the view. A page that answers a sent
  // form has its address in the tab by now. One that named none would
  // still show the form's, which a GET finds no page at.
  function atViewAddress() {
    var search = document.querySelector('form[data-field="search"]');
    return search !== null && location.pathname === new URL(search.action).pathname;
  }

  // Each box change at /foxhole puts the selection in the tab's address.
  // It replaces the tab's history entry and adds none, so one Back still
  // leaves the page.
  document.addEventListener('change', function (e) {
    if (e.target.matches('[data-select]') && atViewAddress()) {
      history.replaceState(history.state, '', carry(location.href));
    }
  });

  // carryOnPress gives a marked link the selection as it is pressed, so a
  // link opened in a new tab carries it too.
  function carryOnPress(e) {
    var link = e.target.closest('a[data-keeps-selection]');
    if (link) { link.href = carry(link.href); }
  }
  document.addEventListener('click', carryOnPress);
  document.addEventListener('auxclick', carryOnPress);

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

/* Keeping the add preview's paste, issues #770 and #775 under ADR 0019. The
   paste box's Preview answers with the add preview, which names the address
   that shows it: /foxhole with the role picked, at the preview's heading,
   and no pasted line. The paste box has no line cap, and the proxy in front
   of the panel turns away an address over 8KB. So the paste is an unsaved
   edit the tab keeps, as every form's is. When a load of an add preview's
   address brings it back into the paste box, the script loads the preview
   again in the background through the address the Preview posts to. The
   preview then takes its place above the action blocks, and the latest
   report folds, as on the page the Preview answers with. A line that
   matches several members comes back unpicked, as the preview offers it.
   The add's Confirm keeps the paste as it is sent too, so a Confirm that
   goes through drops it. In another tab, or without script, the address
   opens the paste box empty with the role picked. */
(function () {
  'use strict';

  // atAddPreview says whether the tab shows an add preview's address. A
  // page that answers a sent form shows it by now.
  function atAddPreview() {
    return new URLSearchParams(location.search).has('add');
  }

  // show puts the answer's add preview where the page the Preview answers
  // with has it, above whatever runs, the latest report and the action
  // blocks, and the answer's latest report, folded, in place of this
  // page's. The paste box it was filled in goes back to how the page
  // rendered it, since the preview has its own, and the paste the tab
  // keeps follows what is typed in the preview's. Focus moves to the
  // preview's heading when the address names it.
  function show(doc, form) {
    var preview = doc.querySelector('main[data-page="foxhole"] [data-field="add-preview"]');
    var below = document.querySelector('main [data-field="command-running"], main [data-field="progress"], main [data-field="report"], main [data-field="note-block"], main [data-field="actions"], main [data-notice="member-list"], main [data-field="changes"]');
    if (!preview || !below) { return; }
    preview = document.adoptNode(preview);
    below.before(preview);
    var report = doc.querySelector('main [data-field="report"]');
    var mine = document.querySelector('main [data-field="report"]');
    if (report && mine) { mine.replaceWith(document.adoptNode(report)); }
    form.reset();
    var box = preview.querySelector('[data-field="add-form"]');
    if (box) { unsavedEdits.follow('add', box); }
    unsavedEdits.markNotSent(preview);
    if (location.hash === '#add-preview') { preview.querySelector('#add-preview').focus(); }
  }

  // preview loads the preview of the paste the paste box brought back, from
  // the address its Preview posts to. An answer with no preview, as a
  // sign-in page or a page that can't match lines while the member list is
  // partial, leaves the paste in the box to preview by hand.
  function preview(form) {
    fetch(form.getAttribute('action').split('#')[0], { method: 'POST', body: new URLSearchParams(new FormData(form)), credentials: 'same-origin' })
      .then(function (res) { return res.text(); })
      .then(function (text) { show(new DOMParser().parseFromString(text, 'text/html'), form); })
      .catch(function () {});
  }

  var form = document.querySelector('main [data-field="add-form"]');
  if (form && atAddPreview() && unsavedEdits.restored(form)) { preview(form); }
})();

/* The member list notice on the Foxhole page (spec #434, ADR 0013). While
   the member list is partial a notice stands in the holder list's place,
   with a Reload link to the page in the view it shows. A page that answers
   a refused selection bar action, or whose address names members, links the
   selection instead. The script loads that address in the background, where
   the page waits for the list on its way, and puts each answer's notice in
   place of this one, so the count and the retry move on. The Reload link
   keeps this page's address, whatever the link in each answer names. An
   answer with no notice means the list has arrived, and the page reloads,
   unless a text box on it holds text, which a reload would lose. The notice
   then says the list has arrived and links the reload. Either reload brings
   the page's unsaved edits back, as the add preview's paste. Without script
   the Reload link does the same by hand. */
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
      unsavedEdits.restoreNext();
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

  notice.addEventListener('click', function (e) {
    if (e.target.closest('[data-field="reload"]')) { unsavedEdits.restoreNext(); }
  });

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
   that address. So does the address a press's sign-in comes back to, the
   running action's Stop included. The heading is marked tabindex="-1", so
   the browser scrolls to it and focuses it, with or without script, and a
   keyboard or screen reader user starts at the block rather than at the
   top of the page. The script focuses it too, on load and when the
   fragment changes, for a browser that only scrolls. Cancel on a Retry
   pressed in a Foxhole change log entry names that entry's summary line
   instead, which takes focus with no mark. */
(function () {
  'use strict';

  function land() {
    var target = location.hash.length > 1 && document.getElementById(location.hash.slice(1));
    if (target && target.matches('[tabindex="-1"], summary')) { target.focus({ preventScroll: true }); }
  }

  land();
  window.addEventListener('hashchange', land);
})();

/* Signing in at a block, issue #757 under ADR 0013. A browser never sends
   an address's fragment to the panel. So when the panel sends a visitor to
   sign in, next names the page they asked for without the fragment that
   opens it at a block. The browser keeps the fragment on the sign-in
   page's own address. When the visitor sends a sign-in form, the script
   puts that fragment on next, so the sign-in comes back to the block. The
   landing's sign-in page has no next, and gets one. A save or a press sent
   with no session comes back from its sign-in to the page it was sent
   from, since the sign-in form names that page as next. Any other form
   sent with no session comes back to the landing, at the block its form's
   action names when the landing has one. Without script, the sign-in
   comes back to the top of the page. The Foxhole page's Sign in again for
   kept notes sends its form with submit(), which fires no submit event, so
   this script never sees it. That sign-in comes back to the view with no
   fragment, and a kept note can take focus. */
(function () {
  'use strict';

  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (!location.hash || !form.matches('form[action="/auth/start"]')) { return; }
    var next = form.querySelector('input[name="next"]');
    if (!next) {
      next = document.createElement('input');
      next.type = 'hidden';
      next.name = 'next';
      next.value = '/';
      form.appendChild(next);
    }
    next.value = next.value.split('#')[0] + location.hash;
  });
})();
