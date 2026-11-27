//go:build linux

package metrics

import "testing"

func TestParseTracepointFieldsGenericSyscall(t *testing.T) {
	data := []byte(`name: sys_enter_ioctl
ID: 27
format:
	field:unsigned short common_type;	offset:0;	size:2;	signed:0;
	field:unsigned char common_flags;	offset:2;	size:1;	signed:0;
	field:unsigned char common_preempt_count;	offset:3;	size:1;	signed:0;
	field:int common_pid;	offset:4;	size:4;	signed:1;
	field:long id;	offset:8;	size:8;	signed:1;
	field:unsigned long args[6];	offset:16;	size:48;	signed:0;
`)
	fields, err := parseTracepointFields(data)
	if err != nil {
		t.Fatal(err)
	}
	args := fields["args"]
	if args.offset != 16 || args.size != 48 {
		t.Fatalf("args=%+v", args)
	}
	if err := validateDriverTracepointFields("sys_enter_ioctl", fields); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDriverTracepointFieldsRejectsMismatch(t *testing.T) {
	fields := map[string]tracepointField{
		"args": {offset: 24, size: 48},
	}
	if err := validateDriverTracepointFields("sys_enter_ioctl", fields); err == nil {
		t.Fatal("expected layout mismatch error")
	}
}

func TestDriverPointCopiesDeviceFromLabels(t *testing.T) {
	point := driverPoint(Gauge("npu_ioctl_total", "count", 1, map[string]string{
		"device": "davinci0",
	}))
	if point.Source != driverSourceName || point.Device != "davinci0" {
		t.Fatalf("point source=%q device=%q", point.Source, point.Device)
	}
}
