const CACHE_NAME = "xirang-cache-v2";
const PRE_CACHE = [
  "/",
  "/index.html",
  "/favicon.svg",
  "/xirang-mark.svg",
  "/manifest.webmanifest"
];

const STATIC_DESTINATIONS = new Set(["style", "script", "image", "font"]);

// JavaScript MIME essences (WHATWG MIME Sniffing) that Nginx's mime.types maps
// for .js/.mjs across versions. With X-Content-Type-Options: nosniff the
// browser rejects any other script type, so such a response (for example the
// SPA index.html fallback) must never be cached or replayed for a script.
const SCRIPT_MIME_TYPES = new Set([
  "text/javascript",
  "application/javascript",
  "application/x-javascript",
  "application/ecmascript",
  "text/ecmascript"
]);

function shouldBypassCache(requestUrl, request) {
  if (requestUrl.origin !== self.location.origin) {
    return true;
  }

  if (requestUrl.pathname.startsWith("/api") || requestUrl.pathname.startsWith("/ws")) {
    return true;
  }

  const cacheControl = request.headers.get("cache-control") || "";
  if (cacheControl.includes("no-store") || cacheControl.includes("no-cache")) {
    return true;
  }

  return false;
}

function shouldCacheResponse(response) {
  return Boolean(response && response.ok && response.type === "basic");
}

function isScriptResponse(response) {
  const contentType = response.headers.get("content-type") || "";
  return SCRIPT_MIME_TYPES.has(contentType.split(";")[0].trim().toLowerCase());
}

function isUsableStaticResponse(request, response) {
  return request.destination !== "script" || isScriptResponse(response);
}

function fetchStatic(request) {
  return fetch(request).then((response) => {
    if (shouldCacheResponse(response) && isUsableStaticResponse(request, response)) {
      const cloned = response.clone();
      caches.open(CACHE_NAME).then((cache) => cache.put(request, cloned));
    }
    return response;
  });
}

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then((cache) => cache.addAll(PRE_CACHE))
      .then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((key) => key !== CACHE_NAME).map((key) => caches.delete(key)))
      )
      .then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") {
    return;
  }

  const requestUrl = new URL(request.url);
  if (shouldBypassCache(requestUrl, request)) {
    return;
  }

  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request)
        .then((response) => {
          if (shouldCacheResponse(response)) {
            const cloned = response.clone();
            caches.open(CACHE_NAME).then((cache) => cache.put(request, cloned));
          }
          return response;
        })
        .catch(() => caches.match(request).then((cached) => cached || caches.match("/index.html")))
    );
    return;
  }

  if (!STATIC_DESTINATIONS.has(request.destination)) {
    return;
  }

  event.respondWith(
    caches.match(request).then((cached) => {
      if (!cached) {
        return fetchStatic(request);
      }
      if (isUsableStaticResponse(request, cached)) {
        return cached;
      }
      // A legacy entry (e.g. HTML cached before MIME validation) must not keep
      // winning: evict it before refetching so a valid response replaces it and
      // an invalid one leaves no entry behind.
      return caches
        .open(CACHE_NAME)
        .then((cache) => cache.delete(request))
        .catch(() => undefined)
        .then(() => fetchStatic(request));
    })
  );
});
