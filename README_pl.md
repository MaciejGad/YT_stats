# yt-stats

[English](README.md) | **Polski**

Prywatny, chroniony hasłem panel statystyk dowolnego kanału YouTube. ID kanału podajesz w konfiguracji, a nazwa, handle i awatar kanału są pobierane z YouTube i pokazywane w nagłówku. Panel powstał dla [@YarnPowerPL](https://www.youtube.com/@YarnPowerPL) i działa pod `https://maciejgad.pl/yt/`, ale każdy może go wdrożyć dla własnego kanału.

Pokazuje listę filmów z liczbą wyświetleń (sortowanie: najnowsze lub najpopularniejsze) oraz podsumowanie kanału: subskrybentów, wyświetlenia i liczbę filmów. Dane pochodzą z YouTube Data API v3, z zapasowym kluczem API na wypadek wyczerpania limitu.

## Budowa

- Jedna binarka Go (`cmd/yt-stats`) bez zewnętrznych usług ani bazy danych.
- Szablony HTML, CSS i JavaScript leżą osobno w `web/` — można je zmieniać bez rekompilacji, a adresy plików statycznych są wersjonowane automatycznie.
- Logowanie hasłem (Argon2id), sesje w pliku JSON, ochrona CSRF i limit prób logowania.
- Na serwerze: usługa systemd za Nginx i Cloudflare.

Szczegóły: [requirements_pl.md](requirements_pl.md), [plan_pl.md](plan_pl.md), [deploy/README_pl.md](deploy/README_pl.md).

## Konfiguracja

| Pole | Znaczenie |
| --- | --- |
| `listen` | Adres nasłuchu, np. `127.0.0.1:8091`. |
| `channel_id` | ID kanału (`UC…`). Znajdziesz je w YouTube Studio → Ustawienia → Kanał → Ustawienia zaawansowane albo zapytaniem `channels.list?part=id&forHandle=@handle`. |
| `api_key`, `api_key_fallback` | Klucze YouTube Data API v3 z Google Cloud Console; zapasowy jest opcjonalny. |
| `username`, `password_hash` | Jedyne konto; hash tworzy `yt-stats hash-password`. |
| `session_file` | Plik z sesjami. |
| `web_dir` | Katalog z szablonami i plikami statycznymi (`web/`). |

Panel działa pod `/yt/`. `web/logo.png` i `web/favicon.png` są widoczne tylko do czasu pobrania awatara kanału; możesz je podmienić.

## Uruchomienie lokalne

```bash
cp config.example.json config.json   # uzupełnij pola
go run ./cmd/yt-stats hash-password  # skrót hasła do config.json
go run ./cmd/yt-stats -config config.json
```

Testy:

```bash
go test ./...
node --test test/js/*.test.js
```

## Jak powstał projekt

Kod, testy, skrypty wdrożeniowe i dokumentacja zostały napisane przez [Claude Code](https://claude.com/claude-code) z modelem **Claude Opus 5.5**.

Rolą człowieka było:

- określenie wymagań i decyzje w przeglądzie ([requirements_pl.md](requirements_pl.md)),
- ustalenie planu prac krok po kroku ([plan_pl.md](plan_pl.md)),
- weryfikacja każdego kroku — przegląd zmian, uruchomienie testów i scenariuszy manualnych, akceptacja przed commitem,
- wdrożenie na serwer i przełączenie ruchu.
