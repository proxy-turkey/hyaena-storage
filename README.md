# Telegram Storage (Go)

Telegram'ı arka uç depolama olarak kullanan WeTransfer-benzeri dosya paylaşım servisi.
**Yalnızca Go** (GoMTProto / gotd/td + chi + modernc.org/sqlite). Python sürümü kaldırıldı.

- Her gün otomatik **1 depolama kanalı** oluşturulur (filosu büyür).
- Dosyalar **20 MB segmentlere** bölünür, kanallara **round-robin** dağıtılır.
- Paylaşım linkiyle indirilebilir. Dosyalar **kalıcı** veya **süreli** olabilir.
- **Süre seçimi:** 1 Saat, 1 Gün, 1 Hafta, 1 Ay (seçilmezse Süresiz). Süresi dolan dosya
  otomatik silinir (DB + Telegram mesajları).
- Admin paneli şifre korumalıdır.
- Upload: sürükle-bırak / dosya seç, **URL ile upload**, **Ctrl+V pano yapıştırma**.

## Kurulum

```bash
cd /home/hyaena/telegram
go mod tidy

# .env düzenle (ADMIN_PASSWORD zorunlu)
cp .env.example .env
nano .env
# config.ini: api_id / api_hash dolu olmalı
```

## Çalıştırma (yerel — ilk çalıştırma)

İlk çalıştırmada **terminalde interaktif Telegram login** gerekir (telefon + kod):

```bash
./run.sh
# > Telefon numarası (+90...): ...
# > Telegram kodu: ...
# Login tamamlanınca session dosyası yazılır; sonraki açılışlar prompt sormaz.
```

Servis `http://127.0.0.1:8021` üzerinde çalışır.

## systemd (otomatik başlatma)

```bash
go build -o tgshare .
mkdir -p ~/.config/systemd/user
cp deploy/storage.service ~/.config/systemd/user/
systemctl --user daemon-reload

# İLK login'i önce terminalde yap (yukarıdaki gibi), sonra servisi başlat:
systemctl --user enable --now storage
loginctl enable-linger $USER    # oturum kapansa da çalışsın
systemctl --user status storage
```

## Kanal filosu

- **Bootstrap:** ilk kurulumda (hiç kanal yokken) `BOOTSTRAP_CHANNELS` (varsayılan 1)
  kadar kanal açılır.
- **Peg:** filo kurulduktan sonra **hedefe tamamlama yoktur**; kanal sayısı mevcut hâline
  peg'lenir.
- **Büyüme:** her gün saat `04:00`'da **+1 kanal** eklenir → kanal başına mesaj yükü düşer,
  ban/hız riski azalır.

## Yapılandırma (`.env`)

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `ADMIN_PASSWORD` | `degistir-ben` | Admin panel şifresi (**değiştir!**) |
| `TELEGRAM_API_ID/HASH` | – | config.ini yerine env ile de verilebilir |
| `SESSION_FILE` | `session` | Telegram oturum dosyası yolu |
| `SEGMENT_BYTES` | `20971520` | Segment boyutu (20 MB) |
| `MAX_UPLOAD_BYTES` | `107374182400` | Tek dosya limiti (100 GB) |
| `MAX_CONCURRENT_DOWNLOADS` | `16` | Eşzamanlı indirme sınırı (RAM koruması) |
| `BOOTSTRAP_CHANNELS` | `1` | İlk kurulumda açılacak kanal (yalnızca filo boşsa) |
| `CHANNEL_INTERVAL_SN` | `90` | Kanal oluşturma arası (sn) |
| `HOST` / `PORT` | `0.0.0.0` / `8000` | Web arayüzü |
| `RATE_LIMIT_PER_MIN` | `20` | Per-IP istek limiti |
| `DATABASE_URL` | – | Postgres (Supabase) pooler bağlantısı |
| `PUBLIC_BASE_URL` / `DIRECT_BASE_URL` | – | Paylaşım linki tabanları (CF cache'li / doğrudan) |
| `DIRECT_THRESHOLD_BYTES` | `503316480` | Bu boyut üstü dosyalar direct tabandan |
| `CF_ZONE_ID/EMAIL/KEY` | – | Dosya silinince edge cache purge |

`config.ini`: Telegram `api_id`, `api_hash`, `two_fa_password`.

## VPS dağıtımı (orfi)

```bash
# yerelde derle (VPS'te Go gerekmez)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o tgshare .
scp tgshare root@orfi:/opt/tgshare/tgshare
ssh root@orfi systemctl restart tgshare
```

`/opt/tgshare/`: binary + `.env` + `session` + `data/`. systemd unit'i
`Restart=always` ile kuruludur. Toprak: kullanıcı → Cloudflare Worker
(`storage.hyaena.co.uk`, bkz. `worker/`) → VPS:8080 → Telegram. İndirmeler
Worker üzerinden edge cache'lenir; 480 MB üstü linkler `direct.hyaena.co.uk`
üzerinden Cloudflare'siz gider.

## İşletimsel notlar

- **Telegram heartbeat:** süreç her 5 dakikada canlı bir RPC atar; 3 üst üste
  hata süreci kapatır ve systemd 5 sn'de taze başlatır. (Ekim 2026'daki
  `CONNECTION_LAYER_INVALID` arızası — günlerce süren bellek-durumu çürümesi —
  bu mekanizmayla dakikalar içinde iyileşirdi.)
- **Referans tazeleme:** her gün 05:00'te tüm parçaların Telegram
  `file_reference`'ı mesaj id'lerinden yenilenir (bayat referans = yavaş ilk
  indirme).
- **Rate limit güveni:** `X-Forwarded-For` yalnızca bağlantı Cloudflare
  aralıklarından geliyorsa kabul edilir; doğrudan bağlanan istemcilerde gerçek
  `RemoteAddr` esas alınır (spoof ile limit atlama kapalı).
- **Worker deploy:** `cd worker && npx wrangler deploy` (cache anahtarı gelen
  istektir; hata yanıtları `no-store` ile negatif cache'lenmez).

## API

Açık (rate-limited): `/api/upload/start`, `/api/upload/by-url`,
`/api/upload/{token}/parts/{i}`, `/api/upload/{token}/finish`,
`/api/upload/{token}/status`, `/api/upload/{token}` (cancel),
`/api/download/{token}`, `/api/download/{token}/{name}`

Admin (şifreli): `/api/admin/login`, `/api/admin/summary`, `/api/admin/channels`,
`/api/admin/files`, `/api/admin/settings`

## Testler

```bash
go test ./...
```

Python'dan portlanan birim testleri: parça sayısı, round-robin, admin token (HMAC + TTL),
share token, sanitize_filename, rate limiter, DB CRUD + cascade.

## Notlar / Sınırlar

- **"Sınırsız" pratikte büyük depolamadır**, gerçek limitsiz değil: tek mesaj 2 GB
  (Premium 4 GB), aşırı kullanım flood/ban riski, geçici disk alanı gerekir.
- `config.ini` ve `session` (GoMTProto oturumu) gizlidir; paylaşma.
- Kanal oluşturma + upload ana hesapla yapılır; günde 1 kanal makul risktir.
- İleride Cloudflare Tunnel ile internete açılabilir (yerel HTTP zaten).
