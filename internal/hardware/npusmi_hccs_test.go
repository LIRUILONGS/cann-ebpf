package hardware

import "testing"

// Fixture reproduced from npu-smi info -t hccs -i 0 -c 0 output on the
// 910B2/25.2.1 validation host (2026-11-28). It is fixed text so the parser does
// not depend on a live device.
const npuSmiHCCSFixture = "\thccs health status             : OK\n" +
	"\thccs lane mode                 : [4            4            4            4            4            4            4            ]\n" +
	"\thccs link lane list            : [1111         1111         1111         1111         1111         1111         1111         ]\n" +
	"\thccs link speed                : [224          224          224          224          224          224          224          ]\n" +
	"\thccs tx packets                : [403999333    2179466223   1000847787   3593254491   1395768721   1532432792   4145519937   ]\n" +
	"\thccs tx bytes                  : [8079986660   43589324460  20016955740  71865089820  27915374420  30648655840  82910398740  ]\n" +
	"\thccs rx packets                : [3284429185   1228718047   3024113010   2447768645   808808863    393861640    2459214519   ]\n" +
	"\thccs rx bytes                  : [65688583700  24574360940  60482260200  48955372900  16176177260  7877232800   49184290380  ]\n" +
	"\thccs retry count               : [0            0            0            0            0            0            0            ]\n" +
	"\thccs error count               : [0            0            0            0            0            0            0            ]\n" +
	"\thccs first error lane          : NA\n"

func TestParseNPUSmiHCCSSevenLanes(t *testing.T) {
	sample, err := ParseNPUSmiHCCS([]byte(npuSmiHCCSFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sample.Schema != SampleSchemaV1 || sample.Source != "npu-smi" {
		t.Fatalf("identity = %+v", sample)
	}
	if sample.HCCSHealth != "OK" {
		t.Fatalf("health = %q", sample.HCCSHealth)
	}
	if len(sample.HCCSLanes) != 7 {
		t.Fatalf("lanes = %d, want 7", len(sample.HCCSLanes))
	}
	first := sample.HCCSLanes[0]
	if first.Lane != 0 || first.LinkMode == nil || *first.LinkMode != 4 {
		t.Fatalf("lane 0 mode = %+v", first)
	}
	if first.LinkSpeed == nil || *first.LinkSpeed != 224 {
		t.Fatalf("lane 0 link speed = %v", first.LinkSpeed)
	}
	if first.TXBytes == nil || *first.TXBytes != 8079986660 {
		t.Fatalf("lane 0 tx bytes = %v", first.TXBytes)
	}
	if first.RXBytes == nil || *first.RXBytes != 65688583700 {
		t.Fatalf("lane 0 rx bytes = %v", first.RXBytes)
	}
	if first.TXPackets == nil || *first.TXPackets != 403999333 {
		t.Fatalf("lane 0 tx packets = %v", first.TXPackets)
	}
	if first.RXPackets == nil || *first.RXPackets != 3284429185 {
		t.Fatalf("lane 0 rx packets = %v", first.RXPackets)
	}
	if first.Retry == nil || *first.Retry != 0 {
		t.Fatalf("lane 0 retry = %v (zero must be present, not unknown)", first.Retry)
	}
	if first.Error == nil || *first.Error != 0 {
		t.Fatalf("lane 0 error = %v", first.Error)
	}
	if first.LinkLane != "1111" {
		t.Fatalf("lane 0 link lane = %q", first.LinkLane)
	}
}

func TestParseNPUSmiHCCSDerivesLaneCount(t *testing.T) {
	raw := "hccs health status : OK\n" +
		"hccs lane mode : [4 4 4]\n" +
		"hccs link speed : [224 224 224]\n" +
		"hccs tx packets : [1 2 3]\n" +
		"hccs tx bytes : [10 20 30]\n" +
		"hccs rx packets : [4 5 6]\n" +
		"hccs rx bytes : [40 50 60]\n" +
		"hccs retry count : [0 0 0]\n" +
		"hccs error count : [0 0 0]\n"
	sample, err := ParseNPUSmiHCCS([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(sample.HCCSLanes) != 3 {
		t.Fatalf("lanes = %d, want 3 (must not hard-code 7)", len(sample.HCCSLanes))
	}
	if *sample.HCCSLanes[2].TXBytes != 30 {
		t.Fatalf("lane 2 tx bytes = %v", sample.HCCSLanes[2].TXBytes)
	}
}

func TestParseNPUSmiHCCSMissingValuesStayUnknown(t *testing.T) {
	raw := "hccs health status : OK\n" +
		"hccs lane mode : [4 4]\n" +
		"hccs link speed : [224 224]\n" +
		"hccs tx packets : [1 2]\n" +
		"hccs tx bytes : NA\n" +
		"hccs rx packets : [3 4]\n" +
		"hccs rx bytes : [5 6]\n" +
		"hccs retry count : NA\n" +
		"hccs error count : [0 0]\n"
	sample, err := ParseNPUSmiHCCS([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, lane := range sample.HCCSLanes {
		if lane.TXBytes != nil {
			t.Fatalf("lane %d tx bytes = %v, want nil for NA", lane.Lane, *lane.TXBytes)
		}
		if lane.Retry != nil {
			t.Fatalf("lane %d retry = %v, want nil for NA", lane.Lane, *lane.Retry)
		}
	}
}

func TestParseNPUSmiHCCSNoArrays(t *testing.T) {
	if _, err := ParseNPUSmiHCCS([]byte("hccs health status : OK\n")); err == nil {
		t.Fatal("expected error when no lane arrays are present")
	}
}
