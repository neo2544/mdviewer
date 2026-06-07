package main

import (
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
