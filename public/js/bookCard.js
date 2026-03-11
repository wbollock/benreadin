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
  const libRows = (library_results || []).map(lr => `
    <div class="library-row">
      <span class="library-key">${escHtml(lr.library_key)}</span>
      ${lr.overdrive_url
        ? `<a href="${escHtml(lr.overdrive_url)}" target="_blank" rel="noopener" style="text-decoration:none" title="${lr.status === 'available' ? 'Borrow on Libby — Kindle delivery available' : 'View on OverDrive'}">${availabilityBadge(lr)}</a>`
        : availabilityBadge(lr)
      }
    </div>
  `).join('');

  // Price pills + action buttons
  let pricePills = '';
  let actionButtons = '';

  // Extract affiliate tag once for reuse.
  const affiliateTag = amazon_result?.affiliate_url
    ? (amazon_result.affiliate_url.match(/tag=([^&]+)/)?.[1] || '')
    : '';

  if (amazon_result && amazon_result.available) {
    const kindle    = formatPrice(amazon_result.kindle_price);
    const paperback = formatPrice(amazon_result.paperback_price);
    const hardcover = formatPrice(amazon_result.hardcover_price);

    const pills = [];
    if (kindle)    pills.push(`<span class="price-pill"><span class="pill-label">Kindle</span><span class="pill-amount">${kindle}</span></span>`);
    if (paperback) pills.push(`<span class="price-pill"><span class="pill-label">Paperback</span><span class="pill-amount">${paperback}</span></span>`);
    if (hardcover) pills.push(`<span class="price-pill"><span class="pill-label">Hardcover</span><span class="pill-amount">${hardcover}</span></span>`);
    if (pills.length) pricePills = `<div class="price-row">${pills.join('')}</div>`;

    if (amazon_result.kindle_asin) {
      const kindleUrl  = `https://www.amazon.com/dp/${escHtml(amazon_result.kindle_asin)}?tag=${encodeURIComponent(affiliateTag)}`;
      const sampleUrl  = kindleUrl + '#sampleButton';
      actionButtons += `<a href="${kindleUrl}" target="_blank" rel="noopener" class="btn-kindle">Buy for Kindle</a>`;
      actionButtons += `<a href="${sampleUrl}" target="_blank" rel="noopener" class="btn-secondary btn-sm">Send Sample</a>`;
    }

    if (amazon_result.affiliate_url) {
      actionButtons += `<a href="${escHtml(amazon_result.affiliate_url)}" target="_blank" rel="noopener" class="btn-secondary btn-sm">
        Amazon
        <svg width="10" height="10" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15,3 21,3 21,9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>
      </a>`;
    }
  }

  // Gutenberg free ebook badge + download
  if (gutenberg_result) {
    pricePills = `<div class="price-row"><span class="price-pill price-pill-free">Free on Project Gutenberg</span></div>` + pricePills;
    actionButtons = `<a href="${escHtml(gutenberg_result.epub_url)}" target="_blank" rel="noopener" class="btn-kindle btn-gutenberg" title="Download EPUB — then use Send to Kindle app or email to your Kindle address">Download EPUB (Free)</a>` + actionButtons;
  }

  // Cover image
  const cover = book.cover_url
    ? `<img src="${escHtml(book.cover_url)}" alt="${escHtml(book.title)}" loading="lazy" onerror="this.parentElement.innerHTML='<div class=\\'book-cover-placeholder\\'>📚</div>'">`
    : `<div class="book-cover-placeholder">📚</div>`;

  const description = book.description
    ? `<p class="book-description">${escHtml(book.description)}</p>`
    : '';

  return `
    <article class="book-card${show === false ? ' hidden' : ''}">
      <div class="book-cover">${cover}</div>
      <div class="book-info">
        <div>
          <div class="book-title">${escHtml(book.title)}</div>
          <div class="book-author">${escHtml(book.author)}</div>
          ${description}
        </div>
        ${libRows ? `<div class="library-list">${libRows}</div>` : ''}
        ${pricePills}
        ${actionButtons ? `<div class="card-actions">${actionButtons}</div>` : ''}
      </div>
    </article>
  `;
}
