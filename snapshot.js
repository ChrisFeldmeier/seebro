// Interactive-element snapshot engine.
//
// Runs in a per-frame isolated world (Page.createIsolatedWorld): it sees the
// page's DOM but not its JavaScript, and the page cannot see it. It replaces
// Accessibility.getFullAXTree for interactive snapshots, which on large pages
// serialises the whole tree (Wikipedia: 27k nodes, 10 MB JSON, ~1s).
//
// Elements keep their id for the lifetime of the document (WeakMap), so refs
// stay valid across snapshots until the element is removed.
(function () {
  if (globalThis.__sb) return;

  const ids = new WeakMap();
  const els = new Map();
  let next = 1;

  const ROLE_OK = new Set(["button", "link", "textbox", "searchbox", "combobox", "listbox", "option",
    "checkbox", "radio", "switch", "slider", "spinbutton", "menuitem", "menuitemcheckbox",
    "menuitemradio", "tab", "treeitem", "gridcell", "columnheader"]);
  const INPUT_ROLE = {
    checkbox: "checkbox", radio: "radio", range: "slider", number: "spinbutton", search: "searchbox",
    button: "button", submit: "button", reset: "button", image: "button", file: "button", color: "button",
  };

  const clean = (s, max) => {
    s = (s || "").replace(/\s+/g, " ").trim();
    return s.length > max ? s.slice(0, max - 1) + "…" : s;
  };

  function implicitRole(el) {
    const tag = el.tagName;
    switch (tag) {
      case "A": case "AREA": return el.hasAttribute("href") ? "link" : "";
      case "BUTTON": return "button";
      case "SUMMARY": return "button";
      case "TEXTAREA": return "textbox";
      case "SELECT": return el.multiple || el.size > 1 ? "listbox" : "combobox";
      case "OPTION": return el.parentElement && el.closest("select") ? "" : "option";
      case "INPUT": {
        const t = (el.type || "text").toLowerCase();
        if (t === "hidden") return "";
        return INPUT_ROLE[t] || "textbox";
      }
    }
    if (el.isContentEditable && (!el.parentElement || !el.parentElement.isContentEditable)) return "textbox";
    return "";
  }

  function roleOf(el) {
    const explicit = (el.getAttribute("role") || "").trim().split(/\s+/)[0];
    if (explicit && ROLE_OK.has(explicit)) return explicit;
    if (explicit === "none" || explicit === "presentation") return implicitRole(el) ? implicitRole(el) : "";
    return implicitRole(el);
  }

  function textByIds(el, attr) {
    const ids = (el.getAttribute(attr) || "").trim();
    if (!ids) return "";
    const root = el.getRootNode();
    return ids.split(/\s+/).map((id) => {
      const n = root.getElementById ? root.getElementById(id) : el.ownerDocument.getElementById(id);
      return n ? n.innerText || n.textContent : "";
    }).join(" ");
  }

  // Text of a <label> without the control it wraps (a label around a
  // <select> must not pick up the option texts).
  function labelText(label, ctrl) {
    let out = "";
    const walker = label.ownerDocument.createTreeWalker(label, NodeFilter.SHOW_TEXT);
    for (let t = walker.nextNode(); t; t = walker.nextNode()) {
      const p = t.parentElement;
      if (p && (ctrl.contains(t) || p.closest("select,option,datalist,script,style"))) continue;
      out += t.nodeValue + " ";
    }
    return out;
  }

  function nameOf(el, role) {
    let n = textByIds(el, "aria-labelledby");
    if (!n) n = el.getAttribute("aria-label") || "";
    if (!n && el.labels && el.labels.length) n = [...el.labels].map((l) => labelText(l, el)).join(" ");
    const tag = el.tagName;
    if (!n && tag === "INPUT") {
      const t = (el.type || "").toLowerCase();
      if (t === "submit" || t === "reset" || t === "button") n = el.value || (t === "submit" ? "Submit" : t === "reset" ? "Reset" : "");
      else if (t === "image") n = el.alt || el.value || "Submit";
      else if (t === "file") n = el.title || "Choose file";
      else n = el.placeholder || el.title || "";
    }
    if (!n && (tag === "TEXTAREA" || tag === "SELECT")) n = el.placeholder || el.title || "";
    if (!n && role !== "textbox" && role !== "combobox" && role !== "searchbox" && role !== "listbox") {
      n = el.innerText || el.textContent || "";
      if (!n.trim()) {
        const img = el.querySelector("img[alt]");
        const svgTitle = el.querySelector("svg title");
        n = el.title || (img && img.alt) || (svgTitle && svgTitle.textContent) || "";
      }
    }
    if (!n) n = el.title || "";
    return clean(n, 100);
  }

  function valueOf(el, role) {
    const tag = el.tagName;
    if (tag === "SELECT") {
      return clean([...el.selectedOptions].map((o) => o.textContent).join(", "), 80);
    }
    if (tag === "INPUT") {
      const t = (el.type || "").toLowerCase();
      if (t === "checkbox" || t === "radio" || t === "submit" || t === "button" || t === "reset" || t === "image") return "";
      if (t === "password") return el.value ? "•••" : "";
      if (t === "file") return clean([...(el.files || [])].map((f) => f.name).join(", "), 80);
      return clean(el.value, 80);
    }
    if (tag === "TEXTAREA") return clean(el.value, 80);
    if (role === "slider" || role === "spinbutton") return el.getAttribute("aria-valuenow") || "";
    return "";
  }

  function visibleRect(el) {
    if (el.checkVisibility && !el.checkVisibility({ checkVisibilityCSS: true, visibilityProperty: true })) return null;
    const r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return null;
    return r;
  }

  function idOf(el) {
    let id = ids.get(el);
    if (!id) {
      id = next++;
      ids.set(el, id);
      els.set(id, new WeakRef(el));
    }
    return id;
  }

  function deepActive(doc) {
    let a = doc.activeElement;
    while (a && a.shadowRoot && a.shadowRoot.activeElement) a = a.shadowRoot.activeElement;
    return a;
  }

  // Clickable elements without an interactive role (div onclick, cursor:pointer
  // cards). Only the outermost one is reported.
  function isClickable(el, parentPointer) {
    if (el.hasAttribute("onclick") || el.hasAttribute("ng-click") || el.hasAttribute("@click")) return true;
    if (parentPointer) return false;
    const tag = el.tagName;
    if (tag !== "DIV" && tag !== "SPAN" && tag !== "LI" && tag !== "IMG" && tag !== "TD" && tag !== "P" && tag !== "LABEL" && tag !== "svg") return false;
    return getComputedStyle(el).cursor === "pointer";
  }

  function snapshot(opts) {
    opts = opts || {};
    if (opts.startId && next < opts.startId) next = opts.startId;
    const doc = document;
    const active = deepActive(doc);
    const vw = innerWidth, vh = innerHeight;
    const nodes = [];
    const limit = opts.maxNodes || 5000;

    // Iterative DFS over elements, descending into open shadow roots.
    // stack entries: [element, insideInteractive, parentHasPointer]
    const stack = [];
    const pushChildren = (parent, inside, pointer) => {
      const kids = parent.shadowRoot ? [...parent.shadowRoot.children, ...parent.children] : parent.children;
      for (let i = kids.length - 1; i >= 0; i--) stack.push([kids[i], inside, pointer]);
    };
    if (opts.root) stack.push([opts.root, false, false]);
    else pushChildren(doc.documentElement || doc, false, false);

    while (stack.length && nodes.length < limit) {
      const [el, inside, parentPointer] = stack.pop();
      const tag = el.tagName;
      if (tag === "SCRIPT" || tag === "STYLE" || tag === "NOSCRIPT" || tag === "TEMPLATE" || tag === "HEAD") continue;
      if (el.hidden || el.getAttribute("aria-hidden") === "true") continue;

      let role = roleOf(el);
      let clickable = false;
      if (!role && !inside && opts.clickables !== false && isClickable(el, parentPointer)) {
        role = "clickable";
        clickable = true;
      }
      let pointer = parentPointer;
      if (role) {
        const r = visibleRect(el);
        if (r) {
          const name = nameOf(el, role);
          if (clickable && !name) {
            role = "";
          } else {
            const n = { id: idOf(el), role, name, x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) };
            const v = valueOf(el, role);
            if (v) n.value = v;
            if (el.disabled || el.getAttribute("aria-disabled") === "true") n.disabled = true;
            if (el.checked === true || el.getAttribute("aria-checked") === "true" || el.getAttribute("aria-selected") === "true") n.checked = true;
            const exp = el.getAttribute("aria-expanded");
            if (exp === "true") n.expanded = true;
            if (exp === "false") n.collapsed = true;
            if (el === active) n.focused = true;
            if (el.required || el.getAttribute("aria-required") === "true") n.required = true;
            if (tag === "SELECT") {
              const o = [...el.options].map((o) => clean(o.textContent, 40));
              n.options = o.length > 12 ? o.slice(0, 12).concat(`…(${o.length})`) : o;
            }
            if (opts.links && role === "link" && el.href) n.href = el.href;
            nodes.push(n);
            pointer = clickable || pointer;
          }
        }
        // Invisible controls are not reported, but their subtree is still
        // walked: display:contents elements have no box of their own.
      }
      if (tag === "IFRAME" || tag === "FRAME") continue; // frames get their own world
      if (!pointer && clickable) pointer = true;
      // Children of buttons/links are part of their name; still walk them for
      // nested real controls, but never report them as clickables.
      pushChildren(el, inside || !!role, pointer || (role && !clickable));
    }

    return { nodes, next, url: location.href, title: doc.title, vw, vh, truncated: nodes.length >= limit };
  }

  function el(id) {
    const ref = els.get(id);
    const e = ref && ref.deref();
    return e && e.isConnected ? e : null;
  }

  globalThis.__sb = { snapshot, el };
})();
