package main

import (
	"errors"
	"math"
	"math/bits"
	"math/rand/v2"
)

// KeeperLabel is what goes next to the name of somebody who only ever goes in
// goal.
const KeeperLabel = "GK"

// Player is the full record, including the weighting. Anything that is served
// to a non-admin request must use PublicPlayer instead, so a weighting cannot
// reach a page by accident.
type Player struct {
	ID        int64
	Name      string
	Active    bool
	Weighting int // MinWeighting to MaxWeighting, only used to even the sides up
	// Provisional marks a weighting that was set in a hurry, usually because
	// someone turned up and got added on the spot. It changes nothing about
	// the split, it is just a reminder to go back and look properly.
	Provisional bool
	// Keeper marks somebody who only ever goes in goal. We share turns in
	// goal, so this is for the handful of people who never play out. The
	// balancer spreads them across the two sides.
	Keeper bool
	Notes  string
}

// PublicPlayer is what anyone is allowed to see: a name, and whether they are
// one of the handful of people who only ever go in goal. That is not a
// judgement about them, it is the whole reason they turn up, and people want
// to know who is in goal before they get there.
type PublicPlayer struct {
	Name   string
	Keeper bool
}

func (p Player) Public() PublicPlayer {
	return PublicPlayer{Name: p.Name, Keeper: p.Keeper}
}

// The weighting scale. It is deliberately short: picking one of five is a
// quicker judgement to make than picking one of ten, and more people sharing
// the same weighting means more equally even splits to choose between.
const (
	MinWeighting     = 1
	MaxWeighting     = 5
	DefaultWeighting = 3
)

// ClampWeighting pulls a weighting back onto the scale.
func ClampWeighting(w int) int {
	if w < MinWeighting {
		return MinWeighting
	}
	if w > MaxWeighting {
		return MaxWeighting
	}
	return w
}

const (
	// keeperPenalty is what it costs, per keeper out of step, to have the
	// dedicated keepers bunched on one side. It is bigger than any gap in
	// total weighting can be (64 players at MaxWeighting), so spreading the
	// keepers always comes first.
	keeperPenalty = 1000
	maxPlayers    = 64
	maxExhaustive = 24
)

// Split is one way of dividing the players into two sides.
type Split struct {
	A    []Player
	B    []Player
	Cost int
}

// Balance divides players into two sides of as equal a size as possible, with
// the total weighting of each side as close as it can get.
//
// It is the totals that are compared, not the averages. With an odd number of
// players one side has an extra body, and that body is an advantage. Matching
// the totals means the smaller side gets the stronger players to make up for
// it, where matching the averages would hand the bigger side a free player.
//
// Usually many splits are equally even. Balance picks one of them at random,
// each as likely as the next. That keeps the sides from being identical week
// after week, and it means nobody can work out anyone's weighting by watching
// which splits keep coming up. Which of the two sides gets the extra player
// is random as well.
func Balance(players []Player, rnd *rand.Rand) (Split, error) {
	n := len(players)
	if n < 4 {
		return Split{}, errors.New("need at least 4 players to make two sides")
	}
	if n > maxPlayers {
		return Split{}, errors.New("more than 64 players is beyond what this handles")
	}

	sq := newSquad(players)

	var mask uint64
	var cost int
	if n <= maxExhaustive {
		mask, cost = searchExhaustive(sq, rnd)
	} else {
		mask, cost = searchHeuristic(sq, rnd)
	}

	// The searches only decide who plays with whom. Which group is called A
	// is a coin toss, otherwise the first name on the list would always be on
	// the same side and the extra player would always go to the other one.
	if rnd.IntN(2) == 0 {
		mask = ^mask & sq.all
	}
	return buildSplit(players, mask, cost), nil
}

// squad is the players boiled down to what the search needs, so scoring a
// split is a few additions rather than a walk over the full records.
type squad struct {
	n           int
	weights     []int
	total       int
	keepers     uint64 // bit i set if player i only goes in goal
	keeperCount int
	all         uint64 // a bit set for every player
}

func newSquad(players []Player) squad {
	sq := squad{n: len(players), weights: make([]int, len(players))}
	for i, p := range players {
		sq.weights[i] = p.Weighting
		sq.total += p.Weighting
		sq.all |= 1 << uint(i)
		if p.Keeper {
			sq.keepers |= 1 << uint(i)
			sq.keeperCount++
		}
	}
	return sq
}

// cost scores one split. Lower is more even, and nought is two sides with the
// same total. Bits set in mask are one side, everyone else is the other.
func (sq squad) cost(mask uint64) int {
	sum := 0
	for m := mask; m != 0; m &= m - 1 {
		sum += sq.weights[bits.TrailingZeros64(m)]
	}
	cost := abs(2*sum - sq.total) // this side's total less the other side's

	// Spread the people who only play in goal. With one or none of them there
	// is nothing to spread, because the rest of us take turns.
	if sq.keeperCount >= 2 {
		mine := bits.OnesCount64(mask & sq.keepers)
		cost += abs(2*mine-sq.keeperCount) * keeperPenalty
	}
	return cost
}

// chooser keeps one split out of all the equally good ones it is shown, each
// with the same chance, without having to remember them all.
type chooser struct {
	rnd  *rand.Rand
	best int
	seen int
	mask uint64
}

func newChooser(rnd *rand.Rand) chooser {
	return chooser{rnd: rnd, best: math.MaxInt}
}

func (c *chooser) offer(mask uint64, cost int) {
	switch {
	case cost < c.best:
		c.best, c.seen, c.mask = cost, 1, mask
	case cost == c.best:
		c.seen++
		if c.rnd.IntN(c.seen) == 0 {
			c.mask = mask
		}
	}
}

// searchExhaustive walks every possible split and picks one of the best.
//
// With an even number of players, every split turns up twice, once as each
// side, so player 0 is pinned to the side being chosen and half the work is
// skipped. With an odd number the two sides are different sizes and there is
// no mirror image to skip: the smaller side is chosen from everybody.
//
// The most work is at 23 and 24 players, 1,352,078 splits either way.
func searchExhaustive(sq squad, rnd *rand.Rand) (uint64, int) {
	pick := newChooser(rnd)
	size := sq.n / 2

	if sq.n%2 == 0 {
		// Choose size-1 of the other n-1 players to join player 0.
		limit := uint64(1) << uint(sq.n-1)
		for sub := (uint64(1) << uint(size-1)) - 1; sub < limit; sub = nextCombination(sub) {
			mask := sub<<1 | 1
			pick.offer(mask, sq.cost(mask))
		}
	} else {
		limit := uint64(1) << uint(sq.n)
		for mask := (uint64(1) << uint(size)) - 1; mask < limit; mask = nextCombination(mask) {
			pick.offer(mask, sq.cost(mask))
		}
	}
	return pick.mask, pick.best
}

// nextCombination returns the next integer with the same number of set bits
// (Gosper's hack), so we only visit masks of the right size.
func nextCombination(v uint64) uint64 {
	if v == 0 {
		return math.MaxUint64 // nothing to permute, so stop the caller's loop
	}
	lowest := v & -v
	ripple := v + lowest
	ones := v ^ ripple
	ones = (ones >> 2) / lowest
	return ripple | ones
}

// searchHeuristic is the fallback for squads too big to enumerate: start from
// random splits and keep swapping pairs while that helps, then pick one of the
// best finishes. It is not guaranteed to find the most even split there is,
// and it does not choose evenly between the ones it does find.
func searchHeuristic(sq squad, rnd *rand.Rand) (uint64, int) {
	const restarts = 200
	pick := newChooser(rnd)
	size := sq.n / 2

	for r := 0; r < restarts; r++ {
		var mask uint64
		for _, idx := range rnd.Perm(sq.n)[:size] {
			mask |= 1 << uint(idx)
		}
		cost := sq.cost(mask)

		improved := true
		for improved {
			improved = false
			for i := 0; i < sq.n; i++ {
				for j := 0; j < sq.n; j++ {
					if mask&(1<<uint(i)) == 0 || mask&(1<<uint(j)) != 0 {
						continue // need i in the group and j out of it
					}
					trial := mask&^(1<<uint(i)) | (1 << uint(j))
					if c := sq.cost(trial); c < cost {
						mask, cost, improved = trial, c, true
					}
				}
			}
		}
		pick.offer(mask, cost)
	}
	return pick.mask, pick.best
}

func buildSplit(players []Player, mask uint64, cost int) Split {
	s := Split{Cost: cost}
	for i, p := range players {
		if mask&(1<<uint(i)) != 0 {
			s.A = append(s.A, p)
		} else {
			s.B = append(s.B, p)
		}
	}
	return s
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
