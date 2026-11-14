package service

// Question generators and solvers are adapted from ranxi2001/sub2api@f895ea743
// (backend/internal/service/quality_random_question.go). Answers are graded
// locally against the solver, so no judge model is involved.

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"slices"
	"strings"
)

// iqCheckQuestion is one generated question with its reference answer.
type iqCheckQuestion struct {
	Family   string
	Prompt   string
	Expected any // iqPathAnswer or iqXORAnswer
}

// newIQCheckQuestions returns one fresh question of each family.
func newIQCheckQuestions() ([]iqCheckQuestion, error) {
	return generateIQCheckQuestions(rand.Reader)
}

// The source is passed explicitly so entropy failures are testable.
func generateIQCheckQuestions(source io.Reader) ([]iqCheckQuestion, error) {
	var seed [16]byte
	if _, err := io.ReadFull(source, seed[:]); err != nil {
		return nil, fmt.Errorf("iq check question entropy unavailable")
	}
	// The PRNG expands a fresh crypto/rand seed; it is not a credential generator.
	rng := mathrand.New(mathrand.NewPCG(binary.LittleEndian.Uint64(seed[:8]), binary.LittleEndian.Uint64(seed[8:]))) //nolint:gosec
	pathPrompt, pathAnswer, err := generateIQPathQuestion(rng)
	if err != nil {
		return nil, err
	}
	xorPrompt, xorAnswer, err := generateIQXORQuestion(rng)
	if err != nil {
		return nil, err
	}
	return []iqCheckQuestion{
		{Family: "paths", Prompt: pathPrompt, Expected: pathAnswer},
		{Family: "weighted_xor", Prompt: xorPrompt, Expected: xorAnswer},
	}, nil
}

// gradeIQCheckAnswer reports whether the model output matches the reference.
// The answer is the last JSON object in the output that has the required
// fields, so stray braces in surrounding text do not spoil it. Output without
// such an object counts as wrong: the question asks for JSON only.
func gradeIQCheckAnswer(question iqCheckQuestion, output string) bool {
	switch expected := question.Expected.(type) {
	case iqPathAnswer:
		var answer struct {
			Count *int   `json:"count"`
			First string `json:"first"`
		}
		if !lastIQCheckObject(output, func(raw json.RawMessage) bool {
			answer.Count, answer.First = nil, ""
			return json.Unmarshal(raw, &answer) == nil && answer.Count != nil
		}) {
			return false
		}
		return *answer.Count == expected.Count && strings.TrimSpace(answer.First) == expected.First
	case iqXORAnswer:
		var answer struct {
			Penalty     *int     `json:"penalty"`
			Assignments []string `json:"assignments"`
		}
		if !lastIQCheckObject(output, func(raw json.RawMessage) bool {
			answer.Penalty, answer.Assignments = nil, nil
			return json.Unmarshal(raw, &answer) == nil && answer.Penalty != nil && answer.Assignments != nil
		}) {
			return false
		}
		assignments := make([]string, len(answer.Assignments))
		for i, value := range answer.Assignments {
			assignments[i] = strings.TrimSpace(value)
		}
		slices.Sort(assignments)
		return *answer.Penalty == expected.Penalty && slices.Equal(assignments, expected.Assignments)
	}
	return false
}

// lastIQCheckObject decodes a JSON value at every "{" in the output and leaves
// the last one that accept takes in place. It reports whether any was taken.
func lastIQCheckObject(output string, accept func(json.RawMessage) bool) bool {
	// Answers are short and come last; bound the scan on runaway output.
	const maxScan = 64 << 10
	if len(output) > maxScan {
		output = output[len(output)-maxScan:]
	}
	var last json.RawMessage
	for i := 0; i < len(output); i++ {
		if output[i] != '{' {
			continue
		}
		var raw json.RawMessage
		if json.NewDecoder(strings.NewReader(output[i:])).Decode(&raw) == nil && accept(raw) {
			last = raw
		}
	}
	return last != nil && accept(last)
}

type iqGridPoint struct{ X, Y int }

type iqPathProblem struct {
	Width, Height, Turns int
	Blocked              [3]iqGridPoint
	Checkpoints          [2]iqGridPoint
}

type iqPathAnswer struct {
	Count int    `json:"count"`
	First string `json:"first"`
}

type iqPathState struct{ X, Y, Last, Turns, Hits, Run int }

// State counting avoids materializing every path. All instances fit in a 7x7
// grid and at most 14 steps, so both time and integer arithmetic are bounded.
func solveIQPath(p iqPathProblem) iqPathAnswer {
	memo := make(map[iqPathState]int)
	blocked := func(x, y int) bool {
		for _, point := range p.Blocked {
			if point.X == x && point.Y == y {
				return true
			}
		}
		return false
	}
	next := func(state iqPathState, direction int) iqPathState {
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
	var count func(iqPathState) int
	count = func(state iqPathState) int {
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
	state := iqPathState{}
	answer := iqPathAnswer{Count: count(state)}
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

func generateIQPathQuestion(rng *mathrand.Rand) (string, iqPathAnswer, error) {
	for attempt := 0; attempt < 128; attempt++ {
		p := iqPathProblem{Width: 5 + rng.IntN(3), Height: 5 + rng.IntN(3), Turns: 4 + rng.IntN(4)}
		var points []iqGridPoint
		for x := 0; x <= p.Width; x++ {
			for y := 0; y <= p.Height; y++ {
				if (x != 0 || y != 0) && (x != p.Width || y != p.Height) {
					points = append(points, iqGridPoint{x, y})
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
		answer := solveIQPath(p)
		if answer.Count < 3 {
			continue
		}
		pointText := func(point iqGridPoint) string { return fmt.Sprintf("(%d,%d)", point.X, point.Y) }
		prompt := fmt.Sprintf("从格点(0,0)走到(%d,%d)，每步只能R=(1,0)或U=(0,1)。不能经过禁点%s、%s、%s；检查点%s、%s必须恰好经过一个。相邻动作字母不同时算一次转向，第一步不算，必须恰好转向%d次。动作串不得包含连续三个R。求合法动作串总数及字典序最小的动作串，规定R<U。只输出JSON对象，字段count为整数，first为动作串，不加解释或代码块。",
			p.Width, p.Height, pointText(p.Blocked[0]), pointText(p.Blocked[1]), pointText(p.Blocked[2]), pointText(p.Checkpoints[0]), pointText(p.Checkpoints[1]), p.Turns)
		return prompt, answer, nil
	}
	return "", iqPathAnswer{}, fmt.Errorf("iq check path question unavailable")
}

type iqXORObservation struct {
	Indices []int
	Value   int
	Weight  int
}

type iqXORAnswer struct {
	Penalty     int      `json:"penalty"`
	Assignments []string `json:"assignments"`
}

// Assignments are produced in ascending lexicographic order.
func solveIQXOR(rows []iqXORObservation) iqXORAnswer {
	answer := iqXORAnswer{Penalty: int(^uint(0) >> 1)}
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

func generateIQXORQuestion(rng *mathrand.Rand) (string, iqXORAnswer, error) {
	indices := [][]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}, {4, 5}, {0, 5}, {0, 2, 4}, {1, 3, 5}, {0, 3, 4}, {1, 2, 5}, {0, 1, 5}}
	for attempt := 0; attempt < 128; attempt++ {
		truth := rng.IntN(64)
		order := rng.Perm(len(indices))
		corrupt := map[int]bool{order[0]: true, order[1]: true, order[2]: true}
		rows := make([]iqXORObservation, len(indices))
		for i, members := range indices {
			value := 0
			for _, index := range members {
				value ^= truth >> index & 1
			}
			if corrupt[i] {
				value ^= 1
			}
			rows[i] = iqXORObservation{Indices: members, Value: value, Weight: 1 + rng.IntN(6)}
		}
		answer := solveIQXOR(rows)
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
	return "", iqXORAnswer{}, fmt.Errorf("iq check xor question unavailable")
}
