"use strict";

// Apply saved theme before first paint to avoid flash of wrong color scheme.
(function () {
  var KEY = "unire_theme";
  function apply(mode) {
    var root = document.documentElement;
    if (mode === "light" || mode === "dark") {
      root.dataset.theme = mode;
    } else {
      delete root.dataset.theme;
    }
  }
  function get() {
    try {
      var v = localStorage.getItem(KEY);
      return v === "light" || v === "dark" ? v : "system";
    } catch (_) {
      return "system";
    }
  }
  function set(mode) {
    var norm = mode === "light" || mode === "dark" ? mode : "system";
    apply(norm);
    try {
      if (norm === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, norm);
    } catch (_) {}
    window.dispatchEvent(new CustomEvent("unire:theme", { detail: norm }));
  }
  apply(get());
  window.getUnireTheme = get;
  window.setUnireTheme = set;
})();
