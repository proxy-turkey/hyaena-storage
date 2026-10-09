#!/usr/bin/env bash
# Hyaena Storage — tek komutlu güvenli deploy (B-05: değişiklik yönetimi)
#
#   ./deploy.sh                    # master'ı test+derle+taşı+sağlık kontrolü
#
# Kurallar:
#   1. Yalnızca temiz git ağacından (commit'lenmiş kod) deploy edilir —
#      repo dışı kod canlıya asla gitmez (Ekim 2026 kazasının dersi).
#   2. Testler geçmeden binary üretilmez.
#   3. Taşıma sonrası /health kontrolü; hata varsa otomatik geri alma.
#
# Ortam değişkenleri:
#   REMOTE_HOST  (varsayılan root@orfi.hyaena.qzz.io)
#   REMOTE_DIR   (varsayılan /opt/tgshare)
#   SKIP_TESTS=1 testleri atlar (acil durum; önerilmez)
set -euo pipefail

REMOTE_HOST="${REMOTE_HOST:-root@orfi.hyaena.qzz.io}"
REMOTE_DIR="${REMOTE_DIR:-/opt/tgshare}"
cd "$(dirname "$0")"

echo "==> 1/6 Kaynak kontrolü"
if [ -n "$(git status --porcelain)" ]; then
  echo "HATA: commit'lenmemiş değişiklikler var. Önce commit'le:" >&2
  git status --short >&2
  exit 1
fi
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
echo "    dal: $BRANCH @ $(git rev-parse --short HEAD)"

if [ "${SKIP_TESTS:-0}" != "1" ]; then
  echo "==> 2/6 Testler"
  go test ./... -count=1 -p 1
else
  echo "==> 2/6 Testler ATLANDI (SKIP_TESTS=1)"
fi

echo "==> 3/6 Derleme"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /tmp/tgshare-deploy .
ssh_run() { ssh -o ConnectTimeout=15 "$REMOTE_HOST" "$@"; }

echo "==> 4/6 Taşıma + önceki sürümü saklama"
ssh_run "cp -f $REMOTE_DIR/tgshare $REMOTE_DIR/tgshare.prev 2>/dev/null || true"
scp -q /tmp/tgshare-deploy "$REMOTE_HOST:$REMOTE_DIR/tgshare.new"
ssh_run "mv $REMOTE_DIR/tgshare.new $REMOTE_DIR/tgshare && chmod +x $REMOTE_DIR/tgshare"

echo "==> 5/6 Yeniden başlatma"
ssh_run "systemctl restart tgshare"

echo "==> 6/6 Sağlık kontrolü"
OK=0
for i in $(seq 1 10); do
  sleep 2
  if ssh_run "curl -sf -m 5 http://127.0.0.1:8080/health" | grep -q '"ok":true'; then
    OK=1
    break
  fi
done
if [ "$OK" = "1" ]; then
  echo "DEPLOY BAŞARILI ✔  $(date -u '+%Y-%m-%d %H:%M UTC') — $(git rev-parse --short HEAD)"
else
  echo "SAĞLIK KONTROLÜ BAŞARISIZ — önceki sürüme dönülüyor!" >&2
  ssh_run "cp -f $REMOTE_DIR/tgshare.prev $REMOTE_DIR/tgshare && chmod +x $REMOTE_DIR/tgshare && systemctl restart tgshare" || true
  exit 1
fi
