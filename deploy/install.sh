#!/usr/bin/env bash
# Instaluje lub aktualizuje yt-stats na serwerze. Uruchamiany jako root
# z rozpakowanej paczki z deploy/build.sh:
#
#   sudo ./install.sh
#
# Co robi (bezpiecznie przy wielokrotnym uruchomieniu):
#   - zakłada użytkownika systemowego yt-stats,
#   - kopiuje binarkę do /opt/yt-stats/ i pliki interfejsu do /opt/yt-stats/web/,
#     zachowując poprzednią wersję jako *.prev (do wycofania),
#   - przy pierwszej instalacji tworzy /etc/yt-stats/config.json z przykładu
#     i kończy pracę, żebyś uzupełnił klucz API i hash hasła,
#   - sprawdza konfigurację (check-config) i dopiero wtedy restartuje usługę.
set -euo pipefail

cd "$(dirname "$0")"

SERVICE=yt-stats
APP_DIR=/opt/yt-stats
CONF_DIR=/etc/yt-stats
CONF="$CONF_DIR/config.json"
UNIT=/etc/systemd/system/yt-stats.service

die() { echo "BŁĄD: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "uruchom jako root (sudo ./install.sh)"
[ -x ./yt-stats ] && [ -d ./web ] || die "uruchom z katalogu rozpakowanej paczki"

# Architektura paczki musi pasować do serwera.
want="$(cat ARCH)"
case "$(uname -m)" in
  x86_64) have=amd64 ;;
  aarch64|arm64) have=arm64 ;;
  *) have="$(uname -m)" ;;
esac
[ "$want" = "$have" ] || die "paczka jest dla $want, a serwer to $have (zbuduj: deploy/build.sh $have)"

echo "==> Użytkownik $SERVICE"
if ! id "$SERVICE" >/dev/null 2>&1; then
  useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$SERVICE"
  echo "utworzono"
else
  echo "istnieje"
fi

echo "==> Pliki aplikacji w $APP_DIR"
install -d -o root -g root -m 0755 "$APP_DIR"
if [ -f "$APP_DIR/yt-stats" ]; then
  cp -p "$APP_DIR/yt-stats" "$APP_DIR/yt-stats.prev"
fi
install -o root -g root -m 0755 ./yt-stats "$APP_DIR/yt-stats.new"
mv -f "$APP_DIR/yt-stats.new" "$APP_DIR/yt-stats"

rm -rf "$APP_DIR/web.new"
cp -R ./web "$APP_DIR/web.new"
chown -R root:root "$APP_DIR/web.new"
find "$APP_DIR/web.new" -type d -exec chmod 0755 {} +
find "$APP_DIR/web.new" -type f -exec chmod 0644 {} +
if [ -d "$APP_DIR/web" ]; then
  rm -rf "$APP_DIR/web.prev"
  mv "$APP_DIR/web" "$APP_DIR/web.prev"
fi
mv "$APP_DIR/web.new" "$APP_DIR/web"
install -o root -g root -m 0644 ./README.md "$APP_DIR/README.md"
echo "wersja $(cat VERSION)"

echo "==> Konfiguracja w $CONF"
install -d -o root -g "$SERVICE" -m 0750 "$CONF_DIR"
if [ ! -f "$CONF" ]; then
  install -o root -g "$SERVICE" -m 0640 ./config.server.example.json "$CONF"
  install -o root -g root -m 0644 ./yt-stats.service "$UNIT"
  systemctl daemon-reload
  cat <<EOF

Utworzono $CONF z pustymi polami. Uzupełnij je:
  1. Hash hasła:  $APP_DIR/yt-stats hash-password
  2. Edycja:      nano $CONF   (api_key, username, password_hash)
  3. Ponownie:    sudo ./install.sh
EOF
  exit 0
fi
chown root:"$SERVICE" "$CONF"
chmod 0640 "$CONF"

# Sprawdzenie jako użytkownik usługi: te same uprawnienia co w działającej usłudze.
if ! runuser -u "$SERVICE" -- "$APP_DIR/yt-stats" check-config -config "$CONF"; then
  die "konfiguracja jest niepoprawna; usługa nie została zrestartowana"
fi

echo "==> Usługa systemd"
install -o root -g root -m 0644 ./yt-stats.service "$UNIT"
systemctl daemon-reload
systemctl enable "$SERVICE" >/dev/null
systemctl restart "$SERVICE"

for _ in 1 2 3 4 5 6 7 8 9 10; do
  if systemctl is-active --quiet "$SERVICE"; then break; fi
  sleep 1
done
if ! systemctl is-active --quiet "$SERVICE"; then
  journalctl -u "$SERVICE" -n 30 --no-pager >&2 || true
  die "usługa nie wystartowała (powyżej ostatnie wpisy z dziennika)"
fi

if command -v curl >/dev/null; then
  # systemd zgłasza „active” zaraz po starcie procesu, zanim serwer otworzy
  # port, więc czekamy do 10 s na odpowiedź.
  code=000
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    code="$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8091/yt/login || true)"
    [ "$code" = "000" ] || break
    sleep 0.5
  done
  [ "$code" = "200" ] || die "usługa działa, ale /yt/login zwraca $code"
  echo "GET /yt/login → 200"
fi

echo
echo "Gotowe: $(systemctl is-active "$SERVICE"), wersja $(cat VERSION)."
echo "Dziennik: journalctl -u $SERVICE -f"
