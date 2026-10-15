// Page text extraction, evaluated as (function)(opts).
// opts.mode: "readability" (default) | "raw" | "markdown"
// Returns {text, url, title} in one round trip.
//
// Works on the live, rendered DOM: innerText of a detached clone has no
// layout and loses all line breaks ("submitlogin1.Things…").
(function (opts) {
  const mode = (opts && opts.mode) || "readability";
  // lite: engines where innerText/layout are expensive (obscura) build the
  // text from the DOM structure with textContent instead.
  const lite = !!(opts && opts.lite);
  const tx = (el) => (lite ? el.textContent : el.innerText || el.textContent) || "";
  const plain = mode !== "markdown"; // readability/raw via the walker: no markdown syntax
  const JUNK = [
    "nav", "footer", "aside", "header", "[role=navigation]", "[role=banner]",
    "[role=contentinfo]", "[aria-hidden=true]", ".ad", ".ads", ".advertisement",
    ".sidebar", ".cookie-banner", "#cookie-consent", ".popup", ".modal",
    ".language-selector", ".locale-selector", "[data-language-picker]",
    "script", "style", "noscript", "template", "svg", "[hidden]",
  ];
  const tidy = (s) => s.replace(/[ \t ]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
  const out = (text) => ({ text, url: location.href, title: document.title });

  if (mode === "raw" && !lite) {
    return out(tidy(document.body ? document.body.innerText : ""));
  }

  const root = mode === "raw" ? document.body :
    document.querySelector("article") ||
    document.querySelector("[role=main]") ||
    document.querySelector("main") ||
    document.body;
  if (!root) return out("");

  if (mode === "readability" && !lite) {
    // Hide junk with a constructable stylesheet (not subject to CSP
    // style-src, no DOM mutation), read the rendered text, restore.
    const docs = [document];
    for (const f of Array.from(document.querySelectorAll("iframe"))) {
      try { if (f.contentDocument && f.contentDocument.body) docs.push(f.contentDocument); } catch (e) {}
    }
    const parts = [];
    for (const doc of docs) {
      const sheet = new doc.defaultView.CSSStyleSheet();
      sheet.replaceSync(JUNK.join(",") + "{display:none !important}");
      const prev = doc.adoptedStyleSheets;
      doc.adoptedStyleSheets = [...prev, sheet];
      try {
        const r = doc === document ? root : doc.body;
        parts.push(r.innerText);
      } finally {
        doc.adoptedStyleSheets = prev;
      }
    }
    return out(tidy(parts.join("\n\n")));
  }

  // ── structural walker: markdown, or plain text in lite mode ──
  const junkSel = mode === "raw" ? "script,style,noscript,template,[hidden]" : JUNK.join(",");
  const BLOCK = new Set(["ADDRESS", "ARTICLE", "ASIDE", "BLOCKQUOTE", "DD", "DETAILS", "DIV", "DL", "DT",
    "FIELDSET", "FIGCAPTION", "FIGURE", "FOOTER", "FORM", "HEADER", "HR", "LI", "MAIN", "NAV", "OL", "P",
    "PRE", "SECTION", "SUMMARY", "TABLE", "UL", "H1", "H2", "H3", "H4", "H5", "H6", "TR"]);
  const visible = (el) => !el.checkVisibility || el.checkVisibility({ checkVisibilityCSS: true, visibilityProperty: true });
  const abs = (u) => { try { return new URL(u, location.href).href; } catch (e) { return u; } };
  const md = [];
  let line = "";
  const flush = (blank) => {
    const t = line.replace(/[ \t]+/g, " ").trim();
    if (t) md.push(t);
    if (blank && md.length && md[md.length - 1] !== "") md.push("");
    line = "";
  };
  // Layout tables (nested tables, long cells, single column) are walked like
  // normal blocks; only real data tables become markdown tables.
  // rows/cells with fallbacks for DOMs without HTMLTableElement.rows/.cells.
  const rowsOf = (t) => Array.from(t.rows || t.querySelectorAll("tr")).filter((r) => r.closest("table") === t);
  const cellsOf = (r) => Array.from(r.cells || r.children).filter((c) => c.tagName === "TD" || c.tagName === "TH");
  const isDataTable = (t) => {
    const rows = rowsOf(t);
    if (t.querySelector("table") || !rows.length) return false;
    if (t.querySelector("th") || t.querySelector("caption")) return true;
    const cols = rows.reduce((n, r) => n + cellsOf(r).length, 0) / rows.length;
    return rows.length > 1 && cols >= 2 && rows.every((r) => cellsOf(r).every((c) => c.textContent.length < 300));
  };
  const inlineText = (el) => tx(el).replace(/\s+/g, " ").trim();

  const walk = (node, ctx) => {
    if (node.nodeType === 3) {
      line += node.nodeValue.replace(/\s+/g, " ");
      return;
    }
    if (node.nodeType !== 1) return;
    const el = node;
    if (el.matches(junkSel) || !visible(el)) return;
    const tag = el.tagName;

    if (/^H[1-6]$/.test(tag)) {
      flush(true);
      line = (plain ? "" : "#".repeat(+tag[1]) + " ") + inlineText(el);
      flush(true);
      return;
    }
    if (tag === "BR") { flush(false); return; }
    if (tag === "HR") { flush(true); if (!plain) md.push("---", ""); return; }
    if (tag === "IMG") {
      const alt = (el.getAttribute("alt") || "").trim();
      if (alt && !plain) line += ` ![${alt}]`;
      return;
    }
    if (tag === "A" && el.getAttribute("href")) {
      const href = el.getAttribute("href");
      const text = inlineText(el);
      if (!text) return;
      line += plain || /^(javascript:|#)/.test(href) ? ` ${text} ` : ` [${text}](${abs(href)}) `;
      return;
    }
    if (tag === "PRE") {
      flush(true);
      const code = tx(el).replace(/\n+$/, "");
      if (plain) md.push(code, ""); else md.push("```", code, "```", "");
      return;
    }
    if (tag === "CODE") { line += plain ? " " + el.textContent.trim() + " " : " `" + el.textContent.trim() + "` "; return; }
    if (tag === "TABLE" && isDataTable(el)) {
      flush(true);
      const rows = rowsOf(el).slice(0, 200).map((r) =>
        cellsOf(r).map((c) => (plain ? inlineText(c) : inlineText(c).replace(/\|/g, "\\|"))));
      if (rows.length) {
        const w = Math.max(...rows.map((r) => r.length));
        rows.forEach((r, i) => {
          while (r.length < w) r.push("");
          if (plain) { md.push(r.join("\t")); return; }
          md.push("| " + r.join(" | ") + " |");
          if (i === 0) md.push("|" + " --- |".repeat(w));
        });
        md.push("");
      }
      return;
    }
    if (tag === "UL" || tag === "OL") {
      flush(false);
      let i = 1;
      for (const li of Array.from(el.children)) {
        if (li.tagName !== "LI" || !visible(li)) continue;
        const bullet = tag === "OL" ? `${i++}.` : "-";
        line = "  ".repeat(ctx.depth) + bullet + " ";
        for (const c of Array.from(li.childNodes)) walk(c, { depth: ctx.depth + 1 });
        flush(false);
      }
      if (ctx.depth === 0) flush(true);
      return;
    }
    if (tag === "IFRAME") {
      try {
        const d = el.contentDocument;
        if (d && d.body) { flush(true); walk(d.body, ctx); flush(true); }
      } catch (e) {}
      return;
    }
    if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA" || tag === "BUTTON") {
      const t = tag === "BUTTON" ? inlineText(el) : (el.value || el.placeholder || "");
      if (t) line += plain ? ` ${t} ` : ` [${t}] `;
      return;
    }

    if (tag === "TD" || tag === "TH") {
      for (const c of Array.from(el.childNodes)) walk(c, ctx);
      line += " ";
      return;
    }
    const block = BLOCK.has(tag);
    if (block) flush(tag === "P" || tag === "BLOCKQUOTE" || tag === "SECTION" || tag === "ARTICLE");
    const quote = tag === "BLOCKQUOTE";
    const start = md.length;
    const kids = el.shadowRoot ? el.shadowRoot.childNodes : el.childNodes;
    for (const c of Array.from(kids)) walk(c, ctx);
    if (block) flush(tag === "P" || quote);
    if (quote) for (let i = start; i < md.length; i++) if (md[i]) md[i] = "> " + md[i];
  };

  walk(root, { depth: 0 });
  flush(false);
  return out(tidy(md.join("\n")));
})
