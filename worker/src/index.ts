/**
 * Hyaena Storage — Cloudflare Worker proxy + edge cache.
 *
 * Kullanıcı → Worker → orfi.hyaena.qzz.io:8080 (VPS) → Telegram
 *
 * - Tüm istekler (index, upload, admin, download) orfi sunucusuna proxy'lenir.
 * - /api/download/* (dosya stream) Cloudflare edge cache'ine yazılır → Northflank
 *   egress yok, VPS egress'i de minimize (cache HIT'ler edge'den servis edilir).
 * - Hata yanıtları (404/5xx) cache'lenmez ve no-store ile işaretlenir —
 *   hazır olmayan dosyanın 404'ü edge'e negatif cache yazılmasın.
 */

export interface Env {
  ORIGIN_URL: string;
  CACHE_TTL: string; // saniye
}

const CACHEABLE = /^\/api\/download\/[^/]+\/[^/]+$/; // /api/download/{token}/{name}

// ---- Dış sağlık izleme durumu (B-02 L3): cron 5 dk'da bir origin'i yoklar ----
// Per-isolate globaldir (worker izoleleri bağımsız); en güncel durumu yansıtır.
let healthState = {
  last_check: null as string | null,
  last_ok: null as string | null,
  consecutive_failures: 0,
};

export default {
  async scheduled(_event: ScheduledController, env: Env, _ctx: ExecutionContext): Promise<void> {
    const origin = env.ORIGIN_URL || "http://orfi.hyaena.qzz.io:8080";
    try {
      const r = await fetch(`${origin}/health`, { signal: AbortSignal.timeout(10000) });
      healthState.last_check = new Date().toISOString();
      if (r.ok) {
        healthState.last_ok = healthState.last_check;
        if (healthState.consecutive_failures >= 3) {
          console.log(`Origin KURTARDI: ${healthState.consecutive_failures} başarısızlık sonrası ${healthState.last_check}`);
        }
        healthState.consecutive_failures = 0;
      } else {
        healthState.consecutive_failures++;
        console.error(`Origin sağlık kontrolü HTTP ${r.status} (${healthState.consecutive_failures} art arda)`);
      }
    } catch (e) {
      healthState.last_check = new Date().toISOString();
      healthState.consecutive_failures++;
      console.error(`Origin sağlık kontrolü hatası: ${e} (${healthState.consecutive_failures} art arda)`);
    }
  },

  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    const origin = env.ORIGIN_URL || "http://orfi.hyaena.qzz.io:8080";

    // Origin tamamen ölse bile durumu dışarıdan görebilmek için (B-02 L3).
    // Bu uç nokta origin'e proxy edilmez — worker kendi belleğinden cevaplar.
    if (url.pathname === "/__health-status") {
      return Response.json({
        ...healthState,
        healthy: healthState.consecutive_failures === 0 && healthState.last_ok !== null,
      }, { headers: { "Cache-Control": "no-store" } });
    }

    // Sadece dosya stream path'ini cache'le; diğerleri passthrough
    const isCacheable = CACHEABLE.test(url.pathname);
    const cache = caches.default;

    if (isCacheable) {
      const cached = await cache.match(request);
      if (cached) {
        return cached;
      }
    }

    // Origin'e proxy isteği. url.pathname percent-encoding'i korur (decode
    // etmez) — %20'li dosya adları origin'e HAM halde ulaşır.
    const originUrl = origin + url.pathname + url.search;
    const originReq = new Request(originUrl, request);

    let response: Response;
    try {
      response = await fetch(originReq);
    } catch (e) {
      return new Response(`Proxy error: ${e}`, { status: 502 });
    }

    // Hatalı yanıt: cacheleme yok, edge'in negatif cache'lemesini de engelle.
    // (Örn. dosya henüz hazır değil → 404; hazır olunca indirilebilmeli.)
    if (!response.ok) {
      const headers = new Headers(response.headers);
      headers.set("Cache-Control", "no-store");
      return new Response(response.body, {
        status: response.status,
        statusText: response.statusText,
        headers,
      });
    }

    // Başarılı dosya yanıtını cache'le (200). Cache anahtarı = gelen istek;
    // match de put da aynı URL'yi kullanır.
    if (isCacheable) {
      const clone = response.clone();
      const headers = new Headers(clone.headers);
      headers.set("Cache-Control", `public, max-age=${env.CACHE_TTL || "2592000"}`);
      const cacheable = new Response(clone.body, {
        status: clone.status,
        statusText: clone.statusText,
        headers,
      });
      ctx.waitUntil(cache.put(request, cacheable));
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
