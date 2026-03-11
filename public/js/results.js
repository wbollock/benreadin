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
  let activeFilter  = 'all';
  let activeSort    = 'default';

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
    // Return the "best" status across all libraries for a book.
    if (!libraryResults || libraryResults.length === 0) return 'not_found';
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

  function filterBook(event) {
    const status = bestStatus(event.library_results);
    if (activeFilter === 'all') return true;
    if (activeFilter === 'available') return status === 'available';
    if (activeFilter === 'wait') return status === 'wait';
    if (activeFilter === 'not_found') return status === 'not_found' || status === 'unavailable';
    return true;
  }

  function sortedBooks() {
    const copy = allBooks.slice();
    switch (activeSort) {
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
        copy.sort((a, b) => {
          const wa = bestStatus(a.library_results) === 'available' ? -Infinity : minWait(a.library_results);
          const wb = bestStatus(b.library_results) === 'available' ? -Infinity : minWait(b.library_results);
          return wa - wb;
        });
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
      if (visibleCount !== undefined && visibleCount !== allBooks.length) {
        resultsCount.textContent = `(${visibleCount} of ${allBooks.length})`;
      } else {
        resultsCount.textContent = `(${allBooks.length})`;
      }
    }
  }

  // ---- Filter / sort event listeners ----

  filterBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      filterBtns.forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      activeFilter = btn.dataset.filter;
      renderGrid();
    });
  });

  sortSelect.addEventListener('change', () => {
    activeSort = sortSelect.value;
    renderGrid();
  });

  // ---- SSE ----

  const params = new URLSearchParams(window.location.search);
  const shelfUrl  = params.get('url');
  const libraries = params.getAll('libraries');

  // Build key→name map from URL params (set by search page).
  const urlLibNames = {};
  params.getAll('library_name').forEach(s => {
    const sep = s.indexOf(':');
    if (sep > 0) urlLibNames[s.slice(0, sep)] = s.slice(sep + 1);
  });

  // Aliases override URL names; URL names override raw keys.
  function loadAliases() {
    try { return JSON.parse(localStorage.getItem('shelfprice_lib_aliases') || '{}'); } catch { return {}; }
  }
  function saveAlias(key, alias) {
    const aliases = loadAliases();
    if (alias) aliases[key] = alias; else delete aliases[key];
    localStorage.setItem('shelfprice_lib_aliases', JSON.stringify(aliases));
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

  let activeES = null;

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
    const copyBtn = document.getElementById('copy-link-btn');
    if (copyBtn) copyBtn.style.display = 'none';
  }

  function startStream(refresh) {
    if (activeES) activeES.close();
    resetState();
    setProgress(5, 'Connecting...');

    const p = new URLSearchParams(baseParams);
    if (refresh) p.set('refresh', 'true');
    const es = activeES = new EventSource('/api/search?' + p.toString());

    es.addEventListener('progress', e => {
      const data = JSON.parse(e.data);
      if (data.total) totalBooks = data.total;
      if (data.completed !== undefined) completedBooks = data.completed;

      if (totalBooks > 0 && completedBooks > 0) {
        const pct = 5 + (completedBooks / totalBooks) * 90;
        setProgress(pct, `Checking book ${completedBooks} of ${totalBooks}...`);
      } else if (data.message) {
        setProgress(10, data.message);
      }
    });

    es.addEventListener('book', e => {
      const event = JSON.parse(e.data);
      allBooks.push(event);
      // Always append incrementally during streaming — no full re-render per book.
      // Sort order is applied once when the stream ends.
      if (filterBook(event)) {
        bookGrid.insertAdjacentHTML('beforeend', buildBookCard(event, true));
      }
      const visibleCount = bookGrid.querySelectorAll('.book-card').length;
      updateCount(visibleCount < allBooks.length ? visibleCount : undefined);
      resultsHeader.style.display = 'block';
    });

    es.addEventListener('done', e => {
      const data = JSON.parse(e.data);
      setProgress(100, data.message || 'Done');
      es.close();
      // One final render to apply sort order and correct counts.
      renderGrid();
      setTimeout(() => {
        document.getElementById('status-area').style.opacity = '0.4';
      }, 2000);
      // Create a shortlink for easy sharing/bookmarking.
      createShortlink();
    });

    es.addEventListener('recommendations', e => {
      const recs = JSON.parse(e.data);
      if (!recs || recs.length === 0) return;

      recsGrid.innerHTML = recs.map(rec => {
        const cover = rec.cover_url
          ? `<img src="${escHtml(rec.cover_url)}" alt="${escHtml(rec.title)}" loading="lazy" onerror="this.parentElement.innerHTML='<div class=\\'rec-cover-placeholder\\'>📚</div>'">`
          : `<div class="rec-cover-placeholder">📚</div>`;

        const libBadges = (rec.library_results || [])
          .filter(lr => lr.status === 'available')
          .map(lr => `<span class="badge badge-available" title="${escHtml(lr.library_key)}">&#10003; ${escHtml(lr.library_key)}</span>`)
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
    });

    es.addEventListener('error', e => {
      try {
        const data = JSON.parse(e.data);
        showError(data.message || 'An error occurred.');
      } catch { /* connection-level error */ }
      es.close();
    });

    es.onerror = () => {
      if (es.readyState === EventSource.CLOSED) return;
      showError('Connection lost. Please try again.');
      es.close();
    };
  }

  document.getElementById('refresh-btn').addEventListener('click', () => {
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

  startStream(false);

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
