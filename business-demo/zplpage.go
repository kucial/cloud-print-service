package main

import "strings"

// splitZPLPages splits a ZPL stream into its `^XA…^XZ` format blocks. Setup commands
// before the first block stay with page 1. Business systems own their ZPL, so this demo
// keeps its own copy rather than importing anything from the print service.
func splitZPLPages(zpl string) []string {
	var pages []string
	upper := strings.ToUpper(zpl)
	cursor := 0
	for {
		xa := strings.Index(upper[cursor:], "^XA")
		if xa == -1 {
			if xa = strings.Index(upper[cursor:], "~XA"); xa == -1 {
				break
			}
		}
		start := cursor + xa
		after := start + 3
		xz := strings.Index(upper[after:], "^XZ")
		if xz == -1 {
			if xz = strings.Index(upper[after:], "~XZ"); xz == -1 {
				pages = append(pages, zpl[cursor:])
				break
			}
		}
		end := after + xz + 3
		if len(pages) == 0 && cursor < start {
			pages = append(pages, zpl[cursor:end])
		} else {
			pages = append(pages, zpl[start:end])
		}
		cursor = end
	}
	if len(pages) == 0 && strings.TrimSpace(zpl) != "" {
		pages = append(pages, zpl)
	}
	return pages
}

func matchesPaperSize(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if a == b {
		return true
	}
	is4x6 := func(s string) bool {
		return s == "4x6" || s == "4x6in" || s == "100x150mm" || s == "100x150" || s == "101.6x152.4mm"
	}
	is4x2 := func(s string) bool {
		return s == "4x2" || s == "4x2in" || s == "100x50mm" || s == "100x50" || s == "101.6x50.8mm"
	}
	is2x1 := func(s string) bool {
		return s == "2x1" || s == "2x1in" || s == "50x25mm" || s == "50x25" || s == "50.8x25.4mm"
	}
	if is4x6(a) && is4x6(b) {
		return true
	}
	if is4x2(a) && is4x2(b) {
		return true
	}
	if is2x1(a) && is2x1(b) {
		return true
	}
	return false
}
