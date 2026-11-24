# Wymagania — statystyki YouTube YarnPower

[English](requirements.md) | **Polski**

## Cel i zakres

Prywatny panel pod adresem `https://maciejgad.pl/yt/` pokazuje bieżące statystyki kanału [@YarnPowerPL](https://www.youtube.com/@YarnPowerPL). Kanał wskazuje konfiguracja, więc ten sam panel może wdrożyć ktoś inny dla własnego kanału. Zastępuje funkcje obecnych `index.php`, `stat.php`, `functions.php` i `script.js`. Zakres obejmuje logowanie, podsumowanie kanału i tabelę filmów. Historia CSV, wykresy, cron i okresowe odświeżanie w tle pozostają poza zakresem.

Katalog `old_implementation/` zawiera kopię starej wersji wyłącznie do podglądu. Jest wykluczony z repozytorium przez `.gitignore`.

## Przebieg korzystania z panelu

1. Wejście na `/yt/` otwiera panel przy ważnej sesji lub przekierowuje do formularza logowania.
2. Formularz przyjmuje nazwę użytkownika i hasło jednego konta z konfiguracji. Udane logowanie przenosi do `/yt/`. Nieudane wyświetla wspólny komunikat dla błędnej nazwy i błędnego hasła.
3. Po otwarciu panel wysyła jedno żądanie o statystyki. Użytkownik może ponowić je przyciskiem „Odśwież” albo przeładować stronę. Panel nie odświeża danych samoczynnie.
4. Panel pokazuje łączną liczbę wyświetleń kanału, liczbę subskrybentów, tytuł i liczbę wyświetleń każdego filmu oraz czas ostatniego udanego pobrania w strefie `Europe/Warsaw`. Tytuł jest linkiem do YouTube. Gdy API ukrywa licznik, panel pokazuje „Niedostępne”.
5. W trakcie pobierania panel pokazuje stan ładowania. Przy awarii YouTube wyświetla ostatni dostępny wynik z oznaczeniem, że dane są z pamięci podręcznej, oraz z pierwotnym czasem pobrania. Bez zapisanego wyniku pokazuje komunikat i umożliwia ponowienie.
6. „Odśwież” w ciągu 60 sekund od ostatniego pobrania zwraca dane z pamięci podręcznej. To zachowanie zamierzone; widoczny czas pobrania pokazuje, z kiedy są dane.

## Wygląd i responsywność

- Interfejs jest odnowiony i wyraźnie nowocześniejszy niż obecna strona. Cały tekst jest po polsku, dokument ma `lang="pl"`.
- Strona działa bez poziomego przewijania na szerokościach od 360 px. Sprawdzamy ją na 375 px (telefon), 768 px (tablet) i 1280 px (komputer) oraz na prawdziwym telefonie.
- Kolejność od góry: nagłówek z awatarem i nazwą kanału oraz przyciskiem „Wyloguj”, lista filmów, a pod nią mniejszy panel podsumowania.
- Panel podsumowania pokazuje subskrybentów, wyświetlenia kanału, liczbę filmów na liście, czas pobrania i przycisk „Odśwież”. Jest przyklejony do dołu ekranu (nie przewija się z listą), a na końcu strony zajmuje swoje miejsce pod listą.
- Kolorystyka nawiązuje do włóczki: granat, żółć kłębka i ciepłe, wełniane tło. Awatar kanału jest pokazywany w kole i wygląda dobrze w obu motywach.
- Od 768 px lista filmów jest tabelą z kolumnami „Tytuł” i „Wyświetlenia”; liczby są wyrównane do prawej cyframi o stałej szerokości. Poniżej 768 px każdy film jest wierszem-kartą z tytułem i liczbą wyświetleń pod nim.
- Domyślna kolejność to kolejność playlisty przesłanych filmów (najnowsze na górze). Użytkownik może przełączyć sortowanie po liczbie wyświetleń malejąco. Sortowanie działa w przeglądarce i nie wysyła nowego żądania.
- Liczby są formatowane w przeglądarce według `pl-PL` (np. `12 345`). JSON pozostaje ciągiem cyfr.
- Kolorystyka dopasowuje się do `prefers-color-scheme` (jasny i ciemny motyw). Kontrast tekstu spełnia WCAG AA, fokus klawiatury jest widoczny, elementy dotykowe mają co najmniej 44 × 44 px.
- Stan ładowania blokuje przycisk „Odśwież”. Komunikaty o stanie (ładowanie, dane z pamięci podręcznej, błąd) są ogłaszane przez `aria-live`.
- Formularz logowania ma ten sam styl i jest wygodny na telefonie.
- Strona nie ładuje zasobów z zewnętrznych serwerów (CDN, czcionki, skrypty).

## Logowanie i sesja

- Opcja „Zapamiętaj mnie na 90 dni” zapisuje trwałe ciasteczko i sesję z tym samym terminem ważności, liczonym od logowania. Używanie panelu nie przedłuża sesji. Po restarcie aplikacji sesja nadal działa.
- Bez zaznaczenia opcji przeglądarka dostaje ciasteczko sesyjne, a sesja na serwerze ma limit 12 godzin od logowania. Przeglądarka może przywrócić takie ciasteczko po ponownym uruchomieniu, zależnie od ustawień przywracania sesji.
- Ciasteczko zawiera losowy identyfikator o długości 32 bajtów zakodowany base64url. Go przechowuje wyłącznie hash SHA-256 identyfikatora z terminem wygaśnięcia w prywatnym pliku. Ciasteczko ma `Secure`, `HttpOnly`, `SameSite=Lax` i `Path=/yt/`, bez atrybutu `Domain`.
- „Wyloguj” unieważnia bieżącą sesję po stronie serwera i usuwa ciasteczko. Wygasłe wpisy są usuwane przy operacjach na pliku sesji.
- Polecenie `yt-stats revoke-sessions` usuwa wszystkie sesje (np. po zmianie hasła). Wykonuje się je przy zatrzymanej usłudze. Usługa trzyma blokadę (`flock`) pliku sesji, więc polecenie uruchomione przy działającej usłudze kończy się błędem i niczego nie zmienia.
- Dostęp do panelu i danych wymaga sesji. Formularze logowania i wylogowania mają ochronę CSRF.
- Przy nieznanej nazwie użytkownika aplikacja weryfikuje hasło względem stałego, fikcyjnego hasha Argon2id, aby czas odpowiedzi nie zdradzał poprawności nazwy.
- Hash hasła Argon2id oraz klucze YouTube API znajdują się w konfiguracji poza katalogiem publicznym. Odpowiedzi i logi nie ujawniają hasła, kluczy API ani identyfikatorów sesji. Strony logowania i chronione odpowiedzi mają `Cache-Control: no-store`.
- Adres klienta do limitów pochodzi z `X-Real-IP` tylko wtedy, gdy połączenie przychodzi z `127.0.0.1` lub `::1`. W przeciwnym razie aplikacja używa adresu połączenia. Nginx wypełnia `X-Real-IP` adresem z `CF-Connecting-IP` (zob. „Środowisko”).

## Limity

| Obszar | Limit |
| --- | --- |
| Nieudane logowania z jednego IP | 5 w ciągu 15 minut, potem `429` z `Retry-After`. Udane logowanie zeruje licznik. |
| Nieudane logowania łącznie | 50 na godzinę, potem `429` dla wszystkich do końca okna. |
| Równoległe weryfikacje hasła | 2; kolejne czekają do 3 s, potem `503`. |
| Pamiętane adresy IP w limiterze | 10 000, najstarsze usuwane. |
| Rozmiar formularza | 4 KiB; nazwa do 64 bajtów, hasło do 256 bajtów. |
| Parametry Argon2id | m = 19 MiB, t = 2, p = 1, sól 16 B, klucz 32 B (minimum OWASP). |
| Sesje przed logowaniem (CSRF) | w pamięci, ważne 30 minut, maksymalnie 1000. |
| Aktywne sesje w pliku | maksymalnie 100; przy przekroczeniu usuwana najstarsza. Plik sesji większy niż 1 MiB jest odrzucany przy odczycie. |
| Pojedyncze żądanie do YouTube | 10 s, odpowiedź do 5 MiB. |
| Całe odświeżenie statystyk | 30 s, maksymalnie 200 stron playlisty (10 000 filmów). |
| Serwer HTTP | `ReadHeaderTimeout` 5 s, `ReadTimeout` 10 s, `WriteTimeout` 40 s, `IdleTimeout` 60 s, nagłówki do 16 KiB. |
| Pamięć usługi | `MemoryMax=128M` w systemd; po pomiarze na Mikrusie limit można obniżyć. Oczekiwany RSS w spoczynku poniżej 30 MB. |

## Pobieranie danych

- Identyfikator kanału pochodzi z konfiguracji (`channel_id`), więc panel może wdrożyć każdy dla własnego kanału. Dla @YarnPowerPL to `UC5711W83ebIxkC8bHjOKI7g` (stara wersja doklejała `UC` w `functions.php`). Przed wdrożeniem potwierdzamy go zapytaniem `channels.list?forHandle=@handle`.
- Nazwa, handle (`customUrl`) i awatar kanału pochodzą z tego samego zapytania `channels.list` (`part=snippet`), więc nie zużywają dodatkowego limitu. Pokazują je nagłówek panelu i strona logowania; do pierwszego udanego pobrania widać ogólny tytuł i dołączone `logo.png`.
- Awatar pobiera serwer (tylko po HTTPS z hostów obrazków Google `ggpht.com`, `googleusercontent.com`, `ytimg.com`, do 1 MiB, JPEG/PNG/WebP/GIF rozpoznane po zawartości, bez klucza API) i podaje go pod `/yt/channel-logo`, więc strona nadal nie ładuje niczego z zewnętrznych serwerów. Awatar jest pobierany ponownie tylko po zmianie jego adresu. Nieudane pobranie awatara nie przerywa odświeżenia.
- Zaraz po starcie aplikacja raz pobiera dane, żeby strona logowania znała nazwę i awatar kanału. Potem, jak dotąd, YouTube jest odpytywany tylko po autoryzowanym żądaniu.
- Go odpytuje YouTube Data API v3 po autoryzowanym żądaniu, jeśli ostatni udany odczyt jest starszy niż 60 sekund. Równoległe odświeżenia współdzielą jedno pobranie.
- Aplikacja odczytuje statystyki kanału oraz identyfikator playlisty przesłanych filmów. Obsługuje wszystkie strony playlisty (stara wersja pobierała tylko pierwsze 50 filmów) i pobiera statystyki filmów grupami po 50. Prywatne i usunięte filmy pomija, jeśli API nie udostępnia ich szczegółów.
- Po błędzie aplikacja zwraca ostatni udany wynik z maksymalnie 24 godzin, z kodem `200` i `stale: true`. Przez 30 sekund od błędu kolejne żądania korzystają z tego samego stanu zamiast natychmiast ponawiać zapytania do YouTube. Brak wyniku w tym oknie oznacza odpowiedź `503`.
- Cache statystyk mieści się w pamięci jednego procesu. Restart może wyczyścić cache i pierwsze żądanie pobierze dane ponownie. Plik sesji jest trwały i niezależny od cache.

### Zapasowy klucz API

- Konfiguracja ma klucz główny i opcjonalny klucz zapasowy.
- Gdy YouTube odpowie na żądanie kluczem głównym błędem `403` z powodem `quotaExceeded` lub `dailyLimitExceeded`, aplikacja ponawia to samo żądanie kluczem zapasowym i używa go aż do najbliższej północy czasu pacyficznego (`America/Los_Angeles`), kiedy YouTube odnawia limit. Potem wraca do klucza głównego.
- Inne błędy (nieprawidłowy klucz, błąd 5xx, timeout, odmowa dostępu) nie przełączają klucza.
- Gdy limit wyczerpie też klucz zapasowy, odświeżenie kończy się błędem i obowiązuje zwykła obsługa awarii (dane awaryjne albo `503`).
- Logi podają, który klucz był użyty (`primary` / `fallback`) oraz moment przełączenia, nigdy wartości kluczy.

## Interfejs HTTP

| Metoda i ścieżka | Działanie |
| --- | --- |
| `GET /yt/` | Panel albo `303` do `/yt/login`. |
| `GET /yt/login` | Formularz logowania; przy ważnej sesji `303` do `/yt/`. |
| `POST /yt/login` | Weryfikacja hasła, utworzenie sesji i `303` do `/yt/` po sukcesie. |
| `POST /yt/logout` | Unieważnienie sesji i `303` do `/yt/login`. |
| `GET /yt/stat.json` | Statystyki lub `401` przy braku ważnej sesji. |
| `GET /yt/script.js`, `GET /yt/style.css`, `GET /yt/logo.png`, `GET /yt/favicon.png` | Pliki statyczne, dostępne bez sesji. |
| `GET /yt/channel-logo` | Awatar kanału, dostępny bez sesji (pokazuje go strona logowania); `404` do czasu pobrania. Wersjonowany jak pliki statyczne. |

- Każda trasa `GET` obsługuje też `HEAD`. Inna metoda na znanej ścieżce zwraca `405` z nagłówkiem `Allow`. Nieznana ścieżka zwraca `404`.
- JSON zachowuje dotychczasowe pola `stats.subscriberCount`, `stats.viewsCount`, `videos[].videoId`, `videos[].title`, `videos[].viewCount` i `update_time`. Liczniki pozostają ciągami cyfr. Brak licznika reprezentuje `null`. Pole `stale` informuje, czy wynik pochodzi z odczytu awaryjnego. Pole `channel` zawiera `title`, `handle` (może go nie być), `url` i `logoVersion` (brak bez awatara).
- `update_time` ma format ISO 8601 z przesunięciem strefy `Europe/Warsaw`, np. `2026-11-23T22:45:10+01:00`. Przeglądarka wyświetla go w strefie `Europe/Warsaw` niezależnie od strefy urządzenia.
- Przy błędzie endpoint zwraca `{"error": "<krótki komunikat>"}` bez szczegółów wewnętrznych. Skrypt po `401` przekierowuje do formularza logowania.
- Wszystkie odpowiedzi mają nagłówki: `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`. HSTS ustawia Nginx.

### Pliki interfejsu

- Aplikacja to jedna binarka Go. Szablony HTML (`index.html`, `login.html`) oraz pliki `script.js`, `style.css`, `logo.png` i `favicon.png` leżą obok niej w katalogu wskazanym w konfiguracji (`web_dir`) i nie są wkompilowane.
- Zmiana tych plików działa bez rekompilacji i bez restartu: aplikacja wczytuje szablon ponownie, gdy zmieni się czas modyfikacji pliku. Jeżeli nowa wersja szablonu ma błąd składni, aplikacja loguje błąd i dalej używa ostatniej poprawnej wersji.
- Przy starcie brak lub błąd któregokolwiek pliku przerywa uruchomienie z komunikatem wskazującym plik.
- Szablony wskazują pliki statyczne przez `{{asset "nazwa"}}`, co daje adres z wersją, np. `/yt/script.js?v=1a2b…` (skrót zawartości). Zmiana pliku zmienia adres, więc ani przeglądarka, ani Cloudflare nie podają starej kopii. Adres z aktualną wersją ma `Cache-Control: public, max-age=31536000, immutable`, a adres bez wersji lub z nieaktualną wersją `no-cache`.

## Środowisko

Aplikacja działa jako jeden lekki proces Go pod `systemd`. Domena stoi za Cloudflare: HTTPS kończy się w Cloudflare, który łączy się z Nginx po HTTP. Nginx przekazuje ruch z `/yt/` do `127.0.0.1:8091` z zachowaniem prefiksu i ustawia `X-Real-IP` na prawdziwy adres klienta z `CF-Connecting-IP`, przyjmowanego tylko od adresów Cloudflare (moduł `real_ip`). Po przełączeniu czyścimy cache Cloudflare dla dawnych plików z `/yt/`. Binarkę na Linuksa budujemy na Macu. Binarka zawiera bazę stref czasowych (`time/tzdata`), więc nie zależy od `/usr/share/zoneinfo` na serwerze. Aplikacja nie wymaga bazy danych. Zużycie RAM w spoczynku, podczas logowania i pobierania oraz czas kompilacji mierzymy przed odbiorem.

Lokalnie aplikację testujemy po zwykłym HTTP na `http://localhost:8091/yt/` w Chrome lub Firefox. Te przeglądarki traktują `localhost` jako bezpieczny kontekst i przyjmują ciasteczko z atrybutem `Secure`. Safari tego nie robi, więc lokalnie go nie używamy.

## Kryteria odbioru

- Pierwsze logowanie, ponowne wejście bez hasła z trwałym ciasteczkiem, restart usługi, wygaśnięcie obu rodzajów sesji, wylogowanie i `revoke-sessions`.
- Brak dostępu do panelu i `stat.json` bez sesji. Żądania do starych plików z danymi nie omijają logowania.
- Odrzucenie formularza z błędnym tokenem CSRF, zadziałanie limitu prób (`429`) i limitu równoległych weryfikacji.
- Komplet nagłówków bezpieczeństwa i `Cache-Control: no-store` na stronach chronionych.
- Kompletność listy filmów (liczba zgodna z kanałem pomniejszona o prywatne i usunięte) i obsługa awarii API, w tym przełączenie na klucz zapasowy.
- Wygląd na 375, 768 i 1280 px oraz na prawdziwym telefonie, w jasnym i ciemnym motywie, bez poziomego przewijania.
- Zmiana pliku w `web_dir` widoczna po przeładowaniu strony bez restartu usługi.
- Logi bez sekretów, zmierzone RSS i czas kompilacji.

## Źródła techniczne

- [YouTube Data API v3 — pobieranie listy filmów](https://developers.google.com/youtube/data/guides/implementation/videos)
- [YouTube Data API v3 — pobieranie szczegółów wielu filmów](https://developers.google.com/youtube/v3/docs/videos/list)
- [YouTube Data API v3 — błędy i limity](https://developers.google.com/youtube/v3/docs/errors)
- [OWASP — zarządzanie sesjami](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
- [OWASP — przechowywanie haseł](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
