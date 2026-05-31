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
  const kindle = result.has_kindle ? ' <span class="badge-kindle-pill" title="Kindle delivery available">K</span>' : '';
  switch (result.status) {
    case 'available':
      return `<span class="badge badge-available">Available${result.available_copies > 0 ? ` (${result.available_copies})` : ''}${kindle}</span>`;
    case 'wait':
      return `<span class="badge badge-wait">${result.estimated_wait_days > 0 ? result.estimated_wait_days + '-day wait' : 'On hold'}${kindle}</span>`;
    case 'unavailable':
      return `<span class="badge badge-unavail">Not in catalog</span>`;
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

  // Cover image — width/height prevent CLS while image loads.
  const cover = book.cover_url
    ? `<img src="${escHtml(book.cover_url)}" alt="${escHtml(book.title)}" loading="lazy" decoding="async" width="76" height="114" onerror="this.parentElement.innerHTML='<div class=\\'book-cover-placeholder\\'></div>'">`
    : `<div class="book-cover-placeholder"></div>`;

  const description = book.description
    ? `<details class="book-description-details"><summary>About this book</summary><p class="book-description">${escHtml(book.description)}</p></details>`
    : '';

  const pageCount = book.page_count
    ? `<span class="book-pages">${book.page_count} pages</span>`
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
    <article class="book-card${show === false ? ' hidden' : ''}" data-status="${escHtml(bestSt)}" data-grid="${escHtml(book.goodreads_id || '')}">
      <div class="book-cover">${cover}</div>
      <div class="book-info">
        <div>
          <div class="book-title-row">
            <div class="book-title">${escHtml(book.title)}</div>
            ${ratingStr}
          </div>
          <div class="book-author">${escHtml(book.author)}</div>
          ${pageCount ? `<div class="book-meta">${pageCount}</div>` : ''}
          ${description}
        </div>
        ${libRows ? `<div class="library-list">${libRows}</div>` : ''}
        ${pricePills}
        ${actionButtons}
      </div>
    </article>
  `;
}

// buildSkeletonCard renders a generic shimmer placeholder shown while the
// Goodreads shelf is being fetched. index varies the widths so cards look natural.
function buildSkeletonCard(_, index) {
  const titles  = ['72%','65%','80%','55%','68%','75%'];
  const authors = ['48%','55%','40%','58%','45%','52%'];
  const badges  = ['88%','78%','92%','70%','85%','75%'];
  const i = (index || 0) % titles.length;
  return `
    <article class="book-card book-card-skeleton" aria-hidden="true">
      <div class="book-cover">
        <div class="skeleton" style="width:76px;height:114px;border-radius:var(--radius-sm);flex-shrink:0;"></div>
      </div>
      <div class="book-info" style="gap:12px;">
        <div>
          <div class="skeleton" style="height:15px;width:${titles[i]};margin-bottom:8px;border-radius:4px;"></div>
          <div class="skeleton" style="height:13px;width:${authors[i]};border-radius:4px;"></div>
        </div>
        <div class="skeleton" style="height:24px;width:${badges[i]};border-radius:100px;"></div>
        <div class="skeleton" style="height:24px;width:${badges[(i+2)%titles.length]};border-radius:100px;"></div>
      </div>
    </article>
  `;
}

// buildStubCard renders an immediate placeholder card before availability data
// arrives. index is used for a staggered cascade animation.
function buildStubCard(book, libraryKeys, index) {
  const cover = book.cover_url
    ? `<img src="${escHtml(book.cover_url)}" alt="${escHtml(book.title)}" loading="lazy" decoding="async" width="76" height="114" onerror="this.parentElement.innerHTML='<div class=\\'book-cover-placeholder\\'></div>'">`
    : `<div class="book-cover-placeholder"></div>`;

  const avgRating = book.average_rating
    ? `<span class="rating-avg" title="Goodreads average rating">★ ${book.average_rating.toFixed(2)}</span>`
    : '';
  const userRating = book.user_rating
    ? `<span class="rating-user" title="Your Goodreads rating">${'★'.repeat(book.user_rating)}${'☆'.repeat(5 - book.user_rating)}</span>`
    : '';
  const ratingStr = (avgRating || userRating)
    ? `<div class="book-rating-row">${avgRating}${userRating}</div>`
    : '';

  const stubRows = (libraryKeys || []).map(k => {
    const name = typeof window.getLibName === 'function' ? window.getLibName(k) : k;
    return `<div class="library-row">
      <span class="lib-label">${escHtml(name)}</span>
      <span class="badge badge-checking">Checking…</span>
    </div>`;
  }).join('');

  // Stagger up to 240ms so the grid feels like a waterfall.
  const delay = index !== undefined ? Math.min(index * 15, 240) : 0;
  const delayStyle = delay > 0 ? `animation-delay:${delay}ms;` : '';

  return `
    <article class="book-card book-card--stub" data-grid="${escHtml(book.goodreads_id || '')}" style="${delayStyle}">
      <div class="book-cover">${cover}</div>
      <div class="book-info">
        <div>
          <div class="book-title-row">
            <div class="book-title">${escHtml(book.title)}</div>
            ${ratingStr}
          </div>
          <div class="book-author">${escHtml(book.author)}</div>
        </div>
        ${stubRows ? `<div class="library-list">${stubRows}</div>` : ''}
      </div>
    </article>
  `;
}
