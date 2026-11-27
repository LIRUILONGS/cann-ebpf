package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const hccsFixture = "hccs health status : OK\n" +
	"hccs lane mode : [4 4]\n" +
	"hccs link lane list : [1111 1111]\n" +
	"hccs link speed : [224 224]\n" +
	"hccs tx packets : [11 12]\n" +
	"hccs tx bytes : [101 102]\n" +
	"hccs rx packets : [21 22]\n" +
	"hccs rx bytes : [201 202]\n" +
	"hccs retry count : [0 0]\n" +
	"hccs error count : [0 0]\n"

func TestNPUSmiHCCSSourceCollectsCountersWithLaneLabels(t *testing.T) {
	source := NewNPUSmiHCCSSource("npu-smi", []int{0}, 30*time.Second, time.Minute)
	var calls [][]string
	source.Runner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "npu-smi" {
			return nil, errors.New("unexpected command " + name)
		}
		calls = append(calls, append([]string(nil), args...))
		return []byte(hccsFixture), nil
	}

	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	wantArgs := []string{"info", "-t", "hccs", "-i", "0", "-c", "0"}
	if strings.Join(calls[0], " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", calls[0], wantArgs)
	}

	for _, name := range []string{
		"npu_hccs_tx_bytes_total", "npu_hccs_rx_bytes_total",
		"npu_hccs_tx_packets_total", "npu_hccs_rx_packets_total",
		"npu_hccs_retry_total", "npu_hccs_error_total",
	} {
		found := false
		for _, point := range points {
			if point.Name != name {
				continue
			}
			found = true
			if point.Kind != KindCounter {
				t.Fatalf("%s kind = %s, want counter", name, point.Kind)
			}
			if point.Source != "npu-smi" || point.Device != "NPU-0" {
				t.Fatalf("%s identity = %+v", name, point)
			}
			wantUnit := "count"
			if name == "npu_hccs_tx_bytes_total" || name == "npu_hccs_rx_bytes_total" {
				wantUnit = "bytes"
			}
			if point.Unit != wantUnit {
				t.Fatalf("%s unit = %q, want %q", name, point.Unit, wantUnit)
			}
			if point.Labels["lane"] == "" {
				t.Fatalf("%s missing lane label: %+v", name, point)
			}
		}
		if !found {
			t.Fatalf("missing counter %s in %v", name, pointNames(points))
		}
	}

	for _, point := range points {
		if strings.HasPrefix(point.Name, "npu_hccs_") && point.Labels["lane"] == "" {
			t.Fatalf("%s missing lane label: %+v", point.Name, point)
		}
		if point.Name == "npu_hccs_healthy" && point.Labels["status"] != "OK" {
			t.Fatalf("healthy status label = %q, want OK", point.Labels["status"])
		}
	}
	assertPointValue(t, points, "npu_hccs_link_speed", 224)
	assertPointValue(t, points, "npu_hccs_lane_mode", 4)
	assertPointValue(t, points, "npu_hccs_healthy", 1)
	for _, point := range points {
		if point.Name == "npu_hccs_link_speed" && point.Unit != "raw" {
			t.Fatalf("link speed unit = %q, want raw", point.Unit)
		}
		if strings.Contains(point.Name, "_rate") || strings.Contains(point.Name, "per_second") {
			t.Fatalf("unexpected derived rate metric %s", point.Name)
		}
	}
}

func TestNPUSmiHCCSSourceWarnsOnOneDeviceFailure(t *testing.T) {
	source := NewNPUSmiHCCSSource("npu-smi", []int{0, 1}, 30*time.Second, time.Minute)
	source.Runner = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 5 && args[4] == "1" {
			return nil, errors.New("device offline")
		}
		return []byte(hccsFixture), nil
	}
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(points) == 0 {
		t.Fatal("expected points from the healthy device")
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "-i 1") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestNPUSmiHCCSSourceIntervalPolicy(t *testing.T) {
	if got := NewNPUSmiHCCSSource("npu-smi", nil, time.Second, time.Minute).Interval(); got != 30*time.Second {
		t.Fatalf("below-10s interval = %s, want 30s", got)
	}
	if got := NewNPUSmiHCCSSource("npu-smi", nil, 10*time.Second, time.Minute).Interval(); got != 10*time.Second {
		t.Fatalf("explicit diagnostic interval = %s, want 10s", got)
	}
	if got := NewNPUSmiHCCSSource("npu-smi", nil, 30*time.Second, time.Minute).Interval(); got != 30*time.Second {
		t.Fatalf("default interval = %s, want 30s", got)
	}
}
