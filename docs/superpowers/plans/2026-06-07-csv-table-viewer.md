# CSV/TSV Table Viewer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render `.csv`/`.tsv` files as a read-only, paginated table with a sticky header and page-size selector, backed by a cached byte-offset index so unchanged files page without re-scanning.

**Architecture:** Add a `csv` file kind. `handleFile` returns metadata only; a new `GET /api/csv` endpoint serves one page of rows plus the total row count. A per-server cache stores a quote-aware byte-offset index keyed by abs path, invalidated by modTime+size, capped at 16 entries (LRU). The frontend gets a `renderCsv` branch that fetches pages and draws a table with pagination controls.

**Tech Stack:** Go (`net/http`, `encoding/csv`, `sync`), embedded vanilla JS/CSS/HTML in `web.go`.

Spec: `docs/superpowers/specs/2026-06-07-csv-table-viewer-design.md`

---

## File Structure

- `web.go` — all backend + embedded frontend (single file, existing convention). Add: `csvResponse`/`csvIndex`/`csvCache` types, `scanCSVRecordOffsets`, `buildCSVIndex`, `readCSVPage`, `handleCSV`, route registration, `handleFile` kind change, `handleSaveFile` allow-list change, frontend `renderCsv` + branch + CSS + i18n.
- `csv_test.go` — new test file for the scanner, page reads, cache behavior, and the HTTP handler.

> Note: the frontend is embedded inside `web.go` as a Go string. **Backticks are forbidden** in that string; use normal double-quoted JS strings. i18n uses the `i18nDict()` / `t()` / `data-i18n` structure, and several lists are duplicated in 3 places — keep them in sync.

---

## Task 1: Quote-aware record offset scanner

**Files:**
- Modify: `web.go` (add `scanCSVRecordOffsets` near the other file handlers, e.g. after `handleRaw`)
- Test: `csv_test.go` (create)

- [ ] **Step 1: Write the failing tests**

Create `csv_test.go`:

```go
package main

import (
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestScanCSVRecordOffsets -v`
Expected: FAIL — `undefined: scanCSVRecordOffsets`

- [ ] **Step 3: Write minimal implementation**

Add to `web.go`:

```go
// scanCSVRecordOffsets returns the byte offset of the start of each CSV
// record in r. Newlines inside double-quoted fields are not treated as
// record terminators. A trailing newline does not produce a final empty
// record. The first offset (0) corresponds to the header record.
func scanCSVRecordOffsets(r io.Reader) ([]int64, error) {
	br := bufio.NewReader(r)
	var offsets []int64
	var pos int64
	inQuote := false
	atRecordStart := true
	for {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if atRecordStart {
			offsets = append(offsets, pos)
			atRecordStart = false
		}
		switch b {
		case '"':
			inQuote = !inQuote
		case '\n':
			if !inQuote {
				atRecordStart = true
			}
		}
		pos++
	}
	return offsets, nil
}
```

Ensure `web.go` imports include `bufio` and `io` (add if missing).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TestScanCSVRecordOffsets -v`
Expected: PASS (all sub-tests)

- [ ] **Step 5: Commit**

```bash
git add web.go csv_test.go
git commit -m "feat(csv): quote-aware record offset scanner"
```

---

## Task 2: csvIndex cache with build + invalidation + LRU

**Files:**
- Modify: `web.go` (add `csvIndex`, `csvCache` types; add `csv csvCache` field to `webServer`; add `buildCSVIndex`, `csvCache.get`)
- Test: `csv_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `csv_test.go`:

```go
import (
	"os"
	"path/filepath"
)

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestCSVCache -v`
Expected: FAIL — `undefined: csvCache` / `cache.get`

- [ ] **Step 3: Write minimal implementation**

Add the cache field to `webServer` (in `web.go`):

```go
type webServer struct {
	startDir string
	appRoot  string
	csv      csvCache
}
```

Add types and methods to `web.go`:

```go
const csvCacheCap = 16

type csvIndex struct {
	modTime time.Time
	size    int64
	header  []string
	offsets []int64 // byte offset of each DATA record start (header excluded)
	total   int     // data rows (== len(offsets))
	delim   rune
}

type csvCache struct {
	mu     sync.Mutex
	m      map[string]*csvIndex
	order  []string // LRU; most-recently-used at the end
	builds int      // test instrumentation: number of (re)builds
}

// get returns a cached index for absPath, rebuilding if the file's modTime or
// size changed since the cached entry. Safe for concurrent use; zero value ready.
func (c *csvCache) get(absPath string, delim rune) (*csvIndex, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]*csvIndex)
	}
	if idx, ok := c.m[absPath]; ok &&
		idx.modTime.Equal(info.ModTime()) && idx.size == info.Size() && idx.delim == delim {
		c.touch(absPath)
		return idx, nil
	}
	idx, err := buildCSVIndex(absPath, delim, info)
	if err != nil {
		return nil, err
	}
	c.builds++
	c.m[absPath] = idx
	c.touch(absPath)
	c.evict()
	return idx, nil
}

func (c *csvCache) touch(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

func (c *csvCache) evict() {
	for len(c.order) > csvCacheCap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.m, oldest)
	}
}

// buildCSVIndex scans the whole file once to build the offset index and parse
// the header.
func buildCSVIndex(absPath string, delim rune, info os.FileInfo) (*csvIndex, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	all, err := scanCSVRecordOffsets(f)
	if err != nil {
		return nil, err
	}

	idx := &csvIndex{
		modTime: info.ModTime(),
		size:    info.Size(),
		delim:   delim,
	}
	if len(all) == 0 {
		return idx, nil // empty file: no header, no rows
	}

	// Parse the header record (record 0).
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hr := csv.NewReader(f)
	hr.Comma = delim
	hr.FieldsPerRecord = -1
	hr.LazyQuotes = true
	header, err := hr.Read()
	if err != nil && err != io.EOF {
		return nil, err
	}
	idx.header = header

	// Data record offsets exclude the header.
	idx.offsets = all[1:]
	idx.total = len(idx.offsets)
	return idx, nil
}
```

Ensure `web.go` imports include `encoding/csv`, `sync`, `time` (add if missing).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TestCSVCache -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web.go csv_test.go
git commit -m "feat(csv): cached offset index with modTime+size invalidation and LRU cap"
```

---

## Task 3: readCSVPage — seek to page start and read N rows

**Files:**
- Modify: `web.go` (add `readCSVPage`)
- Test: `csv_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `csv_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestReadCSVPage -v`
Expected: FAIL — `undefined: readCSVPage`

- [ ] **Step 3: Write minimal implementation**

Add to `web.go`:

```go
// readCSVPage seeks to data row `offset` and returns up to `limit` rows.
// Returns an empty slice if offset is at or beyond the end.
func readCSVPage(absPath string, idx *csvIndex, offset, limit int) ([][]string, error) {
	if offset < 0 || offset >= len(idx.offsets) || limit <= 0 {
		return [][]string{}, nil
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(idx.offsets[offset], io.SeekStart); err != nil {
		return nil, err
	}
	rd := csv.NewReader(f)
	rd.Comma = idx.delim
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true

	rows := make([][]string, 0, limit)
	for len(rows) < limit {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, rec)
	}
	return rows, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TestReadCSVPage -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web.go csv_test.go
git commit -m "feat(csv): readCSVPage seeks to page start via cached offsets"
```

---

## Task 4: handleCSV HTTP endpoint + route + kind wiring

**Files:**
- Modify: `web.go` (add `csvResponse`, `handleCSV`; register route; change `handleFile` for `.csv`/`.tsv`; remove `.csv`/`.tsv` from `handleSaveFile` allow-list)
- Test: `csv_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `csv_test.go`:

```go
import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
)

func TestHandleCSVPagination(t *testing.T) {
	p := writeTempCSV(t, "name,age\nA,1\nB,2\nC,3\n")
	s := &webServer{appRoot: t.TempDir()}

	req := httptest.NewRequest("GET", "/api/csv?path="+url.QueryEscape(p)+"&page=1&page_size=2", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp csvResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalRows != 3 {
		t.Fatalf("total = %d, want 3", resp.TotalRows)
	}
	if !reflect.DeepEqual(resp.Header, []string{"name", "age"}) {
		t.Fatalf("header = %v", resp.Header)
	}
	if len(resp.Rows) != 2 || resp.Rows[0][0] != "A" || resp.Rows[1][0] != "B" {
		t.Fatalf("page1 rows = %v", resp.Rows)
	}

	// page 2 → just C
	req = httptest.NewRequest("GET", "/api/csv?path="+url.QueryEscape(p)+"&page=2&page_size=2", nil)
	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) != 1 || resp.Rows[0][0] != "C" {
		t.Fatalf("page2 rows = %v", resp.Rows)
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run "TestHandleCSV|TestHandleFileMarksCSVKind" -v`
Expected: FAIL — `undefined: csvResponse` and kind mismatch

- [ ] **Step 3: Write minimal implementation**

Add the response type to `web.go`:

```go
type csvResponse struct {
	Path      string     `json:"path"`
	Delimiter string     `json:"delimiter"`
	Header    []string   `json:"header"`
	Rows      [][]string `json:"rows"`
	Page      int        `json:"page"`
	PageSize  int        `json:"page_size"`
	TotalRows int        `json:"total_rows"`
}
```

Register the route in `routes()` (next to the other `/api/...` lines):

```go
	mux.HandleFunc("/api/csv", s.handleCSV)
```

Add the handler to `web.go`:

```go
var csvPageSizes = map[int]bool{50: true, 100: true, 500: true}

func (s *webServer) handleCSV(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}

	delim := ','
	if strings.ToLower(filepath.Ext(absPath)) == ".tsv" {
		delim = '\t'
	}

	page := 1
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v >= 1 {
		page = v
	}
	pageSize := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && csvPageSizes[v] {
		pageSize = v
	}

	idx, err := s.csv.get(absPath, delim)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	offset := (page - 1) * pageSize
	rows, err := readCSVPage(absPath, idx, offset, pageSize)
	if err != nil {
		// Index/seek mismatch (rare LazyQuotes edge): drop cache and full re-parse.
		rows, err = fallbackCSVPage(absPath, delim, offset, pageSize)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	delimStr := ","
	if delim == '\t' {
		delimStr = "\t"
	}
	s.writeJSON(w, http.StatusOK, csvResponse{
		Path:      absPath,
		Delimiter: delimStr,
		Header:    idx.header,
		Rows:      rows,
		Page:      page,
		PageSize:  pageSize,
		TotalRows: idx.total,
	})
}

// fallbackCSVPage re-parses from the start, skipping `offset` data rows. Used
// when the cached offset index does not align with csv.Reader parsing.
func fallbackCSVPage(absPath string, delim rune, offset, limit int) ([][]string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rd := csv.NewReader(f)
	rd.Comma = delim
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true

	// Skip header.
	if _, err := rd.Read(); err != nil {
		if err == io.EOF {
			return [][]string{}, nil
		}
		return nil, err
	}
	// Skip `offset` data rows.
	for i := 0; i < offset; i++ {
		if _, err := rd.Read(); err != nil {
			if err == io.EOF {
				return [][]string{}, nil
			}
			return nil, err
		}
	}
	rows := make([][]string, 0, limit)
	for len(rows) < limit {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, rec)
	}
	return rows, nil
}
```

Ensure `web.go` imports include `strconv` (add if missing).

Change `handleFile` — remove `.csv`, `.tsv` from the big `text` case list (lines ~506-520) and add a dedicated case **before** the `text` case:

```go
	case ".csv", ".tsv":
		resp.Kind = "csv"
		// Table data is fetched separately via /api/csv (paginated).
```

Change `handleSaveFile` — remove `.csv`, `.tsv` from the allow-list (line ~584) so saving a CSV/TSV is rejected (read-only).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -v`
Expected: PASS (all tests, including existing memo/search tests)

- [ ] **Step 5: Commit**

```bash
git add web.go csv_test.go
git commit -m "feat(csv): /api/csv paginated endpoint + csv kind wiring + read-only"
```

---

## Task 5: Frontend — renderCsv branch, table, controls, styles

**Files:**
- Modify: `web.go` (embedded frontend: `renderPreview` branch, `renderCsv` + helpers, CSS, kind chip)

> No automated test (embedded JS). Verify by build + browser.

- [ ] **Step 1: Add the renderPreview branch**

In `renderPreview` (around line 7548, before the `data.kind === "text"` branch), add:

```js
      if (data.kind === "csv") { await renderCsv(data); return; }
```

- [ ] **Step 2: Add renderCsv and helpers**

Add near `renderCodeFile` (around line 6927). Use only double-quoted strings (no backticks):

```js
    var csvState = { path: null, page: 1, pageSize: 100, total: 0 };

    async function renderCsv(data) {
      csvState.path = data.path;
      csvState.page = 1;
      csvState.pageSize = 100;
      await loadCsvPage();
    }

    async function loadCsvPage() {
      var url = "/api/csv?path=" + encodeURIComponent(csvState.path) +
                "&page=" + csvState.page + "&page_size=" + csvState.pageSize;
      var resp;
      try {
        var res = await fetch(url);
        if (!res.ok) throw new Error(await res.text());
        resp = await res.json();
      } catch (e) {
        previewBodyEl.innerHTML = "<div class=\"empty\">" + t("csvError") + "</div>";
        return;
      }
      csvState.total = resp.total_rows;
      drawCsv(resp);
    }

    function drawCsv(resp) {
      var totalPages = Math.max(1, Math.ceil(resp.total_rows / resp.page_size));
      if (csvState.page > totalPages) { csvState.page = totalPages; }

      previewBodyEl.innerHTML = "";
      var wrap = document.createElement("div");
      wrap.className = "csv-wrap";

      // Controls
      var bar = document.createElement("div");
      bar.className = "csv-bar";

      var prev = document.createElement("button");
      prev.className = "csv-btn";
      prev.textContent = t("csvPrev");
      prev.disabled = csvState.page <= 1;
      prev.onclick = function () { if (csvState.page > 1) { csvState.page--; loadCsvPage(); } };

      var next = document.createElement("button");
      next.className = "csv-btn";
      next.textContent = t("csvNext");
      next.disabled = csvState.page >= totalPages;
      next.onclick = function () { if (csvState.page < totalPages) { csvState.page++; loadCsvPage(); } };

      var info = document.createElement("span");
      info.className = "csv-info";
      info.textContent = csvState.page + " / " + totalPages + " " + t("csvPage") +
                         " · " + resp.total_rows + " " + t("csvRows");

      var sizeLabel = document.createElement("span");
      sizeLabel.className = "csv-info";
      sizeLabel.textContent = t("csvPageSize") + ":";

      var sizeSel = document.createElement("select");
      sizeSel.className = "csv-select";
      [50, 100, 500].forEach(function (n) {
        var opt = document.createElement("option");
        opt.value = String(n);
        opt.textContent = String(n);
        if (n === resp.page_size) opt.selected = true;
        sizeSel.appendChild(opt);
      });
      sizeSel.onchange = function () {
        csvState.pageSize = parseInt(sizeSel.value, 10);
        csvState.page = 1;
        loadCsvPage();
      };

      bar.appendChild(prev);
      bar.appendChild(next);
      bar.appendChild(info);
      bar.appendChild(sizeLabel);
      bar.appendChild(sizeSel);
      wrap.appendChild(bar);

      // Table
      var scroll = document.createElement("div");
      scroll.className = "csv-scroll";
      var table = document.createElement("table");
      table.className = "csv-table";

      var thead = document.createElement("thead");
      var htr = document.createElement("tr");
      (resp.header || []).forEach(function (h) {
        var th = document.createElement("th");
        th.textContent = h;
        htr.appendChild(th);
      });
      thead.appendChild(htr);
      table.appendChild(thead);

      var tbody = document.createElement("tbody");
      (resp.rows || []).forEach(function (row) {
        var tr = document.createElement("tr");
        row.forEach(function (cell) {
          var td = document.createElement("td");
          td.textContent = cell;
          tr.appendChild(td);
        });
        tbody.appendChild(tr);
      });
      table.appendChild(tbody);
      scroll.appendChild(table);
      wrap.appendChild(scroll);

      previewBodyEl.appendChild(wrap);
    }
```

- [ ] **Step 3: Add CSS**

Add to the embedded `<style>` block (near other preview styles):

```css
    .csv-wrap { display: flex; flex-direction: column; gap: 8px; height: 100%; }
    .csv-bar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 4px 2px; }
    .csv-btn { padding: 4px 10px; border: 1px solid var(--border); border-radius: 6px; background: var(--panel); cursor: pointer; }
    .csv-btn:disabled { opacity: 0.4; cursor: default; }
    .csv-info { font-size: 12px; color: var(--muted); }
    .csv-select { padding: 3px 6px; border: 1px solid var(--border); border-radius: 6px; background: var(--panel); }
    .csv-scroll { overflow: auto; flex: 1; border: 1px solid var(--border); border-radius: 8px; }
    .csv-table { border-collapse: collapse; width: max-content; min-width: 100%; font-size: 13px; }
    .csv-table th, .csv-table td { border: 1px solid var(--border); padding: 4px 8px; text-align: left; max-width: 360px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: top; }
    .csv-table thead th { position: sticky; top: 0; background: var(--panel); z-index: 1; font-weight: 600; }
```

> If `--panel`/`--border`/`--muted` are not the exact CSS variable names in this file, substitute the actual ones used by other components (check existing `.chip`/preview styles).

- [ ] **Step 4: Add the kind chip color**

Near line 1804 (the other `.chip[data-kind=...]` rules), add:

```css
    .chip[data-kind="csv"]::before { background: oklch(0.74 0.15 195); box-shadow: 0 0 0 2px oklch(0.74 0.15 195 / 0.25); }
```

- [ ] **Step 5: Build to verify it compiles**

Run: `go build -o mdviewer .`
Expected: builds with no errors

- [ ] **Step 6: Commit**

```bash
git add web.go
git commit -m "feat(csv): frontend table renderer with pagination + sticky header"
```

---

## Task 6: i18n labels (EN/KO)

**Files:**
- Modify: `web.go` (embedded `i18nDict()` — EN and KO maps; both must get all 6 keys)

- [ ] **Step 1: Add the keys**

In the EN dictionary (around line 6173, near `phSearchFiles`):

```js
        csvPrev: "Previous", csvNext: "Next", csvPage: "page",
        csvRows: "rows", csvPageSize: "Page size", csvError: "Cannot display as a table.",
```

In the KO dictionary (around line 6289):

```js
        csvPrev: "이전", csvNext: "다음", csvPage: "페이지",
        csvRows: "행", csvPageSize: "페이지 크기", csvError: "표로 표시할 수 없습니다.",
```

> If there is a third place that mirrors these keys (the spec notes lists are duplicated in 3 places), add the same 6 keys there too. Grep for an existing key like `phSearchFiles` to find all locations: `grep -n "phSearchFiles" web.go`.

- [ ] **Step 2: Build to verify it compiles**

Run: `go build -o mdviewer .`
Expected: builds with no errors

- [ ] **Step 3: Commit**

```bash
git add web.go
git commit -m "feat(csv): i18n labels for pagination controls (EN/KO)"
```

---

## Task 7: Build + manual verification

**Files:** none (verification only)

- [ ] **Step 1: Full test + build**

Run: `go test ./... && go build -o mdviewer .`
Expected: all tests PASS, build succeeds

- [ ] **Step 2: Create a sample CSV**

```bash
printf 'id,name,note\n' > /tmp/sample.csv
for i in $(seq 1 250); do printf '%s,name%s,"a, quoted, cell"\n' "$i" "$i" >> /tmp/sample.csv; done
```

- [ ] **Step 3: Run the server and verify in browser**

Run: `./mdviewer web` (or the project's web run script — check `run-web.sh`)
Then open the served URL, navigate to `/tmp/sample.csv`, and confirm:
- renders as a table with header row `id name note`
- header stays fixed while scrolling rows
- Previous/Next move pages; counter shows "1 / N page · 250 rows"
- page-size selector (50/100/500) changes rows per page and resets to page 1
- quoted cell `a, quoted, cell` shows as a single cell
- the file is read-only (no edit/save affordance for csv)

- [ ] **Step 4: Verify TSV**

Create `/tmp/sample.tsv` (tab-separated) and confirm it also renders as a table.

- [ ] **Step 5: Final commit (if any tweaks were needed)**

```bash
git add -A
git commit -m "test(csv): manual verification fixes"
```

---

## Self-Review Notes

- **Spec coverage:** new `csv` kind (T4), `/api/csv` slicing + total rows (T3/T4), sticky header + page-size selector (T5), read-only (T4 save-list removal), offset-index cache + modTime/size invalidation + LRU (T2), quote-aware scanner (T1), fallback re-parse (T4), i18n (T6), tests (T1-T4). All covered.
- **Type consistency:** `csvIndex{modTime,size,header,offsets,total,delim}`, `csvCache.get`, `buildCSVIndex`, `readCSVPage(absPath, idx, offset, limit)`, `fallbackCSVPage(absPath, delim, offset, limit)`, `csvResponse` JSON tags match the frontend reads (`total_rows`, `page_size`, `header`, `rows`). Consistent across tasks.
- **Imports to verify present in web.go:** `bufio`, `io`, `encoding/csv`, `sync`, `time`, `strconv`. Add any missing.
