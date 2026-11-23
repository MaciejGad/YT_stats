package auth

import (
	"strings"
	"testing"
)

// fastParams przyspieszają testy; produkcja używa DefaultParams.
var fastParams = Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword([]byte("poprawne hasło"), fastParams)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseHash(h)
	if err != nil {
		t.Fatalf("ParseHash(%q): %v", h, err)
	}
	if !parsed.Verify([]byte("poprawne hasło")) {
		t.Error("poprawne hasło nie przeszło weryfikacji")
	}
	for _, bad := range []string{"poprawne hasł", "Poprawne hasło", "", "poprawne hasło "} {
		if parsed.Verify([]byte(bad)) {
			t.Errorf("błędne hasło %q przeszło weryfikację", bad)
		}
	}
}

func TestDefaultParamsFormat(t *testing.T) {
	h, err := HashPassword([]byte("x"), DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("nieoczekiwany format: %s", h)
	}
	p, err := ParseHash(h)
	if err != nil {
		t.Fatal(err)
	}
	if p.Params != DefaultParams {
		t.Errorf("parametry po parsowaniu %+v, oczekiwano %+v", p.Params, DefaultParams)
	}
}

func TestDifferentSalts(t *testing.T) {
	a, _ := HashPassword([]byte("to samo"), fastParams)
	b, _ := HashPassword([]byte("to samo"), fastParams)
	if a == b {
		t.Error("dwa hashe tego samego hasła są identyczne — sól się nie losuje")
	}
}

func TestHashPasswordLimits(t *testing.T) {
	if _, err := HashPassword(nil, fastParams); err == nil {
		t.Error("puste hasło powinno dać błąd")
	}
	if _, err := HashPassword([]byte(strings.Repeat("a", MaxPasswordLen+1)), fastParams); err == nil {
		t.Error("za długie hasło powinno dać błąd")
	}
	if _, err := HashPassword([]byte(strings.Repeat("a", MaxPasswordLen)), fastParams); err != nil {
		t.Errorf("hasło o maksymalnej długości: %v", err)
	}
}

func TestVerifyRejectsTooLong(t *testing.T) {
	long := []byte(strings.Repeat("a", MaxPasswordLen+1))
	h, _ := HashPassword([]byte("a"), fastParams)
	p, _ := ParseHash(h)
	if p.Verify(long) {
		t.Error("za długie hasło przeszło weryfikację")
	}
}

func TestParseHashInvalid(t *testing.T) {
	valid, _ := HashPassword([]byte("x"), fastParams)
	parts := strings.Split(valid, "$")
	join := func(p []string) string { return strings.Join(p, "$") }
	with := func(i int, v string) string {
		c := append([]string(nil), parts...)
		c[i] = v
		return join(c)
	}

	cases := map[string]string{
		"pusty":            "",
		"bcrypt":           "$2y$10$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyz012",
		"argon2i":          with(1, "argon2i"),
		"zła wersja":       with(2, "v=16"),
		"brak parametru p": with(3, "m=8192,t=1"),
		"powtórzony m":     with(3, "m=8192,m=8192,t=1"),
		"nieznany param":   with(3, "m=8192,t=1,x=1"),
		"m nie liczba":     with(3, "m=abc,t=1,p=1"),
		"m za małe":        with(3, "m=1024,t=1,p=1"),
		"m za duże":        with(3, "m=1048576,t=1,p=1"),
		"t zero":           with(3, "m=8192,t=0,p=1"),
		"p zero":           with(3, "m=8192,t=1,p=0"),
		"p za duże":        with(3, "m=8192,t=1,p=300"),
		"sól nie base64":   with(4, "!!!"),
		"sól za krótka":    with(4, "YWJj"),
		"klucz nie base64": with(5, "!!!"),
		"klucz za krótki":  with(5, "YWJjZGVm"),
		"za mało części":   join(parts[:5]),
		"za dużo części":   valid + "$x",
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseHash(s)
			if err == nil {
				t.Fatalf("oczekiwano błędu dla %q", s)
			}
			if len(parts[5]) > 0 && strings.Contains(err.Error(), parts[5]) {
				t.Errorf("komunikat zawiera fragment hasha: %v", err)
			}
		})
	}
}
