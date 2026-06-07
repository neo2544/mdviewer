package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScanCSVRecordOffsets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []int64
	}{
		{"simple", "a,b\n1,2\n3,4\n", []int64{0, 4, 8}},
		{"no trailing newline", "a,b\n1,2", []int64{0, 4}},
		{"quoted comma", "a,b\n\"x,y\",2\n", []int64{0, 4}},
		{"quoted newline", "a,b\n\"x\ny\",2\n", []int64{0, 4}},
		{"crlf", "a,b\r\n1,2\r\n", []int64{0, 5}},
		{"header only", "a,b\n", []int64{0}},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := scanCSVRecordOffsets(strings.NewReader(c.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func writeTempCSV(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCSVCacheReuseAndInvalidate(t *testing.T) {
	p := writeTempCSV(t, "a,b\n1,2\n3,4\n")
	var cache csvCache

	idx1, err := cache.get(p, ',')
	if err != nil {
		t.Fatal(err)
	}
	if idx1.total != 2 {
		t.Fatalf("total = %d, want 2", idx1.total)
	}
	if !reflect.DeepEqual(idx1.header, []string{"a", "b"}) {
		t.Fatalf("header = %v, want [a b]", idx1.header)
	}
	builds1 := cache.builds

	// Same file unchanged → cache hit, no rebuild.
	if _, err := cache.get(p, ','); err != nil {
		t.Fatal(err)
	}
	if cache.builds != builds1 {
		t.Fatalf("rebuilt on unchanged file: builds %d -> %d", builds1, cache.builds)
	}

	// Modify file → rebuild, new total.
	// Bump size so the modTime+size check trips even at coarse mtime resolution.
	if err := os.WriteFile(p, []byte("a,b\n1,2\n3,4\n5,6\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx2, err := cache.get(p, ',')
	if err != nil {
		t.Fatal(err)
	}
	if idx2.total != 3 {
		t.Fatalf("total = %d, want 3", idx2.total)
	}
	if cache.builds == builds1 {
		t.Fatalf("expected rebuild after modification")
	}
}

func TestCSVCacheLRUCap(t *testing.T) {
	var cache csvCache
	for i := 0; i < 20; i++ {
		p := writeTempCSV(t, "a,b\n1,2\n")
		if _, err := cache.get(p, ','); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.m) > 16 {
		t.Fatalf("cache size = %d, want <= 16", len(cache.m))
	}
}

func TestReadCSVPage(t *testing.T) {
	p := writeTempCSV(t, "a,b\n1,2\n3,4\n5,6\n7,8\n")
	var cache csvCache
	idx, err := cache.get(p, ',')
	if err != nil {
		t.Fatal(err)
	}
	if idx.total != 4 {
		t.Fatalf("total = %d, want 4", idx.total)
	}

	// page 1, size 2 → rows 0,1
	rows, err := readCSVPage(p, idx, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"1", "2"}, {"3", "4"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("page1 = %v, want %v", rows, want)
	}

	// page 2, size 2 → rows 2,3 (last page)
	rows, err = readCSVPage(p, idx, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	want = [][]string{{"5", "6"}, {"7", "8"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("page2 = %v, want %v", rows, want)
	}

	// offset beyond range → empty
	rows, err = readCSVPage(p, idx, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("out-of-range page = %v, want empty", rows)
	}
}

func TestReadCSVPageQuotedNewline(t *testing.T) {
	p := writeTempCSV(t, "a,b\n\"x\ny\",2\n3,4\n")
	var cache csvCache
	idx, err := cache.get(p, ',')
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readCSVPage(p, idx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"x\ny", "2"}, {"3", "4"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
}

func TestHandleCSVPagination(t *testing.T) {
	// 75 data rows so that page_size=50 (an allowed size) spans two pages.
	var b strings.Builder
	b.WriteString("name,age\n")
	for i := 1; i <= 75; i++ {
		fmt.Fprintf(&b, "r%d,%d\n", i, i)
	}
	p := writeTempCSV(t, b.String())
	s := &webServer{appRoot: t.TempDir()}

	req := httptest.NewRequest("GET", "/api/csv?path="+url.QueryEscape(p)+"&page=1&page_size=50", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp csvResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalRows != 75 {
		t.Fatalf("total = %d, want 75", resp.TotalRows)
	}
	if !reflect.DeepEqual(resp.Header, []string{"name", "age"}) {
		t.Fatalf("header = %v", resp.Header)
	}
	if len(resp.Rows) != 50 || resp.Rows[0][0] != "r1" || resp.Rows[49][0] != "r50" {
		t.Fatalf("page1 rows (len %d) = %v", len(resp.Rows), resp.Rows)
	}

	// page 2 → remaining 25 rows (r51..r75)
	req = httptest.NewRequest("GET", "/api/csv?path="+url.QueryEscape(p)+"&page=2&page_size=50", nil)
	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) != 25 || resp.Rows[0][0] != "r51" || resp.Rows[24][0] != "r75" {
		t.Fatalf("page2 rows (len %d) = %v", len(resp.Rows), resp.Rows)
	}
}

func TestHandleCSVTSVAndDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.tsv")
	if err := os.WriteFile(p, []byte("a\tb\n1\t2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &webServer{appRoot: t.TempDir()}

	// invalid page_size → defaults to 100; tab delimiter auto-detected
	req := httptest.NewRequest("GET", "/api/csv?path="+url.QueryEscape(p)+"&page=0&page_size=7", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp csvResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Page != 1 || resp.PageSize != 100 {
		t.Fatalf("page/size = %d/%d, want 1/100", resp.Page, resp.PageSize)
	}
	if resp.Delimiter != "\t" {
		t.Fatalf("delimiter = %q, want tab", resp.Delimiter)
	}
	if resp.Rows[0][0] != "1" || resp.Rows[0][1] != "2" {
		t.Fatalf("rows = %v", resp.Rows)
	}
}

func TestHandleFileMarksCSVKind(t *testing.T) {
	p := writeTempCSV(t, "a,b\n1,2\n")
	s := &webServer{appRoot: t.TempDir()}
	req := httptest.NewRequest("GET", "/api/file?path="+url.QueryEscape(p), nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var resp fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Kind != "csv" {
		t.Fatalf("kind = %q, want csv", resp.Kind)
	}
	if resp.Content != "" {
		t.Fatalf("expected no inline content for csv, got %d bytes", len(resp.Content))
	}
}
