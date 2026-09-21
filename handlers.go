package main

import (
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// --- public pages ---

func (a *App) handlePublicLatest(w http.ResponseWriter, r *http.Request) {
	sess, err := a.store.LatestPublished()
	if err != nil {
		// Nothing published yet is the normal state before the first game.
		// No title: the layout falls back to the app name on its own.
		a.render(w, r, "public.html", nil)
		return
	}
	a.showLineup(w, r, sess.ID)
}

func (a *App) handlePublicSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.showLineup(w, r, id)
}

// showLineup renders the public team sheet. It goes through Store.Lineup,
// which does not select the weighting column, so there is nothing sensitive
// in the data this page is handed.
func (a *App) showLineup(w http.ResponseWriter, r *http.Request, id int64) {
	lineup, err := a.store.Lineup(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !lineup.Session.Published && !a.isAdmin(r) {
		http.NotFound(w, r)
		return
	}
	a.render(w, r, "public.html", map[string]any{"Lineup": lineup})
}

// --- admin: games ---

func (a *App) handleAdminSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := a.store.Sessions()
	if err != nil {
		a.fail(w, err)
		return
	}
	provisional, err := a.store.ProvisionalPlayers()
	if err != nil {
		a.fail(w, err)
		return
	}
	a.render(w, r, "admin_sessions.html", map[string]any{
		"Title":       "Games",
		"Sessions":    sessions,
		"Provisional": provisional,
		"Today":       time.Now().Format("2006-01-02"),
	})
}

func (a *App) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	date := strings.TrimSpace(r.PostFormValue("played_on"))
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	id, err := a.store.CreateSession(date)
	if err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/games/%d", id), http.StatusSeeOther)
}

func (a *App) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteSession(id); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

// gameData assembles everything the game page needs in one go. This is the
// admin view, so weightings are included on purpose.
func (a *App) gameData(id int64) (map[string]any, error) {
	sess, err := a.store.Session(id)
	if err != nil {
		return nil, err
	}
	players, err := a.store.Players()
	if err != nil {
		return nil, err
	}
	attending, err := a.store.AttendingIDs(id)
	if err != nil {
		return nil, err
	}
	picked, err := a.store.PickedIDs(id)
	if err != nil {
		return nil, err
	}

	// Show active players, plus anyone inactive who is marked as playing
	// anyway, so a returning player does not vanish from the list.
	var squad []Player
	for _, p := range players {
		if p.Active || attending[p.ID] {
			squad = append(squad, p)
		}
	}

	byID := map[int64]Player{}
	for _, p := range players {
		byID[p.ID] = p
	}

	var teamA, teamB []Player
	for pid, team := range picked {
		p, ok := byID[pid]
		if !ok {
			continue
		}
		if team == "A" {
			teamA = append(teamA, p)
		} else {
			teamB = append(teamB, p)
		}
	}
	byName := func(x, y Player) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) }
	slices.SortFunc(teamA, byName)
	slices.SortFunc(teamB, byName)

	attendingCount := 0
	for _, p := range squad {
		if attending[p.ID] {
			attendingCount++
		}
	}

	return map[string]any{
		"Title":     "Game " + sess.PlayedOn,
		"Session":   sess,
		"Squad":     squad,
		"Attending": attending,
		"Count":     attendingCount,
		"TeamA":     teamA,
		"TeamB":     teamB,
		"AvgA":      averageWeighting(teamA),
		"AvgB":      averageWeighting(teamB),
		"HasPicks":  len(teamA)+len(teamB) > 0,
	}, nil
}

func averageWeighting(players []Player) string {
	if len(players) == 0 {
		return "-"
	}
	total := 0
	for _, p := range players {
		total += p.Weighting
	}
	return fmt.Sprintf("%.1f", float64(total)/float64(len(players)))
}

func (a *App) handleAdminSession(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	data, err := a.gameData(id)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.render(w, r, "admin_session.html", data)
}

// gameID reads the game id out of the path and checks the game is really
// there, so a stale link gets a 404 rather than a cheerful redirect back to a
// game that does not exist.
func (a *App) gameID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	if _, err := a.store.Session(id); err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

// refreshGame answers an action on the game page. For htmx it swaps the page
// body; for a browser with no JavaScript it redirects back to the page.
func (a *App) refreshGame(w http.ResponseWriter, r *http.Request, id int64, extra map[string]any) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, fmt.Sprintf("/admin/games/%d", id), http.StatusSeeOther)
		return
	}
	data, err := a.gameData(id)
	if err != nil {
		a.fail(w, err)
		return
	}
	for k, v := range extra {
		data[k] = v
	}
	data["IsAdmin"] = true
	a.renderPartial(w, "_game.html", data)
}

func (a *App) handleSetAttending(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	playerID, err := strconv.ParseInt(r.PostFormValue("player_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad player", http.StatusBadRequest)
		return
	}
	// An unticked checkbox sends nothing, so presence of the field is the
	// signal that they are playing.
	attending := r.PostFormValue("attending") != ""
	if err := a.store.SetAttending(id, playerID, attending); err != nil {
		a.fail(w, err)
		return
	}
	a.refreshGame(w, r, id, nil)
}

// handleQuickAdd adds somebody who has just turned up, marks them as playing,
// and flags the weighting as provisional so it can be revisited later.
func (a *App) handleQuickAdd(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		a.refreshGame(w, r, id, map[string]any{"Error": "Give them a name first."})
		return
	}

	weighting := parseWeighting(r.PostFormValue("weighting"))
	player := Player{
		Name:        name,
		Active:      true,
		Weighting:   weighting,
		Provisional: true,
		Position:    CleanPosition(r.PostFormValue("position")),
	}

	playerID, err := a.store.CreatePlayer(player)
	if err != nil {
		msg := "Could not add them."
		if strings.Contains(err.Error(), "UNIQUE") {
			msg = name + " is already on the list."
		}
		a.refreshGame(w, r, id, map[string]any{"Error": msg})
		return
	}
	if err := a.store.SetAttending(id, playerID, true); err != nil {
		a.fail(w, err)
		return
	}
	a.refreshGame(w, r, id, map[string]any{"Notice": name + " added and marked as playing."})
}

// handleAdjustWeighting is the nudge up or down next to each name, for when a
// weighting turns out to be off and you want to fix it there and then.
func (a *App) handleAdjustWeighting(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	playerID, err := strconv.ParseInt(r.PostFormValue("player_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad player", http.StatusBadRequest)
		return
	}
	delta, err := strconv.Atoi(r.PostFormValue("delta"))
	if err != nil || delta < -2 || delta > 2 || delta == 0 {
		http.Error(w, "bad adjustment", http.StatusBadRequest)
		return
	}
	if err := a.store.AdjustWeighting(playerID, delta); err != nil {
		a.fail(w, err)
		return
	}

	extra := map[string]any{}
	if picked, err := a.store.PickedIDs(id); err == nil && len(picked) > 0 {
		extra["Notice"] = "Weighting changed. Pick the sides again to take it into account."
	}
	a.refreshGame(w, r, id, extra)
}

func (a *App) handlePickTeams(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	players, err := a.store.AttendingPlayers(id)
	if err != nil {
		a.fail(w, err)
		return
	}

	split, err := Balance(players, a.rnd)
	if err != nil {
		a.refreshGame(w, r, id, map[string]any{"Error": err.Error()})
		return
	}
	if err := a.store.SavePicks(id, split); err != nil {
		a.fail(w, err)
		return
	}
	a.refreshGame(w, r, id, map[string]any{"Notice": "Sides picked."})
}

func (a *App) handleMovePlayer(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	playerID, err := strconv.ParseInt(r.PostFormValue("player_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad player", http.StatusBadRequest)
		return
	}
	if err := a.store.MovePlayer(id, playerID); err != nil {
		a.fail(w, err)
		return
	}
	a.refreshGame(w, r, id, map[string]any{"Notice": "Moved."})
}

func (a *App) handlePublish(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	published := r.PostFormValue("published") != ""
	if err := a.store.SetPublished(id, published); err != nil {
		a.fail(w, err)
		return
	}
	note := "Teams are now visible to everyone."
	if !published {
		note = "Teams hidden again."
	}
	a.refreshGame(w, r, id, map[string]any{"Notice": note})
}

// handleTeamNames renames the two sides for this game. Leaving a box empty
// puts it back to the default.
func (a *App) handleTeamNames(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	nameA := trimTeamName(r.PostFormValue("team_a_name"))
	nameB := trimTeamName(r.PostFormValue("team_b_name"))
	if err := a.store.SetTeamNames(id, nameA, nameB); err != nil {
		a.fail(w, err)
		return
	}
	a.refreshGame(w, r, id, map[string]any{"Notice": "Names saved."})
}

// trimTeamName tidies up whatever was typed and cuts it to length. The limit
// counts characters rather than bytes, both so it agrees with the maxlength
// the browser enforces and so cutting a name never splits one in half.
func trimTeamName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if runes := []rune(name); len(runes) > maxTeamNameLength {
		name = strings.TrimSpace(string(runes[:maxTeamNameLength]))
	}
	return name
}

// --- admin: players ---

func (a *App) handleAdminPlayers(w http.ResponseWriter, r *http.Request) {
	players, err := a.store.Players()
	if err != nil {
		a.fail(w, err)
		return
	}
	a.render(w, r, "admin_players.html", map[string]any{
		"Title":   "Players",
		"Players": players,
		"Notice":  r.URL.Query().Get("notice"),
		"Error":   r.URL.Query().Get("error"),
	})
}

func parseWeighting(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return DefaultWeighting
	}
	return ClampWeighting(v)
}

func (a *App) handleCreatePlayer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		redirectPlayers(w, r, "", "Give them a name first.")
		return
	}

	_, err := a.store.CreatePlayer(Player{
		Name:        name,
		Active:      true,
		Weighting:   parseWeighting(r.PostFormValue("weighting")),
		Provisional: r.PostFormValue("provisional") != "",
		Position:    CleanPosition(r.PostFormValue("position")),
		Notes:       strings.TrimSpace(r.PostFormValue("notes")),
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			redirectPlayers(w, r, "", name+" is already on the list.")
			return
		}
		a.fail(w, err)
		return
	}
	redirectPlayers(w, r, name+" added.", "")
}

func (a *App) handleUpdatePlayer(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	existing, err := a.store.Player(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	existing.Name = strings.TrimSpace(r.PostFormValue("name"))
	existing.Weighting = parseWeighting(r.PostFormValue("weighting"))
	existing.Active = r.PostFormValue("active") != ""
	existing.Provisional = r.PostFormValue("provisional") != ""
	existing.Position = CleanPosition(r.PostFormValue("position"))
	existing.Notes = strings.TrimSpace(r.PostFormValue("notes"))

	if existing.Name == "" {
		redirectPlayers(w, r, "", "A player needs a name.")
		return
	}
	if err := a.store.UpdatePlayer(existing); err != nil {
		a.fail(w, err)
		return
	}
	redirectPlayers(w, r, existing.Name+" updated.", "")
}

func (a *App) handleDeletePlayer(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.store.DeletePlayer(id); err != nil {
		a.fail(w, err)
		return
	}
	redirectPlayers(w, r, "Player removed.", "")
}

func redirectPlayers(w http.ResponseWriter, r *http.Request, notice, errMsg string) {
	url := "/admin/players"
	switch {
	case errMsg != "":
		url += "?error=" + urlEscape(errMsg)
	case notice != "":
		url += "?notice=" + urlEscape(notice)
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func urlEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", "+"), "&", "%26")
}

func (a *App) fail(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
}
