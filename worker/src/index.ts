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
  ORIGIN_SECRET?: string; // Worker↔origin paylaşımlı sır (F-02)
}

const CACHEABLE = /^\/api\/download\/[^/]+\/[^/]+$/; // /api/download/{token}/{name}

// ---- Dış sağlık izleme durumu (B-02 L3) ----
// Workers Free planda cron slotu dolu olduğu için fırsatçı kontrol kullanılır:
// gelen her istekte son kontrolden 5+ dk geçtiyse origin arka planda yoklanır.
// Origin tamamen ölse bile dış monitör (UptimeRobot vb.) storage.hyaena.co.uk/health
// izleyerek Worker'ın 502'sinden durumu görür.
let healthState = {
  last_check: null as string | null,
  last_ok: null as string | null,
  consecutive_failures: 0,
};
let lastCheckAt = 0;

async function probeOrigin(env: Env): Promise<void> {
  const origin = env.ORIGIN_URL || "http://orfi.hyaena.qzz.io:8080";
  lastCheckAt = Date.now();
  try {
    const h = new Headers();
    if (env.ORIGIN_SECRET) h.set("X-Origin-Secret", env.ORIGIN_SECRET);
    const r = await fetch(`${origin}/health`, { headers: h, signal: AbortSignal.timeout(10000) });
    healthState.last_check = new Date().toISOString();
    if (r.ok) {
      if (healthState.consecutive_failures >= 3) {
        console.log(`Origin KURTARDI: ${healthState.consecutive_failures} başarısızlık sonrası ${healthState.last_check}`);
      }
      healthState.last_ok = healthState.last_check;
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
}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    const origin = env.ORIGIN_URL || "http://orfi.hyaena.qzz.io:8080";

    // fırsatçı sağlık kontrolü: son kontrolden 5 dk geçtiyse arka planda tetikle
    if (Date.now() - lastCheckAt > 5 * 60 * 1000) {
      lastCheckAt = Date.now(); // önceden işaretle: eşzamanlı istekler çift tetiklemesin
      ctx.waitUntil(probeOrigin(env));
    }

    // Origin tamamen ölse bile durumu dışarıdan görebilmek için (B-02 L3).
    // Bu uç nokta origin'e proxy edilmez — worker kendi belleğinden cevaplar.
    if (url.pathname === "/__health-status") {
      return Response.json({
        ...healthState,
        healthy: healthState.consecutive_failures === 0 && healthState.last_ok !== null,
      }, { headers: { "Cache-Control": "no-store" } });
    }

    // /api/download/* yalnızca proxy'lenir. Kenar önbelleği Cloudflare'in
    // STANDART cache'idir (zone rule: cache=true + edge_ttl=respect_origin);
    // Cache API katmanı bilinçli olarak KULLANILMAZ — zone purge Cache API
    // girdilerini düşürmez (canlı doğrulandı), standart cache tek-URL purge
    // ile güvenilir temizlenir. TTL'i origin belirler (s-maxage = kalan ömür;
    // süresiz dosyada 24 saat üst sınırı) → süre sonu/silme hiçbir varyant
    // URL'de TTL'den uzun yaşamaz.

    // Origin'e proxy isteği. url.pathname percent-encoding'i korur (decode
    // etmez) — %20'li dosya adları origin'e HAM halde ulaşır.
    // F-02: her alt istek Worker↔origin paylaşımlı sırrı taşır — origin,
    // CF-aralığından gelen admin isteklerini ve ileri-başlı güvenini bu
    // sabit-zamanlı sırla doğrular.
    const originUrl = origin + url.pathname + url.search;
    const originHeaders = new Headers(request.headers);
    if (env.ORIGIN_SECRET) {
      originHeaders.set("X-Origin-Secret", env.ORIGIN_SECRET);
    }
    const originReq = new Request(originUrl, {
      method: request.method,
      headers: originHeaders,
      body: request.body,
      redirect: "manual",
    });

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

    // Başarılı dosya yanıtı origin başlıklarıyla aynen geçer; önbellek TTL'i
    // zone'un respect_origin kuralı üzerinden origin s-maxage'e bağlanır.
    return response;
  },
};
