package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func memosFromResponse(t *testing.T, body *bytes.Buffer) []memo {
	t.Helper()
	var resp struct {
		Memos []memo `json:"memos"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Memos
}

func TestMemoHTTPRoundTrip(t *testing.T) {
	s := &webServer{appRoot: t.TempDir()}

	// Empty initially.
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/memos", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d", rec.Code)
	}
	if got := memosFromResponse(t, rec.Body); len(got) != 0 {
		t.Fatalf("initial memos = %+v, want empty", got)
	}

	// Save two memos.
	save := `{"memos":[
		{"id":"a","title":"A","body":"body a","createdAt":"2026-05-29T00:00:00Z","updatedAt":"2026-05-29T01:00:00Z"},
		{"id":"b","title":"","body":"body b","createdAt":"2026-05-29T00:00:00Z","updatedAt":"2026-05-29T02:00:00Z"}
	]}`
	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST", "/api/memos/save", bytes.NewBufferString(save)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save status %d", rec.Code)
	}
	got := memosFromResponse(t, rec.Body)
	if len(got) != 2 || got[0].ID != "b" {
		t.Fatalf("after save = %+v, want 2 memos sorted newest-first (b,a)", got)
	}

	// Upsert: newer body for a wins, stale write for b ignored.
	save2 := `{"memos":[
		{"id":"a","title":"A2","body":"new a","createdAt":"2026-05-29T00:00:00Z","updatedAt":"2026-05-29T05:00:00Z"},
		{"id":"b","title":"old","body":"stale","createdAt":"2026-05-29T00:00:00Z","updatedAt":"2026-05-28T00:00:00Z"}
	]}`
	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST", "/api/memos/save", bytes.NewBufferString(save2)))
	got = memosFromResponse(t, rec.Body)
	byID := map[string]memo{}
	for _, mm := range got {
		byID[mm.ID] = mm
	}
	if byID["a"].Body != "new a" {
		t.Errorf("memo a = %+v, want body 'new a'", byID["a"])
	}
	if byID["b"].Body != "body b" {
		t.Errorf("memo b clobbered by stale write: %+v", byID["b"])
	}

	// Delete a.
	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST", "/api/memos/delete", bytes.NewBufferString(`{"id":"a"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status %d", rec.Code)
	}
	got = memosFromResponse(t, rec.Body)
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("after delete = %+v, want only b", got)
	}

	// Persisted to disk.
	if disk := s.loadMemos(); len(disk) != 1 || disk[0].ID != "b" {
		t.Fatalf("on-disk = %+v, want only b", disk)
	}
}

func TestMemoSaveRejectsGET(t *testing.T) {
	s := &webServer{appRoot: t.TempDir()}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/memos/save", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/memos/save status = %d, want 405", rec.Code)
	}
}

func TestMemosRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &webServer{appRoot: dir}

	want := []memo{
		{ID: "a", Title: "first", Body: "body a", CreatedAt: "2026-05-29T00:00:00Z", UpdatedAt: "2026-05-29T00:00:00Z"},
		{ID: "b", Title: "", Body: "body b", CreatedAt: "2026-05-29T01:00:00Z", UpdatedAt: "2026-05-29T01:00:00Z"},
	}
	if err := s.saveMemos(want); err != nil {
		t.Fatalf("saveMemos: %v", err)
	}
	got := s.loadMemos()
	if len(got) != len(want) {
		t.Fatalf("loadMemos len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("memo %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMemosPinnedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &webServer{appRoot: dir}
	want := []memo{
		{ID: "p", Title: "pinned one", Body: "x", Pinned: true, CreatedAt: "2026-05-29T00:00:00Z", UpdatedAt: "2026-05-29T00:00:00Z"},
		{ID: "u", Title: "unpinned", Body: "y", Pinned: false, CreatedAt: "2026-05-29T00:00:00Z", UpdatedAt: "2026-05-29T00:00:00Z"},
	}
	if err := s.saveMemos(want); err != nil {
		t.Fatal(err)
	}
	got := s.loadMemos()
	if len(got) != 2 || !got[0].Pinned || got[1].Pinned {
		t.Fatalf("pinned not preserved across save/load: %+v", got)
	}
}

func TestMergeMemosPreservesPinned(t *testing.T) {
	existing := []memo{{ID: "a", Body: "a", Pinned: false, UpdatedAt: "2026-05-29T00:00:00Z"}}
	// Newer write flips pin on; must replace and keep Pinned=true.
	incoming := []memo{{ID: "a", Body: "a", Pinned: true, UpdatedAt: "2026-05-29T01:00:00Z"}}
	got := mergeMemos(existing, incoming)
	if len(got) != 1 || !got[0].Pinned {
		t.Fatalf("merge did not carry pin state: %+v", got)
	}
}

func TestLoadMemosMissingFile(t *testing.T) {
	s := &webServer{appRoot: t.TempDir()}
	if got := s.loadMemos(); got != nil {
		t.Errorf("loadMemos on missing file = %+v, want nil", got)
	}
}

func TestMemosSourceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &webServer{appRoot: dir}
	want := memo{
		ID: "s", Body: "captured text",
		SourcePath: "/notes/spec.md", SourceHash: "design", SourceHeading: "Design",
		CreatedAt: "2026-05-29T00:00:00Z", UpdatedAt: "2026-05-29T00:00:00Z",
	}
	if err := s.saveMemos([]memo{want}); err != nil {
		t.Fatal(err)
	}
	got := s.loadMemos()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("source fields not preserved: %+v", got)
	}
}

func TestMergeMemosPreservesSource(t *testing.T) {
	existing := []memo{{ID: "a", Body: "a", UpdatedAt: "2026-05-29T00:00:00Z"}}
	incoming := []memo{{ID: "a", Body: "a", SourcePath: "/x.md", SourceHash: "h", SourceHeading: "H", UpdatedAt: "2026-05-29T01:00:00Z"}}
	got := mergeMemos(existing, incoming)
	if len(got) != 1 || got[0].SourcePath != "/x.md" || got[0].SourceHash != "h" {
		t.Fatalf("merge dropped source fields: %+v", got)
	}
}

func TestLoadMemosSkipsBlankIDs(t *testing.T) {
	dir := t.TempDir()
	s := &webServer{appRoot: dir}
	if err := s.saveMemos([]memo{{ID: ""}, {ID: "keep", Body: "x"}}); err != nil {
		t.Fatal(err)
	}
	got := s.loadMemos()
	if len(got) != 1 || got[0].ID != "keep" {
		t.Errorf("loadMemos = %+v, want only id=keep", got)
	}
}

func TestMergeMemosUpsert(t *testing.T) {
	existing := []memo{
		{ID: "a", Body: "old a", UpdatedAt: "2026-05-29T00:00:00Z"},
		{ID: "b", Body: "b", UpdatedAt: "2026-05-29T00:00:00Z"},
	}
	incoming := []memo{
		{ID: "a", Body: "new a", UpdatedAt: "2026-05-29T02:00:00Z"}, // newer -> replaces
		{ID: "b", Body: "stale b", UpdatedAt: "2026-05-28T00:00:00Z"}, // older -> ignored
		{ID: "c", Body: "c", UpdatedAt: "2026-05-29T03:00:00Z"},      // new id -> appended
		{ID: "", Body: "noise"},                                      // blank id -> skipped
	}
	got := mergeMemos(existing, incoming)

	byID := map[string]memo{}
	for _, m := range got {
		byID[m.ID] = m
	}
	if len(got) != 3 {
		t.Fatalf("merged len = %d, want 3 (%+v)", len(got), got)
	}
	if byID["a"].Body != "new a" {
		t.Errorf("memo a not replaced by newer: %+v", byID["a"])
	}
	if byID["b"].Body != "b" {
		t.Errorf("memo b clobbered by older write: %+v", byID["b"])
	}
	if byID["c"].Body != "c" {
		t.Errorf("memo c not appended: %+v", byID["c"])
	}
}

func TestMergeMemosEqualUpdatedAtReplaces(t *testing.T) {
	// Equal timestamps: incoming wins (>=) so a re-save of the same memo sticks.
	existing := []memo{{ID: "a", Body: "old", UpdatedAt: "2026-05-29T00:00:00Z"}}
	incoming := []memo{{ID: "a", Body: "new", UpdatedAt: "2026-05-29T00:00:00Z"}}
	got := mergeMemos(existing, incoming)
	if len(got) != 1 || got[0].Body != "new" {
		t.Errorf("equal-timestamp merge = %+v, want body=new", got)
	}
}

func TestSortMemosByUpdatedDesc(t *testing.T) {
	memos := []memo{
		{ID: "old", UpdatedAt: "2026-05-29T00:00:00Z"},
		{ID: "new", UpdatedAt: "2026-05-29T05:00:00Z"},
		{ID: "mid", UpdatedAt: "2026-05-29T02:00:00Z"},
	}
	sortMemosByUpdatedDesc(memos)
	gotOrder := []string{memos[0].ID, memos[1].ID, memos[2].ID}
	wantOrder := []string{"new", "mid", "old"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Errorf("order = %v, want %v", gotOrder, wantOrder)
		}
	}
}
