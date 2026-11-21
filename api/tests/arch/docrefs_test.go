//go:build arch

package arch

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A reference names a document of docs/platform/designs or docs/app/modules (its file
// name without .md) and one or more sections: "auth-y-tenancy §5.2", "auth-y-tenancy
// §5.1.3 to §5.1.10". Each further § in the chain belongs to the same document.
var (
	docRefRe     = regexp.MustCompile(`([a-z0-9][a-z0-9-]*)\s+§(\d+(?:\.\d+)*)((?:\s*(?:,|and|to|y|a)\s*§\d+(?:\.\d+)*)*)`)
	chainRe      = regexp.MustCompile(`§(\d+(?:\.\d+)*)`)
	adrRe        = regexp.MustCompile(`\bADR (\d{4})\b`)
	sectionNumRe = regexp.MustCompile(`\d+(?:\.\d+)*`)
	// The patterns are split so that this file does not match them itself.
	oldDesignRe = regexp.MustCompile(`\(` + `design\b[^)]{0,80}\)?|\bdiseño` + `\s+\d+(?:\.\d+)*`)
)

// commentLine is the comment text found on one line of a source file.
type commentLine struct {
	line int
	text string
}

// commentMarker is what opens or continues a comment at the start of a line.
var commentMarker = regexp.MustCompile(`^\s*(//+|/\*+|\*+/?)?`)

// stripMarker leaves only the text of a comment line, so that a reference split across
// lines reads as one once the lines are joined.
func stripMarker(l string) string {
	return strings.TrimSuffix(commentMarker.ReplaceAllString(l, ""), "*/")
}

// goComments returns the comments of a Go file, one entry per line, with go/scanner so
// that a string such as "https://…" is never taken for a comment.
func goComments(src []byte) []commentLine {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	var out []commentLine
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return out
		}
		if tok != token.COMMENT {
			continue
		}
		start := fset.Position(pos).Line
		for i, l := range strings.Split(lit, "\n") {
			out = append(out, commentLine{start + i, stripMarker(l)})
		}
	}
}

// tsLineComment finds where a comment starts on a TypeScript line: "//" or "/*" at the
// start or after a space (a URL in a string has "://"), or a block comment's "*" line.
var tsLineComment = regexp.MustCompile(`(^|\s)(//|/\*)|^\s*\*`)

// tsComments returns the comment part of every line of a TypeScript file.
func tsComments(src []byte) []commentLine {
	var out []commentLine
	for i, l := range strings.Split(string(src), "\n") {
		if loc := tsLineComment.FindStringIndex(l); loc != nil {
			out = append(out, commentLine{i + 1, stripMarker(l[loc[0]:])})
		}
	}
	return out
}

// allLines returns every line of a Markdown file: in an AGENTS.md all of it is prose.
func allLines(src []byte) []commentLine {
	var out []commentLine
	for i, l := range strings.Split(string(src), "\n") {
		out = append(out, commentLine{i + 1, l})
	}
	return out
}

// docRefFiles lists, relative to root, the files whose references are checked: Go
// under api/ and TypeScript under web/src/ and web/e2e/ (without generated code, test
// fixtures or migrations, which are immutable), and every AGENTS.md.
func docRefFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "gen", "testdata", "migrations":
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Name() == "AGENTS.md",
			strings.HasPrefix(rel, "api/") && strings.HasSuffix(rel, ".go"),
			(strings.HasPrefix(rel, "web/src/") || strings.HasPrefix(rel, "web/e2e/")) &&
				(strings.HasSuffix(rel, ".ts") || strings.HasSuffix(rel, ".tsx")):
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	return out, nil
}

// docIndex holds the documents that references may name.
type docIndex struct {
	root string
	docs map[string]string // name -> path relative to root
	adrs map[string]string // number -> path relative to root
	text map[string][]string
}

func loadDocIndex(root string) (*docIndex, error) {
	idx := &docIndex{root: root, docs: map[string]string{}, adrs: map[string]string{}, text: map[string][]string{}}
	for _, dir := range []string{"docs/platform/designs", "docs/app/modules"} {
		names, err := filepath.Glob(filepath.Join(root, dir, "*.md"))
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", dir, err)
		}
		for _, n := range names {
			idx.docs[strings.TrimSuffix(filepath.Base(n), ".md")] = dir + "/" + filepath.Base(n)
		}
	}
	for _, dir := range []string{"docs/platform/adr", "docs/app/adr"} {
		names, err := filepath.Glob(filepath.Join(root, dir, "[0-9][0-9][0-9][0-9]-*.md"))
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", dir, err)
		}
		for _, n := range names {
			idx.adrs[filepath.Base(n)[:4]] = dir + "/" + filepath.Base(n)
		}
	}
	return idx, nil
}

func (idx *docIndex) lines(doc string) ([]string, error) {
	if l, ok := idx.text[doc]; ok {
		return l, nil
	}
	src, err := os.ReadFile(filepath.Join(idx.root, idx.docs[doc]))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", idx.docs[doc], err)
	}
	idx.text[doc] = strings.Split(string(src), "\n")
	return idx.text[doc], nil
}

// headingLevel returns how many # open a Markdown heading, or 0 for any other line.
func headingLevel(line string) int {
	n := len(line) - len(strings.TrimLeft(line, "#"))
	if n > 0 && strings.HasPrefix(line[n:], " ") {
		return n
	}
	return 0
}

// section checks that doc has section num and returns, if it does not, what is missing.
// §N is the heading "## N.", §N.M the heading "### N.M ", and §N.M.K the item "K." of the
// numbered list of section N.M, between its heading and the next one of its level or
// above.
func (idx *docIndex) section(doc, num string) (string, error) {
	lines, err := idx.lines(doc)
	if err != nil {
		return "", err
	}
	parts := strings.Split(num, ".")
	switch len(parts) {
	case 1:
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "## "+num+". ") }) {
			return fmt.Sprintf("no heading %q", "## "+num+"."), nil
		}
	case 2:
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "### "+num+" ") }) {
			return fmt.Sprintf("no heading %q", "### "+num), nil
		}
	case 3:
		parent := parts[0] + "." + parts[1]
		start := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "### "+parent+" ") })
		if start < 0 {
			return fmt.Sprintf("no heading %q", "### "+parent), nil
		}
		for _, l := range lines[start+1:] {
			if lvl := headingLevel(l); lvl > 0 && lvl <= 3 {
				break
			}
			if strings.HasPrefix(l, parts[2]+". ") {
				return "", nil
			}
		}
		return fmt.Sprintf("no item %q in the numbered list of section %s", parts[2]+".", parent), nil
	default:
		return "no such level: only §N, §N.M and §N.M.K (item K of the list of N.M) are sections", nil
	}
	return "", nil
}

// docRefViolations checks every reference to a design document or an ADR in the files
// of docRefFiles under root, and the old formats that named no document.
func docRefViolations(root string) ([]string, error) {
	idx, err := loadDocIndex(root)
	if err != nil {
		return nil, err
	}
	files, err := docRefFiles(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		var comments []commentLine
		switch {
		case strings.HasSuffix(rel, ".go"):
			comments = goComments(src)
		case strings.HasSuffix(rel, ".md"):
			comments = allLines(src)
		default:
			comments = tsComments(src)
		}
		got, err := checkDocRefs(idx, rel, comments)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

func checkDocRefs(idx *docIndex, rel string, comments []commentLine) ([]string, error) {
	// The comment lines are joined so that a reference split across lines is still one.
	var text strings.Builder
	starts := make([]int, len(comments))
	for i, c := range comments {
		starts[i] = text.Len()
		text.WriteString(c.text)
		text.WriteByte('\n')
	}
	joined := text.String()
	lineAt := func(off int) int {
		i, found := slices.BinarySearch(starts, off)
		if !found {
			i--
		}
		return comments[i].line
	}
	var out []string
	report := func(off int, ref, problem, fix string) {
		out = append(out, fmt.Sprintf("[doc-refs] %s:%d: %q %s; %s", rel, lineAt(off), ref, problem, fix))
	}

	for _, m := range oldDesignRe.FindAllStringIndex(joined, -1) {
		old := strings.Join(strings.Fields(joined[m[0]:m[1]]), " ")
		example := "auth-y-tenancy " + strings.Join(slices.Collect(func(yield func(string) bool) {
			for _, n := range sectionNumRe.FindAllString(old, -1) {
				if !yield("§" + n) {
					return
				}
			}
		}), " and ")
		report(m[0], old, "does not name the document",
			"write it as `"+example+"`: the file name of docs/platform/designs or docs/app/modules without .md, and § before each number")
	}

	covered := map[int]bool{} // offsets of the § that belong to a document's chain
	for _, m := range docRefRe.FindAllStringSubmatchIndex(joined, -1) {
		ref := strings.Join(strings.Fields(joined[m[0]:m[1]]), " ")
		doc := joined[m[2]:m[3]]
		for _, n := range chainRe.FindAllStringSubmatchIndex(joined[m[0]:m[1]], -1) {
			covered[m[0]+n[0]] = true
		}
		if _, ok := idx.docs[doc]; !ok {
			report(m[0], ref, fmt.Sprintf("names %q, which is not in docs/platform/designs/ or docs/app/modules/", doc),
				"use the file name of the document without .md")
			continue
		}
		for _, n := range chainRe.FindAllStringSubmatch(joined[m[0]:m[1]], -1) {
			problem, err := idx.section(doc, n[1])
			if err != nil {
				return nil, err
			}
			if problem != "" {
				report(m[0], ref, fmt.Sprintf("points to §%s, but %s has %s", n[1], idx.docs[doc], problem),
					"find the section that says it in the document and cite its number (do not edit the document to fit)")
			}
		}
	}
	for _, m := range chainRe.FindAllStringIndex(joined, -1) {
		if !covered[m[0]] {
			report(m[0], joined[m[0]:m[1]], "names no document", "put the document before it: `auth-y-tenancy "+joined[m[0]:m[1]]+"`")
		}
	}

	for _, m := range adrRe.FindAllStringSubmatchIndex(joined, -1) {
		if _, ok := idx.adrs[joined[m[2]:m[3]]]; !ok {
			report(m[0], joined[m[0]:m[1]], "has no file NNNN-*.md in docs/platform/adr/ or docs/app/adr/",
				"cite an existing ADR, or write the ADR first")
		}
	}
	return out, nil
}

// TestDocRefs ([doc-refs]) checks that every reference to a design document or an ADR in
// the comments of api/, web/src/ and web/e2e/, and in every AGENTS.md, leads to a section or a file
// that exists. It checks that the section exists, not that it still says the same: when
// a document is renumbered, its references have to be reviewed by hand.
func TestDocRefs(t *testing.T) {
	t.Parallel()

	// A wrong root would find no file and pass without looking at anything.
	files, err := docRefFiles("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"AGENTS.md", "api/main.go", "web/src/router.ts", "web/e2e/members.spec.ts"} {
		if !slices.Contains(files, f) {
			t.Fatalf("%s is not among the checked files: is the repository root right?", f)
		}
	}

	got, err := docRefViolations("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got {
		t.Error(v)
	}
}

// The fixture holds a valid reference of each kind and an invalid one of each failure.
func TestDocRefs_DetectsViolations(t *testing.T) {
	t.Parallel()

	got, err := docRefViolations("testdata/docrefs")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"api/old.go:3: \"(design, 5.2)\" does not name the document",
		"api/old.go:3: \"(design, section 6)\" does not name the document",
		"AGENTS.md:5: \"diseño 7.3\" does not name the document",
		"api/bad.go:3: \"nope §1\" names \"nope\"",
		"api/bad.go:4: \"guide §9\" points to §9, but docs/platform/designs/guide.md has no heading \"## 9.\"",
		"api/bad.go:5: \"guide §2.9\" points to §2.9, but docs/platform/designs/guide.md has no heading \"### 2.9\"",
		"api/bad.go:6: \"guide §2.1.3\" points to §2.1.3, but docs/platform/designs/guide.md has no item \"3.\" in the numbered list of section 2.1",
		"api/bad.go:7: \"guide §2.1 and §2.7\" points to §2.7",
		"api/bad.go:8: \"guide §1.1.1.1\" points to §1.1.1.1",
		"api/bad.go:9: \"§2\" names no document",
		"api/bad.go:10: \"ADR 0099\" has no file",
		"web/src/bad.ts:1: \"guide §2.1.3\" points to §2.1.3",
		"web/e2e/bad.spec.ts:1: \"guide §2.9\" points to §2.9",
		"web/src/bad.ts:2: \"(design, 5.2)\" does not name the document; write it as `auth-y-tenancy §5.2`",
	}
	for _, w := range want {
		if !slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, w) }) {
			t.Errorf("no violation contains %s", w)
		}
	}
	for _, v := range got {
		for _, ok := range []string{"api/good.go", "web/src/good.ts", "api/gen/", "api/migrations/"} {
			if strings.Contains(v, ok) {
				t.Errorf("valid or skipped file reported: %s", v)
			}
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d violations, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
}
