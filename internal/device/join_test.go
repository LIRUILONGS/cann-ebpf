package device

import (
	"strings"
	"testing"

	"cann-ebpf/internal/runlog"
)

func hostSpan(id string, pid, tid int, start, end int64, function, category string) runlog.Span {
	return runlog.Span{
		Schema: runlog.SpanSchemaV1, RunID: "r", Profile: "p",
		TsNS: start, StartNS: start, EndNS: end, PID: pid, TID: tid, TGID: pid,
		Function: function, Category: category, DurationNS: end - start, SpanID: id,
	}
}

func TestJoinAPIRecordPath(t *testing.T) {
	host := []runlog.Span{
		hostSpan("h1", 100, 11, 1000, 2000, "aclrtSynchronizeStream", "sync"),
		hostSpan("h2", 100, 12, 3000, 3500, "aclrtSynchronizeStream", "sync"),
		hostSpan("h3", 100, 13, 5000, 5100, "aclrtRecordEvent", "event"),
	}
	dev := []Span{
		{Schema: SchemaV1, Kind: KindAPI, Name: "aclrtSynchronizeStream", PID: 100, TID: 11, StartNS: 1000, EndNS: 2000, CorrelationID: 11},
		{Schema: SchemaV1, Kind: KindKernel, Name: "ffn", DeviceID: 0, PID: 100, TID: 11, StartNS: 1100, EndNS: 1900, CorrelationID: 11},
		{Schema: SchemaV1, Kind: KindMemcpy, Name: "h2d", DeviceID: 0, PID: 100, TID: 11, StartNS: 1200, EndNS: 1300, CorrelationID: 11},
		{Schema: SchemaV1, Kind: KindAPI, Name: "different_name", PID: 100, TID: 12, StartNS: 3000, EndNS: 3500, CorrelationID: 22},
		{Schema: SchemaV1, Kind: KindKernel, Name: "attn", DeviceID: 1, PID: 100, TID: 12, StartNS: 3000, EndNS: 3300, CorrelationID: 22},
	}
	report := Join(host, dev, Options{SlowThresholdNS: 200})
	if report.Schema != JoinReportSchemaV1 {
		t.Fatalf("schema = %s", report.Schema)
	}
	if report.MatchedHostSpans != 2 || report.HostSpans != 3 {
		t.Fatalf("matched = %d/%d", report.MatchedHostSpans, report.HostSpans)
	}
	if report.TimeWindowFallback {
		t.Fatal("should not use time-window fallback")
	}
	if len(report.Devices) != 2 {
		t.Fatalf("devices = %+v", report.Devices)
	}
	// h1: 800ns kernel + 100ns memcpy overlap over 1000ns busy union 800ns = 0.8
	first := report.Hosts[0]
	if !first.Matched || first.BusyPct < 0.79 || first.BusyPct > 0.81 {
		t.Fatalf("h1 = %+v", first)
	}
	if first.KernelNS != 800 || first.MemcpyNS != 100 {
		t.Fatalf("h1 kinds = %+v", first)
	}
	if report.Hosts[1].Reason != ReasonAPINameMismatch || !report.Hosts[1].Matched {
		t.Fatalf("h2 = %+v", report.Hosts[1])
	}
	if report.Hosts[2].Reason != ReasonNoAPIRecord || report.Hosts[2].Matched {
		t.Fatalf("h3 = %+v", report.Hosts[2])
	}
	found := false
	for _, verdict := range report.Verdicts {
		if verdict.Verdict == "device_bound" && verdict.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("verdicts = %+v", report.Verdicts)
	}
}

func TestJoinAppliesContainerPIDMap(t *testing.T) {
	host := []runlog.Span{
		hostSpan("h1", 900, 91, 1000, 2000, "aclrtSynchronizeStream", "sync"),
	}
	dev := []Span{
		{Schema: SchemaV1, Kind: KindAPI, Name: "aclrtSynchronizeStream", PID: 10, TID: 11, StartNS: 1000, EndNS: 2000, CorrelationID: 7},
		{Schema: SchemaV1, Kind: KindKernel, Name: "ffn", DeviceID: 0, StartNS: 1100, EndNS: 1900, CorrelationID: 7},
	}
	report := Join(host, dev, Options{
		PIDMap: map[int]int{10: 900},
		TIDMap: map[int]int{11: 91},
	})
	if !report.PIDMapApplied || report.PIDMapEntries != 2 {
		t.Fatalf("pid map = %+v", report)
	}
	if report.MatchedHostSpans != 1 || report.Hosts[0].Reason != ReasonOK {
		t.Fatalf("host = %+v", report.Hosts[0])
	}
}

func TestTranslateIDsOnlyAPIRecords(t *testing.T) {
	spans := []Span{
		{Kind: KindAPI, PID: 10, TID: 11},
		{Kind: KindKernel, PID: 10, TID: 11},
	}
	translated := TranslateIDs(spans, map[int]int{10: 900}, map[int]int{11: 91})
	if translated[0].PID != 900 || translated[0].TID != 91 {
		t.Fatalf("api = %+v", translated[0])
	}
	if translated[1].PID != 10 || translated[1].TID != 11 {
		t.Fatalf("kernel should be unchanged: %+v", translated[1])
	}
	if spans[0].PID != 10 {
		t.Fatal("input mutated")
	}
}

func TestJoinTimeWindowFallback(t *testing.T) {
	host := []runlog.Span{hostSpan("h1", 100, 11, 1000, 2000, "aclrtSynchronizeStream", "sync")}
	dev := []Span{
		{Schema: SchemaV1, Kind: KindKernel, Name: "ffn", DeviceID: 0, PID: 100, StartNS: 1200, EndNS: 1800},
	}
	report := Join(host, dev, Options{})
	if !report.TimeWindowFallback {
		t.Fatal("expected time-window fallback")
	}
	if report.MatchedHostSpans != 1 || report.Hosts[0].Reason != ReasonTimeWindow {
		t.Fatalf("host = %+v", report.Hosts[0])
	}
	if !strings.Contains(strings.Join(report.Warnings, " "), "时间窗") {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}

func TestJoinClockOffset(t *testing.T) {
	host := []runlog.Span{hostSpan("h1", 100, 11, 1000, 2000, "aclrtSynchronizeStream", "sync")}
	offset := int64(1_000_000)
	dev := []Span{
		{Schema: SchemaV1, Kind: KindAPI, Name: "aclrtSynchronizeStream", PID: 100, TID: 11, StartNS: 1000 + offset, EndNS: 2000 + offset, CorrelationID: 7},
		{Schema: SchemaV1, Kind: KindKernel, Name: "ffn", DeviceID: 0, PID: 100, TID: 11, StartNS: 1200 + offset, EndNS: 1800 + offset, CorrelationID: 7},
	}
	report := Join(host, dev, Options{})
	if report.ClockOffsetNS != offset {
		t.Fatalf("clock offset = %d", report.ClockOffsetNS)
	}
	if report.MatchedHostSpans != 1 {
		t.Fatalf("matched = %d hosts=%+v", report.MatchedHostSpans, report.Hosts)
	}
}

func TestJoinNoDeviceRecords(t *testing.T) {
	host := []runlog.Span{hostSpan("h1", 100, 11, 1000, 2000, "aclrtSynchronizeStream", "sync")}
	dev := []Span{{Schema: SchemaV1, Kind: KindAPI, Name: "aclrtSynchronizeStream", PID: 100, TID: 11, StartNS: 1000, EndNS: 2000, CorrelationID: 9}}
	report := Join(host, dev, Options{})
	if report.MatchedHostSpans != 0 || report.Hosts[0].Reason != ReasonNoDeviceRecord {
		t.Fatalf("host = %+v", report.Hosts[0])
	}
}

func TestJoinAcceptsAdjacentCorrelatedKernelWithinTolerance(t *testing.T) {
	host := []runlog.Span{hostSpan("h1", 100, 11, 1000, 2000, "aclrtSynchronizeStream", "sync")}
	dev := []Span{
		{Schema: SchemaV1, Kind: KindAPI, Name: "aclrtSynchronizeStream", PID: 100, TID: 11, StartNS: 1000, EndNS: 2000, CorrelationID: 7},
		{Schema: SchemaV1, Kind: KindKernel, Name: "ffn", DeviceID: 0, StartNS: 2000 + 1677, EndNS: 2200 + 1677, CorrelationID: 7},
	}
	report := Join(host, dev, Options{TimeToleranceNS: 100_000, SlowThresholdNS: 200})
	if !report.Hosts[0].Matched || report.Hosts[0].Reason != ReasonOK {
		t.Fatalf("host = %+v", report.Hosts[0])
	}
	if len(report.Hosts[0].Matches) != 1 {
		t.Fatalf("matches = %+v", report.Hosts[0].Matches)
	}
	match := report.Hosts[0].Matches[0]
	if !match.Adjacent || match.GapNS != 1677 || match.OverlapNS != 0 {
		t.Fatalf("match = %+v", match)
	}
	if report.Hosts[0].DeviceBusyNS != 0 || report.Hosts[0].KernelNS != 0 {
		t.Fatalf("adjacent interval must not contribute busy/kernel time: %+v", report.Hosts[0])
	}
	if report.AdjacentMatches != 1 {
		t.Fatalf("adjacent matches = %d", report.AdjacentMatches)
	}
	if report.SlowSyncSpans != 1 || report.MatchedSlowSync != 1 || report.SlowSyncMatchRate != 1 {
		t.Fatalf("slow sync = %+v", report)
	}
}

func TestJoinRejectsAdjacentKernelOutsideTolerance(t *testing.T) {
	host := []runlog.Span{hostSpan("h1", 100, 11, 1000, 2000, "aclrtSynchronizeStream", "sync")}
	dev := []Span{
		{Schema: SchemaV1, Kind: KindAPI, Name: "aclrtSynchronizeStream", PID: 100, TID: 11, StartNS: 1000, EndNS: 2000, CorrelationID: 7},
		{Schema: SchemaV1, Kind: KindKernel, Name: "ffn", DeviceID: 0, StartNS: 2000 + 1677, EndNS: 2200 + 1677, CorrelationID: 7},
	}
	report := Join(host, dev, Options{TimeToleranceNS: 1000})
	if report.Hosts[0].Matched || report.Hosts[0].Reason != ReasonNoDeviceRecord {
		t.Fatalf("host = %+v", report.Hosts[0])
	}
}

func TestUnionDuration(t *testing.T) {
	if got := unionDuration([][2]int64{{0, 10}, {5, 20}, {30, 40}}); got != 30 {
		t.Fatalf("union = %d", got)
	}
}

func TestValidateRejectsBadKind(t *testing.T) {
	span := Span{Schema: SchemaV1, Kind: "WEIRD", StartNS: 1, EndNS: 2}
	if err := span.Validate(); err == nil {
		t.Fatal("expected invalid kind error")
	}
	good := Span{Schema: SchemaV1, Kind: KindKernel, StartNS: 1, EndNS: 2}
	if err := good.Validate(); err != nil {
		t.Fatalf("good span: %v", err)
	}
}

func TestReadSpansLineError(t *testing.T) {
	_, err := ReadSpans(strings.NewReader("{\"schema\":\"cann.device.v1\"}\nnot-json\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v", err)
	}
}
