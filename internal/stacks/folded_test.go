package stacks

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestFoldedRoundTripIsDeterministic(t *testing.T) {
	profile := &Profile{
		Stacks: []ResolvedStack{
			{
				Frames: []Frame{
					{Name: "root;part", Category: "vfs"},
					{Name: "leaf with space", Category: "sync"},
					{Name: "叶子", Category: "other"},
				},
				Count: 2,
			},
			{
				Frames: []Frame{
					{Name: "root;part"},
					{Name: "leaf with space"},
					{Name: "叶子"},
				},
				Count: 3,
			},
			{
				Frames: []Frame{
					{Name: "alpha\nline"},
					{Name: "[unknown]", Unknown: true},
				},
				Count: 1,
			},
			{Frames: []Frame{{Name: "a:b"}}, Count: 2},
			{Frames: []Frame{{Name: "a;b"}}, Count: 1},
		},
		Total: 9,
	}

	var first bytes.Buffer
	if err := WriteFolded(&first, profile); err != nil {
		t.Fatalf("WriteFolded: %v", err)
	}
	want := "a:b 3\nalpha line;[unknown] 1\nroot:part;leaf with space;叶子 5\n"
	if got := first.String(); got != want {
		t.Fatalf("folded output = %q, want %q", got, want)
	}

	var second bytes.Buffer
	if err := WriteFolded(&second, profile); err != nil {
		t.Fatalf("second WriteFolded: %v", err)
	}
	if second.String() != first.String() {
		t.Fatalf("WriteFolded is not deterministic")
	}

	path := mustWriteTestFile(t, t.TempDir(), FoldedFileName, first.String())
	loaded, err := ReadFolded(path)
	if err != nil {
		t.Fatalf("ReadFolded: %v", err)
	}
	if loaded.Total != 9 || len(loaded.Stacks) != 3 {
		t.Fatalf("loaded total/stacks = %d/%d", loaded.Total, len(loaded.Stacks))
	}
	if got := frameNames(loaded.Stacks[0].Frames); !equalStrings(got, []string{"a:b"}) {
		t.Fatalf("first loaded stack = %v", got)
	}
	if loaded.Stacks[0].Count != 3 {
		t.Fatalf("collision count = %d, want 3", loaded.Stacks[0].Count)
	}
	if got := frameNames(loaded.Stacks[1].Frames); !equalStrings(got, []string{"alpha line", "[unknown]"}) {
		t.Fatalf("second loaded stack = %v", got)
	}
	if !loaded.Stacks[1].Frames[1].Unknown {
		t.Fatalf("unknown frame not reconstructed")
	}
	if got := frameNames(loaded.Stacks[2].Frames); !equalStrings(got, []string{"root:part", "leaf with space", "叶子"}) {
		t.Fatalf("third loaded stack = %v", got)
	}
	if loaded.Stacks[2].Count != 5 {
		t.Fatalf("merged count = %d, want 5", loaded.Stacks[2].Count)
	}
	for _, stack := range loaded.Stacks {
		for _, frame := range stack.Frames {
			if frame.Category != "" {
				t.Fatalf("folded frame category = %q, want empty", frame.Category)
			}
		}
	}
}

func TestReadFoldedRejectsMalformedLinesWithLineNumber(t *testing.T) {
	tests := []struct {
		name    string
		content string
		line    int
	}{
		{name: "missing separator", content: "root;leaf 1\nbroken\n", line: 2},
		{name: "bad count", content: "root;leaf nope\n", line: 1},
		{name: "empty line", content: "\n", line: 1},
		{name: "empty frame", content: "root;;leaf 1\n", line: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := mustWriteTestFile(t, t.TempDir(), FoldedFileName, test.content)
			_, err := ReadFolded(path)
			if err == nil {
				t.Fatalf("expected error")
			}
			if marker := fmt.Sprintf(":%d:", test.line); !strings.Contains(err.Error(), marker) {
				t.Fatalf("error %q does not contain line marker %q", err, marker)
			}
		})
	}
}

func TestReadFoldedEmptyAndMaxCount(t *testing.T) {
	emptyPath := mustWriteTestFile(t, t.TempDir(), "empty.folded", "")
	empty, err := ReadFolded(emptyPath)
	if err != nil {
		t.Fatalf("ReadFolded empty: %v", err)
	}
	if empty == nil || len(empty.Stacks) != 0 || empty.Total != 0 {
		t.Fatalf("empty profile = %+v", empty)
	}

	maxPath := mustWriteTestFile(t, t.TempDir(), "max.folded", "root 18446744073709551615\n")
	maxProfile, err := ReadFolded(maxPath)
	if err != nil {
		t.Fatalf("ReadFolded max: %v", err)
	}
	if maxProfile.Total != ^uint64(0) {
		t.Fatalf("max total = %d", maxProfile.Total)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
