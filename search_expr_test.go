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
