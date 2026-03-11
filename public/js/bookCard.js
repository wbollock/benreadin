'use strict';

function escHtml(s) {
  return String(s || '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function formatPrice(n) {
  if (!n || n === 0) return null;
  return '$' + n.toFixed(2);
}

function availabilityBadge(result) {
  const kindle = result.has_kindle ? ' <span class="badge-kindle-pill" title="Kindle delivery available">⚡K</span>' : '';
  switch (result.status) {
    case 'available':
      return `<span class="badge badge-available">&#10003; Available${result.available_copies > 0 ? ` (${result.available_copies})` : ''}${kindle}</span>`;
    case 'wait':
      return `<span class="badge badge-wait">&#8987; ${result.estimated_wait_days > 0 ? result.estimated_wait_days + '-day wait' : 'On hold'}${kindle}</span>`;
    case 'unavailable':
      return `<span class="badge badge-unavail">&#10005; Not in catalog</span>`;
    default:
      return `<span class="badge badge-notfound">Not found</span>`;
  }
}

function buildBookCard(event, show) {
  const { book, library_results, amazon_result, gutenberg_result } = event;

  // Library rows — badge links to OverDrive page where Kindle delivery is available
  const libRows = (library_results || []).map(lr => {
    const displayName = (typeof window.getLibName === 'function')
      ? window.getLibName(lr.library_key)
      : (lr.library_name || lr.library_key);
    return `
    <div class="library-row">
      <span class="lib-label" data-libkey="${escHtml(lr.library_key)}" title="Click to rename">${escHtml(displayName)}</span>
      ${lr.overdrive_url
        ? `<a href="${escHtml(lr.overdrive_url)}" target="_blank" rel="noopener" style="text-decoration:none" title="${lr.status === 'available' ? 'Borrow on Libby — Kindle delivery available' : 'View on OverDrive'}">${availabilityBadge(lr)}</a>`
        : availabilityBadge(lr)
      }
    </div>`;
  }).join('');

  // ---- Price pills ----
  let pricePills = '';
  const affiliateTag = amazon_result?.affiliate_url
    ? (amazon_result.affiliate_url.match(/tag=([^&]+)/)?.[1] || '') : '';

  if (gutenberg_result) {
    pricePills += `<span class="price-pill price-pill-free">Free · Project Gutenberg</span>`;
  }
  if (amazon_result?.available) {
    const kindle    = formatPrice(amazon_result.kindle_price);
    const paperback = formatPrice(amazon_result.paperback_price);
    const hardcover = formatPrice(amazon_result.hardcover_price);
    if (kindle)    pricePills += `<span class="price-pill"><span class="pill-label">Kindle</span><span class="pill-amount">${kindle}</span></span>`;
    if (paperback) pricePills += `<span class="price-pill"><span class="pill-label">Paperback</span><span class="pill-amount">${paperback}</span></span>`;
    if (hardcover) pricePills += `<span class="price-pill"><span class="pill-label">Hardcover</span><span class="pill-amount">${hardcover}</span></span>`;
  }
  if (pricePills) pricePills = `<div class="price-row">${pricePills}</div>`;

  // ---- Goodreads link ----
  const grUrl = book.goodreads_id
    ? `https://www.goodreads.com/book/show/${escHtml(book.goodreads_id)}`
    : null;

  // ---- Action buttons ----
  // Group 1: borrow / get for free (primary)
  let primaryBtns = '';
  // Group 2: kindle actions
  let kindleBtns = '';

  // Best available library link
  const availLib = (library_results || []).find(lr => lr.status === 'available' && lr.overdrive_url);
  const anyLib   = (library_results || []).find(lr => lr.overdrive_url);
  if (availLib) {
    primaryBtns += `<a href="${escHtml(availLib.overdrive_url)}" target="_blank" rel="noopener" class="btn-borrow">Borrow on Libby ↗</a>`;
  } else if (anyLib) {
    primaryBtns += `<a href="${escHtml(anyLib.overdrive_url)}" target="_blank" rel="noopener" class="btn-secondary btn-sm">View on Libby ↗</a>`;
  }

  if (gutenberg_result) {
    primaryBtns += `<a href="${escHtml(gutenberg_result.epub_url)}" target="_blank" rel="noopener" class="btn-gutenberg-sm" title="Download EPUB, then send via Send to Kindle app or your @kindle.com email">Download EPUB ↗</a>`;
  }

  // Kindle buttons
  const isbn = book.isbn13 || book.isbn10;
  const kindleSearchQ = isbn ? isbn : (book.title + ' ' + book.author);
  const kindleSearchUrl = `https://www.amazon.com/s?k=${encodeURIComponent(kindleSearchQ)}&i=digital-text`;

  if (amazon_result?.available && amazon_result.kindle_asin) {
    const kindleUrl = `https://www.amazon.com/dp/${escHtml(amazon_result.kindle_asin)}?tag=${encodeURIComponent(affiliateTag)}`;
    kindleBtns += `<a href="${kindleUrl}" target="_blank" rel="noopener" class="btn-kindle">Buy for Kindle</a>`;
    kindleBtns += `<a href="${kindleUrl}#sampleButton" target="_blank" rel="noopener" class="btn-secondary btn-sm" title="Opens Amazon — click 'Send sample' to push a preview to your Kindle">Send Preview ↗</a>`;
  } else {
    kindleBtns += `<a href="${kindleSearchUrl}" target="_blank" rel="noopener" class="btn-kindle">Find on Kindle ↗</a>`;
  }

  // Send Preview fallback — no ASIN, use ISBN field search which usually lands on the product page
  if (!(amazon_result?.available && amazon_result.kindle_asin)) {
    const previewUrl = isbn
      ? `https://www.amazon.com/gp/search?index=digital-text&field-isbn=${encodeURIComponent(isbn)}`
      : `https://www.amazon.com/s?k=${encodeURIComponent(kindleSearchQ)}&i=digital-text`;
    kindleBtns += `<a href="${previewUrl}" target="_blank" rel="noopener" class="btn-secondary btn-sm" title="Opens Kindle product page — click 'Send a free sample' to push a preview to your Kindle">Send Preview ↗</a>`;
  }

  if (amazon_result?.affiliate_url) {
    kindleBtns += `<a href="${escHtml(amazon_result.affiliate_url)}" target="_blank" rel="noopener" class="btn-secondary btn-sm">Amazon ↗</a>`;
  }
  if (grUrl) {
    kindleBtns += `<a href="${grUrl}" target="_blank" rel="noopener" class="btn-secondary btn-sm" title="View on Goodreads">GR ↗</a>`;
  }

  const actionButtons = (primaryBtns || kindleBtns)
    ? `<div class="card-actions">
        ${primaryBtns ? `<div class="card-actions-group">${primaryBtns}</div>` : ''}
        <div class="card-actions-group">${kindleBtns}</div>
       </div>`
    : '';

  // Cover image
  const cover = book.cover_url
    ? `<img src="${escHtml(book.cover_url)}" alt="${escHtml(book.title)}" loading="lazy" onerror="this.parentElement.innerHTML='<div class=\\'book-cover-placeholder\\'>📚</div>'">`
    : `<div class="book-cover-placeholder">📚</div>`;

  const description = book.description
    ? `<details class="book-description-details"><summary>About this book</summary><p class="book-description">${escHtml(book.description)}</p></details>`
    : '';

  const pageCount = book.page_count
    ? `<span class="book-pages">${book.page_count} pp</span>`
    : '';

  const avgRating = book.average_rating
    ? `<span class="rating-avg" title="Goodreads average rating">★ ${book.average_rating.toFixed(2)}</span>`
    : '';
  const userRating = book.user_rating
    ? `<span class="rating-user" title="Your Goodreads rating">${'★'.repeat(book.user_rating)}${'☆'.repeat(5 - book.user_rating)}</span>`
    : '';
  const ratingStr = (avgRating || userRating)
    ? `<div class="book-rating-row">${avgRating}${userRating}</div>`
    : '';

  // Determine best status for data-status attribute (used for live sort insertion).
  const statuses = (library_results || []).map(lr => lr.status);
  const statusPriority = { available: 0, wait: 1, unavailable: 2, not_found: 3 };
  const bestSt = statuses.reduce((best, s) => (statusPriority[s] ?? 3) < (statusPriority[best] ?? 3) ? s : best, statuses[0] || 'not_found');

  return `
    <article class="book-card${show === false ? ' hidden' : ''}" data-status="${escHtml(bestSt)}">
      <div class="book-cover">${cover}</div>
      <div class="book-info">
        <div>
          <div class="book-title-row">
            <div class="book-title">${escHtml(book.title)}</div>
            ${ratingStr}
          </div>
          <div class="book-author">${escHtml(book.author)}${pageCount ? ` · ${pageCount}` : ''}</div>
          ${description}
        </div>
        ${libRows ? `<div class="library-list">${libRows}</div>` : ''}
        ${pricePills}
        ${actionButtons}
      </div>
    </article>
  `;
}
