/**
 * Hyaena Storage — Cloudflare Worker download proxy + edge cache.
 *
 * Kullanıcı → Worker → Northflank origin → Telegram
 *
 * - İlk indirme Northflank'tan çekilir ve Cloudflare edge cache'ine yazılır.
 * - Sonraki aynı dosya indirmeleri Cloudflare edge'den servis edilir
 *   (Northflank egress'i bypass, R2 kullanılmaz — dosya içeriği Telegram'da kalır).
 *
 * Sadece /api/download/* (dosya stream) cache'lenir.
 * Diğer tüm istekler (health, admin, upload, frontend) passthrough — cache'lenmez.
 */

export interface Env {
  ORIGIN_URL: string;
  CACHE_TTL: string; // saniye
}

const CACHEABLE = /^\/api\/download\/[^/]+\/[^/]+$/; // /api/download/{token}/{name}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    const origin = env.ORIGIN_URL || "https://http--hyaena-storage--kkg797wpkmd9.code.run";

    // Sadece dosya stream path'ini cache'le; meta (/api/download/{token}) ve diğerleri passthrough
    const isCacheable = CACHEABLE.test(url.pathname);
    const cache = caches.default;

    if (isCacheable) {
      // Cache-Control yoksa Worker Cache API doğal olarak cache'lemez; biz manuel cache kullanıyoruz
      const cached = await cache.match(request);
      if (cached) {
        // cache hit — edge'den yanıt
        return cached;
      }
    }

    // Origin'e proxy isteği: path'i koru, origin host'a yönlendir
    const originUrl = origin + url.pathname + url.search;
    const originReq = new Request(originUrl, request);

    let response: Response;
    try {
      response = await fetch(originReq);
    } catch (e) {
      return new Response(`Proxy error: ${e}`, { status: 502 });
    }

    // Başarılı dosya yanıtını cache'le (cache miss, 200)
    if (isCacheable && response.ok) {
      // response.body'yi koru; stream'i cache'e yaz ve kullanıcıya yeni bir stream ver
      const clone = response.clone();
      // Cache-Control: indirilen dosya kalıcı; uzun TTL
      const headers = new Headers(clone.headers);
      headers.set("Cache-Control", `public, max-age=${env.CACHE_TTL || "2592000"}`);
      const cacheable = new Response(clone.body, {
        status: clone.status,
        statusText: clone.statusText,
        headers,
      });
      // Doğru cache key: origin URL + query, method GET
      const cacheKey = new Request(originUrl, { method: "GET" });
      ctx.waitUntil(cache.put(cacheKey, cacheable));
      // Origin yanıtına da Cache-Control ekle (davranış tutarlılığı)
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
