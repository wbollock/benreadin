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
  switch (result.status) {
    case 'available':
      return `<span class="badge badge-available">&#10003; Available${result.available_copies > 0 ? ` (${result.available_copies})` : ''}</span>`;
    case 'wait':
      return `<span class="badge badge-wait">&#8987; ${result.estimated_wait_days > 0 ? result.estimated_wait_days + '-day wait' : 'On hold'}</span>`;
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
  if (amazon_result?.available && amazon_result.kindle_asin) {
    const kindleUrl = `https://www.amazon.com/dp/${escHtml(amazon_result.kindle_asin)}?tag=${encodeURIComponent(affiliateTag)}`;
    kindleBtns += `<a href="${kindleUrl}" target="_blank" rel="noopener" class="btn-kindle">Buy for Kindle</a>`;
    kindleBtns += `<a href="${kindleUrl}#sampleButton" target="_blank" rel="noopener" class="btn-secondary btn-sm">Send Sample ↗</a>`;
  } else {
    // No Amazon creds — fall back to an Amazon Kindle search by ISBN or title
    const isbn = book.isbn13 || book.isbn10;
    const q = isbn ? isbn : (book.title + ' ' + book.author);
    const kindleSearchUrl = `https://www.amazon.com/s?k=${encodeURIComponent(q)}&i=digital-text`;
    kindleBtns += `<a href="${kindleSearchUrl}" target="_blank" rel="noopener" class="btn-kindle">Find on Kindle ↗</a>`;
  }

  if (amazon_result?.affiliate_url) {
    kindleBtns += `<a href="${escHtml(amazon_result.affiliate_url)}" target="_blank" rel="noopener" class="btn-secondary btn-sm">Amazon ↗</a>`;
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
    ? `<p class="book-description">${escHtml(book.description)}</p>`
    : '';

  const ratingStr = book.average_rating
    ? `<span class="book-rating" title="Goodreads average rating">★ ${book.average_rating.toFixed(2)}${book.user_rating ? ` &nbsp;·&nbsp; My rating: ${'★'.repeat(book.user_rating)}${'☆'.repeat(5 - book.user_rating)}` : ''}</span>`
    : '';

  return `
    <article class="book-card${show === false ? ' hidden' : ''}">
      <div class="book-cover">${cover}</div>
      <div class="book-info">
        <div>
          <div class="book-title">${escHtml(book.title)}</div>
          <div class="book-author">${escHtml(book.author)}</div>
          ${ratingStr}
          ${description}
        </div>
        ${libRows ? `<div class="library-list">${libRows}</div>` : ''}
        ${pricePills}
        ${actionButtons}
      </div>
    </article>
  `;
}
