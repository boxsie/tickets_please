package store

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatterDelim is the canonical fence for a yaml frontmatter block.
const frontmatterDelim = "---"

// Frontmatter is the loose key→value map used for ReadMarkdown when callers
// don't have a typed record on hand. Comment readers prefer the typed
// readCommentFile helper in comments.go.
type Frontmatter map[string]any

// WriteMarkdown writes a markdown file with a leading yaml frontmatter block.
// Layout:
//
//	---
//	<yaml>
//	---
//	<body>
//
// The body is written verbatim. A single trailing newline is ensured if the
// body doesn't already end with one — otherwise the body lands as-is so
// round-trips are byte-stable for body-with-trailing-newline content.
func WriteMarkdown(path string, fm any, body string) error {
	data, err := EncodeMarkdown(fm, body)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// EncodeMarkdown produces the byte form of WriteMarkdown's output.
func EncodeMarkdown(fm any, body string) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("marshal frontmatter: %w", err)
	}
	// yaml.Marshal already terminates with \n.
	var buf bytes.Buffer
	buf.WriteString(frontmatterDelim)
	buf.WriteByte('\n')
	buf.Write(yamlBytes)
	buf.WriteString(frontmatterDelim)
	buf.WriteByte('\n')
	buf.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// ReadMarkdown loads a frontmatter+body markdown file. It returns a generic
// Frontmatter map; callers that want a typed record should use
// DecodeMarkdownInto instead.
func ReadMarkdown(path string) (Frontmatter, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	yamlBytes, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", path, err)
	}
	fm := Frontmatter{}
	if len(yamlBytes) > 0 {
		if err := yaml.Unmarshal(yamlBytes, &fm); err != nil {
			return nil, "", fmt.Errorf("parse %s frontmatter: %w", path, err)
		}
	}
	return fm, body, nil
}

// DecodeMarkdownInto loads a frontmatter+body file, unmarshalling the
// frontmatter into the typed v.
func DecodeMarkdownInto(path string, v any) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	yamlBytes, body, err := splitFrontmatter(data)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if len(yamlBytes) > 0 {
		if err := yaml.Unmarshal(yamlBytes, v); err != nil {
			return "", fmt.Errorf("parse %s frontmatter: %w", path, err)
		}
	}
	return body, nil
}

// splitFrontmatter takes raw file bytes and splits them into (yamlBytes,
// body). A file without a leading "---\n" line returns (nil, fullBody, nil).
func splitFrontmatter(data []byte) ([]byte, string, error) {
	// Tolerate an optional UTF-8 BOM.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !bytes.HasPrefix(data, []byte(frontmatterDelim)) {
		return nil, string(data), nil
	}
	// Skip the opening fence line (allowing \r\n or \n).
	rest := data[len(frontmatterDelim):]
	if len(rest) == 0 {
		return nil, "", fmt.Errorf("unterminated frontmatter")
	}
	if rest[0] == '\r' {
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != '\n' {
		// "---" not on its own line — treat as plain markdown.
		return nil, string(data), nil
	}
	rest = rest[1:]
	// Find the closing "\n---\n" or "\n---\r\n" or trailing "\n---".
	closeIdx := bytes.Index(rest, []byte("\n"+frontmatterDelim))
	if closeIdx < 0 {
		return nil, "", fmt.Errorf("unterminated frontmatter")
	}
	yamlPart := rest[:closeIdx]
	after := rest[closeIdx+1+len(frontmatterDelim):]
	// Skip the newline that terminates the closing fence.
	if len(after) > 0 && after[0] == '\r' {
		after = after[1:]
	}
	if len(after) > 0 && after[0] == '\n' {
		after = after[1:]
	}
	return yamlPart, string(after), nil
}
