# 불리언 검색 (OR/AND/괄호) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 검색을 단일 키워드에서 OR(`|`)/AND(`&`)/괄호 불리언 표현식으로 확장하고, 키워드별 색상 하이라이트와 하위폴더 스코프·문서 우선 필터를 추가한다.

**Architecture:** 동일 문법의 작은 재귀하강 파서를 Go(백엔드, folder 검색)와 JS(프론트, in-file 하이라이트) 양쪽에 구현한다. 평가 단위는 "행"(in-file=렌더 블록/`<tr>`, folder=텍스트 줄). Go 파서가 테스트의 source-of-truth이고 JS는 브라우저로 검증한다.

**Tech Stack:** Go (net/http, 표준 라이브러리), 임베드된 바닐라 JS/CSS in `web.go` (백틱 금지).

**참고 스펙:** `docs/superpowers/specs/2026-06-11-boolean-search-design.md`

---

## File Structure

- **Create** `search_expr.go` — Go 표현식 파서/평가기(`exprNode`, `parseSearchExpr`, `eval`, `isDocExt`). 단일 책임: 불리언 표현식.
- **Create** `search_expr_test.go` — 파서/평가/문서필터 유닛 테스트.
- **Modify** `web.go`:
  - 백엔드: `searchResult` 구조체, `searchFileForExpr`(신규, `searchFileForNeedle` 대체), `searchDirShallow`/`searchTreeRecursive` 시그니처, `handleSearch`(scope=tree, allFiles, docsOnly).
  - 프론트 CSS: kw 팔레트(`mark.search-mark.kw-N`), `.current` 포커스, 칩/버튼 스타일.
  - 프론트 JS: `parseSearchExpr`/`evalExpr`(JS 미러), `highlightInFile` 재작성, `renderInFileResults` 색상, `runFolderSearch`(칩·전체검색 버튼), `applyFolderScope`(3단), 입력 핸들러 리셋, i18nDict 키, state 필드.
  - HTML: 스코프 토글에 `searchScopeTree` 버튼.
- **Modify** `search_test.go` — `searchDirShallow`/`searchTreeRecursive` 호출부를 신규 시그니처로 갱신(회귀 가드 유지).

---

## Task 1: Go 표현식 파서 + 평가기

**Files:**
- Create: `search_expr.go`
- Test: `search_expr_test.go`

- [ ] **Step 1: 실패 테스트 작성**

`search_expr_test.go`:
```go
package main

import "testing"

func evalQuery(t *testing.T, q, line string) bool {
	t.Helper()
	root, _ := parseSearchExpr(q)
	return root.eval(line) // line은 호출자가 소문자로 전달
}

func TestParseSearchExprTerms(t *testing.T) {
	root, terms := parseSearchExpr("A|B|C")
	if len(terms) != 3 || terms[0] != "a" || terms[2] != "c" {
		t.Fatalf("terms = %v, want [a b c]", terms)
	}
	if !root.eval("xx b yy") {
		t.Error("A|B|C should match a line containing b")
	}
	if root.eval("nothing here") {
		t.Error("A|B|C should not match a line with none")
	}
}

func TestParseSearchExprAnd(t *testing.T) {
	root, _ := parseSearchExpr("a&b")
	if !root.eval("a and b together") {
		t.Error("a&b should match a line containing both")
	}
	if root.eval("only a here") {
		t.Error("a&b should not match a line with only a")
	}
}

func TestParseSearchExprPrecedenceAndParens(t *testing.T) {
	// a&b|c == (a&b)|c
	root, _ := parseSearchExpr("a&b|c")
	if !root.eval("just c") {
		t.Error("a&b|c should match a line with only c")
	}
	if !root.eval("a and b") {
		t.Error("a&b|c should match a line with a and b")
	}
	if root.eval("only a") {
		t.Error("a&b|c should not match a line with only a")
	}
	// (a&b)|c with explicit parens behaves the same
	root2, _ := parseSearchExpr("(a&b)|c")
	if root2.eval("only b") {
		t.Error("(a&b)|c should not match a line with only b")
	}
}

func TestParseSearchExprQuotedLiteral(t *testing.T) {
	// Quotes escape operators: "a&b" is a literal phrase, not an AND.
	root, terms := parseSearchExpr(`"a&b"`)
	if len(terms) != 1 || terms[0] != "a&b" {
		t.Fatalf("terms = %v, want [a&b]", terms)
	}
	if !root.eval("xx a&b yy") {
		t.Error(`"a&b" should match the literal substring a&b`)
	}
	if root.eval("a or b") {
		t.Error(`"a&b" should not behave like AND`)
	}
}

func TestParseSearchExprPhrase(t *testing.T) {
	// Spaces stay inside a term: "hello world" is one phrase.
	root, terms := parseSearchExpr("hello world")
	if len(terms) != 1 || terms[0] != "hello world" {
		t.Fatalf("terms = %v, want [hello world]", terms)
	}
	if !root.eval("say hello world now") {
		t.Error("phrase should match")
	}
}

func TestParseSearchExprUnbalancedFallback(t *testing.T) {
	// Unbalanced parens fall back to a literal whole-string term.
	root, terms := parseSearchExpr("(a&b")
	if len(terms) != 1 || terms[0] != "(a&b" {
		t.Fatalf("terms = %v, want literal fallback [(a&b]", terms)
	}
	if !root.eval("xx (a&b yy") {
		t.Error("fallback should match the literal text")
	}
}

func TestIsDocExt(t *testing.T) {
	doc := []string{"a.md", "b.MARKDOWN", "c.mdx", "d.txt", "e.log"}
	for _, n := range doc {
		if !isDocExt(n) {
			t.Errorf("isDocExt(%q) = false, want true", n)
		}
	}
	code := []string{"a.go", "b.js", "c.csv", "d.tsv", "e.json", "f"}
	for _, n := range code {
		if isDocExt(n) {
			t.Errorf("isDocExt(%q) = true, want false", n)
		}
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `go test ./... -run 'ParseSearchExpr|IsDocExt' -v`
Expected: FAIL — `undefined: parseSearchExpr`, `undefined: isDocExt`.

- [ ] **Step 3: 파서 구현**

`search_expr.go`:
```go
package main

import (
	"path/filepath"
	"strings"
)

// exprNode is a node in a boolean search expression tree.
// op is one of "term", "and", "or". For "term", text holds the lowercased
// keyword/phrase to substring-match; for "and"/"or", kids holds the operands.
type exprNode struct {
	op   string
	text string
	kids []*exprNode
}

// eval reports whether lineLower (already lowercased) satisfies the expression.
func (n *exprNode) eval(lineLower string) bool {
	switch n.op {
	case "term":
		return n.text != "" && strings.Contains(lineLower, n.text)
	case "and":
		for _, k := range n.kids {
			if !k.eval(lineLower) {
				return false
			}
		}
		return true
	case "or":
		for _, k := range n.kids {
			if k.eval(lineLower) {
				return true
			}
		}
		return false
	}
	return false
}

type seToken struct {
	kind string // "term", "and", "or", "lparen", "rparen"
	text string // lowercased keyword for "term"
}

// tokenizeSearch splits q into tokens. & | ( ) are operators; double quotes
// escape them (the quoted run becomes one literal term); other runs accumulate
// into a term whose outer whitespace is trimmed.
func tokenizeSearch(q string) []seToken {
	toks := []seToken{}
	var buf strings.Builder
	flush := func() {
		s := strings.TrimSpace(buf.String())
		buf.Reset()
		if s != "" {
			toks = append(toks, seToken{kind: "term", text: strings.ToLower(s)})
		}
	}
	runes := []rune(q)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch c {
		case '"':
			// Read until the closing quote (or end). The inner text is literal.
			j := i + 1
			var inner strings.Builder
			for j < len(runes) && runes[j] != '"' {
				inner.WriteRune(runes[j])
				j++
			}
			s := inner.String() // keep inner spaces; only this run is the term
			if strings.TrimSpace(s) != "" {
				// Append directly as a literal term (do not trim interior).
				flush() // flush any pending unquoted buffer first
				toks = append(toks, seToken{kind: "term", text: strings.ToLower(s)})
			}
			i = j // skip past closing quote (loop's i++ moves past it)
		case '&':
			flush()
			toks = append(toks, seToken{kind: "and"})
		case '|':
			flush()
			toks = append(toks, seToken{kind: "or"})
		case '(':
			flush()
			toks = append(toks, seToken{kind: "lparen"})
		case ')':
			flush()
			toks = append(toks, seToken{kind: "rparen"})
		default:
			buf.WriteRune(c)
		}
	}
	flush()
	return toks
}

// seParser is a tiny recursive-descent parser over the token list.
type seParser struct {
	toks []seToken
	pos  int
	err  bool
}

func (p *seParser) peek() *seToken {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *seParser) parseOr() *exprNode {
	left := p.parseAnd()
	if left == nil {
		p.err = true
		return nil
	}
	kids := []*exprNode{left}
	for t := p.peek(); t != nil && t.kind == "or"; t = p.peek() {
		p.pos++
		right := p.parseAnd()
		if right == nil {
			p.err = true
			return nil
		}
		kids = append(kids, right)
	}
	if len(kids) == 1 {
		return kids[0]
	}
	return &exprNode{op: "or", kids: kids}
}

func (p *seParser) parseAnd() *exprNode {
	left := p.parseAtom()
	if left == nil {
		return nil
	}
	kids := []*exprNode{left}
	for t := p.peek(); t != nil && t.kind == "and"; t = p.peek() {
		p.pos++
		right := p.parseAtom()
		if right == nil {
			p.err = true
			return nil
		}
		kids = append(kids, right)
	}
	if len(kids) == 1 {
		return kids[0]
	}
	return &exprNode{op: "and", kids: kids}
}

func (p *seParser) parseAtom() *exprNode {
	t := p.peek()
	if t == nil {
		return nil
	}
	if t.kind == "lparen" {
		p.pos++
		inner := p.parseOr()
		closing := p.peek()
		if inner == nil || closing == nil || closing.kind != "rparen" {
			p.err = true
			return nil
		}
		p.pos++
		return inner
	}
	if t.kind == "term" {
		p.pos++
		return &exprNode{op: "term", text: t.text}
	}
	return nil // operator/rparen where an atom was expected
}

// parseSearchExpr parses q into an AST plus the ordered list of distinct
// lowercased terms (encounter order, de-duplicated). On any parse failure it
// falls back to a single literal term holding the whole trimmed/lowercased q,
// so search never breaks.
func parseSearchExpr(q string) (*exprNode, []string) {
	toks := tokenizeSearch(q)
	p := &seParser{toks: toks}
	root := p.parseOr()
	if root == nil || p.err || p.pos != len(toks) {
		lit := strings.ToLower(strings.TrimSpace(q))
		node := &exprNode{op: "term", text: lit}
		if lit == "" {
			return node, nil
		}
		return node, []string{lit}
	}
	return root, collectTerms(root)
}

// collectTerms returns the distinct term texts in encounter order.
func collectTerms(n *exprNode) []string {
	seen := map[string]bool{}
	out := []string{}
	var walk func(*exprNode)
	walk = func(x *exprNode) {
		if x.op == "term" {
			if x.text != "" && !seen[x.text] {
				seen[x.text] = true
				out = append(out, x.text)
			}
			return
		}
		for _, k := range x.kids {
			walk(k)
		}
	}
	walk(n)
	return out
}

// isDocExt reports whether name has a "document" extension (markdown/text).
// Folder searches restrict to these by default; "search all files" lifts it.
func isDocExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".mdx", ".txt", ".log":
		return true
	}
	return false
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `go test ./... -run 'ParseSearchExpr|IsDocExt' -v`
Expected: PASS (모든 신규 테스트).

- [ ] **Step 5: 커밋**

```bash
git add search_expr.go search_expr_test.go
git commit -m "feat(search): Go boolean expression parser + evaluator"
```

---

## Task 2: 백엔드 행 단위 평가 + 문서 필터 + tree 스코프

**Files:**
- Modify: `web.go:13332-13336` (searchResult), `web.go:13866-13965` (search helpers + handleSearch)
- Modify: `search_test.go:124-129` (호출부 시그니처)
- Test: `search_test.go` (신규 docsOnly 테스트)

- [ ] **Step 1: 실패 테스트 작성**

`search_test.go`의 `TestSearchDirShallowVsRecursive`를 신규 시그니처로 교체하고 docsOnly 케이스를 추가한다. 기존 124-139 라인 블록을 아래로 교체:
```go
	exprNeedle, termsNeedle := parseSearchExpr("needle")
	shallow := searchDirShallow(root, exprNeedle, termsNeedle)
	if len(shallow) != 1 || filepath.Base(shallow[0].Path) != "top.md" {
		t.Fatalf("shallow = %+v, want only top.md", shallow)
	}

	rec := searchTreeRecursive(root, exprNeedle, termsNeedle, 4000, false)
	got := map[string]bool{}
	for _, r := range rec {
		got[filepath.Base(r.Path)] = true
	}
	if !got["top.md"] || !got["deep.md"] {
		t.Errorf("recursive should find top.md + deep.md: %+v", got)
	}
	if got["config.md"] || got["pkg.md"] {
		t.Errorf("recursive should skip .git/node_modules: %+v", got)
	}
```

그리고 파일 끝에 신규 테스트 추가:
```go
func TestSearchTreeDocsOnly(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "doc.md", "needle in markdown\n")
	writeTestFile(t, root, "code.go", "needle in code\n")
	expr, terms := parseSearchExpr("needle")

	docs := searchTreeRecursive(root, expr, terms, 4000, true)
	got := map[string]bool{}
	for _, r := range docs {
		got[filepath.Base(r.Path)] = true
	}
	if !got["doc.md"] || got["code.go"] {
		t.Errorf("docsOnly should find doc.md and skip code.go: %+v", got)
	}

	all := searchTreeRecursive(root, expr, terms, 4000, false)
	got = map[string]bool{}
	for _, r := range all {
		got[filepath.Base(r.Path)] = true
	}
	if !got["doc.md"] || !got["code.go"] {
		t.Errorf("allFiles should find both: %+v", got)
	}
}

func TestSearchExprAndAcrossLines(t *testing.T) {
	root := t.TempDir()
	// a and b on different lines -> AND must NOT match this file.
	writeTestFile(t, root, "split.md", "alpha here\nbeta there\n")
	// a and b on the SAME line -> AND matches.
	writeTestFile(t, root, "together.md", "alpha and beta same line\n")
	expr, terms := parseSearchExpr("alpha&beta")

	res := searchDirShallow(root, expr, terms)
	got := map[string]bool{}
	for _, r := range res {
		got[filepath.Base(r.Path)] = true
	}
	if got["split.md"] {
		t.Error("AND must not match keywords on different lines")
	}
	if !got["together.md"] {
		t.Error("AND must match keywords on the same line")
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `go test ./... -run 'Search' -v`
Expected: FAIL — `searchDirShallow`/`searchTreeRecursive` 시그니처 불일치(컴파일 에러), `searchResult`에 MatchedTerms 없음.

- [ ] **Step 3: searchResult 구조체 갱신**

`web.go:13332` 블록을 교체:
```go
type searchResult struct {
	Path         string   `json:"path"`
	Count        int      `json:"count"`
	Snippets     []string `json:"snippets"`
	MatchedTerms []string `json:"matchedTerms"`
}
```

- [ ] **Step 4: searchFileForNeedle → searchFileForExpr 교체**

`web.go:13866-13882`의 `searchFileForNeedle` 전체를 교체:
```go
// searchFileForExpr returns a searchResult for full when at least one line
// satisfies expr. Count is the number of satisfying lines; MatchedTerms lists
// which distinct terms appear anywhere in the file (for the UI color chips).
func searchFileForExpr(full string, expr *exprNode, terms []string) (searchResult, bool) {
	info, err := os.Stat(full)
	if err != nil || info.Size() > searchMaxFileBytes {
		return searchResult{}, false
	}
	data, err := os.ReadFile(full)
	if err != nil || !isProbablyText(data) {
		return searchResult{}, false
	}
	text := string(data)
	count := 0
	matched := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		ll := strings.ToLower(line)
		if expr.eval(ll) {
			count++
		}
		for _, term := range terms {
			if !matched[term] && strings.Contains(ll, term) {
				matched[term] = true
			}
		}
	}
	if count == 0 {
		return searchResult{}, false
	}
	mt := []string{}
	for _, term := range terms {
		if matched[term] {
			mt = append(mt, term)
		}
	}
	snippetNeedle := ""
	if len(mt) > 0 {
		snippetNeedle = mt[0]
	}
	lower := strings.ToLower(text)
	return searchResult{
		Path:         full,
		Count:        count,
		Snippets:     collectSnippets(text, lower, snippetNeedle, searchMaxSnippets),
		MatchedTerms: mt,
	}, true
}
```

- [ ] **Step 5: searchDirShallow / searchTreeRecursive 시그니처 교체**

`web.go:13887-13931`의 두 함수를 교체:
```go
func searchDirShallow(dir string, expr *exprNode, terms []string) []searchResult {
	out := []searchResult{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if res, ok := searchFileForExpr(filepath.Join(dir, e.Name()), expr, terms); ok {
			out = append(out, res)
		}
	}
	return out
}

func searchTreeRecursive(dirRoot string, expr *exprNode, terms []string, maxFiles int, docsOnly bool) []searchResult {
	out := []searchResult{}
	scanned := 0
	_ = filepath.WalkDir(dirRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != dirRoot && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if docsOnly && !isDocExt(d.Name()) {
			return nil
		}
		if scanned >= maxFiles {
			return filepath.SkipAll
		}
		scanned++
		if res, ok := searchFileForExpr(path, expr, terms); ok {
			out = append(out, res)
		}
		return nil
	})
	return out
}
```

- [ ] **Step 6: handleSearch 갱신 (scope=tree, allFiles, expr)**

`web.go:13948-13964`(needle 정의부터 writeJSON까지)를 교체:
```go
	expr, terms := parseSearchExpr(q)
	allFiles := r.URL.Query().Get("allFiles") == "1"

	var out []searchResult
	switch r.URL.Query().Get("scope") {
	case "git":
		root := s.gitRoot(r.Context(), abs)
		if root == "" {
			root = abs
		}
		out = searchTreeRecursive(root, expr, terms, searchMaxGitFiles, !allFiles)
	case "tree":
		out = searchTreeRecursive(abs, expr, terms, searchMaxGitFiles, !allFiles)
	default:
		out = searchDirShallow(abs, expr, terms)
	}

	// Most matches first.
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	s.writeJSON(w, http.StatusOK, out)
```
주의: 교체 후 `needle := strings.ToLower(q)` 라인은 더 이상 쓰이지 않으므로 삭제(미사용 변수 컴파일 에러 방지).

- [ ] **Step 7: 테스트 통과 확인**

Run: `go test ./... -v`
Expected: PASS (기존 + 신규 검색 테스트 전부). 빌드 에러 없음.

- [ ] **Step 8: 커밋**

```bash
git add web.go search_test.go
git commit -m "feat(search): per-line boolean eval, tree scope, docs-only filter"
```

---

## Task 3: 프론트 JS 파서 미러 + 색상 팔레트 CSS

**Files:**
- Modify: `web.go:3986-3995` (search-mark CSS), `web.go` highlightInFile 인근에 parseSearchExpr 추가

- [ ] **Step 1: kw 팔레트 + 포커스 CSS 교체**

`web.go:3986-3995`의 `mark.search-mark { … }`/`mark.search-mark.current { … }` 두 블록을 교체:
```css
    mark.search-mark {
      background: color-mix(in oklab, var(--accent) 35%, transparent);
      color: inherit;
      border-radius: 3px;
      padding: 0 2px;
    }
    /* Per-keyword colors (cycled modulo 8). Translucent so they read on any theme. */
    mark.search-mark.kw-0 { background: oklch(0.80 0.15 25 / 0.40); }
    mark.search-mark.kw-1 { background: oklch(0.83 0.15 70 / 0.40); }
    mark.search-mark.kw-2 { background: oklch(0.86 0.16 110 / 0.42); }
    mark.search-mark.kw-3 { background: oklch(0.80 0.14 150 / 0.40); }
    mark.search-mark.kw-4 { background: oklch(0.80 0.13 200 / 0.40); }
    mark.search-mark.kw-5 { background: oklch(0.78 0.14 260 / 0.44); }
    mark.search-mark.kw-6 { background: oklch(0.78 0.16 310 / 0.44); }
    mark.search-mark.kw-7 { background: oklch(0.80 0.16 350 / 0.42); }
    /* Focused hit keeps its keyword color; outline + weight mark it current. */
    mark.search-mark.current {
      outline: 2px solid var(--text);
      outline-offset: 0;
      font-weight: 700;
    }
    /* Keyword color chips + needle tints share the same hues. */
    .search-file-chip {
      display: inline-block; padding: 0 5px; margin-right: 3px;
      border-radius: 4px; font-size: 10px; line-height: 15px; color: var(--text);
      max-width: 90px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: middle;
    }
    .search-file-chip.kw-0, .search-hit-needle.kw-0 { background: oklch(0.80 0.15 25 / 0.55); }
    .search-file-chip.kw-1, .search-hit-needle.kw-1 { background: oklch(0.83 0.15 70 / 0.55); }
    .search-file-chip.kw-2, .search-hit-needle.kw-2 { background: oklch(0.86 0.16 110 / 0.55); }
    .search-file-chip.kw-3, .search-hit-needle.kw-3 { background: oklch(0.80 0.14 150 / 0.55); }
    .search-file-chip.kw-4, .search-hit-needle.kw-4 { background: oklch(0.80 0.13 200 / 0.55); }
    .search-file-chip.kw-5, .search-hit-needle.kw-5 { background: oklch(0.78 0.14 260 / 0.58); }
    .search-file-chip.kw-6, .search-hit-needle.kw-6 { background: oklch(0.78 0.16 310 / 0.58); }
    .search-file-chip.kw-7, .search-hit-needle.kw-7 { background: oklch(0.80 0.16 350 / 0.55); }
    .search-all-files-btn {
      display: block; width: 100%; margin-top: 6px; padding: 5px 8px;
      border: 1px solid var(--line); border-radius: 6px;
      background: var(--panel); color: var(--text); cursor: pointer; font-size: 12px;
    }
    .search-all-files-btn:hover { border-color: var(--accent); }
```

- [ ] **Step 2: JS 파서/평가기 추가 (highlightInFile 바로 앞)**

`web.go:6308`의 `function highlightInFile(needle) {` **바로 앞 줄**에 삽입:
```javascript
    // parseSearchExpr mirrors the Go parser (search_expr.go): same grammar,
    // same precedence (AND binds tighter than OR), quotes escape operators,
    // unbalanced input falls back to a literal whole-string term. Returns
    // { root, terms, colorOf } where colorOf maps a lowercased term to its
    // color index (encounter order).
    function parseSearchExpr(q) {
      const toks = [];
      let buf = "";
      function flush() {
        const s = buf.trim();
        buf = "";
        if (s) toks.push({ kind: "term", text: s.toLowerCase() });
      }
      const runes = Array.from(q || "");
      for (let i = 0; i < runes.length; i++) {
        const c = runes[i];
        if (c === '"') {
          let j = i + 1, inner = "";
          while (j < runes.length && runes[j] !== '"') { inner += runes[j]; j++; }
          if (inner.trim()) { flush(); toks.push({ kind: "term", text: inner.toLowerCase() }); }
          i = j;
        } else if (c === "&") { flush(); toks.push({ kind: "and" }); }
        else if (c === "|") { flush(); toks.push({ kind: "or" }); }
        else if (c === "(") { flush(); toks.push({ kind: "lparen" }); }
        else if (c === ")") { flush(); toks.push({ kind: "rparen" }); }
        else { buf += c; }
      }
      flush();
      const st = { pos: 0, err: false };
      function peek() { return st.pos < toks.length ? toks[st.pos] : null; }
      function parseAtom() {
        const t = peek();
        if (!t) return null;
        if (t.kind === "lparen") {
          st.pos++;
          const inner = parseOr();
          const close = peek();
          if (!inner || !close || close.kind !== "rparen") { st.err = true; return null; }
          st.pos++;
          return inner;
        }
        if (t.kind === "term") { st.pos++; return { op: "term", text: t.text }; }
        return null;
      }
      function parseAnd() {
        let left = parseAtom();
        if (!left) return null;
        const kids = [left];
        for (let t = peek(); t && t.kind === "and"; t = peek()) {
          st.pos++;
          const r = parseAtom();
          if (!r) { st.err = true; return null; }
          kids.push(r);
        }
        return kids.length === 1 ? kids[0] : { op: "and", kids: kids };
      }
      function parseOr() {
        let left = parseAnd();
        if (!left) { st.err = true; return null; }
        const kids = [left];
        for (let t = peek(); t && t.kind === "or"; t = peek()) {
          st.pos++;
          const r = parseAnd();
          if (!r) { st.err = true; return null; }
          kids.push(r);
        }
        return kids.length === 1 ? kids[0] : { op: "or", kids: kids };
      }
      let root = parseOr();
      if (!root || st.err || st.pos !== toks.length) {
        const lit = (q || "").trim().toLowerCase();
        root = { op: "term", text: lit };
      }
      const terms = [], colorOf = new Map();
      (function walk(n) {
        if (n.op === "term") {
          if (n.text && !colorOf.has(n.text)) { colorOf.set(n.text, terms.length); terms.push(n.text); }
          return;
        }
        for (const k of n.kids) walk(k);
      })(root);
      return { root: root, terms: terms, colorOf: colorOf };
    }

    // evalExpr mirrors exprNode.eval in Go.
    function evalExpr(node, lowerText) {
      if (node.op === "term") return !!node.text && lowerText.indexOf(node.text) >= 0;
      if (node.op === "and") { for (const k of node.kids) if (!evalExpr(k, lowerText)) return false; return true; }
      if (node.op === "or") { for (const k of node.kids) if (evalExpr(k, lowerText)) return true; return false; }
      return false;
    }

    // rowKeyFor returns the nearest "row" element for a text node: a code/table
    // <tr>, else the nearest [data-source-line] block, else previewBodyEl.
    function rowKeyFor(node) {
      let el = node.parentElement;
      while (el && el !== previewBodyEl) {
        if (el.tagName === "TR") return el;
        if (el.getAttribute && el.getAttribute("data-source-line") != null) return el;
        el = el.parentElement;
      }
      return previewBodyEl;
    }
```

- [ ] **Step 3: 빌드만 확인 (런타임 통합은 Task 4)**

Run: `go build -o /tmp/mdviewer-check . && echo OK`
Expected: `OK` (백틱 미사용·문법 오류 없음 확인).

- [ ] **Step 4: 커밋**

```bash
git add web.go
git commit -m "feat(search): JS expression parser mirror + keyword color palette"
```

---

## Task 4: highlightInFile 행 단위 재작성

**Files:**
- Modify: `web.go:6308-6367` (highlightInFile), `web.go:6497-6499` (renderInFileResults needle 색상)

- [ ] **Step 1: highlightInFile 본문 교체**

`web.go:6308-6367`의 `function highlightInFile(needle) { … }` 전체를 교체:
```javascript
    function highlightInFile(needle) {
      clearInFileHighlights();
      const parsed = parseSearchExpr(needle);
      if (!parsed || !parsed.terms.length) { state.searchInFileHits = []; state.searchInFileFocus = -1; return []; }
      // 1. Group every text node by its "row" element, preserving doc order.
      const rows = new Map();
      walkTextNodes(previewBodyEl, function (n) {
        const key = rowKeyFor(n);
        let row = rows.get(key);
        if (!row) { row = { text: "", map: [] }; rows.set(key, row); }
        const v = n.nodeValue || "";
        row.map.push({ node: n, start: row.text.length, end: row.text.length + v.length });
        row.text += v;
      });
      const allHits = [];
      // 2. Evaluate each row; highlight every term occurrence in qualifying rows.
      for (const row of rows.values()) {
        const lower = row.text.toLowerCase();
        if (!evalExpr(parsed.root, lower)) continue;
        const segs = [];
        for (const term of parsed.terms) {
          if (!term) continue;
          let i = lower.indexOf(term);
          while (i >= 0) { segs.push([i, i + term.length, term]); i = lower.indexOf(term, i + term.length); }
        }
        if (!segs.length) continue;
        // Earliest start first; on a tie, the longer match wins. Drop overlaps.
        segs.sort(function (a, b) { return a[0] - b[0] || b[1] - a[1]; });
        const chosen = [];
        let lastEnd = -1;
        for (const s of segs) { if (s[0] >= lastEnd) { chosen.push(s); lastEnd = s[1]; } }
        const rowHits = chosen.map(function (s) {
          return { marks: [], line: null, score: 0, term: s[2], colorIdx: parsed.colorOf.get(s[2]) % 8,
                   text: row.text.slice(s[0], s[1]),
                   before: row.text.slice(Math.max(0, s[0] - 40), s[0]),
                   after: row.text.slice(s[1], s[1] + 40) };
        });
        // 3. Wrap each match's spanning segments per original node within the row.
        for (const entry of row.map) {
          const node = entry.node, text = node.nodeValue || "";
          const local = [];
          for (let ci = 0; ci < chosen.length; ci++) {
            const a = Math.max(chosen[ci][0], entry.start), b = Math.min(chosen[ci][1], entry.end);
            if (a < b) local.push([a - entry.start, b - entry.start, ci]);
          }
          if (!local.length) continue;
          const parent = node.parentNode;
          if (!parent) continue;
          const frag = document.createDocumentFragment();
          let cur = 0;
          for (const seg of local) {
            if (seg[0] > cur) frag.appendChild(document.createTextNode(text.slice(cur, seg[0])));
            const mark = document.createElement("mark");
            mark.className = "search-mark kw-" + rowHits[seg[2]].colorIdx;
            mark.textContent = text.slice(seg[0], seg[1]);
            frag.appendChild(mark);
            rowHits[seg[2]].marks.push(mark);
            cur = seg[1];
          }
          if (cur < text.length) frag.appendChild(document.createTextNode(text.slice(cur)));
          parent.replaceChild(frag, node);
        }
        for (const h of rowHits) allHits.push(h);
      }
      // 4. Resolve line + priority per hit (from its first mark).
      for (const h of allHits) {
        const m0 = h.marks[0];
        h.line = m0 ? lineNumberForHit(m0) : null;
        h.score = m0 ? priorityForHit(h) : 0;
      }
      state.searchInFileHits = allHits;
      state.searchInFileFocus = -1;
      return allHits;
    }
```

- [ ] **Step 2: renderInFileResults — needle 스니펫에 키워드 색 적용**

`web.go:6497-6499`의 세 줄:
```javascript
        const hit = document.createElement("span");
        hit.className = "search-hit-needle";
        hit.textContent = h.text;
```
을 교체:
```javascript
        const hit = document.createElement("span");
        hit.className = "search-hit-needle" + (h.colorIdx != null ? " kw-" + h.colorIdx : "");
        hit.textContent = h.text;
```

- [ ] **Step 3: 빌드 확인**

Run: `go build -o /tmp/mdviewer-check . && echo OK`
Expected: `OK`.

- [ ] **Step 4: 브라우저 수동 검증**

Run: `go run . <적당한 마크다운 폴더 경로>` 로 띄운 뒤(또는 기존 실행 방식) 검색 패널에서 확인:
- `A|B` 입력 → A·B가 **서로 다른 색**으로, 각각을 포함한 줄/문단에 하이라이트.
- `A&B` 입력 → A·B가 **같은 블록(행)에 함께 있는 경우만** 하이라이트, 다른 블록의 단독 A는 미표시.
- `(A&B)|C` → 조합 동작.
- 단일 키워드(`A`) → 기존과 동일하게 전부 하이라이트.
- 히트 클릭 시 포커스 외곽선 표시, 키워드 색 유지.
Expected: 위 동작 모두 정상. 콘솔 에러 없음.

- [ ] **Step 5: 커밋**

```bash
git add web.go
git commit -m "feat(search): row-based multi-keyword in-file highlighting"
```

---

## Task 5: 폴더 검색 3단 스코프 + 전체검색 버튼 + 색상 칩

**Files:**
- Modify: `web.go:4443-4444` (스코프 버튼 HTML), `web.go:4775` (state 읽기), `web.go` state 객체에 folderSearchAllFiles 추가, `web.go:6526-6584` (runFolderSearch), `web.go:8926-8954` (입력 핸들러 + applyFolderScope)

- [ ] **Step 1: 스코프 버튼 HTML에 "하위폴더" 추가**

`web.go:4443-4444` 두 버튼 사이에 중간 버튼을 넣어 3개로 만든다. 4443-4444를 교체:
```html
                <button type="button" class="search-sort-btn active" id="searchScopeFolder" data-scope="folder" data-i18n="scopeFolder" data-i18n-title="scopeFolderTitle" title="Search the current folder only">This folder</button>
                <button type="button" class="search-sort-btn" id="searchScopeTree" data-scope="tree" data-i18n="scopeTree" data-i18n-title="scopeTreeTitle" title="Search this folder and all subfolders">Subfolder</button>
                <button type="button" class="search-sort-btn" id="searchScopeGit" data-scope="git" data-i18n="scopeGit" data-i18n-title="scopeGitTitle" title="Search the whole enclosing Git repo">Git repo</button>
```

- [ ] **Step 2: state 필드 추가 + 스코프 읽기 확장**

`web.go:4775`:
```javascript
      folderSearchScope: (localStorage.getItem("mdviewer.folderSearchScope") === "git") ? "git" : "folder",
```
를 교체:
```javascript
      folderSearchScope: (function () { const v = localStorage.getItem("mdviewer.folderSearchScope"); return (v === "git" || v === "tree") ? v : "folder"; })(),
      folderSearchAllFiles: false,
```

- [ ] **Step 3: applyFolderScope 3단 지원 + allFiles 리셋**

`web.go:8936-8955`의 `applyFolderScope` 함수와 이어지는 버튼 바인딩 블록을 교체:
```javascript
    function applyFolderScope(scope) {
      let next = (scope === "git") ? "git" : (scope === "tree") ? "tree" : "folder";
      if (next === "git" && state.gitRepoRoot === "") next = "folder"; // not a repo → ignore git
      state.folderSearchScope = next;
      state.folderSearchAllFiles = false; // scope change resets the docs-only filter
      try { localStorage.setItem("mdviewer.folderSearchScope", state.folderSearchScope); } catch (e) {}
      const btnFolder = document.getElementById("searchScopeFolder");
      const btnTree = document.getElementById("searchScopeTree");
      const btnGit = document.getElementById("searchScopeGit");
      const titleEl = document.getElementById("searchFolderTitle");
      if (btnFolder) btnFolder.classList.toggle("active", state.folderSearchScope === "folder");
      if (btnTree) btnTree.classList.toggle("active", state.folderSearchScope === "tree");
      if (btnGit) btnGit.classList.toggle("active", state.folderSearchScope === "git");
      if (titleEl) titleEl.textContent = state.folderSearchScope === "git" ? t("folderGit")
                                       : state.folderSearchScope === "tree" ? t("folderTree") : t("folderSame");
      if (state.searchQueryRight) runFolderSearch(state.searchQueryRight);
    }
    {
      const btnFolder = document.getElementById("searchScopeFolder");
      const btnTree = document.getElementById("searchScopeTree");
      const btnGit = document.getElementById("searchScopeGit");
      if (btnFolder) btnFolder.addEventListener("click", function () { applyFolderScope("folder"); });
      if (btnTree) btnTree.addEventListener("click", function () { applyFolderScope("tree"); });
      if (btnGit) btnGit.addEventListener("click", function () { applyFolderScope("git"); });
      applyFolderScope(state.folderSearchScope); // set initial active button + title
    }
```

- [ ] **Step 4: 입력 핸들러에서 allFiles 리셋**

`web.go:8926-8927`:
```javascript
    searchPanelInputEl.addEventListener("input", function () {
      state.searchQueryRight = searchPanelInputEl.value || "";
```
를 교체:
```javascript
    searchPanelInputEl.addEventListener("input", function () {
      state.searchQueryRight = searchPanelInputEl.value || "";
      state.folderSearchAllFiles = false; // a new query resets the docs-only filter
```

- [ ] **Step 5: runFolderSearch — scope/allFiles URL, 칩, 전체검색 버튼**

`web.go:6526-6584`의 `async function runFolderSearch(needle) { … }` 전체를 교체:
```javascript
    async function runFolderSearch(needle) {
      searchFolderHitsEl.innerHTML = "";
      if (!needle) return;
      if (searchFolderAbort) { try { searchFolderAbort.abort(); } catch (e) {} }
      const ctrl = new AbortController();
      searchFolderAbort = ctrl;
      const parsed = parseSearchExpr(needle);
      const scope = state.folderSearchScope;
      const recursive = (scope === "tree" || scope === "git");
      // Git-wide search can take a while; show a loading hint until results land.
      const loadingEl = document.createElement("div");
      loadingEl.className = "search-empty";
      loadingEl.textContent = scope === "git" ? t("searchLoadingGit") : t("searchLoading");
      searchFolderHitsEl.appendChild(loadingEl);
      let results = [];
      try {
        const url = "/api/search?dir=" + encodeURIComponent(state.cwd || "") +
                    "&q=" + encodeURIComponent(needle) +
                    (scope === "git" ? "&scope=git" : scope === "tree" ? "&scope=tree" : "") +
                    (recursive && state.folderSearchAllFiles ? "&allFiles=1" : "");
        const r = await fetch(url, { signal: ctrl.signal });
        if (!r.ok) throw new Error(String(r.status));
        results = await r.json();
      } catch (err) {
        if (err && err.name === "AbortError") return;
        searchFolderHitsEl.innerHTML = "";
        const e = document.createElement("div");
        e.className = "search-empty";
        e.textContent = "Search failed.";
        searchFolderHitsEl.appendChild(e);
        return;
      }
      searchFolderHitsEl.innerHTML = ""; // clear the loading hint
      // Hide the currently-open file from the cross-file list — its
      // matches are already in the "In this file" section.
      const filtered = results.filter(function (r) {
        return r.path !== state.selectedPath;
      });
      if (!filtered.length) {
        const e = document.createElement("div");
        e.className = "search-empty";
        e.textContent = "No matches in other files.";
        searchFolderHitsEl.appendChild(e);
      } else {
        for (const r of filtered) {
          const row = document.createElement("div");
          row.className = "search-file-row";
          row.title = r.path;
          const name = document.createElement("span");
          name.textContent = r.path.split("/").pop();
          row.appendChild(name);
          // Color chips: one per matched keyword, tinted to match in-file colors.
          const terms = r.matchedTerms || [];
          for (const term of terms) {
            const idx = parsed.colorOf.has(term) ? (parsed.colorOf.get(term) % 8) : 0;
            const chip = document.createElement("span");
            chip.className = "search-file-chip kw-" + idx;
            chip.textContent = term;
            chip.title = term;
            row.appendChild(chip);
          }
          const count = document.createElement("span");
          count.className = "search-file-count";
          count.textContent = r.count + (r.count === 1 ? " match" : " matches");
          row.appendChild(count);
          row.addEventListener("click", function () {
            selectFile(r.path, { historyMode: "push" });
          });
          searchFolderHitsEl.appendChild(row);
        }
      }
      // "Search all files" button: only for recursive scopes still in docs-only mode.
      if (recursive && !state.folderSearchAllFiles) {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "search-all-files-btn";
        btn.textContent = t("searchAllFiles");
        btn.addEventListener("click", function () {
          state.folderSearchAllFiles = true;
          runFolderSearch(state.searchQueryRight);
        });
        searchFolderHitsEl.appendChild(btn);
      }
    }
```

- [ ] **Step 6: 빌드 확인**

Run: `go build -o /tmp/mdviewer-check . && echo OK`
Expected: `OK`.

- [ ] **Step 7: 커밋**

```bash
git add web.go
git commit -m "feat(search): subfolder scope, search-all-files button, color chips"
```

---

## Task 6: i18n 키 추가 (EN + KO)

**Files:**
- Modify: `web.go:6712-6713` (EN dict), `web.go:6832-6833` (KO dict)

- [ ] **Step 1: EN 사전에 키 추가**

`web.go:6712`:
```javascript
        scopeFolder: "This folder", scopeFolderTitle: "Search the current folder only", scopeGit: "Git repo", scopeGitTitle: "Search the whole enclosing Git repo",
        folderSame: "Same folder", folderGit: "Git repo",
```
를 교체:
```javascript
        scopeFolder: "This folder", scopeFolderTitle: "Search the current folder only", scopeGit: "Git repo", scopeGitTitle: "Search the whole enclosing Git repo",
        scopeTree: "Subfolder", scopeTreeTitle: "Search this folder and all subfolders",
        folderSame: "Same folder", folderGit: "Git repo", folderTree: "Subfolders",
        searchAllFiles: "Search all files (incl. code)",
```

- [ ] **Step 2: KO 사전에 키 추가**

`web.go:6832`:
```javascript
        scopeFolder: "이 폴더", scopeFolderTitle: "현재 폴더만 검색", scopeGit: "Git 전체", scopeGitTitle: "상위 Git 저장소 전체 검색",
        folderSame: "같은 폴더", folderGit: "Git 저장소",
```
를 교체:
```javascript
        scopeFolder: "이 폴더", scopeFolderTitle: "현재 폴더만 검색", scopeGit: "Git 전체", scopeGitTitle: "상위 Git 저장소 전체 검색",
        scopeTree: "하위폴더", scopeTreeTitle: "이 폴더와 모든 하위폴더 검색",
        folderSame: "같은 폴더", folderGit: "Git 저장소", folderTree: "하위폴더",
        searchAllFiles: "전체 파일 검색 (코드 포함)",
```

- [ ] **Step 3: 빌드 + 전체 테스트**

Run: `go build -o /tmp/mdviewer-check . && go test ./... && echo OK`
Expected: `OK` (빌드 성공, 모든 테스트 통과).

- [ ] **Step 4: 브라우저 최종 검증**

띄운 뒤 검색 패널에서:
- 스코프 토글이 `이 폴더 / 하위폴더 / Git 전체` 3단으로 표시, 클릭 전환 동작.
- `하위폴더`/`Git 전체`에서 `A|B` 검색 → 문서(.md/.txt)만 결과, 각 파일에 키워드 색상 칩 표시, 목록 아래 "전체 파일 검색 (코드 포함)" 버튼.
- 버튼 클릭 → 코드 파일 포함해 재검색, 버튼 사라짐.
- `이 폴더`에서는 버튼 없음(항상 전체).
- 언어 토글(EN/KO) 시 라벨 정상.
Expected: 모두 정상.

- [ ] **Step 5: 커밋**

```bash
git add web.go
git commit -m "feat(search): i18n for subfolder scope + search-all-files"
```

---

## Self-Review 결과

- **Spec coverage:** §1 파서/색상→Task 1·3; §2 in-file 행 평가→Task 4; §3 스코프3단/문서필터/칩/버튼→Task 2·5; §4 제약(백틱·i18n·state)→Task 3·5·6; §5 테스트→Task 1·2. 누락 없음.
- **Placeholder scan:** TBD/TODO 없음, 모든 코드 스텝에 실제 코드 포함.
- **Type consistency:** `exprNode{op,text,kids}`·`parseSearchExpr`·`eval`(Go) / `{root,terms,colorOf}`·`evalExpr`·`rowKeyFor`·`highlightInFile`(JS) / `searchResult.MatchedTerms`·`searchFileForExpr`·`searchDirShallow(dir,expr,terms)`·`searchTreeRecursive(dirRoot,expr,terms,maxFiles,docsOnly)`가 전 태스크에서 일관.
- **알려진 변경점:** 폴더 `count`=만족 줄 수(기존 테스트는 동일값), 리터럴 `&`/`|`는 따옴표 필요.
