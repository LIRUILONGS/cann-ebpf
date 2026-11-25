package elfutil

import (
	"debug/elf"
	"testing"
)

func TestVaddrToOffsetLoadSegment(t *testing.T) {
	f := &elf.File{Progs: []*elf.Prog{{
		ProgHeader: elf.ProgHeader{
			Type:   elf.PT_LOAD,
			Off:    0x200,
			Vaddr:  0x1000,
			Filesz: 0x100,
		},
	}}}
	got, err := vaddrToOffset(f, 0x1010)
	if err != nil {
		t.Fatalf("vaddrToOffset: %v", err)
	}
	if got != 0x210 {
		t.Fatalf("offset = 0x%x, want 0x210", got)
	}
	if _, err := vaddrToOffset(f, 0x2000); err == nil {
		t.Fatal("expected unmapped vaddr error")
	}
}

func TestVaddrToOffsetSectionFallback(t *testing.T) {
	f := &elf.File{Sections: []*elf.Section{{
		SectionHeader: elf.SectionHeader{
			Type:   elf.SHT_PROGBITS,
			Offset: 0x300,
			Addr:   0x4000,
			Size:   0x80,
		},
	}}}
	got, err := vaddrToOffset(f, 0x4010)
	if err != nil {
		t.Fatalf("vaddrToOffset: %v", err)
	}
	if got != 0x310 {
		t.Fatalf("offset = 0x%x, want 0x310", got)
	}
}

func TestSplitVersion(t *testing.T) {
	base, version := splitVersion("aclrtLaunchKernel@CANN_9.0")
	if base != "aclrtLaunchKernel" || version != "CANN_9.0" {
		t.Fatalf("base=%q version=%q", base, version)
	}
}
