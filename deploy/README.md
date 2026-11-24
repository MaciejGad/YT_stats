# Wdrożenie yt-stats na Mikrusie

Usługa działa jako `yt-stats.service` pod systemd, nasłuchuje na `127.0.0.1:8091`, a Nginx (krok 6 planu) przekazuje do niej `/yt/`.

| Ścieżka | Zawartość | Właściciel, uprawnienia |
| --- | --- | --- |
| `/opt/yt-stats/yt-stats` | binarka | `root:root 0755` |
| `/opt/yt-stats/web/` | szablony, CSS, JS, logo (można edytować bez restartu) | `root:root 0644` |
| `/etc/yt-stats/config.json` | konfiguracja z kluczem API i hashem hasła | `root:yt-stats 0640` |
| `/var/lib/yt-stats/sessions.json` | sesje (tylko hashe) | `yt-stats 0600` |
| `/etc/systemd/system/yt-stats.service` | usługa | `root:root 0644` |

## 1. Architektura serwera

Na serwerze:

```bash
uname -m
```

`x86_64` → paczka `amd64`, `aarch64` → paczka `arm64`.

## 2. Budowanie na Macu

W katalogu projektu (uruchamia testy, potem buduje):

```bash
deploy/build.sh amd64
```

Wynik: `dist/yt-stats-<wersja>-linux-amd64.tar.gz` z binarką, katalogiem `web/`, plikiem usługi, przykładową konfiguracją, `install.sh` i tą instrukcją.

## 3. Pierwsza instalacja

```bash
scp dist/yt-stats-*-linux-amd64.tar.gz UŻYTKOWNIK@SERWER:/tmp/
ssh UŻYTKOWNIK@SERWER
cd /tmp && tar xzf yt-stats-*-linux-amd64.tar.gz && cd yt-stats-*-linux-amd64
sudo ./install.sh
```

Pierwsze uruchomienie zakłada użytkownika `yt-stats`, kopiuje pliki i tworzy `/etc/yt-stats/config.json` z pustymi polami, po czym kończy pracę. Uzupełnij konfigurację:

```bash
/opt/yt-stats/yt-stats hash-password
sudo nano /etc/yt-stats/config.json
```

Wpisz `api_key` (opcjonalnie `api_key_fallback`), `username` i wynik `hash-password` w `password_hash`. Potem ponownie:

```bash
sudo ./install.sh
```

Skrypt sprawdzi konfigurację jako użytkownik usługi (`check-config`), uruchomi usługę i sprawdzi, że `/yt/login` odpowiada `200`.

## 4. Aktualizacja

Tak samo jak pierwsza instalacja: zbuduj nową paczkę, skopiuj, rozpakuj, `sudo ./install.sh`. Konfiguracja i sesje zostają. Poprzednia binarka i pliki interfejsu zostają jako `/opt/yt-stats/yt-stats.prev` i `/opt/yt-stats/web.prev`.

Same zmiany w `web/` (CSS, HTML, JS, logo) można też wgrać bezpośrednio do `/opt/yt-stats/web/` — działają po przeładowaniu strony, bez restartu.

## 5. Sprawdzenie po instalacji

```bash
systemctl status yt-stats
ss -ltnp | grep 8091
curl -si http://127.0.0.1:8091/yt/ | head -3
curl -si http://127.0.0.1:8091/yt/stat.json | head -1
journalctl -u yt-stats -n 50 --no-pager
```

Oczekiwane: usługa `active (running)`, nasłuch tylko na `127.0.0.1:8091`, `/yt/` → `303` na `/yt/login`, `/yt/stat.json` → `401`.

Brak sekretów w dzienniku (fragment klucza API i hasha hasła; oba wyniki powinny wynosić `0`):

```bash
sudo journalctl -u yt-stats --no-pager | grep -c 'AIza' ; sudo journalctl -u yt-stats --no-pager | grep -c 'argon2id'
```

Automatyczny restart po awarii:

```bash
sudo systemctl kill -s KILL yt-stats; sleep 7; systemctl is-active yt-stats
```

## 6. Pomiary

Pamięć (RSS w KiB) w spoczynku, podczas logowania i po pobraniu statystyk:

```bash
ps -o rss=,cmd= -C yt-stats
systemctl show yt-stats -p MemoryCurrent -p MemoryPeak
```

Czas weryfikacji hasła na serwerze (różnica między wpisami w dzienniku jest zbyt zgrubna, więc mierzymy samo hashowanie z tymi samymi parametrami):

```bash
time (printf 'test\ntest\n' | /opt/yt-stats/yt-stats hash-password >/dev/null)
```

Wynik to czas dwóch rzeczy naraz: startu procesu i jednego Argon2id (m = 19 MiB, t = 2). Oczekiwany poniżej 300 ms.

## 7. Usunięcie wszystkich sesji (np. po zmianie hasła)

Polecenie musi działać jako użytkownik usługi — uruchomione jako root utworzyłoby plik sesji, którego usługa nie odczyta.

```bash
sudo systemctl stop yt-stats
sudo runuser -u yt-stats -- /opt/yt-stats/yt-stats revoke-sessions -config /etc/yt-stats/config.json
sudo systemctl start yt-stats
```

## 8. Wycofanie wersji

```bash
sudo mv /opt/yt-stats/yt-stats.prev /opt/yt-stats/yt-stats
sudo rm -rf /opt/yt-stats/web && sudo mv /opt/yt-stats/web.prev /opt/yt-stats/web
sudo systemctl restart yt-stats
```

## Rozwiązywanie problemów

**Usługa nie startuje ze statusem `226/NAMESPACE` lub `218/CAPABILITIES`.** Niektóre kontenery (np. LXC bez uprawnień) nie pozwalają na część opcji izolacji. Wyłącz je nakładką, zamiast edytować plik usługi:

```bash
sudo systemctl edit yt-stats
```

i wpisz:

```ini
[Service]
PrivateDevices=no
ProtectKernelTunables=no
ProtectKernelModules=no
ProtectKernelLogs=no
ProtectControlGroups=no
ProtectClock=no
ProtectHostname=no
RestrictNamespaces=no
```

Następnie `sudo systemctl restart yt-stats`. Jeśli nadal nie działa, dopisz `PrivateTmp=no` i `ProtectHome=no`.

**`plik sesji ... jest zablokowany`.** Działa już inna instancja (np. uruchomiona ręcznie). Sprawdź `ps -C yt-stats`.

**`plik konfiguracji ma uprawnienia 0644`.** `sudo chmod 640 /etc/yt-stats/config.json && sudo chown root:yt-stats /etc/yt-stats/config.json`.

**`odczyt pliku sesji: permission denied`.** Plik sesji utworzył root (np. `revoke-sessions` bez `runuser`). Napraw: `sudo chown yt-stats:yt-stats /var/lib/yt-stats/sessions.json*`.
