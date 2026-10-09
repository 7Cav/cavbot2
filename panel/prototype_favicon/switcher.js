// PROTOTYPE for #548, throwaway: never merge. Favicon variant switcher and
// a "favicon lab" card showing the current icon at real tab sizes, in mock
// light and dark tab strips, next to the other variants. H hides the card.
(function () {
  var VARIANTS = [
    { key: "A", name: "Full mascot" },
    { key: "B", name: "Robot head" },
    { key: "C", name: "Bust: head + shield" },
    { key: "D", name: "Logo shield (current art)" },
  ];
  var params = new URLSearchParams(location.search);
  var idx = Math.max(0, VARIANTS.findIndex(function (v) { return v.key === params.get("variant"); }));
  var cur = VARIANTS[idx];

  function go(step) {
    var next = VARIANTS[(idx + step + VARIANTS.length) % VARIANTS.length];
    params.set("variant", next.key);
    location.search = params.toString();
  }
  function icon(key, css) {
    return '<img src="/prototype_favicon/' + key + "-" + css + '.png" srcset="/prototype_favicon/' + key + "-" + css + ".png 1x, /prototype_favicon/" + key + "-" + css * 2 + '.png 2x" width="' + css + '" height="' + css + '" alt="">';
  }
  // Neighbouring tabs get plain placeholder glyphs, no real brands.
  function glyph(color, letter) {
    return '<span class="pf-glyph" style="background:' + color + '">' + letter + "</span>";
  }
  function strip(theme) {
    var tabs = [
      [glyph("#5865f2", "D"), "general | Discord"],
      [glyph("#24292f", "G"), "Pull requests · 7Cav/cavbot2"],
      [icon(cur.key, 16), "Foxhole · cavbot2 panel", true],
      [glyph("#c5221f", "M"), "Inbox (3)"],
      [glyph("#1a73e8", "F"), "7cav.us forums"],
    ];
    return '<div class="pf-strip pf-' + theme + '">' + tabs.map(function (t) {
      return '<div class="pf-tab' + (t[2] ? " pf-active" : "") + '">' + t[0] + "<span>" + t[1] + "</span></div>";
    }).join("") + "</div>";
  }
  function pinned(theme) {
    return '<div class="pf-pinned pf-' + theme + '">' + [glyph("#5865f2", "D"), glyph("#24292f", "G"), icon(cur.key, 16), glyph("#c5221f", "M")].map(function (g, i) {
      return '<div class="pf-ptab' + (i === 2 ? " pf-active" : "") + '">' + g + "</div>";
    }).join("") + "</div>";
  }

  var css = document.createElement("style");
  css.textContent = [
    ".pf-card{position:fixed;top:16px;right:16px;z-index:99999;width:520px;max-width:calc(100vw - 32px);background:#fff;color:#111;border:2px solid #ff00aa;border-radius:10px;box-shadow:0 10px 30px rgba(0,0,0,.35);font:13px/1.4 -apple-system,system-ui,sans-serif;padding:12px 14px}",
    ".pf-card h3{margin:10px 0 6px;font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:#666}",
    ".pf-card h3:first-child{margin-top:0}",
    ".pf-sizes{display:flex;align-items:flex-end;gap:18px}",
    ".pf-sizes figure{margin:0;text-align:center}.pf-sizes figcaption{font-size:11px;color:#666}",
    ".pf-strip{display:flex;gap:2px;padding:6px 6px 0;border-radius:6px 6px 0 0;overflow:hidden}",
    ".pf-light{background:#dfe1e5}.pf-dark{background:#1f1f1f}",
    ".pf-tab{display:flex;align-items:center;gap:6px;min-width:0;flex:1;padding:7px 8px;border-radius:7px 7px 0 0;font-size:12px;white-space:nowrap}",
    ".pf-tab span{overflow:hidden;text-overflow:ellipsis}",
    ".pf-light .pf-tab{color:#3c4043}.pf-light .pf-active{background:#fff;color:#111}",
    ".pf-dark .pf-tab{color:#bdc1c6}.pf-dark .pf-active{background:#3c3c3c;color:#fff}",
    ".pf-tab img,.pf-ptab img{flex:none}",
    ".pf-glyph{flex:none;display:inline-grid;place-items:center;width:16px;height:16px;border-radius:4px;color:#fff;font:700 10px/1 system-ui}",
    ".pf-pinned{display:inline-flex;gap:2px;padding:6px 6px 0;border-radius:6px 6px 0 0;margin-right:8px}",
    ".pf-ptab{padding:7px 10px;border-radius:7px 7px 0 0}.pf-light .pf-ptab.pf-active{background:#fff}.pf-dark .pf-ptab.pf-active{background:#3c3c3c}",
    ".pf-all{display:flex;gap:8px}.pf-all div{display:flex;align-items:center;gap:10px;padding:8px 10px;border-radius:6px;flex:1;justify-content:center}",
    ".pf-all .pf-sel{outline:2px solid #ff00aa}",
    ".pf-note{margin-top:8px;font-size:11px;color:#666}",
    ".pf-bar{position:fixed;left:50%;bottom:18px;transform:translateX(-50%);z-index:99999;display:flex;align-items:center;gap:6px;background:#111;color:#fff;border-radius:999px;padding:6px 8px;box-shadow:0 6px 20px rgba(0,0,0,.4);font:600 13px/1 -apple-system,system-ui,sans-serif}",
    ".pf-bar button{all:unset;cursor:pointer;padding:6px 12px;border-radius:999px;background:#333}.pf-bar button:hover{background:#ff00aa}",
    ".pf-bar .pf-label{padding:0 10px;display:flex;align-items:center;gap:8px}",
  ].join("\n");
  document.head.appendChild(css);

  var card = document.createElement("div");
  card.className = "pf-card";
  card.innerHTML =
    "<h3>" + cur.key + " · " + cur.name + " — actual size (16 is the tab)</h3>" +
    '<div class="pf-sizes">' + [16, 32, 48, 64].map(function (s) {
      return "<figure>" + icon(cur.key, s) + "<figcaption>" + s + "px</figcaption></figure>";
    }).join("") + "</div>" +
    "<h3>Tab strip, light and dark</h3>" + strip("light") + '<div style="height:6px"></div>' + strip("dark") +
    "<h3>Pinned tabs (icon only)</h3>" + pinned("light") + pinned("dark") +
    "<h3>All variants at 16px</h3>" +
    ["light", "dark"].map(function (th) {
      return '<div class="pf-all" style="margin-bottom:4px">' + VARIANTS.map(function (v) {
        return '<div class="pf-' + th + (v.key === cur.key ? " pf-sel" : "") + '">' + icon(v.key, 16) + '<span style="color:' + (th === "dark" ? "#bdc1c6" : "#3c4043") + '">' + v.key + "</span></div>";
      }).join("") + "</div>";
    }).join("") +
    '<div class="pf-note">The real test is this page\'s own tab, up top. H hides this card; ← → switch variants.</div>';
  document.body.appendChild(card);

  var bar = document.createElement("div");
  bar.className = "pf-bar";
  bar.innerHTML = '<button data-step="-1">←</button><span class="pf-label">' + icon(cur.key, 16) + cur.key + " (" + cur.name + ')</span><button data-step="1">→</button>';
  bar.addEventListener("click", function (e) {
    var b = e.target.closest("button");
    if (b) go(Number(b.dataset.step));
  });
  document.body.appendChild(bar);

  document.addEventListener("keydown", function (e) {
    var el = document.activeElement;
    if (el && (el.matches("input,textarea,[contenteditable]") || el.isContentEditable)) return;
    if (e.key === "ArrowLeft") go(-1);
    else if (e.key === "ArrowRight") go(1);
    else if (e.key === "h" || e.key === "H") card.hidden = !card.hidden;
  });
})();
