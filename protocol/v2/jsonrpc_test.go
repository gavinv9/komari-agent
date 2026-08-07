package v2

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConfigParams_JSONRoundTrip(t *testing.T) {
	monthRotate := 15
	interval := 10.0
	includeNics := "eth0"
	excludeNics := "docker0"
	mountpoints := "/;/data"
	memCache := true
	gpu := false

	original := ConfigParams{
		Revision:           42,
		MonthRotate:        &monthRotate,
		Interval:           &interval,
		IncludeNics:        &includeNics,
		ExcludeNics:        &excludeNics,
		IncludeMountpoints: &mountpoints,
		MemoryIncludeCache: &memCache,
		EnableGPU:          &gpu,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ConfigParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Revision != original.Revision {
		t.Errorf("Revision: got %d, want %d", decoded.Revision, original.Revision)
	}
	if decoded.MonthRotate == nil || *decoded.MonthRotate != *original.MonthRotate {
		t.Errorf("MonthRotate mismatch")
	}
	if decoded.Interval == nil || *decoded.Interval != *original.Interval {
		t.Errorf("Interval mismatch")
	}
	if decoded.IncludeNics == nil || *decoded.IncludeNics != *original.IncludeNics {
		t.Errorf("IncludeNics mismatch")
	}
	if decoded.ExcludeNics == nil || *decoded.ExcludeNics != *original.ExcludeNics {
		t.Errorf("ExcludeNics mismatch")
	}
	if decoded.IncludeMountpoints == nil || *decoded.IncludeMountpoints != *original.IncludeMountpoints {
		t.Errorf("IncludeMountpoints mismatch")
	}
	if decoded.MemoryIncludeCache == nil || *decoded.MemoryIncludeCache != *original.MemoryIncludeCache {
		t.Errorf("MemoryIncludeCache mismatch")
	}
	if decoded.EnableGPU == nil || *decoded.EnableGPU != *original.EnableGPU {
		t.Errorf("EnableGPU mismatch")
	}
}

func TestConfigParams_OptionalFieldsOmitted(t *testing.T) {
	// Only Revision set; all pointer fields nil → should be omitted from JSON
	p := ConfigParams{Revision: 1}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["revision"].(float64) != 1 {
		t.Errorf("revision should be 1")
	}
	for _, key := range []string{"month_rotate", "interval", "include_nics", "exclude_nics", "include_mountpoints", "memory_include_cache", "enable_gpu"} {
		if _, ok := raw[key]; ok {
			t.Errorf("expected %q to be omitted", key)
		}
	}
}

func TestConfigResultParams_JSONRoundTrip(t *testing.T) {
	original := ConfigResultParams{
		Revision: 7,
		EventID:  "evt-123",
		Status:   "applied",
		Error:    "",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ConfigResultParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Revision != original.Revision || decoded.Status != original.Status || decoded.EventID != original.EventID {
		t.Errorf("round-trip mismatch: %+v", decoded)
	}
}

func TestRouteParams_JSONRoundTrip(t *testing.T) {
	original := RouteParams{
		TaskID:    99,
		Protocol:  "icmp",
		Target:    "example.com",
		IPVersion: 4,
		MaxHops:   30,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RouteParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Errorf("got %+v, want %+v", decoded, original)
	}
}

func TestRouteResultParams_JSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	original := RouteResultParams{
		TaskID:    99,
		Protocol:  "icmp",
		Target:    "1.2.3.4",
		IPVersion: 4,
		Hops: []RouteHop{
			{TTL: 1, IP: "10.0.0.1", LatencyMS: 1.5},
			{TTL: 2, IP: "172.16.0.1", LatencyMS: 5.2},
			{TTL: 3, Timeout: true},
		},
		FinishedAt: now,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RouteResultParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TaskID != original.TaskID || decoded.Protocol != original.Protocol {
		t.Errorf("top-level fields mismatch")
	}
	if len(decoded.Hops) != len(original.Hops) {
		t.Fatalf("hops count: got %d, want %d", len(decoded.Hops), len(original.Hops))
	}
	for i, hop := range decoded.Hops {
		orig := original.Hops[i]
		if hop.TTL != orig.TTL || hop.IP != orig.IP || hop.Timeout != orig.Timeout {
			t.Errorf("hop[%d] mismatch: got %+v, want %+v", i, hop, orig)
		}
	}
}

func TestBuildRouteResultPayload_Structure(t *testing.T) {
	result := RouteResultParams{
		TaskID:     1,
		Protocol:   "icmp",
		Target:     "8.8.8.8",
		IPVersion:  4,
		Hops:       []RouteHop{{TTL: 1, IP: "10.0.0.1", LatencyMS: 0.5}},
		FinishedAt: time.Now().UTC(),
	}
	payload := BuildRouteResultPayload(result)

	var req Request
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.JSONRPC != Version {
		t.Errorf("jsonrpc: got %q, want %q", req.JSONRPC, Version)
	}
	if req.Method != MethodAgentRouteResult {
		t.Errorf("method: got %q, want %q", req.Method, MethodAgentRouteResult)
	}
}
