#!/usr/bin/env bash
# Telegram Storage — Go sürümü çalıştırıcısı
set -euo pipefail
cd "$(dirname "$0")"

go build -o tgshare .
exec ./tgshare "$@"
