#!/usr/bin/env bash
# Sprawdza wdrożony panel z zewnątrz (z Maca), bez logowania.
#
#   deploy/smoke.sh [https://maciejgad.pl]
set -uo pipefail

BASE="${1:-https://maciejgad.pl}"
UA="Mozilla/5.0 (Macintosh) yt-stats-smoke"
pass=0
fail=0

# fetch_headers URL → nagłówki odpowiedzi (bez podążania za przekierowaniami)
fetch_headers() { curl -s -o /dev/null -D - --max-time 15 -A "$UA" "$1" | tr -d '\r'; }
status() { fetch_headers "$1" | awk 'NR==1 {print $2}'; }
header() { fetch_headers "$1" | awk -v h="$(echo "$2" | tr 'A-Z' 'a-z')" -F': ' 'tolower($1)==h {print $2; exit}'; }

check() { # opis, oczekiwane, faktyczne
  if [ "$2" = "$3" ]; then
    printf '  ok    %-52s %s\n' "$1" "$3"; pass=$((pass + 1))
  else
    printf '  BŁĄD  %-52s jest %q, oczekiwano %q\n' "$1" "$3" "$2"; fail=$((fail + 1))
  fi
}
contains() { # opis, fragment, tekst
  case "$3" in
    *"$2"*) printf '  ok    %-52s\n' "$1"; pass=$((pass + 1)) ;;
    *) printf '  BŁĄD  %-52s brak %s\n' "$1" "$2"; fail=$((fail + 1)) ;;
  esac
}

echo "== Przekierowania"
check "http:// → https://" "301" "$(status "${BASE/https:/http:}/yt/")"
check "/yt → 301" "301" "$(status "$BASE/yt")"
check "/yt → Location" "$BASE/yt/" "$(header "$BASE/yt" Location)"
check "/yt/ bez sesji → 303" "303" "$(status "$BASE/yt/")"
check "/yt/ bez sesji → Location" "/yt/login" "$(header "$BASE/yt/" Location)"

echo "== Logowanie i dane"
h="$(fetch_headers "$BASE/yt/login")"
check "/yt/login → 200" "200" "$(echo "$h" | awk 'NR==1 {print $2}')"
contains "CSP" "default-src 'self'" "$h"
contains "X-Content-Type-Options" "nosniff" "$h"
contains "Cache-Control: no-store" "no-store" "$h"
contains "HSTS" "strict-transport-security" "$(echo "$h" | tr 'A-Z' 'a-z')"
contains "ciasteczko yt_login z Secure i HttpOnly" "HttpOnly; Secure" "$(echo "$h" | grep -i '^set-cookie: yt_login')"
check "/yt/stat.json bez sesji → 401" "401" "$(status "$BASE/yt/stat.json")"
contains "/yt/stat.json bez sesji → JSON z błędem" '"error"' "$(curl -s --max-time 15 -A "$UA" "$BASE/yt/stat.json")"

echo "== Pliki statyczne"
for f in script.js style.css logo.png favicon.png; do
  check "/yt/$f → 200" "200" "$(status "$BASE/yt/$f")"
done
check "script.js bez wersji: Cache-Control" "no-cache" "$(header "$BASE/yt/script.js" Cache-Control)"
css="$(curl -s --max-time 15 -A "$UA" "$BASE/yt/login" | grep -o '/yt/style.css?v=[0-9a-f]*' | head -1)"
contains "logowanie wskazuje wersjonowany style.css" "/yt/style.css?v=" "$css"
[ -n "$css" ] && check "wersjonowany style.css: Cache-Control" "public, max-age=31536000, immutable" "$(header "$BASE$css" Cache-Control)"
printf '  info  %-52s %s\n' "script.js: cf-cache-status" "$(header "$BASE/yt/script.js" cf-cache-status)"

echo "== Dawne adresy (nie mogą zwracać danych)"
for p in index.php stat.php functions.php require_auth.php graph_data.php cron.php cron.sh statistics.csv; do
  check "/yt/$p → 404" "404" "$(status "$BASE/yt/$p")"
done

echo
echo "Wynik: $pass ok, $fail błędów"
[ "$fail" -eq 0 ]
