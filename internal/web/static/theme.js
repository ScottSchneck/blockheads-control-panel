// Applies the saved look before the page draws, so it doesn't flash the
// wrong colours. The account's setting (loaded later) is the real one; this
// copy is only a head start for this browser.
(function () {
  try {
    var p = JSON.parse(localStorage.getItem("bh-prefs") || "{}");
    var root = document.documentElement;
    if (p.mode === "light" || p.mode === "dark" || p.mode === "auto") root.dataset.mode = p.mode;
    if (p.style === "control" || p.style === "treehouse") root.dataset.style = p.style;
  } catch (e) { /* storage off: use the defaults */ }
})();
