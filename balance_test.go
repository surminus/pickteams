package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

func testRand() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func squad(weightings ...int) []Player {
	out := make([]Player, len(weightings))
	for i, w := range weightings {
		out[i] = Player{
			ID:        int64(i + 1),
			Name:      fmt.Sprintf("Player %d", i+1),
			Active:    true,
			Weighting: w,
			Position:  "MID",
		}
	}
	return out
}

func average(players []Player) float64 {
	if len(players) == 0 {
		return 0
	}
	total := 0
	for _, p := range players {
		total += p.Weighting
	}
	return float64(total) / float64(len(players))
}

func TestBalanceNeedsEnoughPlayers(t *testing.T) {
	if _, err := Balance(squad(3, 3, 3), testRand()); err == nil {
		t.Fatal("expected an error with three players")
	}
}

func TestBalanceUsesEveryoneOnce(t *testing.T) {
	players := squad(1, 2, 3, 4, 5, 1, 2, 3, 4, 5)
	split, err := Balance(players, testRand())
	if err != nil {
		t.Fatal(err)
	}

	seen := map[int64]int{}
	for _, p := range append(append([]Player{}, split.A...), split.B...) {
		seen[p.ID]++
	}
	if len(seen) != len(players) {
		t.Fatalf("got %d players across both sides, want %d", len(seen), len(players))
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("player %d appears %d times", id, count)
		}
	}
}

func TestBalanceEvenSizes(t *testing.T) {
	for _, n := range []int{4, 8, 11, 14, 21, 22} {
		weightings := make([]int, n)
		for i := range weightings {
			weightings[i] = i%MaxWeighting + 1
		}
		split, err := Balance(squad(weightings...), testRand())
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if diff := abs(len(split.A) - len(split.B)); diff > 1 {
			t.Errorf("n=%d: sides of %d and %d", n, len(split.A), len(split.B))
		}
	}
}

func TestBalanceEvensOutWeightings(t *testing.T) {
	// Two players at each weighting, so a perfectly even split exists.
	players := squad(1, 1, 2, 2, 3, 3, 4, 4, 5, 5)
	for i := 0; i < 20; i++ {
		split, err := Balance(players, testRand())
		if err != nil {
			t.Fatal(err)
		}
		if gap := math.Abs(average(split.A) - average(split.B)); gap > 0.3 {
			t.Fatalf("average gap of %.2f is too wide: %v vs %v", gap, average(split.A), average(split.B))
		}
	}
}

func TestBalanceSplitsDedicatedKeepers(t *testing.T) {
	players := squad(3, 3, 3, 3, 3, 3, 3, 3)
	players[0].Position = "GK"
	players[1].Position = "GK"

	for i := 0; i < 25; i++ {
		split, err := Balance(players, rand.New(rand.NewPCG(uint64(i), 99)))
		if err != nil {
			t.Fatal(err)
		}
		if countKeepers(split.A) != 1 || countKeepers(split.B) != 1 {
			t.Fatalf("run %d put %d and %d dedicated keepers on the two sides",
				i, countKeepers(split.A), countKeepers(split.B))
		}
	}
}

func TestBalanceSplitsThreeKeepersAsEvenlyAsItCan(t *testing.T) {
	players := squad(3, 3, 3, 3, 3, 3, 3, 3)
	for i := 0; i < 3; i++ {
		players[i].Position = "GK"
	}
	split, err := Balance(players, testRand())
	if err != nil {
		t.Fatal(err)
	}
	if d := abs(countKeepers(split.A) - countKeepers(split.B)); d != 1 {
		t.Errorf("three keepers split %d and %d", countKeepers(split.A), countKeepers(split.B))
	}
}

func TestBalanceWithOneKeeperDoesNotCrash(t *testing.T) {
	players := squad(3, 3, 3, 3, 3, 3)
	players[0].Position = "GK"
	if _, err := Balance(players, testRand()); err != nil {
		t.Fatal(err)
	}
}

// Nobody dedicated to goal means we all take turns, so the keeper rule should
// not push the split around at all.
func TestBalanceIgnoresGoalWhenEveryoneTakesTurns(t *testing.T) {
	players := squad(1, 1, 2, 2, 4, 4, 5, 5)
	split, err := Balance(players, testRand())
	if err != nil {
		t.Fatal(err)
	}
	if gap := math.Abs(average(split.A) - average(split.B)); gap > 0.01 {
		t.Errorf("average gap of %.2f, an exactly even split was available", gap)
	}
}

func TestBalanceSpreadsOutfieldPositions(t *testing.T) {
	players := squad(3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3)
	for i := range players {
		players[i].Position = OutfieldPositions[i%len(OutfieldPositions)]
	}
	split, err := Balance(players, testRand())
	if err != nil {
		t.Fatal(err)
	}
	for _, pos := range OutfieldPositions {
		if d := abs(countPosition(split.A, pos) - countPosition(split.B, pos)); d > 1 {
			t.Errorf("%s is off by %d between the sides", pos, d)
		}
	}
}

func TestBalanceVariesBetweenRuns(t *testing.T) {
	// Lots of identical players means lots of equally good splits, and the
	// same one should not come back every time. Identical output would let
	// people work out the weightings by watching week to week.
	players := squad(2, 2, 3, 3, 3, 3, 4, 4, 3, 3)
	seen := map[string]bool{}
	for i := 0; i < 15; i++ {
		split, err := Balance(players, rand.New(rand.NewPCG(uint64(i), 7)))
		if err != nil {
			t.Fatal(err)
		}
		key := ""
		for _, p := range split.A {
			key += fmt.Sprintf("%d,", p.ID)
		}
		seen[key] = true
	}
	if len(seen) < 3 {
		t.Errorf("only %d distinct splits across 15 runs", len(seen))
	}
}

func TestBalanceHeuristicForBigSquads(t *testing.T) {
	weightings := make([]int, 30)
	for i := range weightings {
		weightings[i] = i%MaxWeighting + 1
	}
	players := squad(weightings...)
	players[0].Position = "GK"
	players[1].Position = "GK"

	split, err := Balance(players, testRand())
	if err != nil {
		t.Fatal(err)
	}
	if len(split.A)+len(split.B) != 30 {
		t.Fatalf("lost players: %d + %d", len(split.A), len(split.B))
	}
	if gap := math.Abs(average(split.A) - average(split.B)); gap > 0.5 {
		t.Errorf("average gap of %.2f is too wide for 30 players", gap)
	}
}

func TestBalanceTooManyPlayers(t *testing.T) {
	weightings := make([]int, 65)
	for i := range weightings {
		weightings[i] = 3
	}
	if _, err := Balance(squad(weightings...), testRand()); err == nil {
		t.Fatal("expected an error above 64 players")
	}
}

func countKeepers(players []Player) int {
	n := 0
	for _, p := range players {
		if p.IsKeeper() {
			n++
		}
	}
	return n
}

func countPosition(players []Player, pos string) int {
	n := 0
	for _, p := range players {
		if p.Position == pos {
			n++
		}
	}
	return n
}
