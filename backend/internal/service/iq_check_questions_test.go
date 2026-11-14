//go:build unit

package service

import (
	"fmt"
	mathrand "math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIQCheckQuestionsAreFreshAndCoverBothFamilies(t *testing.T) {
	questions, err := generateIQCheckQuestions(strings.NewReader("abcdefghABCDEFGH"))
	require.NoError(t, err)
	require.Len(t, questions, 2)
	require.Equal(t, "paths", questions[0].Family)
	require.IsType(t, iqPathAnswer{}, questions[0].Expected)
	require.Equal(t, "weighted_xor", questions[1].Family)
	require.IsType(t, iqXORAnswer{}, questions[1].Expected)
	for _, question := range questions {
		require.Contains(t, question.Prompt, "只输出JSON对象")
	}
	other, err := generateIQCheckQuestions(strings.NewReader("ijklmnopIJKLMNOP"))
	require.NoError(t, err)
	require.NotEqual(t, questions[0].Prompt, other[0].Prompt)
	_, err = generateIQCheckQuestions(strings.NewReader("short"))
	require.ErrorContains(t, err, "entropy unavailable")
}

func TestGradeIQCheckAnswer(t *testing.T) {
	path := iqCheckQuestion{Expected: iqPathAnswer{Count: 12, First: "RRURU"}}
	xor := iqCheckQuestion{Expected: iqXORAnswer{Penalty: 3, Assignments: []string{"001011", "110100"}}}
	cases := []struct {
		name     string
		question iqCheckQuestion
		output   string
		want     bool
	}{
		{"path exact", path, `{"count":12,"first":"RRURU"}`, true},
		{"path in code block with text", path, "答案如下\n```json\n{\"count\": 12, \"first\": \"RRURU\"}\n```", true},
		{"path wrong count", path, `{"count":11,"first":"RRURU"}`, false},
		{"path wrong first", path, `{"count":12,"first":"RRUUR"}`, false},
		{"path missing count", path, `{"first":"RRURU"}`, false},
		{"path count as string", path, `{"count":"12","first":"RRURU"}`, false},
		{"xor unordered assignments", xor, `{"penalty":3,"assignments":["110100","001011"]}`, true},
		{"xor missing assignment", xor, `{"penalty":3,"assignments":["001011"]}`, false},
		{"xor wrong penalty", xor, `{"penalty":2,"assignments":["001011","110100"]}`, false},
		{"braces in the explanation", path, "思路：动作集合{R,U}，按{转向}计数。\n{\"count\":12,\"first\":\"RRURU\"}", true},
		{"last complete object wins", path, `格式 {"count":0,"first":""}，答案 {"count":12,"first":"RRURU"}`, true},
		{"trailing object without fields is ignored", path, `{"count":12,"first":"RRURU"} {"note":"done"}`, true},
		{"xor after braces", xor, `{penalty} = 3: {"penalty":3,"assignments":["001011","110100"]}`, true},
		{"not json", path, "12, RRURU", false},
		{"empty", xor, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, gradeIQCheckAnswer(tc.question, tc.output))
		})
	}
}

// Compare the state-counting solver against a separate whole-walk enumeration
// on a small synthetic grid.
func TestIQPathSolverAgainstWalkEnumeration(t *testing.T) {
	for turns := 0; turns < 8; turns++ {
		p := iqPathProblem{Width: 4, Height: 3, Turns: turns,
			Blocked:     [3]iqGridPoint{{1, 1}, {2, 2}, {3, 0}},
			Checkpoints: [2]iqGridPoint{{1, 0}, {0, 2}}}
		expected := iqPathAnswer{}
		for mask := 0; mask < 1<<7; mask++ {
			x, y, hits, changes := 0, 0, 0, 0
			var word strings.Builder
			valid := true
			for i := 0; i < 7; i++ {
				if mask>>i&1 != 0 {
					x++
					_ = word.WriteByte('R')
				} else {
					y++
					_ = word.WriteByte('U')
				}
				for _, point := range p.Blocked {
					if point == (iqGridPoint{x, y}) {
						valid = false
					}
				}
				for _, point := range p.Checkpoints {
					if point == (iqGridPoint{x, y}) {
						hits++
					}
				}
			}
			path := word.String()
			for i := 1; i < len(path); i++ {
				if path[i] != path[i-1] {
					changes++
				}
			}
			if valid && x == p.Width && y == p.Height && hits == 1 && changes == turns && !strings.Contains(path, "RRR") {
				expected.Count++
				if expected.First == "" || path < expected.First {
					expected.First = path
				}
			}
		}
		require.Equal(t, expected, solveIQPath(p))
	}
}

func TestIQXORSolverFindsAllMinimumWeightRepairs(t *testing.T) {
	rows := []iqXORObservation{{[]int{0}, 0, 2}, {[]int{0}, 1, 5}, {[]int{1}, 0, 3}}
	answer := solveIQXOR(rows)
	require.Equal(t, 2, answer.Penalty)
	var expected []string
	for i := 0; i < 16; i++ {
		expected = append(expected, fmt.Sprintf("10%04b", i))
	}
	require.Equal(t, expected, answer.Assignments)
	rng := mathrand.New(mathrand.NewPCG(7, 11)) //nolint:gosec
	for i := 0; i < 8; i++ {
		_, reference, err := generateIQXORQuestion(rng)
		require.NoError(t, err)
		require.Positive(t, reference.Penalty)
		require.NotEmpty(t, reference.Assignments)
		require.LessOrEqual(t, len(reference.Assignments), 4)
	}
}
