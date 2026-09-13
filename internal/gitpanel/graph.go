package gitpanel

import "strings"

// maxLanes caps the rail width so the graph stays readable in a narrow
// sidebar; excess lanes beyond the cap collapse onto the last column.
const maxLanes = 8

// graphRow is the rail rendered to the left of a commit. Rail is a string of
// lane glyphs; Lanes is the lane count at that row (for width accounting).
type graphRow struct {
	Rail  string
	Lanes int
}

// buildGraph assigns lanes over the commits' parent links and returns one
// rail string per commit, using ● for the node, │ for a continuing lane,
// ╮/╭ where the commit forks a new lane for an extra parent, and ╯/╰ where a
// lane converges back into the commit. Commits are expected newest-first, as
// produced by `git log`.
//
// The algorithm tracks a set of "lanes", each waiting for a specific commit
// hash (a child already claimed it as a parent). Processing a commit:
//  1. its lane is the leftmost lane waiting for it, or a fresh lane if none;
//  2. any other lanes waiting for it converge in (a merge);
//  3. its first parent continues in its lane, and each extra parent (of a
//     merge commit) opens a new lane (a fork).
func buildGraph(commits []Commit) []graphRow {
	rows := make([]graphRow, len(commits))
	var lanes []string // Hash each lane waits for; "" means free.

	for i, c := range commits {
		commitLane := indexOf(lanes, c.Hash)
		if commitLane < 0 {
			commitLane = allocLane(&lanes)
		}
		// Lanes (other than the commit's own) that resolve at this commit.
		var merges []int
		for j, h := range lanes {
			if j != commitLane && h == c.Hash {
				merges = append(merges, j)
			}
		}

		// Continue the first parent in the commit lane; free the merged
		// lanes; open a new lane for each additional parent.
		var forks []int
		if len(c.Parents) == 0 {
			lanes[commitLane] = ""
		} else {
			lanes[commitLane] = c.Parents[0]
			for _, p := range c.Parents[1:] {
				fl := allocLane(&lanes)
				lanes[fl] = p
				forks = append(forks, fl)
			}
		}
		for _, m := range merges {
			lanes[m] = ""
		}

		width := len(lanes)
		rows[i] = graphRow{
			Rail:  renderRail(lanes, commitLane, merges, forks, width),
			Lanes: width,
		}
		trimLanes(&lanes)
	}
	return rows
}

// renderRail draws a single rail line. lanes reflects the post-update state
// (parents already assigned), so a still-active lane column is a pass-through
// │. commitLane carries the node; merges and forks carry converging and
// diverging diagonals.
func renderRail(lanes []string, commitLane int, merges, forks []int, width int) string {
	cols := make([]rune, width)
	for j := range width {
		switch {
		case j == commitLane:
			cols[j] = '●'
		case containsInt(merges, j):
			if j > commitLane {
				cols[j] = '╯'
			} else {
				cols[j] = '╰'
			}
		case containsInt(forks, j):
			if j > commitLane {
				cols[j] = '╮'
			} else {
				cols[j] = '╭'
			}
		case lanes[j] != "":
			cols[j] = '│'
		default:
			cols[j] = ' '
		}
	}
	// Bridge the node to each diagonal with horizontal rails so a merge/fork
	// two or more columns away reads as connected.
	bridge(cols, commitLane, merges)
	bridge(cols, commitLane, forks)
	return strings.TrimRight(string(cols), " ")
}

// bridge fills the columns strictly between the node and each target with a
// horizontal rail, using ┼ where it must cross a live pass-through lane.
func bridge(cols []rune, from int, targets []int) {
	for _, to := range targets {
		lo, hi := from, to
		if lo > hi {
			lo, hi = hi, lo
		}
		for j := lo + 1; j < hi; j++ {
			switch cols[j] {
			case '│':
				cols[j] = '┼'
			case ' ':
				cols[j] = '─'
			}
		}
	}
}

// indexOf returns the first index in lanes holding h, or -1.
func indexOf(lanes []string, h string) int {
	for i, v := range lanes {
		if v == h {
			return i
		}
	}
	return -1
}

// allocLane returns a free lane index, reusing an empty slot when possible and
// otherwise appending (up to maxLanes, beyond which it reuses the last lane).
func allocLane(lanes *[]string) int {
	for i, v := range *lanes {
		if v == "" {
			return i
		}
	}
	if len(*lanes) >= maxLanes {
		return len(*lanes) - 1
	}
	*lanes = append(*lanes, "")
	return len(*lanes) - 1
}

// trimLanes drops trailing free lanes to keep rails compact.
func trimLanes(lanes *[]string) {
	s := *lanes
	for len(s) > 0 && s[len(s)-1] == "" {
		s = s[:len(s)-1]
	}
	*lanes = s
}

// containsInt reports whether xs contains x.
func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
