# Plan implementacji — statystyki YouTube YarnPower

Zachowanie aplikacji, limity i kryteria odbioru opisuje [wymagania.md](wymagania.md). Każdy krok kończy się zielonym `go test ./...` i przejściem scenariuszy manualnych z tego kroku.

## Struktura projektu

```
cmd/yt-stats/        main: serwer, polecenia hash-password i revoke-sessions
internal/config/     wczytanie i walidacja konfiguracji
internal/youtube/    klient API, paginacja, zapasowy klucz
internal/stats/      cache, single-flight, stan awaryjny
internal/auth/       Argon2id, sesje, plik sesji, CSRF, limiter
internal/web/        handlery HTTP, nagłówki, ładowanie plików z web_dir
web/                 index.html, login.html, script.js, style.css, favicon.svg
deploy/              yt-stats.service, przykład konfiguracji Nginx
config.example.json  przykładowa konfiguracja z pustymi wartościami
```

## 1. Przygotuj projekt Go

- Utwórz moduł Go w `~/Code/yt_stats` oraz `git init` (`.gitignore` już wyklucza `old_implementation/`, binarkę i lokalną konfigurację).
- Oprzyj serwer na `net/http`, widoki na `html/template`, a format danych na `encoding/json`. Do Argon2id dodaj `golang.org/x/crypto` w wersji zgodnej z używanym Go. Zaimportuj `time/tzdata`. Zapisz `go.mod` i `go.sum`.
- Konfiguracja w JSON (`config.json`, uprawnienia `0600`) z polami: `listen`, `channel_id`, `api_key`, `api_key_fallback` (opcjonalny), `username`, `password_hash`, `session_file`, `web_dir`. Walidacja zgłasza wszystkie brakujące lub błędne pola naraz, z ich nazwami i bez wartości. Ścieżkę konfiguracji podaje flaga `-config`.
- Polecenie `yt-stats hash-password` czyta hasło bez echa dwa razy i wypisuje hash Argon2id w formacie PHC (`$argon2id$v=19$m=19456,t=2,p=1$...`).
- Potwierdź identyfikator kanału zapytaniem `channels.list?part=id&forHandle=@YarnPowerPL` (oczekiwany `UC5711W83ebIxkC8bHjOKI7g`).
- Przygotuj ładowanie plików z `web_dir`: przy starcie wczytaj i sparsuj wszystkie pliki, w czasie działania przy każdym żądaniu porównuj czas modyfikacji i wczytuj ponownie tylko zmienione. Błędny szablon zostawia poprzednią wersję i trafia do logu.

**Testy jednostkowe**
- `config`: poprawny plik; brak każdego wymaganego pola osobno (komunikat zawiera nazwę pola); kilka braków naraz; nieznane pole; zły format `listen`; `channel_id` bez prefiksu `UC`; `password_hash` w innym formacie niż PHC Argon2id; brak `api_key_fallback` jest dozwolony; komunikat błędu nie zawiera wartości klucza.
- `hash-password`: hash da się zweryfikować tym samym hasłem i nie da się innym; dwa wywołania dają różne sole; niezgodne powtórzenie hasła daje błąd.
- Ładowanie `web_dir`: brak pliku przy starcie daje błąd z nazwą pliku; błąd składni szablonu przy starcie daje błąd; zmiana pliku (z podmienionym czasem modyfikacji) jest widoczna w kolejnym renderowaniu; zmiana na błędny szablon zostawia poprzednią wersję; równoległe renderowanie podczas podmiany przechodzi pod `-race`.

**Scenariusze manualne**
- Uruchom `go run ./cmd/yt-stats -config config.example.json`: start przerwany, komunikat wymienia wszystkie puste pola.
- Uruchom `go run ./cmd/yt-stats hash-password`: hasło nie pojawia się na ekranie, wynik zaczyna się od `$argon2id$`.
- Wywołaj `channels.list` z `forHandle` (np. `curl` z kluczem w zmiennej środowiskowej) i porównaj ID z konfiguracją.

## 2. Zaimplementuj klienta YouTube i cache

- Pobierz przez `channels.list` (`part=statistics,contentDetails`) statystyki i identyfikator playlisty przesłanych filmów. Ukryty licznik subskrybentów (`hiddenSubscriberCount`) daje `null`.
- Pobieraj kolejne strony `playlistItems.list` (`maxResults=50`) według `nextPageToken`, maksymalnie 200 stron. Szczegóły pobieraj grupami po 50 identyfikatorów przez `videos.list` z `part=snippet,statistics` i parametrem `fields` zawężającym odpowiedź. Odtwórz kolejność playlisty, usuń duplikaty i pomiń filmy, których API nie zwróciło.
- Użyj stałego adresu `https://www.googleapis.com/youtube/v3/`, limitu 10 s na żądanie, 30 s na całe odświeżenie i 5 MiB na odpowiedź. Błędy sanituj przed logowaniem (usuń parametr `key` z URL, loguj kod HTTP i `reason`).
- Obsłuż zapasowy klucz: przy `403` z powodem `quotaExceeded` lub `dailyLimitExceeded` ponów to samo żądanie kluczem zapasowym, zapamiętaj przełączenie do najbliższej północy `America/Los_Angeles` i zaloguj je. Inne błędy nie przełączają klucza.
- Dodaj cache w `internal/stats` z jednym wspólnym pobraniem dla równoległych żądań. Zastępuj poprzedni wynik dopiero po udanym pobraniu całości. Ustaw 60 s ważności, 24 h maksymalnego wieku wyniku awaryjnego i 30 s przerwy po błędzie. `update_time` zapisuj w ISO 8601 w strefie `Europe/Warsaw`.
- Zegar i adres API wstrzykuj przez interfejsy, żeby testy mogły je podmienić.

**Testy jednostkowe** (lokalny serwer `httptest`, kontrolowany zegar)
- Kanał: statystyki i playlista; ukryty licznik subskrybentów daje `null`; brak kanału (`items: []`) daje błąd.
- Paginacja: 1 strona, 3 strony, pusty kanał, przekroczony limit 200 stron daje błąd.
- Grupowanie: 0, 1, 50, 51 i 120 filmów daje odpowiednio 0, 1, 1, 2 i 3 wywołania `videos.list`; każde z co najwyżej 50 ID.
- Kolejność zgodna z playlistą mimo innej kolejności w `videos.list`; duplikat w playliście występuje raz; film nieobecny w `videos.list` jest pominięty; brak `viewCount` daje `null`.
- Błędy: timeout pojedynczego żądania, przekroczenie 30 s całości, odpowiedź większa niż 5 MiB, nieprawidłowy JSON, `500`, `403 forbidden`; log błędu nie zawiera klucza.
- Klucz zapasowy: `quotaExceeded` na kluczu głównym przełącza i ponawia to samo żądanie; kolejne odświeżenie od razu używa zapasowego; po północy czasu pacyficznego wraca klucz główny (test także przy zmianie czasu letniego); `keyInvalid` nie przełącza; brak skonfigurowanego klucza zapasowego daje zwykły błąd; limit wyczerpany na obu kluczach daje błąd.
- Cache: drugie żądanie w ciągu 60 s nie odpytuje API; po 61 s odpytuje; 10 równoległych żądań wywołuje jedno pobranie (pod `-race`); błąd przy wyniku sprzed 23 h zwraca `stale: true`; błąd przy wyniku sprzed 25 h daje `503`; w ciągu 30 s po błędzie brak nowych wywołań API; po 30 s nowa próba; częściowo udane pobranie nie podmienia poprzedniego wyniku.
- `update_time`: format ISO 8601 z przesunięciem `+01:00` zimą i `+02:00` latem.

**Scenariusze manualne**
- Mały program albo test z tagiem `integration` uruchomiony z prawdziwym kluczem: liczba filmów zgodna z kanałem, tytuły i liczniki zgodne z YouTube.
- Uruchom go z błędnym kluczem głównym i prawidłowym zapasowym: klucz nie przełącza się (to nie jest błąd limitu), log zawiera powód i nie zawiera klucza.

## 3. Dodaj logowanie i trwałe sesje

- Przygotuj formularz i weryfikację hasła Argon2id z parametrami z wymagań. Przy nieznanej nazwie użytkownika weryfikuj hasło względem stałego, fikcyjnego hasha. Porównuj nazwę w stałym czasie.
- Dodaj limiter: 5 nieudanych prób na IP w 15 minut, 50 łącznie na godzinę, `429` z `Retry-After`, maksymalnie 10 000 adresów. Ogranicz równoległe weryfikacje do 2 z oczekiwaniem do 3 s, potem `503`. Ogranicz formularz do 4 KiB. Adres klienta bierz z `X-Real-IP` tylko przy połączeniu z loopbacka.
- Zabezpiecz oba formularze tokenem CSRF. Dla logowania powiąż token z krótką sesją przed logowaniem (w pamięci, 30 minut, maksymalnie 1000), a po sukcesie wystaw nowy identyfikator sesji właściwej. Token wylogowania jest powiązany z sesją właściwą. Porównuj tokeny w stałym czasie.
- Wygeneruj identyfikatory przez `crypto/rand`. Zapisuj hash SHA-256 i termin wygaśnięcia (maksymalnie 100 sesji, przy nadmiarze usuń najstarszą). Synchronizuj modyfikacje, a plik zastępuj atomowo: zapis tymczasowy w tym samym katalogu, `fsync`, zmiana nazwy. Ustaw uprawnienia `0600`. Aktualizuj stan w pamięci dopiero po udanym zapisie. Trzymaj `flock` na pliku blokady przez cały czas działania.
- Dodaj polecenie `yt-stats revoke-sessions`, które bierze tę samą blokadę i opróżnia plik sesji.
- Dodaj wspólne sprawdzanie sesji przed renderowaniem panelu i zwróceniem danych. Ciasteczko ustawiaj i usuwaj z tą samą ścieżką. Przy błędzie zapisu wylogowania pokaż błąd i umożliw ponowienie.
- Zmierz czas weryfikacji Argon2id na Mikrusie (oczekiwany poniżej 300 ms).

**Testy jednostkowe** (kontrolowany zegar, katalog tymczasowy)
- Hasło: poprawne logowanie; błędne hasło; błędna nazwa; obie porażki dają identyczną odpowiedź; przy błędnej nazwie Argon2 też się wykonuje (licznik wywołań w atrapie).
- Sesja bez „Zapamiętaj”: ciasteczko bez `Expires`/`Max-Age`, ważna 11 h 59 min, nieważna po 12 h.
- Sesja z „Zapamiętaj”: `Max-Age` 90 dni, ważna 89 dni 23 h, nieważna po 90 dniach; korzystanie z panelu nie przesuwa terminu.
- Ciasteczko: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/yt/`, bez `Domain`; identyfikator to 43 znaki base64url; w pliku jest tylko hash, a nie identyfikator.
- Restart: nowa instancja magazynu wczytuje plik i akceptuje sesję; wygasłe wpisy znikają przy następnym zapisie.
- Plik sesji: uszkodzony JSON; plik większy niż 1 MiB; brak uprawnień do odczytu; brak uprawnień do zapisu przy logowaniu (logowanie zwraca błąd, sesja nie powstaje w pamięci); błąd zapisu przy wylogowaniu (błąd i sesja nadal ważna, ponowienie działa); uprawnienia po zapisie to `0600`; 101. sesja usuwa najstarszą.
- Blokada: druga instancja magazynu na tym samym pliku i `revoke-sessions` przy działającym magazynie kończą się błędem; `revoke-sessions` bez działającej usługi opróżnia plik.
- CSRF: brak tokenu, zły token, token z innej sesji przed logowaniem, wygasła sesja przed logowaniem — wszystkie dają `403`; po zalogowaniu identyfikator sesji jest inny niż przed logowaniem.
- Limiter: 5 porażek przepuszcza, 6. daje `429` z `Retry-After`; po 15 minutach znowu działa; sukces zeruje licznik; 51. porażka z różnych IP w ciągu godziny daje `429`; `X-Real-IP` ignorowany przy połączeniu spoza loopbacka; tablica nie przekracza 10 000 adresów.
- Równoległość: 3 jednoczesne weryfikacje (atrapa Argon2 z opóźnieniem) — 2 ruszają od razu, 3. czeka; po 3 s czekania `503`.
- Formularz większy niż 4 KiB daje `413`; nazwa ponad 64 B lub hasło ponad 256 B daje porażkę logowania bez wywołania Argon2.

**Scenariusze manualne**
- Zaloguj się w Chrome na `http://localhost:8091/yt/` bez „Zapamiętaj”; w DevTools → Application → Cookies sprawdź atrybuty i brak daty wygaśnięcia.
- Zaloguj się z „Zapamiętaj”, zrestartuj proces, przeładuj stronę — panel otwiera się bez logowania.
- Wyloguj się, użyj przycisku Wstecz i przeładuj — formularz logowania, nie panel.
- Podaj 6 razy złe hasło — po 5. próbie komunikat o limicie.
- Wyślij formularz logowania `curl`-em bez tokenu CSRF — `403`.
- Zatrzymaj proces, uruchom `revoke-sessions`, uruchom proces — wcześniej zalogowana przeglądarka trafia na logowanie. Uruchom `revoke-sessions` przy działającym procesie — błąd o blokadzie.
- Sprawdź `ls -l` pliku sesji (`-rw-------`) i jego zawartość (tylko hashe i daty).

## 4. Zbuduj panel i handlery HTTP

- Zaprojektuj od nowa `web/index.html`, `web/login.html`, `web/style.css`, `web/script.js` i `web/favicon.svg` według sekcji „Wygląd i responsywność” w wymaganiach. Ze starej wersji bierzemy tylko zakres danych; obsługę błędów i nieużywany kod wykresów piszemy od nowa albo pomijamy. Bez `setInterval`, bez zasobów zewnętrznych, bez skryptów i stylów inline (CSP).
- Skrypt pobiera `/yt/stat.json` przy wejściu i po kliknięciu „Odśwież”. Obsługuje ładowanie, pustą listę, dane awaryjne (`stale`), `503` i inne błędy z ponowieniem, błąd sieci oraz `401` (przekierowanie do logowania). Tytuły wstawia przez `textContent`, linki buduje wyłącznie z `videoId` (`https://www.youtube.com/watch?v=` + `encodeURIComponent`), z `rel="noopener"`.
- Liczby formatuje `Intl.NumberFormat('pl-PL')`, czas `Intl.DateTimeFormat('pl-PL', {timeZone: 'Europe/Warsaw'})`, `null` wyświetla jako „Niedostępne”. Sortowanie (kolejność playlisty / wyświetlenia malejąco) działa bez nowego żądania; „Niedostępne” trafia na koniec.
- Zaimplementuj trasy z tabeli w wymaganiach z obsługą `HEAD`, `405` i `404`, middleware nagłówków bezpieczeństwa oraz `Cache-Control: no-store` dla stron logowania, panelu i `stat.json`. Pliki statyczne serwuj z `web_dir` z krótkim `Cache-Control` (np. `max-age=300`) i `ETag`.
- Ustaw limity czasu serwera HTTP z wymagań.

**Testy jednostkowe** (`httptest`, atrapa klienta YouTube)
- Kontrakt JSON: pola `stats.subscriberCount`, `stats.viewsCount`, `videos[].videoId`, `videos[].title`, `videos[].viewCount`, `update_time`, `stale`; liczniki to ciągi cyfr albo `null`; `update_time` parsuje się jako RFC 3339; `Content-Type: application/json; charset=utf-8`.
- Bez sesji: `GET /yt/` daje `303` na `/yt/login`; `GET /yt/stat.json` daje `401` z JSON; atrapa YouTube nie została wywołana.
- `GET /yt/login` z ważną sesją daje `303` na `/yt/`.
- Błędy: `503` bez wyniku awaryjnego; komunikat w `error` nie zawiera szczegółów wewnętrznych.
- Trasy: `HEAD` na każdej trasie `GET`; `POST /yt/stat.json` daje `405` z `Allow`; `/yt/nieznane`, `/yt/index.php`, `/yt/stat.php`, `/yt/statistics.csv` dają `404`; `/yt/../config.json` i `/yt/%2e%2e/` nie wychodzą poza `web_dir`.
- Nagłówki: CSP, `X-Content-Type-Options`, `Referrer-Policy` na każdej odpowiedzi, także `404`; `no-store` na stronach chronionych i logowaniu.
- Renderowanie: tytuł filmu z `<script>` w danych nie trafia do HTML bez escapowania (szablony nie wstawiają danych filmów, ale test pilnuje tego założenia); formularze zawierają token CSRF.
- Pliki: zmiana `web/script.js` w katalogu tymczasowym jest serwowana bez restartu; `ETag` zmienia się po zmianie pliku.

**Testy przeglądarkowe skryptu** (opcjonalnie, `node --test` z atrapą DOM albo ręcznie)
- Formatowanie liczb i daty, sortowanie z wartościami `null`, budowa linku z `videoId` zawierającego znaki specjalne.

**Scenariusze manualne** (`http://localhost:8091/yt/`, Chrome i Firefox)
- Pierwsze wejście: stan ładowania, potem kafelki, lista i czas pobrania w czasie polskim; przy zmienionej strefie systemu (np. na `America/New_York`) czas dalej polski.
- „Odśwież” dwa razy w ciągu minuty: przycisk zablokowany w trakcie, czas pobrania się nie zmienia. Po minucie czas się zmienia.
- Sortowanie po wyświetleniach i powrót do kolejności publikacji.
- Klucz API zmieniony na błędny i restart: komunikat błędu z przyciskiem ponowienia. Przy działającym wyniku w pamięci i zepsutym kluczu (podmiana adresu API na atrapę): baner „dane z pamięci podręcznej” z pierwotnym czasem.
- Usuń ciasteczko w DevTools i kliknij „Odśwież”: przekierowanie do logowania.
- Responsywność w trybie urządzenia DevTools: 360, 375, 768, 1280 px — brak poziomego przewijania, tabela od 768 px, karty poniżej. Jasny i ciemny motyw (DevTools → Rendering → `prefers-color-scheme`).
- Klawiatura: Tab przechodzi przez logowanie, „Odśwież”, sortowanie, linki i „Wyloguj” z widocznym fokusem.
- Konsola przeglądarki bez błędów i bez naruszeń CSP.
- Zmień kolor w `web/style.css` i tekst w `web/index.html`, przeładuj stronę bez restartu — zmiany widoczne. Wprowadź błąd składni w `index.html` — strona działa dalej w starej wersji, log zawiera błąd.
- Telefon w tej samej sieci nie jest wymagany lokalnie; prawdziwy telefon sprawdzamy w kroku 6.

## 5. Zbuduj i uruchom wersję na Mikrusie

- Na Macu uruchom `go test ./...`, `go test -race ./...` i `go vet ./...`.
- Na Mikrusie sprawdź `uname -m`. Dla `x86_64` zbuduj na Macu poleceniem `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(git describe --always --dirty)" -o yt-stats ./cmd/yt-stats`. Dla `aarch64` ustaw `GOARCH=arm64`. Zmierz czas kompilacji (`time`) dla budowania od zera (`go clean -cache`) i przyrostowego.
- Skopiuj binarkę do `/opt/yt-stats/yt-stats`, a katalog `web/` do `/opt/yt-stats/web/`. Utwórz dedykowanego użytkownika systemowego `yt-stats`, konfigurację w `/etc/yt-stats/config.json` (właściciel `root:yt-stats`, `0640`) oraz katalog stanu `/var/lib/yt-stats/` (`StateDirectory=`).
- Skonfiguruj `deploy/yt-stats.service`: `Restart=on-failure`, `User=yt-stats`, `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `PrivateDevices=yes`, `StateDirectory=yt-stats`, `ReadOnlyPaths=/opt/yt-stats`, `MemoryMax=128M`. Uruchom serwis na `127.0.0.1:8091`.
- Wygeneruj hash hasła poleceniem `yt-stats hash-password` i wpisz do konfiguracji.

**Testy automatyczne**
- `go test ./...`, `go test -race ./...`, `go vet ./...` zielone przed każdym wdrożeniem.
- `systemd-analyze verify deploy/yt-stats.service` na serwerze.

**Scenariusze manualne** (na serwerze)
- `systemctl status yt-stats` — aktywny; `ss -ltnp | grep 8091` — nasłuch tylko na `127.0.0.1`.
- `curl -i http://127.0.0.1:8091/yt/` — `303` na `/yt/login`; `curl -i http://127.0.0.1:8091/yt/stat.json` — `401`.
- `kill -9` procesu — systemd uruchamia go ponownie w kilka sekund.
- `journalctl -u yt-stats` — brak kluczy API, hasła, hasha i identyfikatorów sesji (`grep` po fragmencie klucza zwraca pustkę).
- Pomiar RSS (`ps -o rss= -p $(pidof yt-stats)` albo `systemctl show -p MemoryCurrent`) w spoczynku, przy logowaniu i przy pobraniu statystyk; zapisz wyniki i w razie potrzeby obniż `MemoryMax`.
- Czas jednej weryfikacji Argon2id na Mikrusie (log czasu logowania albo krótki benchmark).
- Edycja pliku w `/opt/yt-stats/web/` (jako root) — zmiana widoczna bez restartu usługi.

## 6. Przełącz Nginx, zweryfikuj i zakończ migrację

- Zachowaj poprzednią konfigurację Nginx i katalog starej aplikacji. W wirtualnym hoście HTTPS ustaw `location = /yt { return 301 /yt/; }` oraz osobną lokalizację `location ^~ /yt/` z `proxy_pass http://127.0.0.1:8091;`. Brak ścieżki po adresie proxy zachowuje prefiks `/yt/`. Prefiks `^~` zapewnia pierwszeństwo przed ogólnymi regułami dla PHP i JS w obecnym hoście. Przekaż `Host`, `X-Forwarded-Proto` oraz `X-Real-IP $remote_addr` (nadpisany, nie dopisany). Dodaj `Strict-Transport-Security`. Sprawdź, że host na porcie 80 przekierowuje `/yt/` na HTTPS.
- Wykonaj `nginx -t`, przeładuj konfigurację i przejdź kryteria odbioru.
- Po potwierdzeniu działania usuń wpis starego crona (`cron.sh` / `cron.php`), jeśli istnieje, i przenieś dawne pliki poza katalog publiczny. Przed wyłączeniem PHP-FPM na całym serwerze sprawdź pozostałe strony na serwerze, które mogą korzystać z PHP.
- Jeżeli po przełączeniu panel nie spełnia warunków odbioru, przywróć poprzednie pliki i konfigurację hosta, wykonaj `nginx -t` i ponownie przeładuj Nginx. Trzymaj kopię poprzedniej konfiguracji wraz z prywatną kopią starych plików do czasu zakończenia odbioru.

**Testy automatyczne**
- `nginx -t` przed każdym przeładowaniem.
- Skrypt `deploy/smoke.sh` uruchamiany z Maca (`curl`) sprawdzający: `/yt` → `301`; `/yt/` bez sesji → `303`; `/yt/stat.json` bez sesji → `401`; stare adresy `/yt/index.php`, `/yt/stat.php`, `/yt/functions.php`, `/yt/require_auth.php`, `/yt/graph_data.php`, `/yt/cron.php`, `/yt/cron.sh`, `/yt/statistics.csv` → `404`; obecność CSP, `nosniff`, HSTS; `http://` → przekierowanie na `https://`.

**Scenariusze manualne** (kryteria odbioru)
- Pierwsze logowanie z „Zapamiętaj” na komputerze; zamknięcie przeglądarki i ponowne wejście bez hasła.
- `systemctl restart yt-stats` — sesja nadal działa, pierwsze odświeżenie pobiera dane od nowa.
- Logowanie bez „Zapamiętaj” na telefonie; sprawdzenie wyglądu na telefonie i tablecie w obu motywach.
- Wygaśnięcie: tymczasowo skrócony termin w pliku sesji (przy zatrzymanej usłudze) — po starcie przeglądarka trafia na logowanie.
- Wylogowanie i próba wejścia przyciskiem Wstecz.
- 6 błędnych logowań z telefonu na danych komórkowych — `429` tylko dla tego IP, komputer loguje się dalej (potwierdza poprawne `X-Real-IP`).
- Liczba filmów w panelu porównana z kanałem w YouTube Studio (bez prywatnych i usuniętych).
- Awaria API: tymczasowo błędny klucz główny w konfiguracji i restart po wcześniejszym pobraniu — brak danych i komunikat; potem przywrócenie klucza. Przełączenia na klucz zapasowy nie da się wywołać na żądanie; potwierdzamy je testami z kroku 2 i obserwacją logów.
- Edycja `style.css` na serwerze widoczna po przeładowaniu bez restartu.
- Stare adresy z listy w przeglądarce — żaden nie zwraca dawnych danych.

## Źródła techniczne

- [Nginx — wybór lokalizacji](https://nginx.org/en/docs/http/ngx_http_core_module.html#location)
- [Nginx — sposób przekazywania ścieżki w proxy_pass](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_pass)
- [OWASP — ochrona przed CSRF](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)
- [systemd — opcje ochrony usług](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
