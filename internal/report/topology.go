package report

import (
	"fmt"
	"sort"
	"strings"
)

// TopologySpan is one OTel/W3C-shaped span used for service topology.
//
// TraceID, SpanID and ParentSpanID are the only source of parent/child edges.
// The builder never invents an upstream link from timing, process co-location,
// or service order.
type TopologySpan struct {
	TraceID      string `json:"trace_id,omitempty"`
	SpanID       string `json:"span_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
	Service      string `json:"service"`
	Name         string `json:"name,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Status       string `json:"status,omitempty"`
	StartNS      int64  `json:"start_ns,omitempty"`
	EndNS        int64  `json:"end_ns,omitempty"`
}

// TopologyOptions controls optional attribution. RootService is only honoured
// when it is observed in the input. Source defaults to otel.jsonl.
type TopologyOptions struct {
	RootService string
	Source      string
}

type topologyNodeAgg struct {
	service     string
	ok          int
	errors      int
	otherStatus string
}

type topologyEdgeAgg struct {
	from    string
	to      string
	calls   int
	totalNS int64
}

const defaultTopologySource = "otel.jsonl"

// BuildServiceTopology derives a service graph from trace parent/child links.
// It reports scope=end_to_end only when at least one resolved parent/child
// edge crosses a service boundary. Without upstream trace propagation it
// reports scope=process and explains the limitation in status.reason.
func BuildServiceTopology(spans []TopologySpan, opts TopologyOptions) ServiceTopologyData {
	data := ServiceTopologyData{
		Nodes: []TopologyNode{},
		Edges: []TopologyEdge{},
	}
	if len(spans) == 0 {
		data.Status = ViewStatus{Available: false, Reason: "no topology spans", Source: "none"}
		return data
	}

	source := strings.TrimSpace(opts.Source)
	if source == "" {
		source = defaultTopologySource
	}

	valid := make([]TopologySpan, 0, len(spans))
	missingService := 0
	for _, span := range spans {
		span.TraceID = strings.TrimSpace(span.TraceID)
		span.SpanID = strings.TrimSpace(span.SpanID)
		span.ParentSpanID = strings.TrimSpace(span.ParentSpanID)
		span.Service = strings.TrimSpace(span.Service)
		span.Name = strings.TrimSpace(span.Name)
		span.Kind = strings.TrimSpace(span.Kind)
		span.Status = strings.TrimSpace(span.Status)
		if span.Service == "" {
			missingService++
			continue
		}
		valid = append(valid, span)
	}
	if len(valid) == 0 {
		data.Status = ViewStatus{
			Available: false,
			Reason:    "no service attribution in topology spans",
			Source:    source,
		}
		return data
	}

	nodesByService := make(map[string]*topologyNodeAgg, len(valid))
	for _, span := range valid {
		agg := nodesByService[span.Service]
		if agg == nil {
			agg = &topologyNodeAgg{service: span.Service}
			nodesByService[span.Service] = agg
		}
		statusClass := topologyStatusClass(span.Status)
		switch statusClass {
		case "OK":
			agg.ok++
		case "error":
			agg.errors++
		default:
			if agg.otherStatus == "" && statusClass != "" {
				agg.otherStatus = statusClass
			}
		}
	}
	nodes := make([]TopologyNode, 0, len(nodesByService))
	for _, agg := range nodesByService {
		status := agg.otherStatus
		if agg.errors > 0 {
			status = "error"
		} else if agg.ok > 0 {
			status = "OK"
		}
		nodes = append(nodes, TopologyNode{
			ID:      agg.service,
			Service: agg.service,
			Name:    agg.service,
			Kind:    "service",
			Status:  status,
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	traceLinked := 0
	for _, span := range valid {
		if span.TraceID != "" && span.SpanID != "" {
			traceLinked++
		}
	}
	coverage := float64(traceLinked) / float64(len(valid))

	bySpanID := make(map[string]int, len(valid))
	ambiguous := make(map[string]struct{})
	for i, span := range valid {
		if span.TraceID == "" || span.SpanID == "" {
			continue
		}
		key := topologyKey(span.TraceID, span.SpanID)
		if _, exists := bySpanID[key]; exists {
			ambiguous[key] = struct{}{}
			continue
		}
		bySpanID[key] = i
	}
	for key := range ambiguous {
		delete(bySpanID, key)
	}

	edgesByPair := make(map[string]*topologyEdgeAgg, len(valid))
	parentRefs := 0
	resolvedParents := 0
	for _, span := range valid {
		if span.TraceID == "" || span.ParentSpanID == "" {
			continue
		}
		parentRefs++
		parentIndex, ok := bySpanID[topologyKey(span.TraceID, span.ParentSpanID)]
		if !ok {
			continue
		}
		parent := valid[parentIndex]
		resolvedParents++
		if parent.Service == span.Service {
			continue
		}
		key := parent.Service + "\x00" + span.Service
		agg := edgesByPair[key]
		if agg == nil {
			agg = &topologyEdgeAgg{from: parent.Service, to: span.Service}
			edgesByPair[key] = agg
		}
		agg.calls++
		if span.EndNS > span.StartNS {
			agg.totalNS += span.EndNS - span.StartNS
		}
	}
	edges := make([]TopologyEdge, 0, len(edgesByPair))
	for _, agg := range edgesByPair {
		// LatencyMS is the mean observed duration of child spans for this edge.
		latencyMS := 0.0
		if agg.calls > 0 {
			latencyMS = float64(agg.totalNS) / float64(agg.calls) / 1e6
		}
		edges = append(edges, TopologyEdge{
			From:      agg.from,
			To:        agg.to,
			Calls:     agg.calls,
			LatencyMS: latencyMS,
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From == edges[j].From {
			return edges[i].To < edges[j].To
		}
		return edges[i].From < edges[j].From
	})

	scope := "process"
	if len(edges) > 0 {
		scope = "end_to_end"
	}
	data.Scope = scope
	data.Coverage = coverage
	data.Nodes = nodes
	data.Edges = edges
	data.RootService = chooseTopologyRoot(valid, bySpanID, nodesByService, opts.RootService)

	status := ViewStatus{Available: true, Source: source}
	var partial []string
	if missingService > 0 {
		partial = append(partial, fmt.Sprintf("%d span(s) without service attribution were ignored", missingService))
	}
	if parentRefs > resolvedParents {
		partial = append(partial, fmt.Sprintf("%d parent span reference(s) were not exported and could not be resolved", parentRefs-resolvedParents))
	}
	if coverage < 1 {
		partial = append(partial, fmt.Sprintf("%.1f%% of spans carry trace context", coverage*100))
	}
	if scope == "process" {
		if traceLinked == 0 {
			status.Reason = "process scope only: no upstream trace propagation (spans lack trace_id/span_id), so end-to-end topology is not claimed"
		} else {
			status.Reason = "process scope only: trace propagation is local and no cross-service parent/child edge was observed, so end-to-end topology is not claimed"
		}
		status.Confidence = "partial"
	} else if len(partial) > 0 {
		status.Reason = "partial end-to-end coverage: " + strings.Join(partial, "; ")
		status.Confidence = "partial"
	} else {
		status.Confidence = "exact"
	}
	data.Status = status
	return data
}

func topologyKey(traceID, spanID string) string {
	return traceID + "\x00" + spanID
}

func topologyStatusClass(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "unset", "0", "status_code_unset":
		return ""
	case "ok", "1", "status_code_ok":
		return "OK"
	case "error", "2", "status_code_error":
		return "error"
	default:
		return strings.TrimSpace(status)
	}
}

// chooseTopologyRoot prefers an explicitly supplied observed service, then the
// earliest trace-root span. A span whose parent was not exported is treated as
// the observed top boundary, not as a fabricated link to an unknown service.
func chooseTopologyRoot(spans []TopologySpan, bySpanID map[string]int, nodes map[string]*topologyNodeAgg, requested string) string {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		if _, ok := nodes[requested]; ok {
			return requested
		}
	}
	type candidate struct {
		service  string
		startNS  int64
		hasStart bool
	}
	candidates := make([]candidate, 0, len(spans))
	for _, span := range spans {
		if span.TraceID == "" || span.SpanID == "" {
			continue
		}
		if span.ParentSpanID != "" {
			if _, parentPresent := bySpanID[topologyKey(span.TraceID, span.ParentSpanID)]; parentPresent {
				continue
			}
		}
		candidates = append(candidates, candidate{
			service:  span.Service,
			startNS:  span.StartNS,
			hasStart: span.StartNS > 0,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].hasStart != candidates[j].hasStart {
			return candidates[i].hasStart
		}
		if candidates[i].startNS != candidates[j].startNS {
			return candidates[i].startNS < candidates[j].startNS
		}
		return candidates[i].service < candidates[j].service
	})
	if len(candidates) > 0 {
		return candidates[0].service
	}
	if len(nodes) == 1 {
		for service := range nodes {
			return service
		}
	}
	return ""
}
