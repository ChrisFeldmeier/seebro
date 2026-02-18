// Lightweight readability extraction — strips nav/footer/aside/ads,
// prefers article/main content, falls back to body.innerText.
(() => {
  const strip = ['nav', 'footer', 'aside', 'header', '[role="navigation"]',
    '[role="banner"]', '[role="contentinfo"]', '[aria-hidden="true"]',
    '.ad', '.ads', '.advertisement', '.sidebar', '.cookie-banner',
    '#cookie-consent', '.popup', '.modal',
    '#SIvCob', '[data-locale-picker]', '[role="listbox"]',
    '#Lb4nn', '.language-selector', '.locale-selector',
    '[data-language-picker]', '#langsec-button'];

  // Try article or main first
  let root = document.querySelector('article') ||
             document.querySelector('[role="main"]') ||
             document.querySelector('main');

  if (!root) {
    // Clone body and strip junk
    root = document.body.cloneNode(true);
    for (const sel of strip) {
      root.querySelectorAll(sel).forEach(el => el.remove());
    }
  } else {
    root = root.cloneNode(true);
  }

  // Clean up: remove scripts, styles, hidden elements
  root.querySelectorAll('script, style, noscript, svg, [hidden]').forEach(el => el.remove());

  return root.innerText.replace(/\n{3,}/g, '\n\n').trim();
})()
