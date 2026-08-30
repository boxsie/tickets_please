package worker

import (
	"strings"
	"unicode/utf8"
)

const minLearningChunkChars = 80

// chunkLearningText splits learnings on Markdown paragraph boundaries, then
// merges short runs so tiny list items do not become weak standalone vectors.
// Heading-only blocks stay attached to the paragraph that follows them.
func chunkLearningText(text string) []string {
	blocks := markdownBlocks(text)
	if len(blocks) == 0 {
		return nil
	}

	units := make([]string, 0, len(blocks))
	var headings []string
	for _, block := range blocks {
		if isHeadingBlock(block) {
			headings = append(headings, block)
			continue
		}
		if len(headings) > 0 {
			block = strings.Join(append(headings, block), "\n\n")
			headings = headings[:0]
		}
		units = append(units, block)
	}
	if len(headings) > 0 {
		trailing := strings.Join(headings, "\n\n")
		if len(units) == 0 {
			units = append(units, trailing)
		} else {
			units[len(units)-1] += "\n\n" + trailing
		}
	}

	chunks := make([]string, 0, len(units))
	var current string
	for _, unit := range units {
		if current == "" {
			current = unit
		} else if utf8.RuneCountInString(current) < minLearningChunkChars {
			// A short tail immediately before a new heading belongs to the
			// preceding section. Merge it backward so the next chunk starts
			// with — and therefore cannot detach — its heading.
			if startsWithHeading(unit) && len(chunks) > 0 {
				chunks[len(chunks)-1] += "\n\n" + current
				current = unit
			} else {
				current += "\n\n" + unit
			}
		} else {
			chunks = append(chunks, current)
			current = unit
		}
	}
	if current != "" {
		if utf8.RuneCountInString(current) < minLearningChunkChars && len(chunks) > 0 {
			chunks[len(chunks)-1] += "\n\n" + current
		} else {
			chunks = append(chunks, current)
		}
	}
	return chunks
}

func markdownBlocks(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	blocks := make([]string, 0, len(lines))
	var current []string
	flush := func() {
		block := strings.TrimSpace(strings.Join(current, "\n"))
		if block != "" {
			blocks = append(blocks, block)
		}
		current = current[:0]
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return blocks
}

func isHeadingBlock(block string) bool {
	lines := strings.Split(block, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") || len(strings.TrimLeft(trimmed, "#")) == len(trimmed) {
			return false
		}
	}
	return true
}

func startsWithHeading(block string) bool {
	first, _, _ := strings.Cut(block, "\n")
	return isHeadingBlock(first)
}
