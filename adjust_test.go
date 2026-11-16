package main

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// pickedGame sets up game 1 with eight players ticked in and the sides picked,
// and hands back who ended up where.
func pickedGame(t *testing.T) (*App, map[int64]string) {
	t.Helper()
	app := newTestApp(t)

	for i, w := range []string{"5", "4", "4", "3", "3", "2", "2", "1"} {
		form := url.Values{"name": {fmt.Sprintf("Player %d", i+1)}, "weighting": {w}}
		if rec := app.post(t, "/admin/players", form, true); rec.Code != http.StatusSeeOther {
			t.Fatalf("adding player %d: got %d", i+1, rec.Code)
		}
	}
	app.post(t, "/admin/games", url.Values{"played_on": {"2026-09-24"}}, true)

	all, err := app.store.Players()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		app.post(t, "/admin/games/1/attending", url.Values{"player_id": {itoa(p.ID)}, "attending": {"1"}}, true)
	}
	if rec := app.post(t, "/admin/games/1/pick", nil, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("picking sides: got %d", rec.Code)
	}
	return app, sides(t, app)
}

func sides(t *testing.T, app *App) map[int64]string {
	t.Helper()
	picked, err := app.store.PickedIDs(1)
	if err != nil {
		t.Fatal(err)
	}
	return picked
}

// onSide returns somebody from the given side, the lowest id for the sake of
// always getting the same one.
func onSide(picked map[int64]string, team string) int64 {
	var found int64
	for id, side := range picked {
		if side == team && (found == 0 || id < found) {
			found = id
		}
	}
	return found
}

func sideTotals(t *testing.T, app *App, picked map[int64]string) (int, int) {
	t.Helper()
	var a, b int
	for id, side := range picked {
		p, err := app.store.Player(id)
		if err != nil {
			t.Fatal(err)
		}
		if side == "A" {
			a += p.Weighting
		} else {
			b += p.Weighting
		}
	}
	return a, b
}

func TestSwapTradesTwoPlayersAndLeavesTheRest(t *testing.T) {
	app, before := pickedGame(t)
	fromA, fromB := onSide(before, "A"), onSide(before, "B")

	form := url.Values{"player_id": {itoa(fromA)}, "with_id": {itoa(fromB)}}
	if rec := app.post(t, "/admin/games/1/swap", form, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("swapping: got %d", rec.Code)
	}

	want := maps.Clone(before)
	want[fromA], want[fromB] = "B", "A"
	if after := sides(t, app); !maps.Equal(after, want) {
		t.Errorf("after the swap the sides are %v, want %v", after, want)
	}
}

// Choosing somebody to swap shows, against everyone on the other side, what
// the totals would become. Those numbers have to be the ones the swap then
// produces.
func TestSwapShowsWhatItWouldDoBeforeDoingIt(t *testing.T) {
	for _, from := range []string{"A", "B"} {
		app, before := pickedGame(t)
		other := "B"
		if from == "B" {
			other = "A"
		}
		chosen, with := onSide(before, from), onSide(before, other)

		// Work out what the totals should be by doing the swap by hand.
		swapped := maps.Clone(before)
		swapped[chosen], swapped[with] = other, from
		wantA, wantB := sideTotals(t, app, swapped)

		rec := app.get(t, "/admin/games/1?swap="+itoa(chosen), true)
		if rec.Code != http.StatusOK {
			t.Fatalf("starting a swap: got %d", rec.Code)
		}
		body := rec.Body.String()

		button := fmt.Sprintf(`<input type="hidden" name="with_id" value="%d">`, with)
		_, rest, found := strings.Cut(body, button)
		if !found {
			t.Fatalf("from %s: no swap button for player %d", from, with)
		}
		label, _, _ := strings.Cut(rest, "</button>")
		if want := fmt.Sprintf("%d v %d", wantA, wantB); !strings.Contains(label, want) {
			t.Errorf("from %s: the button reads %q, want it to promise %s", from, strings.TrimSpace(label), want)
		}
		// Only the other side is on offer: four of them.
		if got := strings.Count(body, `name="with_id"`); got != 4 {
			t.Errorf("from %s: %d players offered for the swap, want the 4 on the other side", from, got)
		}

		// Looking is not doing.
		if after := sides(t, app); !maps.Equal(after, before) {
			t.Errorf("from %s: starting a swap changed the sides", from)
		}

		form := url.Values{"player_id": {itoa(chosen)}, "with_id": {itoa(with)}}
		app.post(t, "/admin/games/1/swap", form, true)
		gotA, gotB := sideTotals(t, app, sides(t, app))
		if gotA != wantA || gotB != wantB {
			t.Errorf("from %s: the swap left totals of %d v %d, the button promised %d v %d", from, gotA, gotB, wantA, wantB)
		}
	}
}

func TestSwapWithSomeoneOnTheSameSideDoesNothing(t *testing.T) {
	app, before := pickedGame(t)
	first := onSide(before, "A")
	var second int64
	for id, side := range before {
		if side == "A" && id != first {
			second = id
		}
	}

	form := url.Values{"player_id": {itoa(first)}, "with_id": {itoa(second)}}
	if rec := app.post(t, "/admin/games/1/swap", form, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d", rec.Code)
	}
	if after := sides(t, app); !maps.Equal(after, before) {
		t.Errorf("two players from the same side were swapped: %v", after)
	}

	// Nor does swapping somebody with themselves, or with nobody.
	app.post(t, "/admin/games/1/swap", url.Values{"player_id": {itoa(first)}, "with_id": {itoa(first)}}, true)
	app.post(t, "/admin/games/1/swap", url.Values{"player_id": {itoa(first)}, "with_id": {"9999"}}, true)
	if after := sides(t, app); !maps.Equal(after, before) {
		t.Errorf("a nonsense swap changed the sides: %v", after)
	}
}

func TestTakingSomeoneOutLeavesEveryoneElseWhereTheyWere(t *testing.T) {
	app, before := pickedGame(t)
	leaving := onSide(before, "A")

	// No "attending" field is how an unticked box arrives.
	if rec := app.post(t, "/admin/games/1/attending", url.Values{"player_id": {itoa(leaving)}}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d", rec.Code)
	}

	want := maps.Clone(before)
	delete(want, leaving)
	if after := sides(t, app); !maps.Equal(after, want) {
		t.Errorf("after taking one player out the sides are %v, want %v", after, want)
	}

	lineup, err := app.store.Lineup(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(lineup.TeamA) + len(lineup.TeamB); got != 7 {
		t.Errorf("the public lineup has %d players, want 7", got)
	}
}

func TestLatecomerCanBePutOnASide(t *testing.T) {
	app, before := pickedGame(t)

	app.post(t, "/admin/games/1/quick-add", url.Values{"name": {"Nico"}, "weighting": {"4"}}, true)
	var nico Player
	all, _ := app.store.Players()
	for _, p := range all {
		if p.Name == "Nico" {
			nico = p
		}
	}

	// Ticked in, but not on a side until somebody says which.
	if after := sides(t, app); !maps.Equal(after, before) {
		t.Fatalf("adding a latecomer disturbed the sides: %v", after)
	}
	totalA, totalB := sideTotals(t, app, before)
	body := app.get(t, "/admin/games/1", true).Body.String()
	if !strings.Contains(body, "Not on a side yet") {
		t.Fatal("the game page does not offer to put the latecomer on a side")
	}
	for _, want := range []string{
		fmt.Sprintf("%s &middot; %d</button>", DefaultTeamAName, totalA+4),
		fmt.Sprintf("%s &middot; %d</button>", DefaultTeamBName, totalB+4),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the game page is missing the button %q", want)
		}
	}

	form := url.Values{"player_id": {itoa(nico.ID)}, "team": {"B"}}
	if rec := app.post(t, "/admin/games/1/assign", form, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("assigning: got %d", rec.Code)
	}
	want := maps.Clone(before)
	want[nico.ID] = "B"
	if after := sides(t, app); !maps.Equal(after, want) {
		t.Errorf("after adding the latecomer the sides are %v, want %v", after, want)
	}
	if strings.Contains(app.get(t, "/admin/games/1", true).Body.String(), "Not on a side yet") {
		t.Error("the latecomer is on a side now, the page should stop offering")
	}
}

func TestOnlySomeonePlayingCanBePutOnASide(t *testing.T) {
	app, before := pickedGame(t)
	app.post(t, "/admin/players", url.Values{"name": {"Nico"}, "weighting": {"4"}}, true)
	var nico Player
	all, _ := app.store.Players()
	for _, p := range all {
		if p.Name == "Nico" {
			nico = p
		}
	}

	app.post(t, "/admin/games/1/assign", url.Values{"player_id": {itoa(nico.ID)}, "team": {"A"}}, true)
	if after := sides(t, app); !maps.Equal(after, before) {
		t.Errorf("somebody not ticked in was put on a side: %v", after)
	}

	form := url.Values{"player_id": {itoa(onSide(before, "A"))}, "team": {"C"}}
	if rec := app.post(t, "/admin/games/1/assign", form, true); rec.Code != http.StatusBadRequest {
		t.Errorf("got %d for a side that does not exist, want 400", rec.Code)
	}
}
