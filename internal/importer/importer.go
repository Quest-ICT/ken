// Package importer parses the user's flat Markdown "memory" files (YAML
// frontmatter: name/description/metadata.type; body with **Why:** / **How to
// apply:** lines and [[wikilinks]]) into a neutral Memory shape for migration
// into ken. It is a pure parser — it does not touch the store.
package importer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Memory is one parsed flat-memory file, mapped onto ken's entry fields.
type Memory struct {
	Slug      string
	Kind      string // user | feedback | project | reference
	Title     string
	Summary   string
	Problem   string
	Rationale string
	Solution  string
	Links     []Link
	Source    string // filename
}

// Link is a [[wikilink]] found in the body.
type Link struct {
	ToSlug string
	Type   string
}

var linkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// ParseFile parses one memory .md file.
func ParseFile(path string) (*Memory, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fmBytes, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, err
	}

	var f struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Metadata    struct {
			Type string `yaml:"type"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(fmBytes, &f); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	if strings.TrimSpace(f.Name) == "" {
		return nil, errors.New("frontmatter is missing 'name'")
	}

	lead, why, how := splitSections(body)
	title := firstNonEmpty(f.Description, humanize(f.Name))
	m := &Memory{
		Slug:      strings.TrimSpace(f.Name),
		Kind:      normalizeKind(f.Metadata.Type),
		Title:     title,
		Summary:   truncate(title, 160),
		Problem:   lead,
		Rationale: why,
		Solution:  how,
		Links:     extractLinks(body),
		Source:    filepath.Base(path),
	}
	// Unstructured memory (no Why/How): keep the whole body as the solution so
	// nothing is lost.
	if why == "" && how == "" {
		m.Solution = strings.TrimSpace(body)
		m.Problem = ""
	}
	return m, nil
}

// splitFrontmatter separates a leading `---`...`---` YAML block from the body.
func splitFrontmatter(raw []byte) (fm []byte, body string, err error) {
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	s = strings.TrimPrefix(s, "\ufeff") // strip UTF-8 BOM if present
	if !strings.HasPrefix(s, "---") {
		return nil, "", errors.New("no YAML frontmatter (file must start with ---)")
	}
	rest := s[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return nil, "", errors.New("unterminated YAML frontmatter")
	}
	fmText := rest[:idx]
	after := rest[idx+len("\n---"):]
	return []byte(fmText), strings.TrimSpace(after), nil
}

// splitSections splits a body into the lead text, the **Why:** section, and the
// **How to apply:** section (case-insensitive markers).
func splitSections(body string) (lead, why, how string) {
	lb := strings.ToLower(body)
	const wm, hm = "**why:**", "**how to apply:**"
	iWhy := strings.Index(lb, wm)
	iHow := strings.Index(lb, hm)

	leadEnd := len(body)
	if iWhy >= 0 && iWhy < leadEnd {
		leadEnd = iWhy
	}
	if iHow >= 0 && iHow < leadEnd {
		leadEnd = iHow
	}
	lead = strings.TrimSpace(body[:leadEnd])

	if iWhy >= 0 {
		end := len(body)
		if iHow > iWhy {
			end = iHow
		}
		why = strings.TrimSpace(body[iWhy+len(wm) : end])
	}
	if iHow >= 0 {
		end := len(body)
		if iWhy > iHow {
			end = iWhy
		}
		how = strings.TrimSpace(body[iHow+len(hm) : end])
	}
	return lead, why, how
}

func extractLinks(body string) []Link {
	seen := map[string]bool{}
	var out []Link
	for _, m := range linkRe.FindAllStringSubmatch(body, -1) {
		slug := strings.TrimSpace(m[1])
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, Link{ToSlug: slug, Type: "relates"})
	}
	return out
}

func normalizeKind(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "user", "feedback", "project", "reference":
		return strings.ToLower(strings.TrimSpace(t))
	default:
		return "reference"
	}
}

func humanize(slug string) string {
	s := strings.TrimSpace(strings.ReplaceAll(slug, "-", " "))
	if s == "" {
		return s
	}
	r, sz := utf8.DecodeRuneInString(s) // rune-safe title-case of the first character
	return string(unicode.ToUpper(r)) + s[sz:]
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}
