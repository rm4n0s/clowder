// The service worker of Clowder (SPEC D58). It caches the files of /static/
// and nothing else: for every other request (a page, /api/, /files/,
// /avatars/, a stream, anything that is not GET) the fetch handler returns
// without calling respondWith, so the browser goes to the network as if no
// worker were there.
//
// The server serves this file at /sw.js with the placeholder below replaced by
// a hash of all the embedded static files. The cache name carries it, so a
// release with other static files is another cache: the browser installs the
// new worker (its bytes changed), which deletes the caches of other versions
// when it activates. The pages name every static file with "?v=" and the same
// hash, and the cache key is the whole address, query included, so a page of a
// new release asks for addresses that an old worker never kept: they go to the
// network, and a page of the old release keeps getting its own files.
'use strict';

const VERSION = '__CLOWDER_STATIC_VERSION__';
const PREFIX = 'clowder-static-';
const CACHE = PREFIX + VERSION;

self.addEventListener('install', (event) => {
  // Take over at once: the files of the old version are not wanted by the pages
  // of the new one.
  event.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    const names = await caches.keys();
    await Promise.all(names.filter((n) => n.startsWith(PREFIX) && n !== CACHE).map((n) => caches.delete(n)));
    await self.clients.claim();
  })());
});

// isStatic reports whether the request is a GET of a file under /static/ of
// this site. A range request is left to the network.
function isStatic(request) {
  if (request.method !== 'GET' || request.headers.has('range')) {
    return false;
  }
  const url = new URL(request.url);
  return url.origin === self.location.origin && url.pathname.startsWith('/static/');
}

// fromCache answers a static file from the cache, and fetches and keeps it
// when it is not there yet (only a 200 answer is kept).
async function fromCache(request) {
  const cache = await caches.open(CACHE);
  const hit = await cache.match(request);
  if (hit) {
    return hit;
  }
  const response = await fetch(request);
  if (response.status === 200 && response.type === 'basic') {
    await cache.put(request, response.clone());
  }
  return response;
}

self.addEventListener('fetch', (event) => {
  if (!isStatic(event.request)) {
    return;
  }
  event.respondWith(fromCache(event.request));
});
