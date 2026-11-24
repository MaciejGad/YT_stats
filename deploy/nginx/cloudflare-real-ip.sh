#!/usr/bin/env bash
# Tworzy /etc/nginx/snippets/cloudflare-real-ip.conf z oficjalnej listy
# adresów Cloudflare. Uruchamiany na serwerze jako root:
#
#   sudo ./cloudflare-real-ip.sh
#
# Nginx bierze wtedy adres klienta z nagłówka CF-Connecting-IP, ale tylko
# dla połączeń przychodzących z adresów Cloudflare (inni nie mogą go podrobić).
set -euo pipefail

OUT=/etc/nginx/snippets/cloudflare-real-ip.conf
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

v4="$(curl -fsS --max-time 15 https://www.cloudflare.com/ips-v4)"
v6="$(curl -fsS --max-time 15 https://www.cloudflare.com/ips-v6)"
[ -n "$v4" ] && [ -n "$v6" ] || { echo "Nie udało się pobrać listy adresów Cloudflare" >&2; exit 1; }

{
  echo "# Adresy Cloudflare ($(date -u +%Y-%m-%d)), źródło: https://www.cloudflare.com/ips/"
  echo "# Wygenerowane przez cloudflare-real-ip.sh — nie edytuj ręcznie."
  for net in $v4 $v6; do
    case "$net" in
      */*) echo "set_real_ip_from $net;" ;;
      *) echo "Nieoczekiwany wpis: $net" >&2; exit 1 ;;
    esac
  done
  echo "real_ip_header CF-Connecting-IP;"
} > "$TMP"

install -d -m 0755 /etc/nginx/snippets
install -m 0644 "$TMP" "$OUT"
echo "Zapisano $OUT ($(grep -c set_real_ip_from "$OUT") zakresów)."
