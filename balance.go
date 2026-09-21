package main

import (
	"errors"
	"math"
	"math/rand/v2"
)

// AllPositions are the positions a player can be given. A player has one of
// them, or none at all, which means put them anywhere.
//
// GK is different from the rest. We share turns in goal, so most people are
// outfield players and GK is reserved for someone who only ever goes in goal.
// The balancer treats that as a thing to spread out rather than a shape to
// even up.
var AllPositions = []string{"GK", "DEF", "MID", "ATT"}

// OutfieldPositions are the ones whose spread across the two sides is worth
// evening out.
var OutfieldPositions = []string{"DEF", "MID", "ATT"}

// PositionLabel is how a position reads on screen.
func PositionLabel(pos string) string {
	switch pos {
	case "GK":
		return "In goal only"
	case "DEF":
		return "Defence"
	case "MID":
		return "Midfield"
	case "ATT":
		return "Attack"
	default:
		return "Anywhere"
	}
}

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
	// Position is one of AllPositions, or empty for someone who will play
	// anywhere. Somebody good enough to be weighted highly in one position is
	// usually fine in the others, so this is about the shape of the side
	// rather than about ability.
	Position string
	Notes    string
}

// PublicPlayer is what anyone is allowed to see: a name and where they play.
type PublicPlayer struct {
	Name     string
	Position string
}

func (p Player) Public() PublicPlayer {
	return PublicPlayer{Name: p.Name, Position: p.Position}
}

// IsKeeper reports whether this is somebody who only ever goes in goal, as
// opposed to the rest of us taking a turn.
func (p Player) IsKeeper() bool { return p.Position == "GK" }

// PositionLabel is the on-screen wording for this player's position.
func (p Player) PositionLabel() string { return PositionLabel(p.Position) }

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

// Weights for the cost function. Everything is expressed in hundredths of a
// weighting point so the whole thing stays integer.
const (
	positionPenalty = 30   // 0.3 of a weighting point per position out of step
	keeperPenalty   = 5000 // both dedicated keepers on one side spoils the game
	costSlack       = 15   // splits within 0.15 of a point of the best are all fair game
	maxCandidates   = 4096
	maxExhaustive   = 24
)

// Split is one way of dividing the players into two sides.
type Split struct {
	A    []Player
	B    []Player
	Cost int
}

// Balance divides players into two sides of as equal a size as possible,
// evening out the total weighting and the spread of positions.
//
// It does not always return the single lowest-cost split. It picks at random
// from all the splits that are within costSlack of the best one. That keeps
// the sides from being identical week after week, and it means nobody can work
// out anyone's weighting by watching which splits keep coming up.
func Balance(players []Player, rnd *rand.Rand) (Split, error) {
	n := len(players)
	if n < 4 {
		return Split{}, errors.New("need at least 4 players to make two sides")
	}
	if n > 64 {
		return Split{}, errors.New("more than 64 players is beyond what this handles")
	}

	var cands []candidate
	if n <= maxExhaustive {
		cands = searchExhaustive(players, rnd)
	} else {
		cands = searchHeuristic(players, rnd)
	}
	if len(cands) == 0 {
		return Split{}, errors.New("no valid split found")
	}

	pick := cands[rnd.IntN(len(cands))]
	return buildSplit(players, pick.mask, pick.cost), nil
}

type candidate struct {
	mask uint64
	cost int
}

// searchExhaustive walks every possible even split. Player 0 is pinned to side
// A, which halves the work by ignoring the mirror image of each split. For 22
// players that is 352,716 combinations, which takes a few milliseconds.
func searchExhaustive(players []Player, rnd *rand.Rand) []candidate {
	n := len(players)
	sizeA := n / 2

	best := math.MaxInt
	var cands []candidate

	add := func(mask uint64, cost int) {
		if cost < best {
			best = cost
			kept := cands[:0]
			for _, c := range cands {
				if c.cost <= best+costSlack {
					kept = append(kept, c)
				}
			}
			cands = kept
		}
		if cost > best+costSlack {
			return
		}
		if len(cands) < maxCandidates {
			cands = append(cands, candidate{mask, cost})
		} else if rnd.IntN(2) == 0 {
			cands[rnd.IntN(len(cands))] = candidate{mask, cost}
		}
	}

	// Choose sizeA-1 of the remaining n-1 players to join player 0.
	rest := n - 1
	want := sizeA - 1
	if want < 0 {
		return nil
	}
	limit := uint64(1) << uint(rest)
	for sub := (uint64(1) << uint(want)) - 1; sub < limit; sub = nextCombination(sub) {
		mask := (sub << 1) | 1 // shift up to leave room for the pinned player 0
		add(mask, evaluate(players, mask))
	}
	return cands
}

// nextCombination returns the next integer with the same number of set bits
// (Gosper's hack), so we only visit masks of the right size.
func nextCombination(v uint64) uint64 {
	if v == 0 {
		return 0
	}
	lowest := v & -v
	ripple := v + lowest
	ones := v ^ ripple
	ones = (ones >> 2) / lowest
	return ripple | ones
}

// searchHeuristic is the fallback for squads too big to enumerate: start from
// random even splits and keep swapping pairs while that helps.
func searchHeuristic(players []Player, rnd *rand.Rand) []candidate {
	n := len(players)
	sizeA := n / 2
	const restarts = 200

	best := math.MaxInt
	var cands []candidate

	for r := 0; r < restarts; r++ {
		order := rnd.Perm(n)
		var mask uint64
		for _, idx := range order[:sizeA] {
			mask |= 1 << uint(idx)
		}
		cost := evaluate(players, mask)

		improved := true
		for improved {
			improved = false
			for i := 0; i < n; i++ {
				for j := 0; j < n; j++ {
					if mask&(1<<uint(i)) == 0 || mask&(1<<uint(j)) != 0 {
						continue // need i on side A and j on side B
					}
					trial := mask&^(1<<uint(i)) | (1 << uint(j))
					if c := evaluate(players, trial); c < cost {
						mask, cost, improved = trial, c, true
					}
				}
			}
		}

		if cost < best {
			best = cost
			kept := cands[:0]
			for _, c := range cands {
				if c.cost <= best+costSlack {
					kept = append(kept, c)
				}
			}
			cands = kept
		}
		if cost <= best+costSlack {
			cands = append(cands, candidate{mask, cost})
		}
	}
	return cands
}

// evaluate scores one split. Lower is more even. Bits set in mask are side A.
func evaluate(players []Player, mask uint64) int {
	var sumA, sumB, countA, countB int
	var keepersA, keepersB, keepersTotal int
	posA := map[string]int{}
	posB := map[string]int{}

	for i, p := range players {
		onA := mask&(1<<uint(i)) != 0
		if p.IsKeeper() {
			keepersTotal++
		}
		if onA {
			sumA += p.Weighting
			countA++
			posA[p.Position]++
			if p.IsKeeper() {
				keepersA++
			}
		} else {
			sumB += p.Weighting
			countB++
			posB[p.Position]++
			if p.IsKeeper() {
				keepersB++
			}
		}
	}

	if countA == 0 || countB == 0 {
		return math.MaxInt / 2
	}

	// Compare averages rather than totals so an odd number of players, where
	// one side has an extra body, is still judged fairly.
	avgA := float64(sumA) / float64(countA)
	avgB := float64(sumB) / float64(countB)
	cost := int(math.Round(math.Abs(avgA-avgB) * 100))

	for _, pos := range OutfieldPositions {
		cost += abs(posA[pos]-posB[pos]) * positionPenalty
	}

	// Spread the people who only play in goal. With one or none of them there
	// is nothing to spread, because the rest of us take turns.
	if keepersTotal >= 2 {
		cost += abs(keepersA-keepersB) * keeperPenalty
	}

	return cost
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
