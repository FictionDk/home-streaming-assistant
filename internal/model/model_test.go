package model

import (
	"strings"
	"testing"
	"time"
)

func TestValidateClientPath(t *testing.T) {
	cases := []struct {
		path string
		ok   bool
	}{
		{"", true},
		{"a", true},
		{"a/b/c.txt", true},
		{"/abs", false},
		{"a/../b", false},
		{"./a", false},
		{"a//b", false},
		{"a/b/", false},
		{"a\\b", false},
		{"a\x00b", false},
		{"..", false},
		{".", false},
	}
	for _, c := range cases {
		err := ValidateClientPath(c.path)
		if (err == nil) != c.ok {
			t.Errorf("ValidateClientPath(%q) err=%v want ok=%v", c.path, err, c.ok)
		}
	}
}

func TestJoinClientPath(t *testing.T) {
	if _, err := JoinClientPath("a", ".."); err == nil {
		t.Fatal("dot-dot child accepted")
	}
	if _, err := JoinClientPath("a", "b/c"); err == nil {
		t.Fatal("slash in child accepted")
	}
	if got, err := JoinClientPath("a", "b"); err != nil || got != "a/b" {
		t.Fatalf("JoinClientPath=%q err=%v", got, err)
	}
	if got, err := JoinClientPath("", "b"); err != nil || got != "b" {
		t.Fatalf("JoinClientPath root=%q err=%v", got, err)
	}
}

func TestComputeVersion(t *testing.T) {
	size := int64(10)
	mod := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	v1, err := ComputeVersion(SourceAliyun, "a/b", &size, &mod, nil, nil)
	if err != nil || v1 == nil {
		t.Fatalf("ComputeVersion: %v", err)
	}
	v2, _ := ComputeVersion(SourceAliyun, "a/b", &size, &mod, nil, nil)
	if *v1 != *v2 {
		t.Fatal("version not deterministic")
	}
	v3, _ := ComputeVersion(SourceAliyun, "a/b", &size, &mod, strp("id1"), nil)
	if *v1 == *v3 {
		t.Fatal("upstream id must change version")
	}
	if _, err := ComputeVersion(SourceAliyun, "a/b", &size, nil, strp("id1"), nil); err == nil {
		t.Fatal("size-only must not produce a version")
	}
}

func strp(s string) *string { return &s }

func TestBestHashRanking(t *testing.T) {
	got := BestHash([]HashPayload{{Type: "md5", Value: "m"}, {Type: "sha256", Value: "s"}, {Type: "sha1", Value: "1"}})
	if got == nil || got.Type != "sha256" {
		t.Fatalf("BestHash picked %+v", got)
	}
	if BestHash([]HashPayload{{Type: "crc32", Value: "x"}}) != nil {
		t.Fatal("unknown hash type must be rejected")
	}
}

func TestSortEntriesDirsFirst(t *testing.T) {
	es := []Entry{{Name: "b.txt"}, {Name: "z"}, {Name: "a"}, {Name: "a.txt"}}
	es[1].IsDir = true
	es[2].IsDir = true
	SortEntries(es)
	want := []string{"a", "z", "a.txt", "b.txt"}
	for i, e := range es {
		if e.Name != want[i] {
			t.Fatalf("pos %d = %s want %s", i, e.Name, want[i])
		}
	}
}

func TestValidateUpstreamName(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", "a\\b", "a\x00"} {
		if err := ValidateUpstreamName(bad); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if err := ValidateUpstreamName("ok-name.txt"); err != nil {
		t.Errorf("good name rejected: %v", err)
	}
}

func TestVersionPayloadCanonical(t *testing.T) {
	// Same logical inputs in different construction order must agree.
	size := int64(3)
	m1 := time.Date(2025, 6, 1, 12, 0, 0, 0, time.FixedZone("X", 3600))
	v1, err := ComputeVersion(SourceGDrive, "p", &size, &m1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m2 := m1.UTC()
	v2, _ := ComputeVersion(SourceGDrive, "p", &size, &m2, nil, nil)
	if !strings.EqualFold(*v1, *v2) {
		t.Fatal("timezone must be normalized")
	}
}
