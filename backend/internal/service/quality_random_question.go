package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"strings"
)

const QualityRandomQuestionKind = "random_reasoning"
const qualityRandomGeneratorVersion = "1"

// Result-only metadata. Rules store no seed, fixed question or reference answer.
type QualityRandomQuestion struct {
	ID      string `json:"id"`
	Family  string `json:"family"`
	Version string `json:"version"`
}

func isQualityAnswerQuestion(kind string) bool {
	return kind == "candy" || kind == QualityRandomQuestionKind
}

// One fresh question per sample, shared by every model in that sample. A batch
// alternates families with a random starting family instead of repeating one
// fixed question. The source is passed explicitly so entropy failures are testable.
func generateQualityRandomSamples(cfg *PelicanTestConfig, source io.Reader) ([]*PelicanTestConfig, error) {
	var seed [16]byte
	if _, err := io.ReadFull(source, seed[:]); err != nil {
		return nil, fmt.Errorf("random question entropy unavailable")
	}
	// The PRNG expands a fresh crypto/rand seed; it is not a credential generator.
	rng := mathrand.New(mathrand.NewPCG(binary.LittleEndian.Uint64(seed[:8]), binary.LittleEndian.Uint64(seed[8:]))) //nolint:gosec
	first := rng.IntN(2)
	samples := make([]*PelicanTestConfig, cfg.ParallelCount)
	seen := make(map[string]bool, len(samples))
	for i := range samples {
		for attempt := 0; attempt < 32; attempt++ {
			var prompt, family string
			var answer any
			var err error
			if (first+i)%2 == 0 {
				family = "paths"
				prompt, answer, err = generateQualityPathQuestion(rng)
			} else {
				family = "weighted_xor"
				prompt, answer, err = generateQualityXORQuestion(rng)
			}
			if err != nil {
				return nil, err
			}
			digest := sha256.Sum256([]byte(prompt))
			id := fmt.Sprintf("%x", digest[:16])
			if seen[id] {
				continue
			}
			seen[id] = true
			reference, err := json.Marshal(answer)
			if err != nil {
				return nil, fmt.Errorf("random question reference unavailable")
			}
			snapshot := *cfg
			policy := *cfg.Quality
			policy.ExpectedAnswer = string(reference)
			snapshot.Quality = &policy
			snapshot.Prompt = prompt
			snapshot.RandomQuestion = &QualityRandomQuestion{ID: id, Family: family, Version: qualityRandomGeneratorVersion}
			samples[i] = &snapshot
			break
		}
		if samples[i] == nil {
			return nil, fmt.Errorf("random question batch unavailable")
		}
	}
	return samples, nil
}

type qualityGridPoint struct{ X, Y int }

type qualityPathProblem struct {
	Width, Height, Turns int
	Blocked              [3]qualityGridPoint
	Checkpoints          [2]qualityGridPoint
}

type qualityPathAnswer struct {
	Count int    `json:"count"`
	First string `json:"first"`
}

type qualityPathState struct{ X, Y, Last, Turns, Hits, Run int }

// State counting avoids materializing every path. All instances fit in a 7x7
// grid and at most 14 steps, so both time and integer arithmetic are bounded.
func solveQualityPath(p qualityPathProblem) qualityPathAnswer {
	memo := make(map[qualityPathState]int)
	blocked := func(x, y int) bool {
		for _, point := range p.Blocked {
			if point.X == x && point.Y == y {
				return true
			}
		}
		return false
	}
	next := func(state qualityPathState, direction int) qualityPathState {
		if direction == 1 {
			state.X++
			state.Run++
		} else {
			state.Y++
			state.Run = 0
		}
		if state.Last != 0 && state.Last != direction {
			state.Turns++
		}
		state.Last = direction
		for _, point := range p.Checkpoints {
			if point.X == state.X && point.Y == state.Y {
				state.Hits++
			}
		}
		return state
	}
	var count func(qualityPathState) int
	count = func(state qualityPathState) int {
		if state.X > p.Width || state.Y > p.Height || state.Turns > p.Turns || state.Hits > 1 || state.Run >= 3 || blocked(state.X, state.Y) {
			return 0
		}
		if state.X == p.Width && state.Y == p.Height {
			if state.Turns == p.Turns && state.Hits == 1 {
				return 1
			}
			return 0
		}
		if value, ok := memo[state]; ok {
			return value
		}
		value := count(next(state, 1)) + count(next(state, 2))
		memo[state] = value
		return value
	}
	state := qualityPathState{}
	answer := qualityPathAnswer{Count: count(state)}
	if answer.Count == 0 {
		return answer
	}
	var first strings.Builder
	for state.X != p.Width || state.Y != p.Height {
		if right := next(state, 1); count(right) > 0 {
			_ = first.WriteByte('R')
			state = right
		} else {
			_ = first.WriteByte('U')
			state = next(state, 2)
		}
	}
	answer.First = first.String()
	return answer
}

func generateQualityPathQuestion(rng *mathrand.Rand) (string, any, error) {
	for attempt := 0; attempt < 128; attempt++ {
		p := qualityPathProblem{Width: 5 + rng.IntN(3), Height: 5 + rng.IntN(3), Turns: 4 + rng.IntN(4)}
		var points []qualityGridPoint
		for x := 0; x <= p.Width; x++ {
			for y := 0; y <= p.Height; y++ {
				if (x != 0 || y != 0) && (x != p.Width || y != p.Height) {
					points = append(points, qualityGridPoint{x, y})
				}
			}
		}
		order := rng.Perm(len(points))
		for i := range p.Blocked {
			p.Blocked[i] = points[order[i]]
		}
		for i := range p.Checkpoints {
			p.Checkpoints[i] = points[order[i+3]]
		}
		answer := solveQualityPath(p)
		if answer.Count < 3 {
			continue
		}
		pointText := func(point qualityGridPoint) string { return fmt.Sprintf("(%d,%d)", point.X, point.Y) }
		prompt := fmt.Sprintf("从格点(0,0)走到(%d,%d)，每步只能R=(1,0)或U=(0,1)。不能经过禁点%s、%s、%s；检查点%s、%s必须恰好经过一个。相邻动作字母不同时算一次转向，第一步不算，必须恰好转向%d次。动作串不得包含连续三个R。求合法动作串总数及字典序最小的动作串，规定R<U。只输出JSON对象，字段count为整数，first为动作串，不加解释或代码块。",
			p.Width, p.Height, pointText(p.Blocked[0]), pointText(p.Blocked[1]), pointText(p.Blocked[2]), pointText(p.Checkpoints[0]), pointText(p.Checkpoints[1]), p.Turns)
		return prompt, answer, nil
	}
	return "", nil, fmt.Errorf("random path question unavailable")
}

type qualityXORObservation struct {
	Indices []int
	Value   int
	Weight  int
}

type qualityXORAnswer struct {
	Penalty     int      `json:"penalty"`
	Assignments []string `json:"assignments"`
}

func solveQualityXOR(rows []qualityXORObservation) qualityXORAnswer {
	answer := qualityXORAnswer{Penalty: int(^uint(0) >> 1)}
	for assignment := 0; assignment < 64; assignment++ {
		bits := make([]byte, 6)
		for i := range bits {
			bits[i] = '0' + byte(assignment>>(5-i)&1)
		}
		penalty := 0
		for _, row := range rows {
			value := 0
			for _, index := range row.Indices {
				value ^= int(bits[index] - '0')
			}
			if value != row.Value {
				penalty += row.Weight
			}
		}
		if penalty < answer.Penalty {
			answer.Penalty = penalty
			answer.Assignments = nil
		}
		if penalty == answer.Penalty {
			answer.Assignments = append(answer.Assignments, string(bits))
		}
	}
	return answer
}

func generateQualityXORQuestion(rng *mathrand.Rand) (string, any, error) {
	indices := [][]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}, {4, 5}, {0, 5}, {0, 2, 4}, {1, 3, 5}, {0, 3, 4}, {1, 2, 5}, {0, 1, 5}}
	for attempt := 0; attempt < 128; attempt++ {
		truth := rng.IntN(64)
		order := rng.Perm(len(indices))
		corrupt := map[int]bool{order[0]: true, order[1]: true, order[2]: true}
		rows := make([]qualityXORObservation, len(indices))
		for i, members := range indices {
			value := 0
			for _, index := range members {
				value ^= truth >> index & 1
			}
			if corrupt[i] {
				value ^= 1
			}
			rows[i] = qualityXORObservation{Indices: members, Value: value, Weight: 1 + rng.IntN(6)}
		}
		answer := solveQualityXOR(rows)
		if answer.Penalty == 0 || len(answer.Assignments) > 4 {
			continue
		}
		var prompt strings.Builder
		_, _ = prompt.WriteString("寄存器A,B,C,D,E,F各取0或1。以下观测可能有误，XOR为异或，即各位之和模2。总代价为所有不成立观测的违背代价之和；不限制错误条数，不是让错误条数最少。\n")
		for i, row := range rows {
			members := make([]string, len(row.Indices))
			for j, index := range row.Indices {
				members[j] = string(rune('A' + index))
			}
			fmt.Fprintf(&prompt, "%d. %s = %d；违背代价%d\n", i+1, strings.Join(members, " XOR "), row.Value, row.Weight)
		}
		_, _ = prompt.WriteString("求最小总代价及所有最优赋值。只输出JSON对象，字段penalty为整数，assignments为六位01字符串数组，从左至右对应ABCDEF，按字典序升序。不加解释或代码块。")
		return prompt.String(), answer, nil
	}
	return "", nil, fmt.Errorf("random XOR question unavailable")
}

func newQualityRandomSamples(cfg *PelicanTestConfig) ([]*PelicanTestConfig, error) {
	return generateQualityRandomSamples(cfg, rand.Reader)
}
