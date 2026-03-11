'use strict';

(function () {
  const THEME_KEY = 'shelfprice_theme';

  function isDark() {
    return document.documentElement.dataset.theme === 'dark';
  }

  function applyTheme(dark) {
    document.documentElement.dataset.theme = dark ? 'dark' : 'light';
    localStorage.setItem(THEME_KEY, dark ? 'dark' : 'light');
    const btn = document.getElementById('theme-toggle');
    if (btn) btn.textContent = dark ? '☀️' : '🌙';
  }

  // Set initial button icon after DOM loads.
  document.addEventListener('DOMContentLoaded', () => {
    const btn = document.getElementById('theme-toggle');
    if (!btn) return;
    btn.textContent = isDark() ? '☀️' : '🌙';
    btn.addEventListener('click', () => applyTheme(!isDark()));
  });
})();
