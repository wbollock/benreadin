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
  // True when sort/filter changed mid-stream so the final renderGrid is needed
  let gridDirty     = false;

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
      gridDirty = true;
      renderGrid();
    });
  });

  sortSelect.addEventListener('change', () => {
    activeSort = sortSelect.value;
    gridDirty = true;
    renderGrid();
  });

  // ---- SSE ----

  const params = new URLSearchParams(window.location.search);
  const shelfUrl  = params.get('url');
  const libraries = params.getAll('libraries');

  if (!shelfUrl) {
    showError('No URL provided. Go back and enter a shelf URL.');
    return;
  }

  const sseParams = new URLSearchParams();
  sseParams.set('url', shelfUrl);
  libraries.forEach(l => sseParams.append('libraries', l));

  setProgress(5, 'Connecting...');

  const es = new EventSource('/api/search?' + sseParams.toString());

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
    // Incremental append while streaming (avoid full re-render on every book)
    if (!gridDirty) {
      if (filterBook(event)) {
        bookGrid.insertAdjacentHTML('beforeend', buildBookCard(event, true));
      }
      updateCount(bookGrid.querySelectorAll('.book-card').length);
      resultsHeader.style.display = 'block';
    } else {
      // Sort or filter changed mid-stream — re-render to keep order/visibility correct
      renderGrid();
    }
  });

  es.addEventListener('done', e => {
    const data = JSON.parse(e.data);
    setProgress(100, data.message || 'Done');
    es.close();
    // Only re-render if sort/filter changed during streaming; otherwise the
    // incremental appends are already correct and a full rebuild would cause a flash.
    if (gridDirty) {
      renderGrid();
      gridDirty = false;
    }
    setTimeout(() => {
      document.getElementById('status-area').style.opacity = '0.4';
    }, 2000);
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

  recsToggle.addEventListener('click', () => {
    const expanded = recsToggle.getAttribute('aria-expanded') === 'true';
    recsToggle.setAttribute('aria-expanded', String(!expanded));
    recsToggle.textContent = expanded ? 'Show' : 'Hide';
    recsGrid.style.display = expanded ? 'none' : '';
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
})();
