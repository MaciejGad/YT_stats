package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- pre-login sessions ---

func TestPreLoginEnsureAndCheck(t *testing.T) {
	clk := newTestClock()
	p := NewPreLogin(0, 0, clk.Now)
	id, csrf, created, err := p.Ensure("")
	if err != nil || !created || !validToken(id) || !validToken(csrf) {
		t.Fatalf("Ensure: %q %q %v %v", id, csrf, created, err)
	}
	// The same session returns the same token.
	id2, csrf2, created2, _ := p.Ensure(id)
	if id2 != id || csrf2 != csrf || created2 {
		t.Error("istniejąca sesja powinna zostać użyta ponownie")
	}
	if !p.Check(id, csrf) {
		t.Error("poprawny token odrzucony")
	}
}

func TestPreLoginRejects(t *testing.T) {
	clk := newTestClock()
	p := NewPreLogin(0, 0, clk.Now)
	id, csrf, _, _ := p.Ensure("")
	other, otherCSRF, _, _ := p.Ensure("")

	cases := map[string][2]string{
		"brak tokenu":         {id, ""},
		"zły token":           {id, strings.Repeat("a", 43)},
		"token innej sesji":   {id, otherCSRF},
		"brak sesji":          {"", csrf},
		"nieznana sesja":      {strings.Repeat("b", 43), csrf},
		"sesja innego tokenu": {other, csrf},
		"zły format id sesji": {"!!!", csrf},
	}
	for name, c := range cases {
		if p.Check(c[0], c[1]) {
			t.Errorf("%s: przyjęto", name)
		}
	}
}

func TestPreLoginExpires(t *testing.T) {
	clk := newTestClock()
	p := NewPreLogin(0, 0, clk.Now)
	id, csrf, _, _ := p.Ensure("")
	clk.Add(29 * time.Minute)
	if !p.Check(id, csrf) {
		t.Error("sesja powinna być ważna po 29 min")
	}
	clk.Add(time.Minute)
	if p.Check(id, csrf) {
		t.Error("sesja powinna wygasnąć po 30 min")
	}
	newID, _, created, _ := p.Ensure(id)
	if !created || newID == id {
		t.Error("po wygaśnięciu Ensure powinien utworzyć nową sesję")
	}
}

func TestPreLoginDelete(t *testing.T) {
	p := NewPreLogin(0, 0, nil)
	id, csrf, _, _ := p.Ensure("")
	p.Delete(id)
	if p.Check(id, csrf) {
		t.Error("usunięta sesja nadal działa")
	}
}

func TestPreLoginMax(t *testing.T) {
	clk := newTestClock()
	p := NewPreLogin(0, 1000, clk.Now)
	first, firstCSRF, _, _ := p.Ensure("")
	for range 1000 {
		clk.Add(time.Millisecond)
		p.Ensure("")
	}
	if p.Len() != 1000 {
		t.Errorf("Len = %d, oczekiwano 1000", p.Len())
	}
	if p.Check(first, firstCSRF) {
		t.Error("najstarsza sesja powinna zostać usunięta")
	}
}

func TestPreLoginEvictsExpiredFirst(t *testing.T) {
	clk := newTestClock()
	p := NewPreLogin(time.Minute, 3, clk.Now)
	p.Ensure("")
	p.Ensure("")
	clk.Add(2 * time.Minute)
	keep, keepCSRF, _, _ := p.Ensure("")
	p.Ensure("")
	if p.Len() != 2 || !p.Check(keep, keepCSRF) {
		t.Errorf("Len = %d, ważna sesja zachowana = %v", p.Len(), p.Check(keep, keepCSRF))
	}
}

// --- limiter ---

func newTestLimiter(clk *testClock) *Limiter { return NewLimiter(LimiterOptions{Now: clk.Now}) }

func TestLimiterPerIP(t *testing.T) {
	clk := newTestClock()
	l := newTestLimiter(clk)
	for i := range 5 {
		if ok, _ := l.Allow("1.1.1.1"); !ok {
			t.Fatalf("próba %d zablokowana", i+1)
		}
		l.Failure("1.1.1.1")
		clk.Add(time.Second)
	}
	ok, wait := l.Allow("1.1.1.1")
	if ok {
		t.Fatal("6. próba powinna być zablokowana")
	}
	// The first failure was 5 s ago → unblocked in 15 min − 5 s.
	if wait != 15*time.Minute-5*time.Second {
		t.Errorf("wait = %v", wait)
	}
	if ok, _ := l.Allow("2.2.2.2"); !ok {
		t.Error("inny adres nie powinien być zablokowany")
	}
	clk.Add(wait)
	if ok, _ := l.Allow("1.1.1.1"); !ok {
		t.Error("po upływie okna adres powinien być odblokowany")
	}
}

func TestLimiterSlidingWindow(t *testing.T) {
	clk := newTestClock()
	l := newTestLimiter(clk)
	for range 4 {
		l.Failure("ip")
	}
	clk.Add(14 * time.Minute)
	l.Failure("ip") // 5th failure
	if ok, _ := l.Allow("ip"); ok {
		t.Fatal("5 porażek w 15 min powinno blokować")
	}
	clk.Add(time.Minute) // the first 4 drop out of the window
	if ok, _ := l.Allow("ip"); !ok {
		t.Error("po wypadnięciu starych porażek adres powinien być odblokowany")
	}
}

func TestLimiterSuccessResets(t *testing.T) {
	clk := newTestClock()
	l := newTestLimiter(clk)
	for range 4 {
		l.Failure("ip")
	}
	l.Success("ip")
	for range 4 {
		l.Failure("ip")
	}
	if ok, _ := l.Allow("ip"); !ok {
		t.Error("sukces powinien zerować licznik adresu")
	}
}

func TestLimiterGlobal(t *testing.T) {
	clk := newTestClock()
	l := newTestLimiter(clk)
	for i := range 50 {
		l.Failure(fmt.Sprintf("10.0.%d.%d", i/250, i%250))
		clk.Add(time.Second)
	}
	ok, wait := l.Allow("192.168.0.1")
	if ok {
		t.Fatal("51. próba z nowego adresu powinna być zablokowana limitem globalnym")
	}
	if wait != time.Hour-50*time.Second {
		t.Errorf("wait = %v", wait)
	}
	clk.Add(wait)
	if ok, _ := l.Allow("192.168.0.1"); !ok {
		t.Error("po godzinie limit globalny powinien zniknąć")
	}
}

func TestLimiterGlobalNotResetBySuccess(t *testing.T) {
	clk := newTestClock()
	l := newTestLimiter(clk)
	for i := range 50 {
		l.Failure(fmt.Sprint(i))
	}
	l.Success("0")
	if ok, _ := l.Allow("0"); ok {
		t.Error("sukces jednego adresu nie powinien zdejmować limitu globalnego")
	}
}

func TestLimiterMaxIPs(t *testing.T) {
	clk := newTestClock()
	l := NewLimiter(LimiterOptions{Now: clk.Now, MaxIPs: 100, Global: 1_000_000})
	for i := range 150 {
		l.Failure(fmt.Sprint(i))
		clk.Add(time.Millisecond)
	}
	if n := l.TrackedIPs(); n > 100 {
		t.Errorf("pamiętanych adresów %d, limit 100", n)
	}
	l2 := NewLimiter(LimiterOptions{})
	if l2.o.MaxIPs != 10_000 || l2.o.PerIP != 5 || l2.o.Global != 50 {
		t.Errorf("wartości domyślne: %+v", l2.o)
	}
}

func TestLimiterEvictsExpiredFirst(t *testing.T) {
	clk := newTestClock()
	l := NewLimiter(LimiterOptions{Now: clk.Now, MaxIPs: 3, Global: 1_000_000})
	l.Failure("stary1")
	l.Failure("stary2")
	clk.Add(16 * time.Minute)
	for range 5 {
		l.Failure("aktywny")
	}
	l.Failure("nowy")
	if ok, _ := l.Allow("aktywny"); ok {
		t.Error("aktywnie blokowany adres nie powinien zostać usunięty")
	}
}

func TestLimiterCleansAfterWindow(t *testing.T) {
	clk := newTestClock()
	l := newTestLimiter(clk)
	l.Failure("ip")
	clk.Add(16 * time.Minute)
	l.Allow("ip")
	if l.TrackedIPs() != 0 {
		t.Error("adres bez porażek w oknie powinien zostać usunięty")
	}
}

// --- password verification ---

// countingVerifier counts calls and optionally waits.
type countingVerifier struct {
	password string
	delay    time.Duration
	calls    atomic.Int32
	running  atomic.Int32
	maxSeen  atomic.Int32
}

func (v *countingVerifier) Verify(pw []byte) bool {
	v.calls.Add(1)
	n := v.running.Add(1)
	for {
		m := v.maxSeen.Load()
		if n <= m || v.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(v.delay)
	v.running.Add(-1)
	return string(pw) == v.password
}

func newTestAuth(t *testing.T, o AuthOptions) (*Authenticator, *countingVerifier, *countingVerifier) {
	t.Helper()
	real := &countingVerifier{password: "dobre hasło"}
	dummy := &countingVerifier{password: "\x00nigdy"}
	o.Dummy = dummy
	a, err := newAuthenticator("yarn", real, fastParams, o)
	if err != nil {
		t.Fatal(err)
	}
	return a, real, dummy
}

func TestAuthCheck(t *testing.T) {
	a, real, dummy := newTestAuth(t, AuthOptions{})
	ctx := context.Background()

	if ok, err := a.Check(ctx, "yarn", "dobre hasło"); !ok || err != nil {
		t.Errorf("poprawne dane: %v %v", ok, err)
	}
	if ok, _ := a.Check(ctx, "yarn", "złe"); ok {
		t.Error("złe hasło przyjęte")
	}
	if ok, _ := a.Check(ctx, "Yarn", "dobre hasło"); ok {
		t.Error("nazwa z inną wielkością liter przyjęta")
	}
	if real.calls.Load() != 2 {
		t.Errorf("weryfikacji prawdziwego hasha: %d", real.calls.Load())
	}
	if dummy.calls.Load() != 1 {
		t.Errorf("przy złej nazwie fikcyjny hash powinien być sprawdzony raz, jest %d", dummy.calls.Load())
	}
}

func TestAuthUnknownUserStillRunsArgon(t *testing.T) {
	a, real, dummy := newTestAuth(t, AuthOptions{})
	ok, err := a.Check(context.Background(), "nieznany", "dobre hasło")
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if dummy.calls.Load() != 1 || real.calls.Load() != 0 {
		t.Errorf("dummy=%d real=%d", dummy.calls.Load(), real.calls.Load())
	}
}

func TestAuthLengthLimitsSkipArgon(t *testing.T) {
	a, real, dummy := newTestAuth(t, AuthOptions{})
	ctx := context.Background()
	cases := [][2]string{
		{strings.Repeat("a", 65), "dobre hasło"},
		{"yarn", strings.Repeat("a", 257)},
		{"", "dobre hasło"},
		{"yarn", ""},
	}
	for _, c := range cases {
		if ok, err := a.Check(ctx, c[0], c[1]); ok || err != nil {
			t.Errorf("Check(%d B, %d B) = %v, %v", len(c[0]), len(c[1]), ok, err)
		}
	}
	if real.calls.Load()+dummy.calls.Load() != 0 {
		t.Error("za długie lub puste dane nie powinny uruchamiać Argon2id")
	}
	// The boundaries are allowed.
	if _, err := a.Check(ctx, strings.Repeat("a", 64), strings.Repeat("a", 256)); err != nil {
		t.Error(err)
	}
	if dummy.calls.Load() != 1 {
		t.Error("dane o maksymalnej długości powinny być weryfikowane")
	}
}

func TestAuthConcurrencyLimit(t *testing.T) {
	a, real, _ := newTestAuth(t, AuthOptions{WaitTimeout: 2 * time.Second})
	real.delay = 200 * time.Millisecond

	var wg sync.WaitGroup
	start := time.Now()
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := a.Check(context.Background(), "yarn", "dobre hasło"); !ok || err != nil {
				t.Errorf("ok=%v err=%v", ok, err)
			}
		}()
	}
	wg.Wait()
	if real.maxSeen.Load() != 2 {
		t.Errorf("maksymalnie równoległych weryfikacji %d, oczekiwano 2", real.maxSeen.Load())
	}
	if el := time.Since(start); el < 400*time.Millisecond {
		t.Errorf("trzecia weryfikacja nie czekała (%v)", el)
	}
}

func TestAuthBusyAfterWait(t *testing.T) {
	a, real, _ := newTestAuth(t, AuthOptions{WaitTimeout: 50 * time.Millisecond})
	real.delay = 500 * time.Millisecond

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); a.Check(context.Background(), "yarn", "dobre hasło") }()
	}
	time.Sleep(20 * time.Millisecond) // both verifications have taken slots
	_, err := a.Check(context.Background(), "yarn", "dobre hasło")
	if !errors.Is(err, ErrBusy) {
		t.Errorf("oczekiwano ErrBusy, jest %v", err)
	}
	wg.Wait()
}

func TestAuthContextCancelledWhileWaiting(t *testing.T) {
	a, real, _ := newTestAuth(t, AuthOptions{MaxConcurrent: 1})
	real.delay = 300 * time.Millisecond
	go a.Check(context.Background(), "yarn", "dobre hasło")
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Check(ctx, "yarn", "dobre hasło"); !errors.Is(err, context.Canceled) {
		t.Errorf("oczekiwano context.Canceled, jest %v", err)
	}
}

func TestNewAuthenticatorRealHash(t *testing.T) {
	s, _ := HashPassword([]byte("sekret"), fastParams)
	h, _ := ParseHash(s)
	a, err := NewAuthenticator("yarn", h, AuthOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, _ := a.Check(ctx, "yarn", "sekret"); !ok {
		t.Error("poprawne hasło odrzucone")
	}
	if ok, _ := a.Check(ctx, "inny", "sekret"); ok {
		t.Error("zła nazwa przyjęta")
	}
	d, ok := a.dummy.(*Hash)
	if !ok || d.Params.Memory != h.Params.Memory || d.Params.Time != h.Params.Time {
		t.Errorf("fikcyjny hash ma inne parametry niż prawdziwy: %+v", a.dummy)
	}
}

func TestAuthCancelledContextNeverVerifies(t *testing.T) {
	a, real, _ := newTestAuth(t, AuthOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 50 { // select would pick randomly; the check must be deterministic
		if ok, err := a.Check(ctx, "yarn", "dobre hasło"); ok || !errors.Is(err, context.Canceled) {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	}
	if real.calls.Load() != 0 {
		t.Errorf("anulowane żądanie uruchomiło weryfikację %d razy", real.calls.Load())
	}
}
