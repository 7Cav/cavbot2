---
name: cavbot2 panel
description: The 7Cav forum's dark look, carried into the cavbot2 panel.
colors:
  bg: "#121212"
  surface: "#171717"
  surface-2: "#1b1b1b"
  surface-3: "#212121"
  field: "#1c1c1c"
  line: "#2b2b2b"
  line-2: "#353535"
  line-hi: "#4d4d4d"
  line-strong: "#6b6b6b"
  text: "#a1a1a1"
  text-hi: "#f1f1f1"
  text-mute: "#8d8d8d"
  gold: "#ebc729"
  gold-hi: "#edcd40"
  gold-lo: "#e5bf15"
  gold-tint: "rgba(235, 199, 41, 0.1)"
  btn: "#2e2e2e"
  btn-hi: "#424242"
  btn-edge-d: "#212121"
  btn-edge-l: "#3b3b3b"
  btn-off: "#262626"
  btn-off-edge-d: "#1a1a1a"
  btn-off-edge-l: "#333333"
  danger-hi: "#c84448"
  danger-lo: "#b3393d"
  danger-text: "#f1c9cb"
  ok: "#509652"
  ok-text: "#cfe5cf"
  on-fill: "#ffffff"
  on-gold: "#000000"
typography:
  page-title:
    fontFamily: "Roboto, 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif"
    fontSize: "22px"
    fontWeight: 400
    lineHeight: 1.4
  block-title:
    fontFamily: "Roboto, 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif"
    fontSize: "18px"
    fontWeight: 400
    lineHeight: 1.4
  body:
    fontFamily: "Roboto, 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif"
    fontSize: "15px"
    fontWeight: 400
    lineHeight: 1.4
  body-strong:
    fontFamily: "Roboto, 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif"
    fontSize: "15px"
    fontWeight: 500
    lineHeight: 1.4
  body-small:
    fontFamily: "Roboto, 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.4
  meta:
    fontFamily: "Roboto, 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.4
  count:
    fontFamily: "Roboto, 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.4
    fontFeature: "tnum"
  column-header:
    fontFamily: "Roboto, 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif"
    fontSize: "12px"
    fontWeight: 500
    lineHeight: 1.4
    letterSpacing: "0.04em"
  tag-label:
    fontFamily: "Roboto, 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.3
  mono:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.4
rounded:
  base: "3px"
  inner: "2px"
  full: "50%"
spacing:
  xs: "4px"
  sm: "6px"
  md: "8px"
  lg: "10px"
  row-y: "12px"
  row-x: "14px"
  block-gap: "12px"
  rail-x: "18px"
  page-x: "32px"
  page-x-phone: "16px"
components:
  button:
    backgroundColor: "{colors.btn}"
    textColor: "{colors.on-fill}"
    typography: "{typography.body}"
    rounded: "{rounded.base}"
    padding: "8px 10px"
  button-hover:
    backgroundColor: "{colors.gold-tint}"
    textColor: "{colors.gold}"
  button-cta:
    backgroundColor: "{colors.gold}"
    textColor: "{colors.on-gold}"
    typography: "{typography.body}"
    rounded: "{rounded.base}"
    padding: "8px 10px"
  button-cta-hover:
    backgroundColor: "{colors.gold-tint}"
    textColor: "{colors.gold}"
  button-danger:
    backgroundColor: "{colors.btn}"
    textColor: "{colors.danger-text}"
    typography: "{typography.body}"
    rounded: "{rounded.base}"
    padding: "8px 10px"
  button-danger-hover:
    backgroundColor: "{colors.danger-hi}"
    textColor: "{colors.on-fill}"
  button-danger-filled:
    backgroundColor: "{colors.danger-hi}"
    textColor: "{colors.on-fill}"
    typography: "{typography.body}"
    rounded: "{rounded.base}"
    padding: "8px 10px"
  button-danger-filled-hover:
    backgroundColor: "{colors.danger-lo}"
  button-small:
    typography: "{typography.meta}"
    padding: "3px 6px"
  button-disabled:
    backgroundColor: "{colors.btn-off}"
    textColor: "{colors.text-mute}"
  block:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.base}"
  block-header:
    backgroundColor: "{colors.surface-2}"
    textColor: "{colors.text-hi}"
    typography: "{typography.block-title}"
    rounded: "{rounded.inner}"
    padding: "12px 14px"
  block-row:
    textColor: "{colors.text}"
    typography: "{typography.body}"
    padding: "12px 14px"
  submit-row:
    backgroundColor: "rgba(23, 23, 23, 0.9)"
    rounded: "{rounded.inner}"
    padding: "12px 14px"
  notice:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text-hi}"
    rounded: "{rounded.base}"
    padding: "12px 14px"
  field-input:
    backgroundColor: "{colors.field}"
    textColor: "{colors.text}"
    typography: "{typography.body}"
    rounded: "{rounded.base}"
    padding: "12px"
  field-input-focus:
    backgroundColor: "{colors.surface-3}"
  field-label:
    textColor: "{colors.text-hi}"
    typography: "{typography.body-strong}"
  field-hint:
    textColor: "{colors.text-mute}"
    typography: "{typography.meta}"
  tag:
    textColor: "{colors.text}"
    typography: "{typography.tag-label}"
    rounded: "{rounded.base}"
    padding: "1px 0.4em"
  tag-on:
    textColor: "{colors.ok-text}"
  tag-warn:
    textColor: "{colors.danger-text}"
  tag-approved:
    textColor: "{colors.gold}"
  tag-gone:
    textColor: "{colors.text-mute}"
  tag-pick:
    backgroundColor: "{colors.surface-3}"
    textColor: "{colors.text-hi}"
    typography: "{typography.body-small}"
    padding: "3px 2px 3px 8px"
  role-dot:
    rounded: "{rounded.full}"
    size: "10px"
  rail:
    backgroundColor: "{colors.surface}"
    width: "240px"
  rail-item:
    textColor: "{colors.text-hi}"
    typography: "{typography.body}"
    padding: "10px 18px"
  rail-item-hover:
    backgroundColor: "{colors.surface-3}"
  rail-item-selected:
    backgroundColor: "{colors.surface-3}"
    typography: "{typography.body-strong}"
  filter-link:
    textColor: "{colors.text}"
    padding: "6px 10px"
  filter-link-on:
    textColor: "{colors.text-hi}"
  table-header:
    textColor: "{colors.text-mute}"
    typography: "{typography.column-header}"
    padding: "10px 14px"
  table-cell:
    typography: "{typography.body-small}"
    padding: "12px 14px"
  search-list:
    backgroundColor: "{colors.surface-2}"
  search-option-active:
    backgroundColor: "{colors.surface-3}"
  selection-bar:
    backgroundColor: "{colors.surface-3}"
    padding: "8px 14px"
---

# Design System: cavbot2 panel

## Overview

**Creative North Star: "The Forum's Back Room"**

The panel is a back room of 7cav.us. Someone who signs in through the forum, or taps a recording link in Discord, lands on the same charcoal blocks, the same gold rule under each header and the same Roboto they just left. The look is the forum's stylesheet as it stood on 2026-09-17, and it is frozen. PRODUCT.md owns that commitment and the short list of places the panel may depart from it. This file records how the look is built, so a new page matches it without guessing.

Plain, dense, dependable. Every page is a title, a one-line description and a stack of blocks beside a 240px rail, on a near-black ground where the forum's night-map image shows faintly at the top. The Hubs and Foxhole pages pack tables, forms and change logs at 14px and 15px, because panel admins and Foxhole managers use them at a desk and know the words. The Recordings page is laid out for a phone first, because its link arrives in Discord. Nothing moves except a button. Its colours change in 150ms and it sinks 1px when pressed.

Issue #288 rejected two looks: a centred sign-in gate over the map with Teko display type, and a split-screen sign-in with Teko titles. Neither comes back.

**Key Characteristics:**
- Four charcoal steps carry all the depth. Nothing casts a drop shadow.
- A 2px gold rule sits under every block header. It turns red only on a danger confirm.
- Colour comes as lines, edges, stripes and text. Two kinds of button are filled.
- One face, Roboto, at two weights and six fixed sizes.
- Corners are 3px. The role colour dot is the only circle.
- Every state is written in words, and colour repeats the words.

## Colors

A grey ladder from near-black to near-white, one gold accent, and a red and a green that mark state.

### Primary
- **Cavalry Gold** (#ebc729): the patch yellow in the logo and the forum's accent. It draws the rule under every block header, links, the focus ring, the selected rail item and filter link, an approved Foxholer's tag, and the fill of the one forward button in a row of actions. At 10.87:1 on a block it is safe as text.
- **Gold Highlight** (#edcd40): the light bevel edges of the gold button, and a link under the pointer.
- **Gold Shade** (#e5bf15): the dark bevel edges of the gold button, the approved tag's edge, and the border of a block that waits on a Confirm or shows a running Foxhole action.
- **Gold Tint** (rgba(235, 199, 41, 0.1)): the fill of a default or gold button under the pointer, which is the 7Cav theme's hover over stock XenForo's. The label on it turns gold, at 7.81:1 or more over every surface.

### Secondary
- **Refusal Red** (#c84448): the stripe on a refusal notice, the edge of a danger button and a red tag, the border and header rule of a danger confirm, and the fill of its Confirm. It is 3.74:1 on a block, so it never carries text. It appears as a line, or as a fill under white text at 4.8:1.
- **Refusal Red Deep** (#b3393d): the filled Confirm's hover and its dark bevel edges.
- **Blush** (#f1c9cb): text on a red-edged part, the danger button's label and a red tag's words. 11.92:1 on a block.

### Tertiary
- **Done Green** (#509652): the forum's success stripe. It marks a result notice ("Saved.", "You're signed out.") and edges an "enabled" or "ready" tag. It is 4.97:1 on a block and stays a line.
- **Mint** (#cfe5cf): the words inside a green-edged tag.

### Neutral
- **Night Ground** (#121212): the page behind everything.
- **Block Charcoal** (#171717): blocks, notices and the rail.
- **Header Charcoal** (#1b1b1b): block headers and the picker's search list.
- **Field Charcoal** (#1c1c1c): the well of every text field and search box, as on the forum.
- **Raised Charcoal** (#212121): the selected rail item, a rail item, a holder row or the folded report line under the pointer, a picker tag, the sticky selection bar, the search option Enter would choose, a field with focus.
- **Seam** (#2b2b2b): block edges and the dividers between rows.
- **Strong Seam** (#353535): the line above a submit row, a plain tag's edge, the divider before the no-role view link, and the rule under the holder list's group of selected members a search or a filter hides.
- **Bevel Highlight** (#4d4d4d): the one-pixel highlight along a block header's top, a role with no colour.
- **Field Edge** (#6b6b6b): the border of every text field and search box, and the stripe of a neutral notice. The panel added it, since the forum's field edge is 1.20:1 against its field. It measures 3.52:1 on the ground, 3.20:1 on Field Charcoal and 3.02:1 on Raised Charcoal.
- **Bright Text** (#f1f1f1): headings, names, values. 15.87:1 on a block.
- **Body Grey** (#a1a1a1): running text and what a field holds. 6.94:1 on a block, 6.23:1 in a field with focus.
- **Muted Grey** (#8d8d8d): hints, times, counts, column headers, placeholders. 5.4:1 on a block, 4.85:1 in a field with focus.
- **Button Grey** (#2e2e2e): the default button's fill, with bevel edges of #212121 and #3b3b3b.
- **Off Grey** (#262626): a disabled button's fill, with bevel edges of #1a1a1a and #333333, as on the forum. Muted Grey on it is 4.56:1.
- **Lifted Grey** (#424242): a picker tag's × under the pointer.
- **White** (#ffffff) and **Black** (#000000): labels on the grey and red fills, and the label on gold.

### Named Rules
**The Lines Not Fills Rule.** Colour comes as a line, an edge, a stripe or text. Gold fills only the forward button in a row of actions, and the Gold Tint sits behind a default or gold button while the pointer is on it. Red fills only a danger confirm's Confirm, and a danger button while the pointer is on it. Nothing else gets a coloured background.

**The Red Means Refused Rule.** Red marks a refusal or a removal and nothing else. A notice that refuses nothing, such as an ended session, takes the grey stripe. A result takes the green one.

**The AA Tint Rule.** Where a frozen forum colour fails WCAG AA, a lighter tint replaces it. Every new colour token gets a contrast check before it ships, 4.5:1 for text and 3:1 for a field edge or focus ring.

## Typography

**Body Font:** Roboto (with 'Segoe UI', 'Helvetica Neue', Helvetica, sans-serif)
**Label/Mono Font:** ui-monospace (with SFMono-Regular, Menlo, monospace), for field names in a change log only

**Character:** One working face, the forum's own. Roboto ships inside the bot as a single variable-weight latin file, so no page fetches a font from a third party. Size and weight make the hierarchy. A second family never does.

### Hierarchy
- **Page title** (400, 22px, 1.4): the h1 at the top of each page, with 18px above it and 4px between it and the description.
- **Block title** (400, 18px, 1.4): every block header.
- **Body** (400, 15px, 1.4): set on the root. Running text, buttons, fields, rail items.
- **Body strong** (500, 15px, 1.4): names, field labels, hub names, the selected rail item.
- **Body small** (400, 14px, 1.4): table cells, change log values, report lists, picker tags. The headings inside a danger confirm use this size at 500.
- **Meta** (400, 13px, 1.4): the description under a page title, field hints, times, the small button.
- **Count** (400, 13px, tabular figures): every number that sits beside a label or in a column, so digits line up.
- **Column header** (500, 12px, 0.04em, uppercase): table headers only.
- **Tag label** (400, 12px, 1.3): the words inside a tag.
- **Mono** (400, 13px): the field name on the left of a change log entry.

The rail's "Signed in as" line is 12px and its version line 11px, both Muted Grey.

### Named Rules
**The Two Weights Rule.** 400 for reading, 500 for names. Names, labels and the selected place take 500, and everything else is 400. No bold, no italic, no display face. The font file covers 700, and nothing uses it.

**The Fixed Steps Rule.** Sizes are fixed pixels on a short ladder: 22, 18, 15, 14, 13, 12. Type does not scale with the viewport, and nothing sits between the steps.

## Layout

The page is a two-column grid: a 240px rail and the body beside it. The rail is sticky and full height. The logo sits at its top (26px tall), the page links below it, and the signed-in name, Sign out, "Back to the forum" and the version line at its foot. The body is left-aligned against the rail, never centred, up to 1300px wide, with 10px above, 32px at the sides and 48px below.

A page opens with its title, then one line of muted description, then blocks stacked 12px apart. Block headers, rows and submit rows use 12px top and bottom and 14px at the sides. Change log entries tighten to 8px top and bottom. Controls in a row sit 6px to 10px apart. Sign-in, the no-access and error pages, the hub forms and the Recordings page cap at 720px.

The moderator roles, the rank roles and the recording roles each sit beside their change log as equal columns with a 12px gutter. A rank roles preview spans both columns above them. The three Foxhole action blocks share one row of equal heights with their buttons pinned to each block's foot, and stack into one column under 1100px. While they stack, a link under the page description goes to the list below them. The rank roles section stacks under 1100px too, its change log below the Rank roles table. Tables keep a 640px minimum and scroll sideways inside their block, never the page. The Rank roles table has no minimum, since its three short columns fit a phone.

Under 768px the rail becomes a top bar of two short rows. The logo (20px) and page links sit on the first, and the name, a small Sign out and the forum link on the second. Every grid drops to one column, the side padding drops to 16px, and each change log entry stacks its field name above its values. The Recordings page's facts list stays two columns even at phone width.

### Named Rules
**The Block Stack Rule.** Every page is a title, a one-line description and a stack of blocks. New content goes in a block with a header, never in a box of its own.

## Elevation & Depth

The system is flat. Four charcoal steps carry depth: Night Ground, Block Charcoal, Header Charcoal, Raised Charcoal. A step up means the thing sits on top, is selected, or is under the pointer; a rail item, a holder row and the folded report line go straight to Raised Charcoal under the pointer. The forum's night-map image is the only texture. It is fixed to the viewport at full width, top-centred, and fades into the ground behind the rail and blocks. The forum's 4% block shadow and its header text shadow were dropped, because neither can be seen on this ground.

### Shadow Vocabulary
- **Header highlight** (`box-shadow: inset 0 1px 0 0 #4d4d4d`): a one-pixel highlight along the top edge of a block header. It is the only shadow in the system.

### Named Rules
**The Shade Not Shadow Rule.** Nothing casts a drop shadow. To lift something, step it up one charcoal. To set it apart, give it a 1px line. The sticky selection bar and the picker's search list follow this rule too.

## Shapes

Square-shouldered. Every block, button, field, tag and notice has 3px corners. The header and the submit row round the block corners they fill at 2px, as on the forum, so each curve sits inside the block's 1px edge. The Foxhole page's folded report line does the same. A search box squares its bottom corners where its list hangs from it. The 10px role colour dot is the only circle.

Lines come in three weights. 1px draws an edge or a divider. 2px draws a header rule, a filter link's underline, a selected phone tab's underline and the focus ring. 3px draws a notice's stripe and the selected rail item's edge.

### Named Rules
**The Three Pixel Rule.** Corners are 3px or a full circle. The one exception is 2px, on a part that fills a block's corner inside its edge. No fully rounded ends, no 8px corners.

## Components

### Buttons
Forum-issue hardware: plain, bevelled, sure.
- **Shape.** 3px corners and the forum's two-tone bevel, a 1px edge that is dark on the top and left and light on the bottom and right.
- **Default.** Button Grey with a white label, 8px by 10px, 15px type. Under the pointer it keeps its bevel, and in 150ms the label turns gold on the Gold Tint, as on the forum. A press moves the button down 1px.
- **Gold.** The forward action of a form or a row of actions: Save, Create, Sign in, Preview, Download the mix. Black label on gold, gold bevel. Under the pointer it takes the default's hover and keeps its gold bevel, so the fill gives way to the tint. A row of actions has at most one.
- **Danger.** Red edge, blush label, grey fill, and a red fill with a white label under the pointer. It starts a removal, which then waits on a danger confirm, or stops a running Foxhole action. It never deletes anything by itself.
- **Danger filled.** The Confirm inside a danger confirm, and nowhere else. White label on red, with deep-red bevel and hover.
- **Small.** 3px by 6px at 13px, as on the forum, for the picker's add button, row actions in a table and the Foxhole note's Save. It is 26px tall, over AA's 24px target size.
- **Disabled.** Every kind takes the forum's disabled look, Off Grey with its darker bevel and a Muted Grey label. It doesn't change under the pointer, shows the plain arrow and doesn't sink when pressed. A gold or red button turns grey, so it never reads as a dim live one.
- **Focus.** The 2px gold ring, 2px outside the edge.

### Tags
- **Style.** No fill, a 1px edge, 3px corners and a 12px label. The edge colour and the words both carry the state.
- **States.** A grey edge for neutral ("disabled", "recording", a change log's action). A green edge with Mint words for on ("enabled", "ready"). A red edge with Blush words when something is wrong ("broken", "failed", "cut short by a restart", "approved, doesn't hold Internal" or External). A gold edge with gold words for an approved Foxholer's role. A dashed grey edge with muted words for "not in the server".
- **Picker tag.** Raised Charcoal fill, a 14px bright label, an optional role colour dot, and a × that removes it. The ×'s focus ring sits flush inside the tag.

### Blocks
- **Corner style.** 3px.
- **Background.** Block Charcoal, a 1px Seam edge, 12px below each block.
- **Header.** Header Charcoal, an 18px bright title, the header highlight on top and the 2px gold rule beneath.
- **Rows.** Consecutive rows divide with a 1px Seam.
- **Submit row.** The foot of a form. It has the same padding, a Strong Seam above it, and its buttons left-aligned 8px apart.
- **Shadow strategy.** None. See Elevation & Depth.
- **Danger confirm.** A red edge and a red header rule. The header asks the question ("Remove hub Arma Voice?"). Below it, short 14px/500 headings such as "Goes with the hub" and "Stays" list the consequences, with counts in muted tabular figures. The filled red Confirm comes first in the submit row, then a default Cancel.
- **Preview and progress.** A Gold Shade edge marks a block that waits on a Confirm or shows a running action.

### Notices
- **Style.** Block Charcoal, bright text, a 1px Seam edge and a 3px stripe down the left.
- **Variants.** A red stripe is a refusal, and it is the default. A Field Edge grey stripe is a notice that refuses nothing, such as an ended session or a lost connection to Discord. A green stripe is a result. Inside a block row, a notice drops its bottom margin.

### Inputs / Fields
- **Style.** A Field Charcoal well, a 1px Field Edge border, 3px corners, 12px padding all round, Body Grey 15px text and Muted Grey placeholder. Under 568px the text is 16px, as on the forum, so a phone doesn't zoom in when someone taps the field. The label sits above at 15px/500 with a 6px gap, and the hint below at 13px muted.
- **Focus.** The well steps up to Raised Charcoal, as on the forum. The border turns gold, and a 2px gold outline sits 1px outside it.
- **Checkboxes.** Native, with a gold accent colour.
- **Error.** Fields have no red state. A refusal notice at the top of the block says what was refused and that nothing changed.

### Links
A text link is gold and underlined, and turns Gold Highlight under the pointer. The forum shows the underline only under the pointer. The panel keeps it at rest because gold is 1.57:1 against Body Grey, and AA wants more than colour to pick a link out of the words around it. PRODUCT.md lists this departure. Rail items, filter links and buttons carry no underline, since their place and shape mark them.

### Navigation
- **Rail.** A 240px Block Charcoal column with a 1px Seam on its right. Items are full width, 10px by 18px, in bright 15px text, with a 3px left edge. Under the pointer an item steps to Raised Charcoal. The selected item sits at Raised Charcoal already, with a gold left edge and weight 500, and hovering it changes nothing. Its focus ring sits inside the item.
- **Phone.** The rail turns into the two-row top bar described in Layout. The selected item's gold edge moves to its bottom.
- **Filter links.** A row of plain Body Grey links, 6px by 10px. The current one turns bright with a 2px gold underline. The Foxhole filters and the Recordings page's "Yours / Every recording" switch both use them.

### Tables
The hub list, the Foxhole holder list and the Rank roles table fill their block. Cells are 14px with 12px by 14px padding, tighter on the Foxhole list and the Rank roles table at 7px by 12px. A 1px Seam divides the rows. Headers are 12px uppercase Muted Grey, numbers sit right-aligned in tabular figures, and a hovered Foxhole row steps to Raised Charcoal. Selected members that a search or a filter hides head the holder list in a group of their own. A header row spanning the table names the group, and a Strong Seam closes it off from the matching rows.

The Rank roles table's rows read rank, role, button, count. The rank's abbreviation heads its row at 500 in Bright Text, as a hub's name does. Its role tag and the small "Choose a role" button share one line, and the search opens on the line below. The buttons line up down the right of the column, whatever each role's name. The rank and its count sit on the role's line, with or without the search open. At phone width the button drops under the tag, still on the right.

### Tag picker
The selected set shows as picker tags. A small add button opens a search box with a list hanging below it, in Header Charcoal behind a Field Edge border. The list shows ten rows, then scrolls inside itself, so a guild with thousands of roles never grows the page. Focus stays in the box, so the row Enter would choose draws its own inset gold ring on Raised Charcoal.

### Change log
Each entry is a disclosure folded to one line: a drawn chevron, the forum user's name at 500, the action as a tag and the time in Muted Grey. Opened, it lists each field name in mono on the left and "before → after" on the right. The newest entry starts open. On the Foxhole page an address can name an entry, as Cancel on a Retry pressed in that entry does. The page then opens that entry as well as the newest, and lands on its one-line summary with focus on it.

## Do's and Don'ts

### Do:
- **Do** build a new page as a title, a 13px muted description and a stack of blocks beside the rail, with Block Charcoal, a 1px Seam, 3px corners and 12px between blocks.
- **Do** give every block a header with the 2px gold rule, and switch the rule to red only on a danger confirm.
- **Do** take colours from the `:root` tokens in `panel/static/panel.css`, and check any new one for AA contrast before it ships.
- **Do** draw the 2px gold focus ring on every control, inside the edge where the outside would be clipped.
- **Do** write every state in words inside its tag, notice or flag, so colour only repeats what the words say.
- **Do** set counts and numbers in a column in tabular figures.
- **Do** keep every page usable at 375px with no sideways scroll, and let wide tables scroll inside their own block.

### Don't:
- **Don't** restyle the frozen forum look. It changes only where WCAG AA requires it or the panel needs a state the forum doesn't draw, and the maintainer starts any redesign.
- **Don't** add a display face such as Teko, or load a font, image or script from a third party.
- **Don't** centre a sign-in gate over the map or split the sign-in screen in two. Issue #288 rejected both.
- **Don't** add drop shadows, gradients, glows or blur.
- **Don't** fill anything with gold, red or green except the gold forward button, the red Confirm and the Gold Tint under the pointer.
- **Don't** use red for anything that isn't a refusal or a removal.
- **Don't** show a state by colour alone.
- **Don't** round a corner past 3px or give anything fully rounded ends.
- **Don't** add motion beyond the 150ms change of a button's colours and the 1px press. Under reduced motion the stylesheet drops every transition, so the colours change at once.
