'use strict';

(function () {
  const progressBar    = document.getElementById('progress-bar');
  const progressLabel  = document.getElementById('progress-label');
  const bookGrid       = document.getElementById('book-grid');
  const errorBanner    = document.getElementById('error-banner');
  const resultsHeader  = document.getElementById('results-header');
  const resultsCount   = document.getElementById('results-count');
  const filterBtns     = document.querySelectorAll('#filter-availability .filter-chip');
  const sortSelect     = document.getElementById('sort-select');
  const shuffleBtn     = document.getElementById('shuffle-btn');
  const recsPanel      = document.getElementById('recs-panel');
  const recsGrid       = document.getElementById('recs-grid');
  const recsToggle     = document.getElementById('recs-toggle');
  const loadingDetail  = document.getElementById('loading-detail');

  // All received book events, in arrival order (= shelf order).
  const allBooks = [];
  // Map from goodreads_id → DOM element for O(1) in-place updates.
  const bookElements = new Map();

  let totalBooks    = 0;
  let completedBooks = 0;

  const activeFilters = new Set();
  // Availability chips are mutually-exclusive states; the rest narrow the result.
  const AVAILABILITY_FILTERS = ['available', 'wait', 'not_found'];
  const SORT_KEY = 'benreadin_sort';
  // Stable per-book random keys for the "random" sort. Assigned lazily and kept
  // for the life of the page so streaming in new books (SSE) doesn't reshuffle
  // books already on screen — only re-rolled on an explicit shuffle.
  const randomKeys = new Map();

  // localStorage access throws (not just returns null) when site data is
  // blocked — e.g. Firefox with Strict Enhanced Tracking Protection, which is
  // common on mobile Firefox. An unguarded read at script-init aborts this whole
  // IIFE, so the page never opens the EventSource and is stranded on the static
  // "Starting…". Route every access through here so storage failures degrade
  // gracefully instead of breaking the page.
  const storage = {
    get(key) { try { return localStorage.getItem(key); } catch { return null; } },
    set(key, val) { try { localStorage.setItem(key, val); } catch { /* blocked / quota */ } },
  };

  let activeSort = storage.get(SORT_KEY) || 'rating_desc';

  // Filters default to Available + Kindle; any change the user makes is saved
  // as their new default (the "All" chip saves an empty set = show everything).
  const FILTER_KEY = 'benreadin_filters';
  const VALID_FILTERS = [...AVAILABILITY_FILTERS, 'kindle', 'gutenberg'];
  (function restoreFilters() {
    let saved = null;
    try { saved = JSON.parse(storage.get(FILTER_KEY)); } catch { /* corrupt */ }
    const wanted = Array.isArray(saved) ? saved : ['available', 'kindle'];
    wanted.filter(f => VALID_FILTERS.includes(f)).forEach(f => activeFilters.add(f));
  })();

  function saveFilters() {
    storage.set(FILTER_KEY, JSON.stringify([...activeFilters]));
  }

  // ---- Utilities ----

  function showError(msg) {
    errorBanner.textContent = msg;
    errorBanner.classList.add('visible');
    setProgress(0, msg);
    hideLoadingDetail();
  }

  let progressRaf = null;
  function setProgress(pct, label) {
    if (progressRaf) cancelAnimationFrame(progressRaf);
    progressRaf = requestAnimationFrame(() => {
      progressBar.style.width = Math.min(100, pct) + '%';
      if (label !== undefined) progressLabel.textContent = label;
    });
  }

  // Show what's being checked during the initial shelf fetch, so the wait
  // isn't a blank skeleton screen. Cleared once real book cards arrive.
  function showLoadingDetail() {
    if (!loadingDetail || libraries.length === 0) return;
    const pills = libraries
      .map(key => `<span class="loading-lib">${escHtml(window.getLibName(key))}</span>`)
      .join('');
    const noun = libraries.length === 1 ? 'library' : 'libraries';
    loadingDetail.innerHTML =
      `<span class="loading-detail-label">Checking ${libraries.length} ${noun}:</span>${pills}`;
    loadingDetail.classList.remove('hidden');
  }

  function hideLoadingDetail() {
    if (loadingDetail) loadingDetail.classList.add('hidden');
  }

  // ---- Filter / Sort logic ----

  function bestStatus(libraryResults) {
    if (!Array.isArray(libraryResults) || libraryResults.length === 0) return 'not_found';
    const priority = { available: 0, wait: 1, unavailable: 2, not_found: 3 };
    return libraryResults.reduce((best, lr) => {
      return (priority[lr.status] ?? 3) < (priority[best] ?? 3) ? lr.status : best;
    }, libraryResults[0].status);
  }

  function minWait(libraryResults) {
    if (!libraryResults) return Infinity;
    const waits = libraryResults
      .filter(lr => lr.status === 'wait' && lr.estimated_wait_days > 0)
      .map(lr => lr.estimated_wait_days);
    return waits.length ? Math.min(...waits) : Infinity;
  }

  function bookMatchesFilter(event, filter) {
    const status = bestStatus(event.library_results);
    if (filter === 'available') {
      const hasAnyWait = (event.library_results || []).some(lr => lr.status === 'wait');
      return status === 'available' && !hasAnyWait;
    }
    if (filter === 'wait') return status === 'wait';
    if (filter === 'not_found') return status === 'not_found' || status === 'unavailable';
    if (filter === 'kindle') return (event.library_results || []).some(lr => lr.has_kindle);
    if (filter === 'gutenberg') return !!event.gutenberg_result;
    return false;
  }

  function filterBook(event) {
    if (activeFilters.size === 0) return true;
    // Availability chips (available / on-hold / not-found) are mutually exclusive,
    // so a book matching ANY selected one qualifies on that axis.
    const availSelected = AVAILABILITY_FILTERS.filter(f => activeFilters.has(f));
    if (availSelected.length && !availSelected.some(f => bookMatchesFilter(event, f))) {
      return false;
    }
    // Attribute chips narrow further: every selected one must match (AND).
    if (activeFilters.has('kindle') && !bookMatchesFilter(event, 'kindle')) return false;
    if (activeFilters.has('gutenberg') && !bookMatchesFilter(event, 'gutenberg')) return false;
    return true;
  }

  function sortedBooks() {
    const copy = allBooks.slice();
    switch (activeSort) {
      case 'default_desc':
        copy.reverse();
        break;
      case 'available_first': {
        const order = { available: 0, wait: 1, unavailable: 2, not_found: 3 };
        copy.sort((a, b) => {
          const sa = order[bestStatus(a.library_results)] ?? 3;
          const sb = order[bestStatus(b.library_results)] ?? 3;
          if (sa !== sb) return sa - sb;
          return minWait(a.library_results) - minWait(b.library_results);
        });
        break;
      }
      case 'wait_asc':
        // Books with an actual hold queue lead (shortest wait first) — that's
        // what this sort is for; available-now titles follow, the rest last.
        copy.sort((a, b) => waitSortKey(a) - waitSortKey(b));
        break;
      case 'rating_desc':
        copy.sort((a, b) => (b.book.average_rating || 0) - (a.book.average_rating || 0));
        break;
      case 'title_asc':
        copy.sort((a, b) => a.book.title.localeCompare(b.book.title));
        break;
      case 'title_desc':
        copy.sort((a, b) => b.book.title.localeCompare(a.book.title));
        break;
      case 'random':
        copy.sort((a, b) => randomKey(a) - randomKey(b));
        break;
      default: // shelf order
        break;
    }
    return copy;
  }

  // Sort key for "Shortest wait": known waits ascending, then holds with an
  // unknown wait, then available-now, then unavailable/not-found.
  function waitSortKey(event) {
    const status = bestStatus(event.library_results);
    if (status === 'wait') {
      const w = minWait(event.library_results);
      return Number.isFinite(w) ? w : 1e9;
    }
    if (status === 'available') return 1e10;
    return 1e11;
  }

  function randomKey(event) {
    const id = event.book && event.book.goodreads_id;
    if (!id) return Math.random();
    let key = randomKeys.get(id);
    if (key === undefined) {
      key = Math.random();
      randomKeys.set(id, key);
    }
    return key;
  }

  // ---- DOM helpers ----

  // Parse an HTML string into a real DOM element (no full re-render).
  function parseHTML(html) {
    const t = document.createElement('template');
    t.innerHTML = html.trim();
    return t.content.firstElementChild;
  }

  // Apply the current sort + filter to the existing DOM elements in-place.
  // Uses appendChild to reorder (moves nodes, doesn't clone) — single reflow.
  function applyView() {
    const books = sortedBooks();
    let visibleCount = 0;
    const frag = document.createDocumentFragment();

    for (const event of books) {
      const id = event.book && event.book.goodreads_id;
      const el = id ? bookElements.get(id) : null;
      if (!el) continue;
      const show = filterBook(event);
      el.classList.toggle('hidden', !show);
      if (show) visibleCount++;
      frag.appendChild(el);
    }

    bookGrid.appendChild(frag);
    updateCount(visibleCount < allBooks.length ? visibleCount : undefined);
  }

  function updateCount(visibleCount) {
    if (totalBooks > 0) {
      resultsHeader.style.display = 'block';
      const ready = allBooks.length;
      const streaming = ready < totalBooks;
      if (streaming) {
        resultsCount.textContent = visibleCount !== undefined && visibleCount !== ready
          ? `(${visibleCount} shown — ${ready} / ${totalBooks} checked)`
          : `(${ready} / ${totalBooks} checked)`;
      } else if (visibleCount !== undefined && visibleCount !== ready) {
        resultsCount.textContent = `(${visibleCount} of ${ready})`;
      } else {
        resultsCount.textContent = `(${ready})`;
      }
    }
  }

  // ---- Filter / sort event listeners ----

  function syncFilterUI() {
    filterBtns.forEach(btn => {
      if (btn.dataset.filter === 'all') {
        btn.classList.toggle('active', activeFilters.size === 0);
      } else {
        btn.classList.toggle('active', activeFilters.has(btn.dataset.filter));
      }
    });
  }

  filterBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      if (btn.dataset.filter === 'all') {
        activeFilters.clear();
      } else {
        if (activeFilters.has(btn.dataset.filter)) {
          activeFilters.delete(btn.dataset.filter);
        } else {
          activeFilters.add(btn.dataset.filter);
        }
      }
      saveFilters();
      syncFilterUI();
      applyView();
    });
  });

  // Shuffle is a standalone toggle next to the Sort dropdown, not a dropdown
  // option: while active the button is highlighted and the select shows no
  // selection; picking any sort exits shuffle mode.
  function syncSortUI() {
    if (shuffleBtn) shuffleBtn.classList.toggle('active', activeSort === 'random');
    if (activeSort === 'random') sortSelect.selectedIndex = -1;
  }

  sortSelect.addEventListener('change', () => {
    activeSort = sortSelect.value;
    storage.set(SORT_KEY, activeSort);
    syncSortUI();
    applyView();
  });

  if (shuffleBtn) {
    shuffleBtn.addEventListener('click', () => {
      // Session-only: shuffle is a one-off action, not a default. Persisting
      // "random" used to leave the sort dropdown blank on the next visit.
      activeSort = 'random';
      randomKeys.clear();
      syncSortUI();
      applyView();
    });
  }

  // ---- SSE ----

  const params = new URLSearchParams(window.location.search);
  const shelfUrl  = params.get('url');
  const shelf     = params.get('shelf') || '';
  const libraries = params.getAll('libraries');

  // ---- Client-side results cache (1-hour TTL) ----

  const RESULTS_CACHE_STORAGE_KEY = 'benreadin_results_v1';
  const RESULTS_CACHE_TTL = 60 * 60 * 1000;

  function buildCacheKey() {
    return shelfUrl + '|' + shelf + '|' + [...libraries].sort().join(',');
  }

  function getCachedResults() {
    try {
      const all = JSON.parse(storage.get(RESULTS_CACHE_STORAGE_KEY) || '{}');
      const entry = all[buildCacheKey()];
      if (!entry || Date.now() - entry.timestamp > RESULTS_CACHE_TTL) return null;
      return entry;
    } catch { return null; }
  }

  function saveResultsToCache(books) {
    try {
      const all = JSON.parse(storage.get(RESULTS_CACHE_STORAGE_KEY) || '{}');
      all[buildCacheKey()] = { books, timestamp: Date.now() };
      const keys = Object.keys(all);
      if (keys.length > 15) {
        keys.sort((a, b) => all[a].timestamp - all[b].timestamp);
        delete all[keys[0]];
      }
      storage.set(RESULTS_CACHE_STORAGE_KEY, JSON.stringify(all));
    } catch { /* quota exceeded */ }
  }

  function clearCachedResults() {
    try {
      const all = JSON.parse(storage.get(RESULTS_CACHE_STORAGE_KEY) || '{}');
      delete all[buildCacheKey()];
      storage.set(RESULTS_CACHE_STORAGE_KEY, JSON.stringify(all));
    } catch {}
  }

  function loadFromCache() {
    const entry = getCachedResults();
    if (!entry || !entry.books || entry.books.length === 0) return false;

    entry.books.forEach(b => {
      allBooks.push(b);
      const id = b.book && b.book.goodreads_id;
      const el = parseHTML(buildBookCard(b, filterBook(b)));
      if (id) bookElements.set(id, el);
      bookGrid.appendChild(el);
    });
    totalBooks = allBooks.length;
    applyView();
    resultsHeader.style.display = 'block';

    const ageMin = Math.round((Date.now() - entry.timestamp) / 60000);
    const ageStr = ageMin < 1 ? 'just now' : `${ageMin} min ago`;
    setProgress(100, `Showing saved results from ${ageStr} — click Refresh to update`);
    setTimeout(() => { document.getElementById('status-area').style.opacity = '0.4'; }, 3000);
    document.getElementById('recs-trigger').style.display = 'block';
    return true;
  }

  const urlLibNames = {};
  params.getAll('library_name').forEach(s => {
    const sep = s.indexOf(':');
    if (sep > 0) urlLibNames[s.slice(0, sep)] = s.slice(sep + 1);
  });

  function loadAliases() {
    try { return JSON.parse(storage.get('benreadin_lib_aliases') || '{}'); } catch { return {}; }
  }
  function saveAlias(key, alias) {
    const aliases = loadAliases();
    if (alias) aliases[key] = alias; else delete aliases[key];
    storage.set('benreadin_lib_aliases', JSON.stringify(aliases));
  }
  window.getLibName = key => {
    const aliases = loadAliases();
    return aliases[key] || urlLibNames[key] || key;
  };

  if (!shelfUrl) {
    showError('No URL provided. Go back and enter a shelf URL.');
    return;
  }

  const baseParams = new URLSearchParams();
  baseParams.set('url', shelfUrl);
  if (shelf) baseParams.set('shelf', shelf);
  libraries.forEach(l => baseParams.append('libraries', l));

  // A stored value the select doesn't offer (e.g. "random" persisted by an
  // older shuffle) would leave the dropdown blank — fall back to the default.
  if (![...sortSelect.options].some(o => o.value === activeSort)) {
    activeSort = 'rating_desc';
  }
  sortSelect.value = activeSort;
  syncFilterUI();
  syncSortUI();

  let activeES = null;
  let streamDone = false;
  // Set once all availability results are on screen; enrichment patches
  // (prices, page counts) may still be streaming after this.
  let availabilityDone = false;
  let skeletonsCleared = false;

  const skeletonCount = Math.ceil(window.innerHeight / 140) + 2;

  function showSkeletons(count) {
    bookGrid.insertAdjacentHTML('beforeend',
      Array.from({length: count}, buildSkeletonCard).join(''));
  }

  function clearSkeletons() {
    if (skeletonsCleared) return;
    skeletonsCleared = true;
    bookGrid.querySelectorAll('.book-card-skeleton').forEach(el => el.remove());
  }

  function resetState() {
    allBooks.length = 0;
    bookElements.clear();
    totalBooks = 0;
    completedBooks = 0;
    bookGrid.innerHTML = '';
    errorBanner.classList.remove('visible');
    resultsHeader.style.display = 'none';
    recsPanel.style.display = 'none';
    recsGrid.innerHTML = '';
    document.getElementById('status-area').style.opacity = '1';
    progressBar.classList.remove('indeterminate');
    const copyBtn = document.getElementById('copy-link-btn');
    if (copyBtn) copyBtn.style.display = 'none';
    document.getElementById('recs-trigger').style.display = 'none';
    document.getElementById('recs-panel').style.display = 'none';
    document.getElementById('recs-grid').innerHTML = '';
    document.getElementById('recs-loading').style.display = 'none';
    if (recsES) { recsES.close(); recsES = null; }
    const recsBtn = document.getElementById('recs-btn');
    if (recsBtn) { recsBtn.disabled = false; recsBtn.textContent = RECS_BTN_IDLE; }
  }

  function startStream(refresh) {
    if (activeES) activeES.close();
    streamDone = false;
    availabilityDone = false;
    skeletonsCleared = false;
    resetState();

    showSkeletons(skeletonCount);
    progressBar.classList.add('indeterminate');
    setProgress(0, 'Starting…');
    showLoadingDetail();

    const p = new URLSearchParams(baseParams);
    if (refresh) p.set('refresh', 'true');
    const es = activeES = new EventSource('/api/search?' + p.toString());

    es.addEventListener('progress', e => {
      const data = JSON.parse(e.data);
      if (data.total) {
        totalBooks = data.total;
        progressBar.classList.remove('indeterminate');
      }
      if (data.completed !== undefined) completedBooks = data.completed;

      if (totalBooks > 0 && completedBooks > 0) {
        const pct = 5 + (completedBooks / totalBooks) * 90;
        setProgress(pct, `Checking ${completedBooks} of ${totalBooks}…`);
      } else if (data.message) {
        setProgress(0, data.message);
      }
    });

    es.addEventListener('book_stubs', e => {
      clearSkeletons();
      hideLoadingDetail();
      const {books: stubs} = JSON.parse(e.data);
      const frag = document.createDocumentFragment();
      stubs.forEach((book, i) => {
        const el = parseHTML(buildStubCard(book, libraries, i));
        if (book.goodreads_id) bookElements.set(book.goodreads_id, el);
        frag.appendChild(el);
      });
      bookGrid.appendChild(frag);
      resultsHeader.style.display = 'block';
    });

    es.addEventListener('book', e => {
      clearSkeletons();
      const event = JSON.parse(e.data);
      allBooks.push(event);

      const grId = event.book && event.book.goodreads_id;
      const show = filterBook(event);
      const newEl = parseHTML(buildBookCard(event, show));

      const oldEl = grId ? bookElements.get(grId) : null;
      if (oldEl) {
        // Swap the stub/old card in-place — no grid rebuild.
        oldEl.replaceWith(newEl);
      } else {
        bookGrid.appendChild(newEl);
      }
      if (grId) bookElements.set(grId, newEl);

      const visibleCount = bookGrid.querySelectorAll('.book-card:not(.book-card--stub):not(.hidden)').length;
      updateCount(visibleCount < allBooks.length ? visibleCount : undefined);
      resultsHeader.style.display = 'block';
    });

    // All availability is on screen; prices/metadata keep patching in via
    // "book_update" below. Finish the progress UI now — this is the moment the
    // user has what they came for.
    es.addEventListener('availability_done', e => {
      const data = JSON.parse(e.data);
      availabilityDone = true;
      setProgress(100, data.message || 'Availability checked');
      // Final sort pass — reorder existing elements without rebuilding HTML.
      applyView();
      window.scrollTo({ top: 0, behavior: 'smooth' });
      document.getElementById('recs-trigger').style.display = 'block';
      createShortlink();
    });

    // Late enrichment patch (Amazon prices, page count, better ISBN/cover) for
    // a book whose availability card is already rendered — swap it in place.
    es.addEventListener('book_update', e => {
      const event = JSON.parse(e.data);
      const grId = event.book && event.book.goodreads_id;
      if (!grId) return;
      const idx = allBooks.findIndex(b => b.book && b.book.goodreads_id === grId);
      if (idx === -1) return;
      allBooks[idx] = event;
      const oldEl = bookElements.get(grId);
      const newEl = parseHTML(buildBookCard(event, filterBook(event)));
      if (oldEl) oldEl.replaceWith(newEl);
      bookElements.set(grId, newEl);
    });

    es.addEventListener('done', e => {
      const data = JSON.parse(e.data);
      streamDone = true;
      setProgress(100, data.message || 'Done');
      es.close();
      saveResultsToCache(allBooks.slice());
      if (!availabilityDone) {
        // Fully-cached run: no availability_done was sent, so finish here.
        applyView();
        window.scrollTo({ top: 0, behavior: 'smooth' });
        document.getElementById('recs-trigger').style.display = 'block';
        createShortlink();
      }
      setTimeout(() => {
        document.getElementById('status-area').style.opacity = '0.4';
      }, 2000);
    });

    es.addEventListener('error', e => {
      try {
        const data = JSON.parse(e.data);
        showError(data.message || 'An error occurred.');
      } catch { /* connection-level error */ }
      es.close();
    });

    es.onerror = () => {
      if (streamDone || es.readyState === EventSource.CLOSED) return;
      if (availabilityDone) {
        // Only the enrichment tail was cut off — everything the user came for
        // is already rendered, so finish quietly instead of alarming them.
        streamDone = true;
        es.close();
        saveResultsToCache(allBooks.slice());
        setProgress(100, 'Done');
        return;
      }
      showError('Connection lost. Please try again.');
      es.close();
    };
  }

  document.getElementById('refresh-btn').addEventListener('click', () => {
    clearCachedResults();
    startStream(true);
  });

  // ---- Library rename (click any .lib-label in results) ----

  let renameCallback = null;

  function showRenameModal(key, cb) {
    renameCallback = cb;
    const modal = document.getElementById('rename-modal');
    const input = document.getElementById('rename-input');
    document.getElementById('rename-hint').textContent = `Key: ${key}`;
    input.value = window.getLibName(key);
    modal.style.display = 'flex';
    setTimeout(() => { input.select(); }, 50);
  }
  function closeRenameModal() {
    document.getElementById('rename-modal').style.display = 'none';
    renameCallback = null;
  }
  function commitRename() {
    const val = document.getElementById('rename-input').value.trim();
    if (renameCallback) renameCallback(val || null);
    closeRenameModal();
  }

  document.getElementById('rename-cancel').addEventListener('click', closeRenameModal);
  document.getElementById('rename-save').addEventListener('click', commitRename);
  document.getElementById('rename-input').addEventListener('keydown', e => {
    if (e.key === 'Enter') commitRename();
    if (e.key === 'Escape') closeRenameModal();
  });
  document.getElementById('rename-modal').addEventListener('click', e => {
    if (e.target === document.getElementById('rename-modal')) closeRenameModal();
  });

  bookGrid.addEventListener('click', e => {
    const label = e.target.closest('.lib-label');
    if (!label) return;
    const key = label.dataset.libkey;
    showRenameModal(key, alias => {
      saveAlias(key, alias);
      document.querySelectorAll(`.lib-label[data-libkey="${CSS.escape(key)}"]`).forEach(el => {
        el.textContent = window.getLibName(key);
      });
    });
  });

  recsToggle.addEventListener('click', () => {
    const expanded = recsToggle.getAttribute('aria-expanded') === 'true';
    recsToggle.setAttribute('aria-expanded', String(!expanded));
    recsToggle.textContent = expanded ? 'Show' : 'Hide';
    recsGrid.style.display = expanded ? 'none' : '';
  });

  // ---- Recommendations ----

  // Recs render as ordinary book cards (same component as search results) —
  // only the provenance line is rec-specific.
  function buildRecCard(rec) {
    const because = rec.because_series || rec.because_author
      || (rec.because_subject ? `Matches your ${rec.because_subject} reading` : '');
    const becauseHtml = because ? `<div class="rec-because">${escHtml(because)}</div>` : '';
    return buildBookCard(rec, true, becauseHtml);
  }

  const RECS_BTN_IDLE = 'Recommend me more books ↓';

  let recsES = null;

  document.getElementById('recs-btn').addEventListener('click', () => {
    const btn = document.getElementById('recs-btn');
    const trigger = document.getElementById('recs-trigger');
    const loading = document.getElementById('recs-loading');

    recsPanel.style.display = 'block';
    // Jump the user to the section immediately — otherwise there's no
    // indication anything happened until books start streaming in below
    // the (possibly long) main results grid.
    recsPanel.scrollIntoView({ behavior: 'smooth', block: 'start' });
    recsPanel.classList.add('recs-panel--highlight');
    setTimeout(() => recsPanel.classList.remove('recs-panel--highlight'), 1400);

    if (recsES) return; // already running from a previous click

    btn.disabled = true;
    btn.textContent = 'Finding books for you…';
    loading.style.display = 'flex';
    recsGrid.innerHTML = '';

    let recsFound = 0;
    const p = new URLSearchParams(baseParams);
    const es = recsES = new EventSource('/api/recommendations?' + p.toString());

    const loadingLabel = document.getElementById('recs-loading-label');
    es.addEventListener('rec_progress', e => {
      const data = JSON.parse(e.data);
      const label = { profile: 'Reading your shelves…', series: 'Checking series you’re reading…', authors: 'Checking authors you’ve enjoyed…' }[data.stage] || 'Finding books for you…';
      if (loadingLabel) loadingLabel.textContent = label;
    });

    es.addEventListener('rec', e => {
      const rec = JSON.parse(e.data);
      recsFound++;
      recsGrid.insertAdjacentHTML('beforeend', buildRecCard(rec));
    });

    es.addEventListener('recs_done', () => {
      es.close();
      recsES = null;
      trigger.style.display = 'none';
      loading.style.display = 'none';
      if (recsFound === 0) {
        recsGrid.innerHTML = '<p style="color:var(--text-muted);font-size:.875rem;padding:8px 0;">No available-now Kindle matches at your libraries right now — try again later or add more books to your shelves.</p>';
      }
    });

    es.addEventListener('error', e => {
      try {
        const data = JSON.parse(e.data);
        recsGrid.innerHTML = `<p style="color:var(--red);font-size:.875rem;padding:8px 0;">${escHtml(data.message || 'Failed to load recommendations.')}</p>`;
      } catch { /* connection-level error, handled below */ }
    });

    es.onerror = () => {
      if (!recsES) return; // already closed via recs_done
      recsES = null;
      loading.style.display = 'none';
      btn.disabled = false;
      btn.textContent = RECS_BTN_IDLE;
      if (recsFound === 0) {
        recsGrid.innerHTML = '<p style="color:var(--red);font-size:.875rem;padding:8px 0;">Failed to load recommendations. Please try again.</p>';
      }
      es.close();
    };
  });

  if (!loadFromCache()) {
    startStream(false);
  }

  // ---- Shortlink ----

  async function createShortlink() {
    try {
      const res = await fetch('/api/shorten', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: shelfUrl, libraries, shelf }),
      });
      if (!res.ok) return;
      const { link } = await res.json();
      const fullLink = window.location.origin + link;
      const btn = document.getElementById('copy-link-btn');
      if (!btn) return;
      btn.dataset.href = fullLink;
      btn.style.display = 'inline-flex';
      btn.addEventListener('click', () => {
        if (navigator.share) {
          navigator.share({ title: 'BenReadin results', url: fullLink }).catch(() => {});
          return;
        }
        navigator.clipboard.writeText(fullLink).then(() => {
          const span = btn.querySelector('.btn-text') || btn;
          span.textContent = 'Copied!';
          setTimeout(() => { span.textContent = 'Copy link'; }, 2000);
        });
      });
    } catch { /* non-critical */ }
  }

  // ---- Keyboard shortcuts ----

  document.addEventListener('keydown', e => {
    // Skip when focus is inside an input.
    if (['INPUT', 'SELECT', 'TEXTAREA'].includes(document.activeElement.tagName)) return;
    if (e.key === '/' && !e.ctrlKey && !e.metaKey) {
      e.preventDefault();
      sortSelect.focus();
    }
    if (e.key === '1') filterBtns[0] && filterBtns[0].click();
    if (e.key === '2') filterBtns[1] && filterBtns[1].click();
    if (e.key === '3') filterBtns[2] && filterBtns[2].click();
    if (e.key === '4') filterBtns[3] && filterBtns[3].click();
  });
})();
