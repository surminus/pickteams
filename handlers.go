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
//
// swapID is the player somebody has asked to swap, or nought. When it names a
// player who is on a side, the page shows what the totals would become for a
// swap with each player on the other side, so the choice can be made knowing
// what it does.
func (a *App) gameData(id, swapID int64) (map[string]any, error) {
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

	totalA, totalB := totalWeighting(teamA), totalWeighting(teamB)
	hasPicks := len(teamA)+len(teamB) > 0

	// Anyone ticked in since the sides were picked is waiting to be put on
	// one. Each comes with the total either side would have with them on it.
	var waiting []waitingPlayer
	if hasPicks {
		for _, p := range squad {
			if _, onASide := picked[p.ID]; attending[p.ID] && !onASide {
				waiting = append(waiting, waitingPlayer{
					Player:   p,
					TotalIfA: totalA + p.Weighting,
					TotalIfB: totalB + p.Weighting,
				})
			}
		}
	}

	data := map[string]any{
		"Title":     "Game " + sess.PlayedOn,
		"Session":   sess,
		"Squad":     squad,
		"Attending": attending,
		"Count":     attendingCount,
		"TeamA":     teamA,
		"TeamB":     teamB,
		"TotalA":    totalA,
		"TotalB":    totalB,
		"HasPicks":  hasPicks,
		"Waiting":   waiting,
		"SwapID":    int64(0),
		"SwapSide":  "",
	}

	if side, ok := picked[swapID]; ok {
		chosen, others := byID[swapID], teamB
		if side == "B" {
			others = teamA
		}
		// What the two totals would be after swapping with each of them.
		results := map[int64]string{}
		for _, other := range others {
			shift := other.Weighting - chosen.Weighting
			if side == "B" {
				shift = -shift
			}
			results[other.ID] = fmt.Sprintf("%d v %d", totalA+shift, totalB-shift)
		}
		data["SwapID"] = swapID
		data["SwapSide"] = side
		data["SwapName"] = chosen.Name
		data["SwapResult"] = results
	}
	return data, nil
}

// waitingPlayer is somebody who is playing but is not on a side yet.
type waitingPlayer struct {
	Player
	TotalIfA int
	TotalIfB int
}

// totalWeighting is the number the balancer evens up. It is a total and not
// an average so that a side with an extra player shows the advantage it has.
func totalWeighting(players []Player) int {
	total := 0
	for _, p := range players {
		total += p.Weighting
	}
	return total
}

func (a *App) handleAdminSession(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	// A swap that has been started but not finished lives in the address, so
	// nothing is stored until the second player is chosen.
	swapID, _ := strconv.ParseInt(r.URL.Query().Get("swap"), 10, 64)
	data, err := a.gameData(id, swapID)
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
	data, err := a.gameData(id, 0)
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
		Keeper:      r.PostFormValue("keeper") != "",
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
		extra["Notice"] = "Weighting changed. The totals are up to date, so swap players or pick again if the sides are no longer even."
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

// handleAssignPlayer puts somebody who is playing, but was not there when the
// sides were picked, onto one of them without disturbing anyone else.
func (a *App) handleAssignPlayer(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	playerID, err := strconv.ParseInt(r.PostFormValue("player_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad player", http.StatusBadRequest)
		return
	}
	team := r.PostFormValue("team")
	if team != "A" && team != "B" {
		http.Error(w, "bad side", http.StatusBadRequest)
		return
	}
	added, err := a.store.AssignPlayer(id, playerID, team)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !added {
		a.refreshGame(w, r, id, map[string]any{"Error": "They are not ticked in as playing, so they cannot go on a side."})
		return
	}
	a.refreshGame(w, r, id, map[string]any{"Notice": a.totalsNotice(id, "Added.")})
}

// handleSwapPlayers trades one player from each side, leaving everybody else
// where they are.
func (a *App) handleSwapPlayers(w http.ResponseWriter, r *http.Request) {
	id, ok := a.gameID(w, r)
	if !ok {
		return
	}
	playerID, err1 := strconv.ParseInt(r.PostFormValue("player_id"), 10, 64)
	withID, err2 := strconv.ParseInt(r.PostFormValue("with_id"), 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "bad player", http.StatusBadRequest)
		return
	}
	swapped, err := a.store.SwapPlayers(id, playerID, withID)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !swapped {
		a.refreshGame(w, r, id, map[string]any{"Error": "Those two are not on opposite sides any more, so nothing was swapped."})
		return
	}
	a.refreshGame(w, r, id, map[string]any{"Notice": a.totalsNotice(id, "Swapped.")})
}

// totalsNotice adds where the totals now stand to the end of a notice, so the
// effect of a change is spelled out rather than left to be spotted.
func (a *App) totalsNotice(id int64, done string) string {
	data, err := a.gameData(id, 0)
	if err != nil {
		return done
	}
	sess := data["Session"].(Session)
	return fmt.Sprintf("%s %s now total %d, %s %d.", done, sess.NameA(), data["TotalA"], sess.NameB(), data["TotalB"])
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
		Keeper:      r.PostFormValue("keeper") != "",
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
	existing.Keeper = r.PostFormValue("keeper") != ""
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
