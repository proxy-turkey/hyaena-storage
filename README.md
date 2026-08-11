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
| `SEGMENT_BYTES` | `20971520` | Segment boyutu (20 MB) |
| `MAX_UPLOAD_BYTES` | `107374182400` | Tek dosya limiti (100 GB) |
| `BOOTSTRAP_CHANNELS` | `1` | İlk kurulumda açılacak kanal (yalnızca filo boşsa) |
| `CHANNEL_INTERVAL_SN` | `90` | Kanal oluşturma arası (sn) |
| `PORT` | `8021` | Web portu |
| `RATE_LIMIT_PER_MIN` | `20` | Per-IP istek limiti |

`config.ini`: Telegram `api_id`, `api_hash`, `two_fa_password`.

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
