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
  const recsPanel      = document.getElementById('recs-panel');
  const recsGrid       = document.getElementById('recs-grid');
  const recsToggle     = document.getElementById('recs-toggle');

  // All received book events, in arrival order (= shelf order)
  const allBooks = [];
  let totalBooks    = 0;
  let completedBooks = 0;
  // Multi-select filters: a Set of active filter keys. Empty = show all.
  const activeFilters = new Set();
  // AND vs OR logic when multiple filters are active.
  const FILTER_MODE_KEY = 'benreadin_filter_mode';
  let filterMode = localStorage.getItem(FILTER_MODE_KEY) || 'or';
  // Persist sort preference in localStorage; default to available_first.
  const SORT_KEY = 'benreadin_sort';
  let activeSort = localStorage.getItem(SORT_KEY) || 'available_first';

  // ---- Utilities ----

  function showError(msg) {
    errorBanner.textContent = msg;
    errorBanner.classList.add('visible');
    setProgress(0, msg);
  }

  function setProgress(pct, label) {
    progressBar.style.width = Math.min(100, pct) + '%';
    if (label !== undefined) progressLabel.textContent = label;
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
      // True only when at least one library has it available AND no library has a wait.
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
    const filters = [...activeFilters];
    if (filterMode === 'and') {
      return filters.every(f => bookMatchesFilter(event, f));
    }
    return filters.some(f => bookMatchesFilter(event, f));
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
      case 'kindle_first': {
        // Books where any library has Kindle delivery come first, then available, then rest.
        const statusOrder = { available: 0, wait: 1, unavailable: 2, not_found: 3 };
        copy.sort((a, b) => {
          const aK = (a.library_results || []).some(lr => lr.has_kindle) ? 0 : 1;
          const bK = (b.library_results || []).some(lr => lr.has_kindle) ? 0 : 1;
          if (aK !== bK) return aK - bK;
          // Within same Kindle group, sort available-first
          const sa = statusOrder[bestStatus(a.library_results)] ?? 3;
          const sb = statusOrder[bestStatus(b.library_results)] ?? 3;
          return sa - sb;
        });
        break;
      }
      case 'unavailable_first': {
        const order = { available: 0, wait: 1, unavailable: 2, not_found: 3 };
        copy.sort((a, b) => {
          const sa = order[bestStatus(a.library_results)] ?? 3;
          const sb = order[bestStatus(b.library_results)] ?? 3;
          return sb - sa;
        });
        break;
      }
      case 'wait_asc':
        copy.sort((a, b) => {
          const wa = bestStatus(a.library_results) === 'available' ? -Infinity : minWait(a.library_results);
          const wb = bestStatus(b.library_results) === 'available' ? -Infinity : minWait(b.library_results);
          return wa - wb;
        });
        break;
      case 'wait_desc':
        copy.sort((a, b) => {
          const wa = minWait(a.library_results);
          const wb = minWait(b.library_results);
          return wb - wa;
        });
        break;
      case 'rating_desc':
        copy.sort((a, b) => (b.book.average_rating || 0) - (a.book.average_rating || 0));
        break;
      case 'rating_asc':
        copy.sort((a, b) => (a.book.average_rating || 0) - (b.book.average_rating || 0));
        break;
      case 'user_rating_desc':
        copy.sort((a, b) => (b.book.user_rating || 0) - (a.book.user_rating || 0));
        break;
      case 'user_rating_asc':
        copy.sort((a, b) => (a.book.user_rating || 0) - (b.book.user_rating || 0));
        break;
      case 'pages_asc':
        copy.sort((a, b) => (a.book.page_count || 99999) - (b.book.page_count || 99999));
        break;
      case 'pages_desc':
        copy.sort((a, b) => (b.book.page_count || 0) - (a.book.page_count || 0));
        break;
      case 'title_asc':
        copy.sort((a, b) => a.book.title.localeCompare(b.book.title));
        break;
      case 'title_desc':
        copy.sort((a, b) => b.book.title.localeCompare(a.book.title));
        break;
      default: // shelf order — arrival order
        break;
    }
    return copy;
  }

  function renderGrid() {
    const books = sortedBooks();
    const visible = books.filter(filterBook);

    bookGrid.innerHTML = books.map((event, i) => {
      const show = filterBook(event);
      // Pass index as animation delay so re-renders don't all flash at once
      return buildBookCard(event, show);
    }).join('');

    updateCount(visible.length);
  }

  function updateCount(visibleCount) {
    if (totalBooks > 0) {
      resultsHeader.style.display = 'block';
      const ready = allBooks.length;
      const streaming = ready < totalBooks;
      if (streaming) {
        // Show how many have been checked out of total while stubs are filling in.
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
    const modeToggle = document.getElementById('filter-mode-toggle');
    if (modeToggle) {
      // Only show when 2+ filters are active.
      modeToggle.style.visibility = activeFilters.size >= 2 ? 'visible' : 'hidden';
      modeToggle.querySelectorAll('.filter-mode-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.mode === filterMode);
      });
    }
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
      syncFilterUI();
      renderGrid();
    });
  });

  sortSelect.addEventListener('change', () => {
    activeSort = sortSelect.value;
    localStorage.setItem(SORT_KEY, activeSort);
    renderGrid();
  });

  document.getElementById('filter-mode-toggle').addEventListener('click', e => {
    const btn = e.target.closest('.filter-mode-btn');
    if (!btn) return;
    filterMode = btn.dataset.mode;
    localStorage.setItem(FILTER_MODE_KEY, filterMode);
    syncFilterUI();
    renderGrid();
  });

  // ---- SSE ----

  const params = new URLSearchParams(window.location.search);
  const shelfUrl  = params.get('url');
  const libraries = params.getAll('libraries');

  // ---- Client-side results cache (1-hour TTL) ----

  const RESULTS_CACHE_STORAGE_KEY = 'benreadin_results_v1';
  const RESULTS_CACHE_TTL = 60 * 60 * 1000;

  function buildCacheKey() {
    return shelfUrl + '|' + [...libraries].sort().join(',');
  }

  function getCachedResults() {
    try {
      const all = JSON.parse(localStorage.getItem(RESULTS_CACHE_STORAGE_KEY) || '{}');
      const entry = all[buildCacheKey()];
      if (!entry || Date.now() - entry.timestamp > RESULTS_CACHE_TTL) return null;
      return entry;
    } catch { return null; }
  }

  function saveResultsToCache(books) {
    try {
      const all = JSON.parse(localStorage.getItem(RESULTS_CACHE_STORAGE_KEY) || '{}');
      all[buildCacheKey()] = { books, timestamp: Date.now() };
      // Evict oldest entries if we have too many
      const keys = Object.keys(all);
      if (keys.length > 15) {
        keys.sort((a, b) => all[a].timestamp - all[b].timestamp);
        delete all[keys[0]];
      }
      localStorage.setItem(RESULTS_CACHE_STORAGE_KEY, JSON.stringify(all));
    } catch { /* quota exceeded — non-critical */ }
  }

  function clearCachedResults() {
    try {
      const all = JSON.parse(localStorage.getItem(RESULTS_CACHE_STORAGE_KEY) || '{}');
      delete all[buildCacheKey()];
      localStorage.setItem(RESULTS_CACHE_STORAGE_KEY, JSON.stringify(all));
    } catch {}
  }

  function loadFromCache() {
    const entry = getCachedResults();
    if (!entry || !entry.books || entry.books.length === 0) return false;

    entry.books.forEach(b => allBooks.push(b));
    totalBooks = allBooks.length;

    renderGrid();
    resultsHeader.style.display = 'block';

    const ageMin = Math.round((Date.now() - entry.timestamp) / 60000);
    const ageStr = ageMin < 1 ? 'just now' : `${ageMin} min ago`;
    setProgress(100, `Showing saved results from ${ageStr} — click Refresh to update`);
    setTimeout(() => { document.getElementById('status-area').style.opacity = '0.4'; }, 3000);
    document.getElementById('recs-trigger').style.display = 'block';
    return true;
  }

  // Build key→name map from URL params (set by search page).
  const urlLibNames = {};
  params.getAll('library_name').forEach(s => {
    const sep = s.indexOf(':');
    if (sep > 0) urlLibNames[s.slice(0, sep)] = s.slice(sep + 1);
  });

  // Aliases override URL names; URL names override raw keys.
  function loadAliases() {
    try { return JSON.parse(localStorage.getItem('benreadin_lib_aliases') || '{}'); } catch { return {}; }
  }
  function saveAlias(key, alias) {
    const aliases = loadAliases();
    if (alias) aliases[key] = alias; else delete aliases[key];
    localStorage.setItem('benreadin_lib_aliases', JSON.stringify(aliases));
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
  libraries.forEach(l => baseParams.append('libraries', l));

  // Sync the select element to the persisted sort value.
  sortSelect.value = activeSort;
  // Sync the AND/OR toggle to the persisted filter mode.
  syncFilterUI();

  let activeES = null;
  let streamDone = false;
  let skeletonsCleared = false;

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
  }

  function startStream(refresh) {
    if (activeES) activeES.close();
    streamDone = false;
    skeletonsCleared = false;
    resetState();

    // Show skeleton cards immediately — before the SSE connection even opens —
    // so the user sees a populated grid rather than a blank page.
    showSkeletons(12);
    progressBar.classList.add('indeterminate');
    setProgress(0, 'Fetching your shelf…');

    const p = new URLSearchParams(baseParams);
    if (refresh) p.set('refresh', 'true');
    const es = activeES = new EventSource('/api/search?' + p.toString());

    es.addEventListener('progress', e => {
      const data = JSON.parse(e.data);
      if (data.total) {
        totalBooks = data.total;
        // Switch from sweeping indeterminate bar to a real percentage bar
        // now that we know how many books there are.
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

    // book_stubs: single batch event sent the instant the Goodreads shelf is
    // fetched. Renders the entire book list at once (one DOM operation) with a
    // staggered cascade animation so the grid floods in smoothly.
    es.addEventListener('book_stubs', e => {
      clearSkeletons();
      const {books: stubs} = JSON.parse(e.data);
      bookGrid.insertAdjacentHTML('beforeend',
        stubs.map((book, i) => buildStubCard(book, libraries, i)).join(''));
      resultsHeader.style.display = 'block';
    });

    es.addEventListener('book', e => {
      clearSkeletons();
      const event = JSON.parse(e.data);
      allBooks.push(event);

      const grId = event.book && event.book.goodreads_id;
      const stubEl = grId ? bookGrid.querySelector(`[data-grid="${CSS.escape(grId)}"]`) : null;

      if (filterBook(event)) {
        const html = buildBookCard(event, true);
        if (stubEl) {
          // Replace the stub card in-place with the full card.
          stubEl.outerHTML = html;
        } else {
          // Cache hit (no stub was sent): use sort-aware insertion.
          const status = bestStatus(event.library_results);
          if (activeSort === 'available_first' && status === 'available') {
            const firstNonAvail = bookGrid.querySelector('.book-card[data-status="wait"], .book-card[data-status="unavailable"], .book-card[data-status="not_found"]');
            if (firstNonAvail) {
              firstNonAvail.insertAdjacentHTML('beforebegin', html);
            } else {
              bookGrid.insertAdjacentHTML('beforeend', html);
            }
          } else {
            bookGrid.insertAdjacentHTML('beforeend', html);
          }
        }
      } else if (stubEl) {
        // Book doesn't pass current filter — remove the stub.
        stubEl.remove();
      }

      // Count only completed (non-stub) cards for the visible count.
      const visibleCount = bookGrid.querySelectorAll('.book-card:not(.book-card--stub)').length;
      updateCount(visibleCount < allBooks.length ? visibleCount : undefined);
      resultsHeader.style.display = 'block';
    });

    es.addEventListener('done', e => {
      const data = JSON.parse(e.data);
      streamDone = true;
      setProgress(100, data.message || 'Done');
      es.close();
      // Save results to client cache so return visits render instantly.
      saveResultsToCache(allBooks.slice());
      // One final render to apply exact sort order and correct counts.
      renderGrid();
      // Scroll to top so user sees the sorted results from the beginning.
      window.scrollTo({ top: 0, behavior: 'smooth' });
      setTimeout(() => {
        document.getElementById('status-area').style.opacity = '0.4';
      }, 2000);
      // Show the recommendations trigger button.
      document.getElementById('recs-trigger').style.display = 'block';
      // Create a shortlink for easy sharing/bookmarking.
      createShortlink();
    });

    // No inline recommendations — they're loaded on demand via the button.

    es.addEventListener('error', e => {
      try {
        const data = JSON.parse(e.data);
        showError(data.message || 'An error occurred.');
      } catch { /* connection-level error */ }
      es.close();
    });

    es.onerror = () => {
      // Ignore errors after the stream completed normally — the browser's
      // EventSource auto-reconnect fires onerror when the server closes the
      // connection after sending 'done', but we don't want to show an error.
      if (streamDone || es.readyState === EventSource.CLOSED) return;
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

  // Event delegation — clicking a lib label opens rename.
  bookGrid.addEventListener('click', e => {
    const label = e.target.closest('.lib-label');
    if (!label) return;
    const key = label.dataset.libkey;
    showRenameModal(key, alias => {
      saveAlias(key, alias);
      // Update all matching labels on the page without a full re-render.
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

  function renderRecs(recs) {
    if (!recs || recs.length === 0) {
      recsGrid.innerHTML = '<p style="color:var(--text-muted);font-size:.875rem;padding:8px 0;">No recommendations found — try adding more books to your shelf or checking a different library.</p>';
      recsPanel.style.display = 'block';
      return;
    }
    recsGrid.innerHTML = recs.map(rec => {
      const cover = rec.cover_url
        ? `<img src="${escHtml(rec.cover_url)}" alt="${escHtml(rec.title)}" loading="lazy" onerror="this.parentElement.innerHTML='<div class=\\'rec-cover-placeholder\\'>📚</div>'">`
        : `<div class="rec-cover-placeholder">📚</div>`;
      const libBadges = (rec.library_results || [])
        .filter(lr => lr.status === 'available')
        .map(lr => `<span class="badge badge-available" title="${escHtml(window.getLibName(lr.library_key))}">&#10003; ${escHtml(window.getLibName(lr.library_key))}</span>`)
        .join('');
      const because = rec.because_of_title
        ? `<div class="rec-because">Similar to <em>${escHtml(rec.because_of_title)}</em></div>`
        : '';
      return `
        <div class="rec-card">
          <div class="rec-cover">${cover}</div>
          <div class="rec-info">
            <div class="rec-title">${escHtml(rec.title)}</div>
            <div class="rec-author">${escHtml(rec.author)}</div>
            ${because}
            <div class="rec-badges">${libBadges}</div>
          </div>
        </div>`;
    }).join('');
    recsPanel.style.display = 'block';
  }

  document.getElementById('recs-btn').addEventListener('click', async () => {
    const btn = document.getElementById('recs-btn');
    const trigger = document.getElementById('recs-trigger');
    const loading = document.getElementById('recs-loading');
    btn.disabled = true;
    btn.textContent = 'Finding recommendations…';
    loading.style.display = 'flex';
    recsPanel.style.display = 'block';
    try {
      const p = new URLSearchParams(baseParams);
      const res = await fetch('/api/recommendations?' + p.toString());
      if (!res.ok) throw new Error('Request failed');
      const recs = await res.json();
      trigger.style.display = 'none';
      loading.style.display = 'none';
      renderRecs(recs);
    } catch (err) {
      loading.style.display = 'none';
      btn.disabled = false;
      btn.textContent = '✨ Find recommendations based on your shelf';
      recsGrid.innerHTML = '<p style="color:var(--red);font-size:.875rem;padding:8px 0;">Failed to load recommendations. Please try again.</p>';
    }
  });

  // Load from client cache for instant results; fall back to SSE.
  if (!loadFromCache()) {
    startStream(false);
  }

  // ---- Shortlink ----

  async function createShortlink() {
    try {
      const res = await fetch('/api/shorten', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: shelfUrl, libraries }),
      });
      if (!res.ok) return;
      const { link } = await res.json();
      const fullLink = window.location.origin + link;
      const btn = document.getElementById('copy-link-btn');
      if (!btn) return;
      btn.dataset.href = fullLink;
      btn.style.display = 'inline-flex';
      btn.addEventListener('click', () => {
        navigator.clipboard.writeText(fullLink).then(() => {
          btn.textContent = 'Copied!';
          setTimeout(() => { btn.textContent = 'Copy link'; }, 2000);
        });
      });
    } catch { /* non-critical */ }
  }
})();
