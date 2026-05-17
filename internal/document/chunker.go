package document

import (
	"regexp"
	"strings"
	"unicode"
)

// Chunk represents a text fragment with its metadata.
type Chunk struct {
	Text     string `json:"text"`
	Filename string `json:"filename"`
	Section  string `json:"section"`
	Index    int    `json:"index"`
}

// Chunker splits text into overlapping chunks, preserving section context.
type Chunker struct {
	ChunkSize int
	Overlap   int
}

// sectionHeaderRegex matches typical PDF/markdown section headers:
//   - ALL CAPS lines (tariff manuals)
//   - Numbered sections: "1.2.3 Title" or "ARTÍCULO 5" or "CAPÍTULO II"
//   - Lines ending with a line of dashes/equals (markdown)
var sectionHeaderRegex = regexp.MustCompile(`(?m)^(?:[A-ZÁÉÍÓÚÑ][A-ZÁÉÍÓÚÑ\s\-/,.:;()#]{10,}|(?:CAP[ÍI]TULO|ART[ÍI]CULO|SECCI[ÓO]N|ANEXO|T[ÍI]TULO|[0-9]+[\.\)-])[^a-z]{5,}|[IVX]+[\.\)\-]\s+.+)$`)

// NewChunker creates a new chunker with default or configured values.
func NewChunker(chunkSize, overlap int) *Chunker {
	if chunkSize <= 0 {
		chunkSize = 512
	}
	if overlap < 0 {
		overlap = 64
	}
	if overlap >= chunkSize {
		overlap = chunkSize / 8
	}
	return &Chunker{ChunkSize: chunkSize, Overlap: overlap}
}

// Split splits text into chunks, detecting sections and prepending headers.
func (c *Chunker) Split(text, filename string) []Chunk {
	sections := c.splitSections(text)

	if len(sections) == 0 {
		// No sections found, chunk flat
		return c.chunkSection(text, filename, filename, 0)
	}

	var allChunks []Chunk
	globalIdx := 0
	for _, sec := range sections {
		chunks := c.chunkSection(sec.body, filename, sec.title, globalIdx)
		for i := range chunks {
			chunks[i].Index = globalIdx
			globalIdx++
		}
		allChunks = append(allChunks, chunks...)
	}
	return allChunks
}

type section struct {
	title string
	body  string
}

// splitSections divides text into sections based on detected headers.
func (c *Chunker) splitSections(text string) []section {
	lines := strings.Split(text, "\n")
	var sections []section
	var currentTitle string
	var currentBody strings.Builder

	flush := func() {
		body := strings.TrimSpace(currentBody.String())
		if body != "" {
			sections = append(sections, section{title: currentTitle, body: body})
		}
		currentBody.Reset()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			currentBody.WriteString("\n")
			continue
		}

		// Detect section header
		if sectionHeaderRegex.MatchString(trimmed) && !looksLikeBody(trimmed) {
			flush()
			currentTitle = trimmed
			continue
		}

		currentBody.WriteString(line)
		currentBody.WriteString("\n")
	}
	flush()

	return sections
}

// chunkSection chunks a single section's body, prepending the section title to each chunk.
func (c *Chunker) chunkSection(body, filename, sectionTitle string, startIdx int) []Chunk {
	words := tokenize(body)
	if len(words) == 0 {
		return nil
	}

	// If we have a section title, prefix every chunk with it
	prefix := ""
	if sectionTitle != "" && sectionTitle != filename {
		prefix = "[" + sectionTitle + "]\n"
	}

	var chunks []Chunk
	step := c.ChunkSize - c.Overlap
	if step <= 0 {
		step = c.ChunkSize
	}

	for start := 0; start < len(words); start += step {
		end := start + c.ChunkSize
		if end > len(words) {
			end = len(words)
		}
		chunkText := prefix + strings.Join(words[start:end], " ")
		chunkText = strings.TrimSpace(chunkText)
		if chunkText == "" || chunkText == prefix {
			continue
		}
		chunks = append(chunks, Chunk{
			Text:     chunkText,
			Filename: filename,
			Section:  sectionTitle,
			Index:    startIdx + len(chunks),
		})
		if end == len(words) {
			break
		}
	}
	return chunks
}

// looksLikeBody checks if a matched "header" is actually body text in ALL CAPS.
func looksLikeBody(line string) bool {
	words := strings.Fields(line)
	// Headers are usually short; long all-caps text is probably body
	if len(words) > 15 {
		return true
	}
	// Lines ending in period are probably sentences
	if strings.HasSuffix(line, ".") && len(words) > 3 {
		return true
	}
	return false
}

// tokenize splits text into words.
func tokenize(text string) []string {
	var words []string
	var current strings.Builder

	for _, r := range text {
		if unicode.IsSpace(r) {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		} else {
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}
