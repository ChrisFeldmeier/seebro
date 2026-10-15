// Stealth script injected into every new document via page.AddScriptToEvaluateOnNewDocument.
//
// Principle: change as little as possible in JavaScript. Most automation
// signals are fixed at the browser level, which leaves no JS traces:
//   - navigator.webdriver → false via --disable-blink-features=AutomationControlled
//   - "HeadlessChrome" in the user agent + client hints → Emulation.setUserAgentOverride
//   - plugins, mimeTypes, window.chrome, languages → real values in new headless
// Every JS override here is made to look native (toString, descriptors),
// because a patched function that prints its source is itself a fingerprint.

// Session-stable seed injected by Go (__seebro_seed), so noise is consistent
// across page loads within one Seebro session, like a real device.
const sessionSeed = (typeof __seebro_seed !== 'undefined') ? __seebro_seed : 42;
const stealthLevel = (typeof __seebro_stealth_level !== 'undefined') ? __seebro_stealth_level : 'light';

// Mulberry32 PRNG — deterministic per seed.
const seededRandom = function(seed) {
  let t = (seed + 0x6D2B79F5) | 0;
  t = Math.imul(t ^ (t >>> 15), t | 1);
  t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};

// ── Native-looking overrides ───────────────────────────────
const nativeNames = new WeakMap();
const origToString = Function.prototype.toString;
const patchedToString = function toString() {
  if (nativeNames.has(this)) return 'function ' + nativeNames.get(this) + '() { [native code] }';
  return origToString.call(this);
};
nativeNames.set(patchedToString, 'toString');
Function.prototype.toString = patchedToString;

// replaceMethod swaps obj[name] for a wrapper that looks native and keeps
// the original property descriptor flags.
function replaceMethod(obj, name, makeWrapper) {
  const desc = Object.getOwnPropertyDescriptor(obj, name);
  if (!desc || typeof desc.value !== 'function') return;
  const original = desc.value;
  const wrapper = makeWrapper(original);
  nativeNames.set(wrapper, name);
  Object.defineProperty(obj, name, Object.assign({}, desc, { value: wrapper }));
}

// replaceGetter swaps a prototype getter, keeping it native-looking.
function replaceGetter(proto, name, makeGetter) {
  const desc = Object.getOwnPropertyDescriptor(proto, name);
  if (!desc || typeof desc.get !== 'function') return;
  const getter = makeGetter(desc.get);
  nativeNames.set(getter, 'get ' + name);
  Object.defineProperty(proto, name, Object.assign({}, desc, { get: getter }));
}

// Remove leftovers of ChromeDriver-style automation, if any.
for (const k of Object.keys(window)) {
  if (/^cdc_[a-zA-Z0-9]{22}_/.test(k)) delete window[k];
}

// Headless reports notification permission inconsistently between
// Notification.permission and permissions.query — align them.
if (window.Permissions && Permissions.prototype.query) {
  replaceMethod(Permissions.prototype, 'query', (orig) => function query(parameters) {
    if (parameters && parameters.name === 'notifications' && typeof Notification !== 'undefined') {
      const state = Notification.permission === 'default' ? 'prompt' : Notification.permission;
      return Promise.resolve(Object.setPrototypeOf({ state, onchange: null }, PermissionStatus.prototype));
    }
    return orig.apply(this, arguments);
  });
}

// ── Full mode: fingerprint noise ───────────────────────────
// Off by default: noise can break canvas-heavy sites, and values that differ
// between the page and its workers are detectable too.
if (stealthLevel === 'full') {

  // WebGL vendor/renderer (UNMASKED_VENDOR_WEBGL / UNMASKED_RENDERER_WEBGL)
  for (const Ctx of [window.WebGLRenderingContext, window.WebGL2RenderingContext]) {
    if (!Ctx) continue;
    replaceMethod(Ctx.prototype, 'getParameter', (orig) => function getParameter(p) {
      if (p === 37445) return 'Intel Inc.';
      if (p === 37446) return 'Intel Iris OpenGL Engine';
      return orig.apply(this, arguments);
    });
  }

  // Canvas: deterministic per-session noise on readback.
  const noisify = (data) => {
    const n = Math.min(10, Math.floor(data.length / 400));
    for (let i = 0; i < n; i++) {
      const idx = Math.floor(seededRandom(sessionSeed + i) * (data.length / 4)) * 4;
      data[idx] = data[idx] < 255 ? data[idx] + 1 : data[idx] - 1;
    }
  };
  const noisyCopy = (canvas) => {
    if (!canvas.width || !canvas.height) return null;
    const tmp = document.createElement('canvas');
    tmp.width = canvas.width;
    tmp.height = canvas.height;
    const tctx = tmp.getContext('2d');
    tctx.drawImage(canvas, 0, 0);
    const img = origGetImageData.call(tctx, 0, 0, tmp.width, tmp.height);
    noisify(img.data);
    tctx.putImageData(img, 0, 0);
    return tmp;
  };
  const origGetImageData = CanvasRenderingContext2D.prototype.getImageData;
  replaceMethod(HTMLCanvasElement.prototype, 'toDataURL', (orig) => function toDataURL() {
    const copy = noisyCopy(this);
    return orig.apply(copy || this, arguments);
  });
  replaceMethod(HTMLCanvasElement.prototype, 'toBlob', (orig) => function toBlob() {
    const copy = noisyCopy(this);
    return orig.apply(copy || this, arguments);
  });
  replaceMethod(CanvasRenderingContext2D.prototype, 'getImageData', (orig) => function getImageData() {
    const img = orig.apply(this, arguments);
    noisify(img.data);
    return img;
  });

  // Font metrics: tiny stable width jitter.
  replaceMethod(CanvasRenderingContext2D.prototype, 'measureText', (orig) => function measureText(text) {
    const m = orig.apply(this, arguments);
    const noise = 1 + 0.0001 + seededRandom(sessionSeed + String(text).length) * 0.0002;
    return new Proxy(m, { get: (t, p) => (p === 'width' ? t.width * noise : t[p]) });
  });

  // WebRTC: force relay so local IPs don't leak.
  replaceMethod(window, 'RTCPeerConnection', (Orig) => {
    const Patched = function RTCPeerConnection(config, constraints) {
      if (config && config.iceServers) config.iceTransportPolicy = 'relay';
      return new Orig(config, constraints);
    };
    Patched.prototype = Orig.prototype;
    return Patched;
  });

  // Hardware: only values real Chrome reports (deviceMemory is a power of two).
  const cores = [4, 8, 8, 12, 16][Math.floor(seededRandom(sessionSeed) * 5)];
  const memory = [8, 16, 32][Math.floor(seededRandom(sessionSeed * 2) * 3)];
  replaceGetter(Navigator.prototype, 'hardwareConcurrency', () => function () { return cores; });
  replaceGetter(Navigator.prototype, 'deviceMemory', () => function () { return memory; });
}
