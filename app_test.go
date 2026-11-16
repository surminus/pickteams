package main

import (
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func timeSoon() time.Time { return time.Now().Add(time.Hour) }

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func newTestApp(t *testing.T) *App {
	t.Helper()

	store, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	key, err := store.CookieKey()
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	return &App{
		store:     store,
		tmpl:      tmpl,
		password:  "correct-horse",
		cookieKey: key,
		rnd:       rand.New(rand.NewPCG(3, 4)),
	}
}

// get and post run a request through the full router, optionally signed in.
func (a *App) get(t *testing.T, path string, admin bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if admin {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: a.signToken(timeSoon())})
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

func (a *App) post(t *testing.T, path string, form url.Values, admin bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if admin {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: a.signToken(timeSoon())})
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

func TestPublicPageWithNothingPicked(t *testing.T) {
	app := newTestApp(t)
	w := app.get(t, "/", false)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "No teams have been put up yet") {
		t.Error("expected the empty state")
	}
}

func TestAdminNeedsSigningIn(t *testing.T) {
	app := newTestApp(t)
	for _, path := range []string{"/admin/", "/admin/players", "/admin/games/1"} {
		w := app.get(t, path, false)
		if w.Code != http.StatusSeeOther {
			t.Errorf("%s: got %d, want a redirect to the login page", path, w.Code)
		}
	}
}

func TestLoginRejectsTheWrongPassword(t *testing.T) {
	app := newTestApp(t)
	w := app.post(t, "/login", url.Values{"password": {"nope"}}, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("a failed login should not set a cookie")
	}
}

func TestLoginAcceptsTheRightPassword(t *testing.T) {
	app := newTestApp(t)
	w := app.post(t, "/login", url.Values{"password": {"correct-horse"}}, false)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want a redirect", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !app.validToken(cookies[0].Value) {
		t.Fatal("expected a valid session cookie")
	}
	if !cookies[0].HttpOnly {
		t.Error("the session cookie should be HttpOnly")
	}
}

func TestTamperedCookieIsRejected(t *testing.T) {
	app := newTestApp(t)
	r := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: "9999999999.deadbeef"})
	w := httptest.NewRecorder()
	app.routes().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want a redirect", w.Code)
	}
}

// TestFullRun walks the whole thing: add players, start a game, tick people
// in, pick the sides, publish, and check what the public page gives away.
func TestFullRun(t *testing.T) {
	app := newTestApp(t)

	players := []struct {
		name   string
		w      string
		keeper bool
	}{
		{"Ada", "4", true}, {"Bea", "2", false}, {"Cleo", "3", false}, {"Dara", "1", false},
		{"Erin", "5", true}, {"Fern", "2", false}, {"Gwen", "4", false}, {"Hana", "3", false},
	}
	for _, p := range players {
		form := url.Values{
			"name":      {p.name},
			"weighting": {p.w},
			"notes":     {"private note about " + p.name},
		}
		if p.keeper {
			form.Set("keeper", "1")
		}
		if w := app.post(t, "/admin/players", form, true); w.Code != http.StatusSeeOther {
			t.Fatalf("adding %s: got %d", p.name, w.Code)
		}
	}

	if w := app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true); w.Code != http.StatusSeeOther {
		t.Fatalf("creating a game: got %d", w.Code)
	}

	all, err := app.store.Players()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		form := url.Values{"player_id": {itoa(p.ID)}, "attending": {"1"}}
		if w := app.post(t, "/admin/games/1/attending", form, true); w.Code != http.StatusSeeOther {
			t.Fatalf("ticking %s in: got %d", p.Name, w.Code)
		}
	}

	if w := app.post(t, "/admin/games/1/pick", nil, true); w.Code != http.StatusSeeOther {
		t.Fatalf("picking sides: got %d", w.Code)
	}

	lineup, err := app.store.Lineup(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(lineup.TeamA)+len(lineup.TeamB) != len(players) {
		t.Fatalf("got %d picked, want %d", len(lineup.TeamA)+len(lineup.TeamB), len(players))
	}

	// The admin view of the picked sides shows what was evened up.
	admin := app.get(t, "/admin/games/1", true)
	if admin.Code != http.StatusOK {
		t.Fatalf("admin game page after picking: got %d", admin.Code)
	}
	adminBody := admin.Body.String()
	// 24 points between eight players, and a keeper each way still leaves an
	// exactly even split.
	if got := strings.Count(adminBody, "4 players · total 12"); got != 2 {
		t.Errorf("expected both sides to show 4 players and a total of 12, found %d", got)
	}
	if !strings.Contains(adminBody, `<span class="pos">`+KeeperLabel+`</span>`) {
		t.Error("the admin game page should mark the keepers")
	}

	// Still hidden until it is published.
	if w := app.get(t, "/games/1", false); w.Code != http.StatusNotFound {
		t.Fatalf("an unpublished game should be hidden, got %d", w.Code)
	}

	if w := app.post(t, "/admin/games/1/publish", url.Values{"published": {"1"}}, true); w.Code != http.StatusSeeOther {
		t.Fatalf("publishing: got %d", w.Code)
	}

	public := app.get(t, "/", false)
	if public.Code != http.StatusOK {
		t.Fatalf("public page: got %d", public.Code)
	}
	body := public.Body.String()

	for _, p := range players {
		if !strings.Contains(body, p.name) {
			t.Errorf("%s is missing from the public team sheet", p.name)
		}
	}
	// Nothing about how anyone is weighted should reach this page.
	for _, leak := range []string{"private note", "avg", "total", "Weighting", "weighting", "rough"} {
		if strings.Contains(body, leak) {
			t.Errorf("the public page contains %q", leak)
		}
	}
	if got := strings.Count(body, `<span class="pos">`+KeeperLabel+`</span>`); got != 2 {
		t.Errorf("the public page marks %d people as in goal, want the 2 keepers", got)
	}
}

// TestLineupMarksOnlyKeepers checks the lineup says who is in goal and nothing
// else, including for a player saved back when there were outfield positions.
func TestLineupMarksOnlyKeepers(t *testing.T) {
	app := newTestApp(t)

	for _, p := range []struct{ name, keeper string }{{"Ada", "1"}, {"Bea", ""}, {"Cleo", ""}, {"Dara", ""}} {
		form := url.Values{"name": {p.name}, "weighting": {"3"}}
		if p.keeper != "" {
			form.Set("keeper", p.keeper)
		}
		if w := app.post(t, "/admin/players", form, true); w.Code != http.StatusSeeOther {
			t.Fatalf("adding %s: got %d", p.name, w.Code)
		}
	}
	if _, err := app.store.db.Exec(`UPDATE players SET position = 'DEF' WHERE name = 'Bea'`); err != nil {
		t.Fatal(err)
	}
	if w := app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true); w.Code != http.StatusSeeOther {
		t.Fatalf("creating a game: got %d", w.Code)
	}
	all, err := app.store.Players()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		form := url.Values{"player_id": {itoa(p.ID)}, "attending": {"1"}}
		if w := app.post(t, "/admin/games/1/attending", form, true); w.Code != http.StatusSeeOther {
			t.Fatalf("ticking %s in: got %d", p.Name, w.Code)
		}
	}
	if w := app.post(t, "/admin/games/1/pick", nil, true); w.Code != http.StatusSeeOther {
		t.Fatalf("picking sides: got %d", w.Code)
	}

	lineup, err := app.store.Lineup(1)
	if err != nil {
		t.Fatal(err)
	}
	for _, pp := range append(append([]PublicPlayer{}, lineup.TeamA...), lineup.TeamB...) {
		if want := pp.Name == "Ada"; pp.Keeper != want {
			t.Errorf("%s: keeper is %v in the lineup, want %v", pp.Name, pp.Keeper, want)
		}
	}
	if len(lineup.TeamA)+len(lineup.TeamB) != 4 {
		t.Errorf("got %d players in the lineup, want 4", len(lineup.TeamA)+len(lineup.TeamB))
	}
}

func TestQuickAddMarksTheWeightingAsRough(t *testing.T) {
	app := newTestApp(t)
	if w := app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true); w.Code != http.StatusSeeOther {
		t.Fatalf("creating a game: got %d", w.Code)
	}

	form := url.Values{"name": {"Nico"}, "weighting": {"4"}, "keeper": {"1"}}
	if w := app.post(t, "/admin/games/1/quick-add", form, true); w.Code != http.StatusSeeOther {
		t.Fatalf("quick add: got %d", w.Code)
	}

	provisional, err := app.store.ProvisionalPlayers()
	if err != nil {
		t.Fatal(err)
	}
	if len(provisional) != 1 || provisional[0].Name != "Nico" {
		t.Fatalf("expected Nico flagged as rough, got %+v", provisional)
	}
	if !provisional[0].Keeper {
		t.Error("Nico was added as somebody who only goes in goal, and that was lost")
	}

	attending, err := app.store.AttendingIDs(1)
	if err != nil {
		t.Fatal(err)
	}
	if !attending[provisional[0].ID] {
		t.Error("someone added on the night should be ticked in already")
	}
}

func TestAdjustWeightingClearsTheRoughFlag(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)
	app.post(t, "/admin/games/1/quick-add", url.Values{"name": {"Nico"}, "weighting": {"4"}}, true)

	all, _ := app.store.Players()
	id := all[0].ID

	form := url.Values{"player_id": {itoa(id)}, "delta": {"-2"}}
	if w := app.post(t, "/admin/games/1/weighting", form, true); w.Code != http.StatusSeeOther {
		t.Fatalf("adjusting: got %d", w.Code)
	}

	p, err := app.store.Player(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Weighting != 2 {
		t.Errorf("weighting is %d, want 2", p.Weighting)
	}
	if p.Provisional {
		t.Error("adjusting a weighting by hand should clear the rough flag")
	}
}

func TestOutOfRangeAdjustmentIsRefused(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)
	app.post(t, "/admin/games/1/quick-add", url.Values{"name": {"Nico"}, "weighting": {"3"}}, true)

	all, _ := app.store.Players()
	form := url.Values{"player_id": {itoa(all[0].ID)}, "delta": {"9"}}
	if w := app.post(t, "/admin/games/1/weighting", form, true); w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for a nonsense adjustment", w.Code)
	}
}

func TestWeightingStaysInRange(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)
	app.post(t, "/admin/games/1/quick-add", url.Values{"name": {"Nico"}, "weighting": {"1"}}, true)

	all, _ := app.store.Players()
	id := all[0].ID

	for i := 0; i < 5; i++ {
		app.post(t, "/admin/games/1/weighting", url.Values{"player_id": {itoa(id)}, "delta": {"-2"}}, true)
	}
	p, _ := app.store.Player(id)
	if p.Weighting != MinWeighting {
		t.Errorf("weighting went to %d, should stop at %d", p.Weighting, MinWeighting)
	}

	for i := 0; i < 10; i++ {
		app.post(t, "/admin/games/1/weighting", url.Values{"player_id": {itoa(id)}, "delta": {"2"}}, true)
	}
	p, _ = app.store.Player(id)
	if p.Weighting != MaxWeighting {
		t.Errorf("weighting went to %d, should stop at %d", p.Weighting, MaxWeighting)
	}
}

func TestAdminGamePageRenders(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)
	app.post(t, "/admin/games/1/quick-add", url.Values{"name": {"Nico"}, "weighting": {"4"}}, true)

	w := app.get(t, "/admin/games/1", true)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Who is playing?", "Someone just turned up", "Nico"} {
		if !strings.Contains(body, want) {
			t.Errorf("the game page is missing %q", want)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store, max-age=0" {
		t.Error("admin pages should not be cached")
	}
}

func TestPlayersPageRenders(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/players", url.Values{"name": {"Ada"}, "weighting": {"4"}, "keeper": {"1"}}, true)

	w := app.get(t, "/admin/players", true)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Ada") {
		t.Error("Ada is missing from the players page")
	}
}

func TestDuplicateNameIsRefused(t *testing.T) {
	app := newTestApp(t)
	form := url.Values{"name": {"Ada"}, "weighting": {"4"}}
	app.post(t, "/admin/players", form, true)
	w := app.post(t, "/admin/players", form, true)

	if got := w.Header().Get("Location"); !strings.Contains(got, "already+on+the+list") {
		t.Errorf("redirected to %q, expected a duplicate name message", got)
	}
	all, _ := app.store.Players()
	if len(all) != 1 {
		t.Errorf("got %d players, want 1", len(all))
	}
}

func TestTeamNamesDefaultAndCanBeChanged(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)
	for _, name := range []string{"Ada", "Bea", "Cleo", "Dara"} {
		app.post(t, "/admin/games/1/quick-add", url.Values{"name": {name}, "weighting": {"3"}}, true)
	}
	app.post(t, "/admin/games/1/pick", nil, true)
	app.post(t, "/admin/games/1/publish", url.Values{"published": {"1"}}, true)

	body := app.get(t, "/", false).Body.String()
	for _, want := range []string{DefaultTeamAName, DefaultTeamBName} {
		if !strings.Contains(body, want) {
			t.Errorf("the public page should fall back to %q", want)
		}
	}

	form := url.Values{"team_a_name": {"Bibs"}, "team_b_name": {"Shirts"}}
	if w := app.post(t, "/admin/games/1/names", form, true); w.Code != http.StatusSeeOther {
		t.Fatalf("renaming: got %d", w.Code)
	}

	body = app.get(t, "/", false).Body.String()
	for _, want := range []string{"Bibs", "Shirts"} {
		if !strings.Contains(body, want) {
			t.Errorf("the public page is missing %q", want)
		}
	}
	if strings.Contains(body, DefaultTeamAName) {
		t.Errorf("the public page still shows %q", DefaultTeamAName)
	}

	// Emptying a name puts it back to the default.
	app.post(t, "/admin/games/1/names", url.Values{"team_a_name": {""}, "team_b_name": {"Shirts"}}, true)
	body = app.get(t, "/", false).Body.String()
	if !strings.Contains(body, DefaultTeamAName) {
		t.Errorf("an empty name should fall back to %q", DefaultTeamAName)
	}
}

func TestLongTeamNameIsTrimmed(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)

	long := strings.Repeat("x", 80)
	app.post(t, "/admin/games/1/names", url.Values{"team_a_name": {long}}, true)

	sess, err := app.store.Session(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.TeamAName) > maxTeamNameLength {
		t.Errorf("stored a name of %d characters, limit is %d", len(sess.TeamAName), maxTeamNameLength)
	}
}

func TestTeamNamesAreSeparatePerGame(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-10-01"}}, true)
	app.post(t, "/admin/games/1/names", url.Values{"team_a_name": {"Bibs"}}, true)

	first, _ := app.store.Session(1)
	second, _ := app.store.Session(2)
	if first.NameA() != "Bibs" {
		t.Errorf("first game is called %q", first.NameA())
	}
	if second.NameA() != DefaultTeamAName {
		t.Errorf("second game should still be %q, got %q", DefaultTeamAName, second.NameA())
	}
}

func TestTeamNameLimitCountsCharactersNotBytes(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)

	// Every one of these is three bytes, so a byte limit would cut one in half
	// and leave invalid UTF-8 behind.
	name := strings.Repeat("日", 30)
	app.post(t, "/admin/games/1/names", url.Values{"team_a_name": {name}}, true)

	sess, err := app.store.Session(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := utf8.RuneCountInString(sess.TeamAName); got != maxTeamNameLength {
		t.Errorf("stored %d characters, want %d", got, maxTeamNameLength)
	}
	if !utf8.ValidString(sess.TeamAName) {
		t.Errorf("stored invalid UTF-8: %q", sess.TeamAName)
	}
	if strings.Contains(app.get(t, "/admin/games/1", true).Body.String(), "�") {
		t.Error("the game page is showing a replacement character")
	}
}

func TestActionsOnAGameThatIsNotThere(t *testing.T) {
	app := newTestApp(t)

	paths := []string{
		"/admin/games/999/names",
		"/admin/games/999/publish",
		"/admin/games/999/pick",
		"/admin/games/999/attending",
		"/admin/games/999/weighting",
		"/admin/games/999/quick-add",
		"/admin/games/999/move",
		"/admin/games/999/assign",
		"/admin/games/999/swap",
		"/admin/games/999/delete",
	}
	for _, path := range paths {
		if w := app.post(t, path, url.Values{"name": {"Nico"}}, true); w.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, w.Code)
		}
	}
	if w := app.get(t, "/admin/games/999", true); w.Code != http.StatusNotFound {
		t.Errorf("the game page: got %d, want 404", w.Code)
	}
}

func TestPageTitles(t *testing.T) {
	app := newTestApp(t)
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)

	cases := []struct {
		path  string
		admin bool
		want  string
	}{
		{"/", false, "<title>" + AppName + "</title>"},
		{"/login", false, "<title>Sign in · " + AppName + "</title>"},
		{"/admin/", true, "<title>Games · " + AppName + "</title>"},
		{"/admin/players", true, "<title>Players · " + AppName + "</title>"},
	}
	for _, c := range cases {
		body := app.get(t, c.path, c.admin).Body.String()
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: expected %s", c.path, c.want)
		}
	}
}
