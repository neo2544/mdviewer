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
