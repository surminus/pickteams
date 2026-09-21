// Command pickteams picks two even sides for a friendly game of football.
//
// The weightings that drive the split are visible to whoever runs it and to
// nobody else. Public pages are served from a separate set of queries that
// never read the weighting column at all.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// AppName is the only place the name shown to people lives. Change it here
// and it changes everywhere on screen; the code keeps calling itself
// pickteams.
const AppName = "Pick Teams"

const (
	cookieName    = "pickteams_admin"
	sessionMaxAge = 30 * 24 * time.Hour

	// What the two sides are called when a game has not been given its own
	// names. Most weeks these will do.
	DefaultTeamAName = "Team A"
	DefaultTeamBName = "Team B"

	// maxTeamNameLength keeps a name from wrecking the layout.
	maxTeamNameLength = 24
)

type App struct {
	store     *Store
	tmpl      map[string]*template.Template
	password  string
	cookieKey []byte
	rnd       *rand.Rand
}

func main() {
	var (
		addr   = flag.String("addr", "127.0.0.1:8080", "address to listen on")
		dbPath = flag.String("db", "pickteams.db", "path to the SQLite database file")
	)
	flag.Parse()

	password := os.Getenv("PICKTEAMS_ADMIN_PASSWORD")
	if password == "" {
		log.Fatal("PICKTEAMS_ADMIN_PASSWORD is not set, refusing to start without one")
	}

	store, err := OpenStore(*dbPath)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	defer store.Close()

	key, err := store.CookieKey()
	if err != nil {
		log.Fatalf("reading cookie key: %v", err)
	}

	tmpl, err := parseTemplates()
	if err != nil {
		log.Fatalf("parsing templates: %v", err)
	}

	app := &App{
		store:     store,
		tmpl:      tmpl,
		password:  password,
		cookieKey: key,
		rnd:       rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())),
	}

	log.Printf("%s listening on http://%s", AppName, *addr)
	srv := &http.Server{
		Addr:         *addr,
		Handler:      app.routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func parseTemplates() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"join":         strings.Join,
		"appName":      func() string { return AppName },
		"defaultTeamA": func() string { return DefaultTeamAName },
		"defaultTeamB": func() string { return DefaultTeamBName },
		"maxTeamName":  func() int { return maxTeamNameLength },
		"positions":    func() []string { return AllPositions },
		"posLabel":     PositionLabel,
		"weightings": func() []int {
			out := make([]int, 0, MaxWeighting-MinWeighting+1)
			for w := MinWeighting; w <= MaxWeighting; w++ {
				out = append(out, w)
			}
			return out
		},
		"defaultWeighting": func() int { return DefaultWeighting },
	}

	pages := []string{
		"public.html", "login.html",
		"admin_sessions.html", "admin_session.html", "admin_players.html",
	}
	const partial = "templates/_game.html"

	out := map[string]*template.Template{}
	for _, page := range pages {
		t, err := template.New("layout.html").Funcs(funcs).
			ParseFS(templateFS, "templates/layout.html", "templates/"+page, partial)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", page, err)
		}
		out[page] = t
	}

	// The game panel is rendered on its own when htmx asks for a refresh.
	t, err := template.New("_game.html").Funcs(funcs).ParseFS(templateFS, partial)
	if err != nil {
		return nil, err
	}
	out["_game.html"] = t
	return out, nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()

	static := http.FileServer(http.FS(staticFS))
	mux.Handle("GET /static/", static)

	// Public. None of these read a weighting.
	mux.HandleFunc("GET /{$}", a.handlePublicLatest)
	mux.HandleFunc("GET /games/{id}", a.handlePublicSession)

	mux.HandleFunc("GET /login", a.handleLoginForm)
	mux.HandleFunc("POST /login", a.handleLogin)
	mux.HandleFunc("POST /logout", a.handleLogout)

	// Admin.
	admin := http.NewServeMux()
	admin.HandleFunc("GET /admin/{$}", a.handleAdminSessions)
	admin.HandleFunc("POST /admin/games", a.handleCreateSession)
	admin.HandleFunc("GET /admin/games/{id}", a.handleAdminSession)
	admin.HandleFunc("POST /admin/games/{id}/delete", a.handleDeleteSession)
	admin.HandleFunc("POST /admin/games/{id}/attending", a.handleSetAttending)
	admin.HandleFunc("POST /admin/games/{id}/pick", a.handlePickTeams)
	admin.HandleFunc("POST /admin/games/{id}/quick-add", a.handleQuickAdd)
	admin.HandleFunc("POST /admin/games/{id}/weighting", a.handleAdjustWeighting)
	admin.HandleFunc("POST /admin/games/{id}/move", a.handleMovePlayer)
	admin.HandleFunc("POST /admin/games/{id}/publish", a.handlePublish)
	admin.HandleFunc("POST /admin/games/{id}/names", a.handleTeamNames)
	admin.HandleFunc("GET /admin/players", a.handleAdminPlayers)
	admin.HandleFunc("POST /admin/players", a.handleCreatePlayer)
	admin.HandleFunc("POST /admin/players/{id}", a.handleUpdatePlayer)
	admin.HandleFunc("POST /admin/players/{id}/delete", a.handleDeletePlayer)

	mux.Handle("/admin/", a.requireAdmin(admin))

	return noStore(mux)
}

// noStore keeps admin pages out of the browser cache, so a back button on a
// borrowed laptop does not show a page full of weightings.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin") {
			w.Header().Set("Cache-Control", "no-store, max-age=0")
		}
		next.ServeHTTP(w, r)
	})
}

// --- authentication ---

func (a *App) signToken(expiry time.Time) string {
	exp := strconv.FormatInt(expiry.Unix(), 10)
	mac := hmac.New(sha256.New, a.cookieKey)
	mac.Write([]byte(exp))
	return exp + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *App) validToken(token string) bool {
	exp, sig, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, a.cookieKey)
	mac.Write([]byte(exp))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Before(time.Unix(unix, 0))
}

func (a *App) isAdmin(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return a.validToken(c.Value)
}

func (a *App) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.isAdmin(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if a.isAdmin(r) {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	a.render(w, r, "login.html", map[string]any{"Title": "Sign in"})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	given := r.PostFormValue("password")
	if subtle.ConstantTimeCompare([]byte(given), []byte(a.password)) != 1 {
		// Slow down anyone trying passwords in bulk.
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusUnauthorized)
		a.render(w, r, "login.html", map[string]any{
			"Title": "Sign in",
			"Error": "That password did not match.",
		})
		return
	}

	expiry := time.Now().Add(sessionMaxAge)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    a.signToken(expiry),
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- rendering helpers ---

func (a *App) render(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	t, ok := a.tmpl[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["IsAdmin"] = a.isAdmin(r)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		log.Printf("rendering %s: %v", page, err)
	}
}

// renderPartial writes one named template, used for htmx swaps.
func (a *App) renderPartial(w http.ResponseWriter, page string, data map[string]any) {
	t, ok := a.tmpl[page]
	if !ok {
		http.Error(w, "unknown partial", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		log.Printf("rendering partial %s: %v", page, err)
	}
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("bad id")
	}
	return id, nil
}
