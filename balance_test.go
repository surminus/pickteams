package main

import (
	"fmt"
	"math"
	"math/bits"
	"math/rand/v2"
	"testing"
)

func testRand() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func squadOf(weightings ...int) []Player {
	out := make([]Player, len(weightings))
	for i, w := range weightings {
		out[i] = Player{
			ID:        int64(i + 1),
			Name:      fmt.Sprintf("Player %d", i+1),
			Active:    true,
			Weighting: w,
		}
	}
	return out
}

func total(players []Player) int {
	sum := 0
	for _, p := range players {
		sum += p.Weighting
	}
	return sum
}

func gap(s Split) int { return abs(total(s.A) - total(s.B)) }

func has(players []Player, id int64) bool {
	for _, p := range players {
		if p.ID == id {
			return true
		}
	}
	return false
}

func TestBalanceNeedsEnoughPlayers(t *testing.T) {
	if _, err := Balance(squadOf(3, 3, 3), testRand()); err == nil {
		t.Fatal("expected an error with three players")
	}
}

func TestBalanceUsesEveryoneOnce(t *testing.T) {
	players := squadOf(1, 2, 3, 4, 5, 1, 2, 3, 4, 5)
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
	for _, n := range []int{4, 5, 8, 11, 14, 21, 22, 23, 24} {
		weightings := make([]int, n)
		for i := range weightings {
			weightings[i] = i%MaxWeighting + 1
		}
		split, err := Balance(squadOf(weightings...), testRand())
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if len(split.A)+len(split.B) != n {
			t.Errorf("n=%d: %d players came back", n, len(split.A)+len(split.B))
		}
		if diff := abs(len(split.A) - len(split.B)); diff != n%2 {
			t.Errorf("n=%d: sides of %d and %d", n, len(split.A), len(split.B))
		}
	}
}

func TestBalanceEvensOutTotals(t *testing.T) {
	// Two players at each weighting, so a perfectly even split exists.
	players := squadOf(1, 1, 2, 2, 3, 3, 4, 4, 5, 5)
	for i := 0; i < 20; i++ {
		split, err := Balance(players, rand.New(rand.NewPCG(uint64(i), 3)))
		if err != nil {
			t.Fatal(err)
		}
		if g := gap(split); g != 0 {
			t.Fatalf("totals of %d and %d, an exactly even split was available", total(split.A), total(split.B))
		}
		if split.Cost != 0 {
			t.Errorf("cost is %d for an exactly even split", split.Cost)
		}
	}
}

// TestBalanceMatchesBruteForce checks the search against the slow and obvious
// way of doing it: try every possible side and keep the smallest gap.
func TestBalanceMatchesBruteForce(t *testing.T) {
	rnd := rand.New(rand.NewPCG(4, 4))
	for n := 4; n <= 13; n++ {
		for trial := 0; trial < 20; trial++ {
			weightings := make([]int, n)
			for i := range weightings {
				weightings[i] = MinWeighting + rnd.IntN(MaxWeighting-MinWeighting+1)
			}
			players := squadOf(weightings...)

			sum := total(players)
			want := math.MaxInt
			for mask := 0; mask < 1<<n; mask++ {
				if bits.OnesCount(uint(mask)) != n/2 {
					continue
				}
				side := 0
				for i, w := range weightings {
					if mask&(1<<i) != 0 {
						side += w
					}
				}
				want = min(want, abs(2*side-sum))
			}

			split, err := Balance(players, rnd)
			if err != nil {
				t.Fatal(err)
			}
			if g := gap(split); g != want || split.Cost != want {
				t.Fatalf("%v: gap of %d (cost %d), the best there is is %d", weightings, g, split.Cost, want)
			}
		}
	}
}

// With an odd number of players the side with one fewer has to be given the
// stronger players, so that the totals match. Matching the averages instead
// would leave the bigger side a whole player up.
func TestBalanceMakesUpForTheExtraPlayer(t *testing.T) {
	// 27 points between nine players: 13 against 14 is on offer.
	players := squadOf(5, 4, 4, 3, 3, 3, 2, 2, 1)
	for i := 0; i < 50; i++ {
		split, err := Balance(players, rand.New(rand.NewPCG(uint64(i), 5)))
		if err != nil {
			t.Fatal(err)
		}
		if g := gap(split); g != 1 {
			t.Fatalf("run %d: %d players with %d against %d players with %d",
				i, len(split.A), total(split.A), len(split.B), total(split.B))
		}
	}
}

// When everyone is the same there is no making up for it, and the smaller
// side is simply a player down. The cost should say so rather than call it
// even.
func TestBalanceAdmitsWhenTheExtraPlayerCannotBeMadeUpFor(t *testing.T) {
	split, err := Balance(squadOf(3, 3, 3, 3, 3, 3, 3, 3, 3), testRand())
	if err != nil {
		t.Fatal(err)
	}
	if split.Cost != 3 {
		t.Errorf("cost is %d, want 3: one side is a weighting-3 player up", split.Cost)
	}
}

// Neither which side is the bigger one nor which side the first name on the
// list lands on should be the same every time.
func TestBalanceDoesNotFavourASide(t *testing.T) {
	for _, players := range [][]Player{
		squadOf(5, 4, 4, 3, 3, 3, 2, 2, 1),
		squadOf(5, 4, 4, 3, 3, 3, 2, 2, 1, 3),
	} {
		n := len(players)
		const runs = 400
		firstOnA, firstOnBigger, aIsBigger := 0, 0, 0
		for i := 0; i < runs; i++ {
			split, err := Balance(players, rand.New(rand.NewPCG(uint64(i), 9)))
			if err != nil {
				t.Fatal(err)
			}
			onA := has(split.A, 1)
			if onA {
				firstOnA++
			}
			if len(split.A) > len(split.B) {
				aIsBigger++
			}
			if onA == (len(split.A) > len(split.B)) {
				firstOnBigger++
			}
		}
		if firstOnA < runs/4 || firstOnA > 3*runs/4 {
			t.Errorf("n=%d: the first player was on side A in %d of %d runs", n, firstOnA, runs)
		}
		if n%2 == 1 {
			if aIsBigger < runs/4 || aIsBigger > 3*runs/4 {
				t.Errorf("n=%d: side A had the extra player in %d of %d runs", n, aIsBigger, runs)
			}
			if firstOnBigger == 0 || firstOnBigger == runs {
				t.Errorf("n=%d: the first player was on the bigger side in %d of %d runs", n, firstOnBigger, runs)
			}
		}
	}
}

// Every equally even split should be as likely as the next. With eighteen
// identical players there are 24,310 of them, and each player should end up
// alongside player 1 in 8 runs out of 17.
func TestBalanceChoosesEvenlyBetweenEqualSplits(t *testing.T) {
	weightings := make([]int, 18)
	for i := range weightings {
		weightings[i] = 3
	}
	players := squadOf(weightings...)

	const runs = 2000
	together := map[int64]int{}
	for i := 0; i < runs; i++ {
		split, err := Balance(players, rand.New(rand.NewPCG(uint64(i), 11)))
		if err != nil {
			t.Fatal(err)
		}
		side := split.A
		if !has(side, 1) {
			side = split.B
		}
		for _, p := range side {
			together[p.ID]++
		}
	}

	want := float64(runs) * 8 / 17
	for id := int64(2); id <= 18; id++ {
		if got := float64(together[id]); math.Abs(got-want) > 0.12*want {
			t.Errorf("player %d was with player 1 in %.0f of %d runs, want about %.0f", id, got, runs, want)
		}
	}
}

func TestBalanceSplitsDedicatedKeepers(t *testing.T) {
	players := squadOf(3, 3, 3, 3, 3, 3, 3, 3)
	players[0].Keeper = true
	players[1].Keeper = true

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

// Splitting the keepers comes before evening the totals, however lopsided
// that leaves things.
func TestBalanceSplitsKeepersEvenWhenItCostsBalance(t *testing.T) {
	players := squadOf(5, 5, 1, 1, 1, 1)
	players[2].Keeper = true
	players[3].Keeper = true
	players[4].Keeper = true
	players[5].Keeper = true

	split, err := Balance(players, testRand())
	if err != nil {
		t.Fatal(err)
	}
	if countKeepers(split.A) != 2 || countKeepers(split.B) != 2 {
		t.Errorf("four keepers split %d and %d", countKeepers(split.A), countKeepers(split.B))
	}
}

func TestBalanceSplitsThreeKeepersAsEvenlyAsItCan(t *testing.T) {
	players := squadOf(3, 3, 3, 3, 3, 3, 3, 3)
	for i := 0; i < 3; i++ {
		players[i].Keeper = true
	}
	split, err := Balance(players, testRand())
	if err != nil {
		t.Fatal(err)
	}
	if d := abs(countKeepers(split.A) - countKeepers(split.B)); d != 1 {
		t.Errorf("three keepers split %d and %d", countKeepers(split.A), countKeepers(split.B))
	}
}

// One dedicated keeper is nothing to spread, so it should not push the split
// around at all.
func TestBalanceWithOneKeeperStillEvensTheTotals(t *testing.T) {
	players := squadOf(5, 1, 2, 2, 4, 4)
	players[0].Keeper = true
	split, err := Balance(players, testRand())
	if err != nil {
		t.Fatal(err)
	}
	if g := gap(split); g != 0 {
		t.Errorf("totals of %d and %d, an exactly even split was available", total(split.A), total(split.B))
	}
}

func TestBalanceVariesBetweenRuns(t *testing.T) {
	// Lots of identical players means lots of equally good splits, and the
	// same one should not come back every time. Identical output would let
	// people work out the weightings by watching week to week.
	players := squadOf(2, 2, 3, 3, 3, 3, 4, 4, 3, 3)
	seen := map[string]bool{}
	for i := 0; i < 15; i++ {
		split, err := Balance(players, rand.New(rand.NewPCG(uint64(i), 7)))
		if err != nil {
			t.Fatal(err)
		}
		side := split.A
		if !has(side, 1) {
			side = split.B
		}
		key := ""
		for _, p := range side {
			key += fmt.Sprintf("%d,", p.ID)
		}
		seen[key] = true
	}
	if len(seen) < 3 {
		t.Errorf("only %d distinct splits across 15 runs", len(seen))
	}
}

func TestBalanceHeuristicForBigSquads(t *testing.T) {
	for _, n := range []int{30, 31} {
		weightings := make([]int, n)
		for i := range weightings {
			weightings[i] = i%MaxWeighting + 1
		}
		players := squadOf(weightings...)
		players[0].Keeper = true
		players[1].Keeper = true

		split, err := Balance(players, testRand())
		if err != nil {
			t.Fatal(err)
		}
		if len(split.A)+len(split.B) != n {
			t.Fatalf("n=%d: lost players: %d + %d", n, len(split.A), len(split.B))
		}
		if diff := abs(len(split.A) - len(split.B)); diff != n%2 {
			t.Errorf("n=%d: sides of %d and %d", n, len(split.A), len(split.B))
		}
		if countKeepers(split.A) != 1 || countKeepers(split.B) != 1 {
			t.Errorf("n=%d: keepers split %d and %d", n, countKeepers(split.A), countKeepers(split.B))
		}
		// The totals differ by an even number when the grand total is even
		// and an odd one when it is odd, so 0 or 1 is as close as it gets.
		if g := gap(split); g != total(players)%2 {
			t.Errorf("n=%d: totals of %d and %d", n, total(split.A), total(split.B))
		}
	}
}

func TestBalanceTooManyPlayers(t *testing.T) {
	weightings := make([]int, 65)
	for i := range weightings {
		weightings[i] = 3
	}
	if _, err := Balance(squadOf(weightings...), testRand()); err == nil {
		t.Fatal("expected an error above 64 players")
	}
}

func countKeepers(players []Player) int {
	n := 0
	for _, p := range players {
		if p.Keeper {
			n++
		}
	}
	return n
}
