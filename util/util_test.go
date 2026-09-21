package util

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestConversionsAndHelpers(t *testing.T) {
	if got := ByteToString(StringToByte("goat")); got != "goat" {
		t.Fatalf("round trip = %q, want goat", got)
	}
	if got := Map([]int{1, 2, 3}, func(v int) int { return v * 2 }); !reflect.DeepEqual(got, []int{2, 4, 6}) {
		t.Fatalf("Map() = %v", got)
	}
	if AbsFloat32(-1.5) != 1.5 || AbsFloat32(1.5) != 1.5 {
		t.Fatal("AbsFloat32 returned an unexpected value")
	}
	value := 42
	if got := ToElem(ToPtr(value)); got != value {
		t.Fatalf("pointer round trip = %d", got)
	}
	if got := ToElem[int](nil); got != 0 {
		t.Fatalf("ToElem(nil) = %d, want zero value", got)
	}
}

func TestExtractAndBeautifyJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"surrounding text", `prefix {"name":"goat","nested":{"ok":true}} suffix`, "goat"},
		{"brace in string", `ignore {broken} then {"text":"a } brace"}`, "a } brace"},
		{"literal newline", "result: {\"text\":\"first\nsecond\"}", "first\\nsecond"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ExtractAndBeautifyJSON(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, test.want) {
				t.Fatalf("result %q does not contain %q", got, test.want)
			}
		})
	}

	for _, input := range []string{"plain text", `{not json}`, `{"unterminated": true`} {
		if _, err := ExtractAndBeautifyJSON(input); err == nil {
			t.Fatalf("ExtractAndBeautifyJSON(%q) unexpectedly succeeded", input)
		}
	}
}

func TestGetLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := GetLines(path, 2, 3)
	if err != nil || !reflect.DeepEqual(got, []string{"two", "three"}) {
		t.Fatalf("GetLines() = %v, %v", got, err)
	}
	if _, err := GetLines(path, 0, 1); err == nil {
		t.Fatal("invalid range unexpectedly succeeded")
	}
	if _, err := GetLines(filepath.Join(t.TempDir(), "missing"), 1, 1); err == nil {
		t.Fatal("missing file unexpectedly succeeded")
	}

	longPath := filepath.Join(t.TempDir(), "long.txt")
	if err := os.WriteFile(longPath, []byte(strings.Repeat("x", 70<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := GetLines(longPath, 1, 1); err == nil {
		t.Fatal("scanner error unexpectedly ignored")
	}
}

func TestSafeMap(t *testing.T) {
	m := NewSafeMap[string, int]()
	if _, ok := m.GetOk("missing"); ok || m.Get("missing") != 0 {
		t.Fatal("missing key reported as present")
	}
	m.Set("one", 1)
	if got, ok := m.GetOk("one"); !ok || got != 1 {
		t.Fatalf("GetOk(one) = %d, %v", got, ok)
	}
	snapshot := m.Snapshot()
	snapshot["one"] = 99
	if m.Get("one") != 1 {
		t.Fatal("Snapshot returned the backing map")
	}
	m.Delete("one")
	if _, ok := m.GetOk("one"); ok {
		t.Fatal("Delete did not remove key")
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(v int) { defer wg.Done(); m.Set("shared", v); _ = m.Get("shared") }(i)
	}
	wg.Wait()
}

func TestSafeSet(t *testing.T) {
	set := NewSafeSet[int]()
	set.Add(1)
	set.Add(2)
	set.Add(3)
	if set.Len() != 3 {
		t.Fatalf("Len() = %d", set.Len())
	}
	if got := set.Range(nil); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("Range(nil) = %v", got)
	}
	if got := set.Range(func(v int) bool { return v%2 == 1 }); !reflect.DeepEqual(got, []int{1, 3}) {
		t.Fatalf("Range(odd) = %v", got)
	}
	set.Drop([]int{2, 99})
	if got := set.Range(nil); !reflect.DeepEqual(got, []int{1, 3}) {
		t.Fatalf("after Drop = %v", got)
	}
	set.Truncate()
	if set.Len() != 0 {
		t.Fatalf("Len() after Truncate = %d", set.Len())
	}
}

func TestZipRoundTripAndZipSlipProtection(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "file.txt"), []byte("contents"), 0o640); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "files.zip")
	if err := ZipFile(source, archive); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := UnzipFile(archive, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "nested", "file.txt"))
	if err != nil || string(data) != "contents" {
		t.Fatalf("extracted data = %q, %v", data, err)
	}

	malicious := filepath.Join(t.TempDir(), "malicious.zip")
	f, err := os.Create(malicious)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("bad"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := UnzipFile(malicious, t.TempDir()); err == nil {
		t.Fatal("zip slip archive unexpectedly extracted")
	}

	if err := UnzipFile(filepath.Join(t.TempDir(), "missing.zip"), t.TempDir()); err == nil {
		t.Fatal("missing archive unexpectedly extracted")
	}
	if err := ZipFile(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "out.zip")); err == nil {
		t.Fatal("missing source unexpectedly archived")
	}
	if err := ZipFile(source, filepath.Join(t.TempDir(), "missing", "out.zip")); err == nil || !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "create zip") {
		t.Fatalf("unexpected create error: %v", err)
	}
}
