package driver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// A command's parameters run to the next command prefix: ^PQ5,0,1,Y is one command.
	rePQ = regexp.MustCompile(`(?i)\^PQ[^\^~]*`)
	reXZ = regexp.MustCompile(`(?i)\^[xX][zZ]`)
)

// SplitZPLPages parses a ZPL stream into discrete `^XA...^XZ` format blocks.
// Text outside `^XA...^XZ` (such as download graphics or printer setup commands)
// preceding the first format is preserved with the first block.
func SplitZPLPages(zpl string) []string {
	var pages []string
	upper := strings.ToUpper(zpl)

	cursor := 0
	for {
		xaIdx := strings.Index(upper[cursor:], "^XA")
		if xaIdx == -1 {
			// Check for ~XA variant
			xaIdx = strings.Index(upper[cursor:], "~XA")
			if xaIdx == -1 {
				break
			}
		}
		actualStart := cursor + xaIdx

		// Find matching ^XZ
		searchAfter := actualStart + 3
		xzIdx := strings.Index(upper[searchAfter:], "^XZ")
		if xzIdx == -1 {
			xzIdx = strings.Index(upper[searchAfter:], "~XZ")
			if xzIdx == -1 {
				// Malformed / unclosed format block, take remainder
				pages = append(pages, zpl[cursor:])
				break
			}
		}
		actualEnd := searchAfter + xzIdx + 3

		// If this is the first page and there was prefix content (setup commands),
		// include the prefix in the first page so setup isn't lost.
		if len(pages) == 0 && cursor < actualStart {
			pages = append(pages, zpl[cursor:actualEnd])
		} else {
			pages = append(pages, zpl[actualStart:actualEnd])
		}

		cursor = actualEnd
	}

	if len(pages) == 0 && strings.TrimSpace(zpl) != "" {
		// Non-standard framing, treat whole payload as a single page
		pages = append(pages, zpl)
	}

	return pages
}

// ApplyPageCopies applies targeted copy multipliers to individual pages in a multi-page ZPL stream.
//
// Scenario: Customer orders 50 items with carton size 12.
//   - Page 1: Standard Carton Label (Qty: 12) -> 4 copies
//   - Page 2: Tail/Remainder Carton Label (Qty: 2, [TAIL BOX]) -> 1 copy
//
// Calling `ApplyPageCopies(zpl, []int{4, 1})` injects `^PQ4` into Page 1 and `^PQ1` into Page 2.
// If an entry is `0` (e.g. `[]int{4, 0}` when there is no remainder), that page is dropped from the output stream.
func ApplyPageCopies(zpl string, pageCopies []int) (string, int, error) {
	if len(pageCopies) == 0 {
		return zpl, 1, nil
	}

	// Blocks are edited in place: whatever sits between or around them (graphic
	// downloads, setup commands) reaches the printer unchanged.
	spans := zplBlocks(zpl)
	if len(spans) == 0 {
		spans = [][2]int{{0, len(zpl)}} // non-standard framing: the whole payload is one page
	}
	if len(pageCopies) > len(spans) {
		return "", 0, fmt.Errorf("more page copy rules (%d) than pages in ZPL (%d)", len(pageCopies), len(spans))
	}

	var out strings.Builder
	totalLabels, prev := 0, 0
	for i, span := range spans {
		copies := 1
		if i < len(pageCopies) {
			copies = pageCopies[i]
		}
		if copies < 0 {
			return "", 0, fmt.Errorf("page %d copy count cannot be negative (%d)", i+1, copies)
		}
		if copies > 99999999 {
			return "", 0, fmt.Errorf("page %d copy count exceeds maximum allowable (%d)", i+1, copies)
		}
		out.WriteString(zpl[prev:span[0]])
		prev = span[1]
		// Count of 0: omit this page entirely (e.g. suppress tail box label when remainder is 0)
		if copies == 0 {
			continue
		}
		totalLabels += copies
		out.WriteString(InjectPQ(zpl[span[0]:span[1]], copies))
	}
	out.WriteString(zpl[prev:])
	return out.String(), totalLabels, nil
}

// InjectPQ injects or replaces `^PQ<count>` immediately preceding `^XZ` in a format block.
func InjectPQ(block string, count int) string {
	if count <= 0 {
		return block
	}

	// Strip any existing ^PQ command
	stripped := rePQ.ReplaceAllString(block, "")

	// Find the ^XZ tag to inject before it
	loc := reXZ.FindStringIndex(stripped)
	if loc == nil {
		// No ^XZ found, append ^PQ at end
		return stripped + fmt.Sprintf("^PQ%d", count)
	}

	// Insert ^PQ<count> right before ^XZ
	return stripped[:loc[0]] + fmt.Sprintf("^PQ%d", count) + stripped[loc[0]:]
}

// ExtractPageCopies inspects a ZPL stream and extracts the `^PQ` quantity for each page.
// Returns a slice of counts per page (defaulting to 1 if no ^PQ is present).
func ExtractPageCopies(zpl string) []int {
	pages := SplitZPLPages(zpl)
	counts := make([]int, 0, len(pages))

	for _, p := range pages {
		matches := rePQ.FindString(p)
		if matches == "" {
			counts = append(counts, 1)
			continue
		}
		// Parse ^PQq[,p,r,o]
		raw := strings.TrimPrefix(strings.TrimPrefix(matches, "^"), "~")
		raw = strings.TrimPrefix(strings.TrimPrefix(raw, "PQ"), "pq")
		parts := strings.Split(raw, ",")
		if len(parts) > 0 && parts[0] != "" {
			if q, err := strconv.Atoi(parts[0]); err == nil && q > 0 {
				counts = append(counts, q)
				continue
			}
		}
		counts = append(counts, 1)
	}

	return counts
}
