/**
 * Hyaena Storage — Cloudflare Worker proxy + edge cache.
 *
 * Kullanıcı → Worker → orfi.hyaena.qzz.io:8080 (VPS) → Telegram
 *
 * - Tüm istekler (index, upload, admin, download) orfi sunucusuna proxy'lenir.
 * - /api/download/* (dosya stream) Cloudflare edge cache'ine yazılır → Northflank
 *   egress yok, VPS egress'i de minimize (cache HIT'ler edge'den servis edilir).
 * - Diğer istekler passthrough — cache'lenmez.
 */

export interface Env {
  ORIGIN_URL: string;
  CACHE_TTL: string; // saniye
}

const CACHEABLE = /^\/api\/download\/[^/]+\/[^/]+$/; // /api/download/{token}/{name}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    const origin = env.ORIGIN_URL || "http://orfi.hyaena.qzz.io:8080";

    // Sadece dosya stream path'ini cache'le; diğerleri passthrough
    const isCacheable = CACHEABLE.test(url.pathname);
    const cache = caches.default;

    if (isCacheable) {
      const cached = await cache.match(request);
      if (cached) {
        return cached;
      }
    }

    // Origin'e proxy isteği: HAM path korunarak (decode edilmemiş) gönderilir.
    const rawPath = request.url.split("?")[0];
    const originUrl = origin + rawPath + url.search;
    const originReq = new Request(originUrl, request);

    let response: Response;
    try {
      response = await fetch(originReq);
    } catch (e) {
      return new Response(`Proxy error: ${e}`, { status: 502 });
    }

    // Başarılı dosya yanıtını cache'le (cache miss, 200)
    if (isCacheable && response.ok) {
      const clone = response.clone();
      const headers = new Headers(clone.headers);
      headers.set("Cache-Control", `public, max-age=${env.CACHE_TTL || "2592000"}`);
      const cacheable = new Response(clone.body, {
        status: clone.status,
        statusText: clone.statusText,
        headers,
      });
      const cacheKey = new Request(originUrl, { method: "GET" });
      ctx.waitUntil(cache.put(cacheKey, cacheable));
      const outHeaders = new Headers(response.headers);
      outHeaders.set("Cache-Control", `public, max-age=${env.CACHE_TTL || "2592000"}`);
      return new Response(response.body, {
        status: response.status,
        statusText: response.statusText,
        headers: outHeaders,
      });
    }

    return response;
  },
};
