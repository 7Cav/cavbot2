# Panel prototype

Throwaway. Not production code. Nothing here is imported by the bot.

Question: what should the panel pages from issue #288 look like? Three
variants of the sign-in page, the signed-in page and the error page, on one
route, switchable with `?variant=` and the white bar at the bottom of the page.

Run it:

```bash
open panel/prototype/index.html
```

Or serve the directory over HTTP so the query string survives:

```bash
python3 -m http.server 8765 --directory panel/prototype
```

Variants: `a` forum sibling, `b` gate, `c` console. Screens: `signin`,
`expired`, `no-group`, `home`, `error`. `later=1` fills the signed-in page
with mock hub rows from a later ticket, so the shell is judged with content.

The colors, the two logos and the map background are the ones 7cav.us renders
today, read from its stylesheet on 2026-09-17.
