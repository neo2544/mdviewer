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
