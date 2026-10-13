package driver

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Copies of a ZPL label are the printer's job: ^PQ in the label tells it how many to
// print. Before CPS sets ^PQ from a request's `copies`, it reads what the payload
// already says, because guessing wrong prints the wrong number of labels.

var (
	// ErrCopiesConflict is a label that already sets a quantity above one, sent with
	// copies above one: 3 copies of "^PQ3" could mean 3 or 9 labels.
	ErrCopiesConflict = errors.New("the label already sets its own quantity (^PQ)")
	// ErrCopiesUnreadable is ZPL whose labels CPS cannot find reliably.
	ErrCopiesUnreadable = errors.New("cannot apply copies to this ZPL")

	// Commands that put something on the label. A format block with none of them is
	// setup (^XA^JUS^XZ) and must not be multiplied.
	reMarks = regexp.MustCompile(`(?i)\^(FD|GF|GB|GC|GD|GE|XG|IM)`)
	// ^CC/~CC change the command prefix; after one, "^" no longer means what it did.
	rePrefixChange = regexp.MustCompile(`(?i)[\^~]CC`)
)

// ZPLLabel is one printable ^XA…^XZ block and the quantity it asks for.
type ZPLLabel struct {
	Index      int // position among all blocks
	Start, End int // the block's bytes in the payload
	Quantity   int // ^PQ quantity; 1 when absent
	HasPQ      bool
}

var reXA = regexp.MustCompile(`(?i)[\^~]XA`)

// zplBlocks finds each ^XA…^XZ block in place, so the bytes between and around them
// (downloads, setup commands, line breaks) are never touched.
func zplBlocks(zpl string) [][2]int {
	var spans [][2]int
	for cursor := 0; cursor < len(zpl); {
		a := reXA.FindStringIndex(zpl[cursor:])
		if a == nil {
			break
		}
		start := cursor + a[0]
		z := reXZ.FindStringIndex(zpl[start+3:])
		if z == nil {
			break // an unclosed block is not a label
		}
		end := start + 3 + z[1]
		spans = append(spans, [2]int{start, end})
		cursor = end
	}
	return spans
}

// AnalyzeZPL lists the printable labels in a ZPL stream.
func AnalyzeZPL(zpl string) ([]ZPLLabel, error) {
	if rePrefixChange.MatchString(zpl) {
		return nil, fmt.Errorf("%w: it changes the command prefix (^CC)", ErrCopiesUnreadable)
	}
	var labels []ZPLLabel
	for i, span := range zplBlocks(zpl) {
		block := zpl[span[0]:span[1]]
		if !reMarks.MatchString(block) {
			continue
		}
		l := ZPLLabel{Index: i, Start: span[0], End: span[1], Quantity: 1}
		if pq := rePQ.FindString(block); pq != "" {
			l.HasPQ = true
			l.Quantity = pqQuantity(pq)
		}
		labels = append(labels, l)
	}
	return labels, nil
}

// pqQuantity reads q from "^PQq,p,r,o"; an empty or unreadable q is the printer's
// default, 1.
func pqQuantity(pq string) int {
	q, _, _ := strings.Cut(pq[3:], ",")
	if n, err := strconv.Atoi(strings.TrimSpace(q)); err == nil && n > 0 {
		return n
	}
	return 1
}

// ApplyCopiesZPL prints every label in zpl copies times by setting ^PQ: added where
// the label has none, the quantity replaced where it says 1 (pause, replicate and
// override parameters kept). It refuses ZPL it cannot read and labels that already ask
// for more than one. The result's total is the number of labels that will print.
func ApplyCopiesZPL(zpl string, copies int) (string, int, error) {
	if copies <= 1 {
		labels, err := AnalyzeZPL(zpl)
		total := 0
		for _, l := range labels {
			total += l.Quantity
		}
		return zpl, total, err
	}
	labels, err := AnalyzeZPL(zpl)
	if err != nil {
		return "", 0, err
	}
	if len(labels) == 0 {
		return "", 0, fmt.Errorf("%w: no ^XA…^XZ label with content found", ErrCopiesUnreadable)
	}
	for _, l := range labels {
		if l.Quantity > 1 {
			return "", 0, fmt.Errorf("%w: label %d says ^PQ%d; send copies=1, or drop ^PQ from the label",
				ErrCopiesConflict, l.Index+1, l.Quantity)
		}
	}
	var out strings.Builder
	prev := 0
	for _, l := range labels {
		out.WriteString(zpl[prev:l.Start])
		out.WriteString(setPQ(zpl[l.Start:l.End], copies))
		prev = l.End
	}
	out.WriteString(zpl[prev:])
	return out.String(), copies * len(labels), nil
}

// setPQ sets the quantity of one block: replacing q in an existing ^PQ (keeping its
// other parameters), or adding ^PQ just before ^XZ.
func setPQ(block string, q int) string {
	if loc := rePQ.FindStringIndex(block); loc != nil {
		_, rest, hasRest := strings.Cut(block[loc[0]+3:loc[1]], ",")
		cmd := "^PQ" + strconv.Itoa(q)
		if hasRest {
			cmd += "," + rest
		}
		return block[:loc[0]] + cmd + block[loc[1]:]
	}
	loc := reXZ.FindStringIndex(block)
	return block[:loc[0]] + "^PQ" + strconv.Itoa(q) + block[loc[0]:]
}
