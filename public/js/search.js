'use strict';

(function () {
  const form       = document.getElementById('search-form');
  const urlInput   = document.getElementById('shelf-url');
  const chipWrap   = document.getElementById('library-chips');
  const chipInput  = document.getElementById('library-input');
  const dropdown   = document.getElementById('autocomplete-list');
  const hiddenLibs = document.getElementById('libraries-hidden');
  const searchBtn  = document.getElementById('search-btn');
  const errorBanner = document.getElementById('error-banner');

  const libraries = []; // { key, name }
  let acItems = [];
  let acIndex = -1;
  let debounceTimer = null;

  // ---- Library chip management ----

  function addLibrary(key, name) {
    if (libraries.length >= 15) return;
    if (libraries.find(l => l.key === key)) return;
    libraries.push({ key, name: name || key });
    renderChips();
    chipInput.value = '';
    chipInput.focus();
    closeDropdown();
    syncHidden();
  }

  function removeLibrary(key) {
    const idx = libraries.findIndex(l => l.key === key);
    if (idx !== -1) libraries.splice(idx, 1);
    renderChips();
    syncHidden();
  }

  function renderChips() {
    // Remove existing chips (keep the input)
    Array.from(chipWrap.querySelectorAll('.chip')).forEach(el => el.remove());
    libraries.forEach(lib => {
      const chip = document.createElement('div');
      chip.className = 'chip';
      chip.innerHTML = `${escHtml(lib.name)} <button class="chip-remove" data-key="${escHtml(lib.key)}" title="Remove">&times;</button>`;
      chipWrap.insertBefore(chip, chipInput);
    });
  }

  function syncHidden() {
    hiddenLibs.value = libraries.map(l => l.key).join(',');
  }

  chipWrap.addEventListener('click', e => {
    if (e.target.classList.contains('chip-remove')) {
      removeLibrary(e.target.dataset.key);
    } else {
      chipInput.focus();
    }
  });

  // ---- Autocomplete ----

  chipInput.addEventListener('input', () => {
    clearTimeout(debounceTimer);
    const q = chipInput.value.trim();
    if (!q) { closeDropdown(); return; }
    debounceTimer = setTimeout(() => fetchLibraries(q), 180);
  });

  chipInput.addEventListener('keydown', e => {
    if (e.key === 'Backspace' && chipInput.value === '' && libraries.length > 0) {
      removeLibrary(libraries[libraries.length - 1].key);
      return;
    }
    if (e.key === 'ArrowDown') { acIndex = Math.min(acIndex + 1, acItems.length - 1); renderDropdown(); e.preventDefault(); return; }
    if (e.key === 'ArrowUp')   { acIndex = Math.max(acIndex - 1, 0); renderDropdown(); e.preventDefault(); return; }
    if (e.key === 'Enter' || e.key === 'Tab') {
      if (acItems.length > 0 && acIndex >= 0) {
        addLibrary(acItems[acIndex].key, acItems[acIndex].name);
        e.preventDefault();
      } else if (acItems.length === 1) {
        addLibrary(acItems[0].key, acItems[0].name);
        e.preventDefault();
      } else if (chipInput.value.trim()) {
        // Add raw key
        const raw = chipInput.value.trim().toLowerCase();
        addLibrary(raw, raw);
        e.preventDefault();
      }
    }
    if (e.key === 'Escape') closeDropdown();
  });

  chipInput.addEventListener('focus', () => chipWrap.classList.add('focused'));
  chipInput.addEventListener('blur', () => {
    chipWrap.classList.remove('focused');
    setTimeout(closeDropdown, 200);
  });

  async function fetchLibraries(q) {
    try {
      const res = await fetch(`/api/libraries?q=${encodeURIComponent(q)}`);
      acItems = await res.json();
      acIndex = acItems.length > 0 ? 0 : -1;
      renderDropdown();
    } catch { /* ignore */ }
  }

  function renderDropdown() {
    if (!acItems.length) { closeDropdown(); return; }
    dropdown.innerHTML = acItems.map((item, i) =>
      `<div class="autocomplete-item${i === acIndex ? ' active' : ''}" data-key="${escHtml(item.key)}" data-name="${escHtml(item.name)}">
        <span class="lib-name">${escHtml(item.name)}</span>
        <span class="lib-key">${escHtml(item.key)}</span>
      </div>`
    ).join('');
    dropdown.classList.add('open');
  }

  dropdown.addEventListener('mousedown', e => {
    const item = e.target.closest('.autocomplete-item');
    if (item) addLibrary(item.dataset.key, item.dataset.name);
  });

  function closeDropdown() {
    dropdown.classList.remove('open');
    acItems = [];
    acIndex = -1;
  }

  // ---- URL paste detection: auto-fill libraries from OverReader URL ----

  urlInput.addEventListener('paste', e => {
    setTimeout(() => {
      const val = urlInput.value.trim();
      const match = val.match(/overreader\.com\/overdrive\/([^/]+)\//);
      if (match) {
        const keys = match[1].split(',').map(k => k.trim()).filter(Boolean);
        keys.forEach(k => addLibrary(k, k));
      }
    }, 50);
  });

  // ---- Form submit ----

  function showError(msg) {
    errorBanner.textContent = msg;
    errorBanner.classList.add('visible');
  }
  function clearError() {
    errorBanner.classList.remove('visible');
  }

  form.addEventListener('submit', e => {
    e.preventDefault();
    clearError();

    const url = urlInput.value.trim();
    if (!url) { showError('Please enter a Goodreads or OverReader shelf URL.'); return; }
    if (libraries.length === 0) { showError('Add at least one library to check.'); return; }

    const params = new URLSearchParams();
    params.set('url', url);
    libraries.forEach(l => params.append('libraries', l.key));

    window.location.href = '/results.html?' + params.toString();
  });

  // ---- Utils ----

  function escHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }
})();
