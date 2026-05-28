'use strict';

(function () {
  const THEME_KEY = 'benreadin_theme';

  function isDark() {
    return document.documentElement.dataset.theme === 'dark';
  }

  function applyTheme(dark) {
    document.documentElement.dataset.theme = dark ? 'dark' : 'light';
    localStorage.setItem(THEME_KEY, dark ? 'dark' : 'light');
    syncIcon(dark);
  }

  function syncIcon(dark) {
    document.querySelectorAll('#theme-toggle').forEach(btn => {
      const moon = btn.querySelector('.icon-moon');
      const sun  = btn.querySelector('.icon-sun');
      if (moon) moon.style.display = dark ? 'none' : '';
      if (sun)  sun.style.display  = dark ? '' : 'none';
      btn.setAttribute('aria-label', dark ? 'Switch to light mode' : 'Switch to dark mode');
    });
  }

  document.addEventListener('DOMContentLoaded', () => {
    const btn = document.getElementById('theme-toggle');
    if (!btn) return;
    syncIcon(isDark());
    btn.addEventListener('click', () => applyTheme(!isDark()));
  });
})();
