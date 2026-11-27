package report

import (
	"fmt"
	"math"
	"sort"
)

const slackCDFMinimumSamples = 30

type slackSample struct {
	nodeID  string
	slackMS float64
}

type slackEdge struct {
	from int
	to   int
}

// BuildSlackCDF derives a slack distribution from the critical-path DAG. Slack
// is latest_start - earliest_start, where earliest_start is the observed start
// and latest_start is constrained by the batch end, the containing span end, and
// inferred/critical-path precedence edges. Structural holes in the DAG or fewer
// than slackCDFMinimumSamples nodes make the view unavailable instead of
// producing a misleading distribution.
func BuildSlackCDF(cp CriticalPathData) SlackCDFData {
	data := SlackCDFData{
		Status: ViewStatus{Available: false, Source: criticalPathSourceNone},
		Points: []SlackPoint{},
	}
	if !cp.Status.Available {
		data.Status.Reason = "critical path unavailable"
		if cp.Status.Reason != "" {
			data.Status.Reason += ": " + cp.Status.Reason
		}
		return data
	}

	samples, err := computeSlackSamples(cp)
	if err != nil {
		data.Status.Source = slackSource(cp.Status.Source)
		data.Status.Reason = "critical path DAG is incomplete: " + err.Error()
		return data
	}
	if len(samples) < slackCDFMinimumSamples {
		data.Status.Source = slackSource(cp.Status.Source)
		data.Status.Reason = fmt.Sprintf("slack sample size %d is below the minimum %d",
			len(samples), slackCDFMinimumSamples)
		return data
	}

	values := make([]float64, 0, len(samples))
	for _, sample := range samples {
		values = append(values, sample.slackMS)
	}
	sort.Float64s(values)
	data.MinMS = values[0]
	data.P50MS = slackPercentile(values, 0.50)
	data.P95MS = slackPercentile(values, 0.95)
	for _, value := range values {
		if value < -1e-6 {
			data.NegativeCount++
		}
	}

	type bucket struct {
		value float64
		count int
	}
	buckets := make([]bucket, 0, len(values))
	for _, value := range values {
		if len(buckets) == 0 || math.Abs(value-buckets[len(buckets)-1].value) > 1e-9 {
			buckets = append(buckets, bucket{value: value, count: 1})
			continue
		}
		buckets[len(buckets)-1].count++
	}
	cumulative := 0
	data.Points = make([]SlackPoint, 0, len(buckets))
	for _, item := range buckets {
		cumulative += item.count
		data.Points = append(data.Points, SlackPoint{
			SlackMS:    item.value,
			Count:      item.count,
			Cumulative: cumulative,
		})
	}

	data.Status = ViewStatus{
		Available:  true,
		Reason:     cp.Status.Reason,
		Source:     slackSource(cp.Status.Source),
		Confidence: cp.Status.Confidence,
	}
	if data.Status.Confidence == "" {
		data.Status.Confidence = criticalPathConfidenceInferred
		if data.Status.Reason == "" {
			data.Status.Reason = "slack estimated from an inferred critical-path DAG"
		}
	}
	return data
}

func computeSlackSamples(cp CriticalPathData) ([]slackSample, error) {
	nodes := cp.Nodes
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes")
	}
	if cp.TotalMS < 0 || math.IsNaN(cp.TotalMS) || math.IsInf(cp.TotalMS, 0) {
		return nil, fmt.Errorf("invalid total_ms %v", cp.TotalMS)
	}

	byID := make(map[string]int, len(nodes))
	parent := make([]int, len(nodes))
	for i := range parent {
		parent[i] = -1
	}
	for i, node := range nodes {
		if node.ID == "" {
			return nil, fmt.Errorf("node %d has an empty id", i)
		}
		if _, exists := byID[node.ID]; exists {
			return nil, fmt.Errorf("duplicate node id %q", node.ID)
		}
		if node.StartMS < 0 || math.IsNaN(node.StartMS) || math.IsInf(node.StartMS, 0) {
			return nil, fmt.Errorf("node %q has invalid start_ms", node.ID)
		}
		if node.DurationMS < 0 || math.IsNaN(node.DurationMS) || math.IsInf(node.DurationMS, 0) {
			return nil, fmt.Errorf("node %q has invalid duration_ms", node.ID)
		}
		byID[node.ID] = i
	}
	for i, node := range nodes {
		if node.Parent == "" {
			continue
		}
		parentIndex, ok := byID[node.Parent]
		if !ok {
			return nil, fmt.Errorf("node %q references missing parent %q", node.ID, node.Parent)
		}
		if parentIndex == i {
			return nil, fmt.Errorf("node %q is its own parent", node.ID)
		}
		parent[i] = parentIndex
	}

	const (
		unvisited = iota
		visiting
		visited
	)
	state := make([]int, len(nodes))
	var visit func(int) error
	visit = func(index int) error {
		switch state[index] {
		case visiting:
			return fmt.Errorf("parent cycle at node %q", nodes[index].ID)
		case visited:
			return nil
		}
		state[index] = visiting
		if parentIndex := parent[index]; parentIndex >= 0 {
			if err := visit(parentIndex); err != nil {
				return err
			}
		}
		state[index] = visited
		return nil
	}
	for i := range nodes {
		if err := visit(i); err != nil {
			return nil, err
		}
	}

	children := make([][]int, len(nodes))
	roots := make([]int, 0, len(nodes))
	for i, parentIndex := range parent {
		if parentIndex < 0 {
			roots = append(roots, i)
			continue
		}
		children[parentIndex] = append(children[parentIndex], i)
	}

	edges := make([]slackEdge, 0, len(nodes))
	seenEdges := map[[2]int]struct{}{}
	addEdge := func(from, to int) {
		if from == to {
			return
		}
		key := [2]int{from, to}
		if _, exists := seenEdges[key]; exists {
			return
		}
		seenEdges[key] = struct{}{}
		edges = append(edges, slackEdge{from: from, to: to})
	}

	groups := map[int][]int{-1: roots}
	for index, parentIndex := range parent {
		if parentIndex >= 0 {
			groups[parentIndex] = append(groups[parentIndex], index)
		}
	}
	for _, group := range groups {
		sort.SliceStable(group, func(i, j int) bool {
			left, right := nodes[group[i]], nodes[group[j]]
			if left.StartMS == right.StartMS {
				return left.ID < right.ID
			}
			return left.StartMS < right.StartMS
		})
		for i := 1; i < len(group); i++ {
			previous, current := group[i-1], group[i]
			previousEnd := nodes[previous].StartMS + nodes[previous].DurationMS
			if previousEnd <= nodes[current].StartMS+1e-9 {
				addEdge(previous, current)
			}
		}
	}
	for i := 1; i < len(cp.CriticalPath); i++ {
		from, ok := byID[cp.CriticalPath[i-1]]
		if !ok {
			return nil, fmt.Errorf("critical path references missing node %q", cp.CriticalPath[i-1])
		}
		to, ok := byID[cp.CriticalPath[i]]
		if !ok {
			return nil, fmt.Errorf("critical path references missing node %q", cp.CriticalPath[i])
		}
		addEdge(from, to)
	}

	adjacency := make([][]int, len(nodes))
	indegree := make([]int, len(nodes))
	for _, edge := range edges {
		adjacency[edge.from] = append(adjacency[edge.from], edge.to)
		indegree[edge.to]++
	}
	queue := make([]int, 0, len(nodes))
	for i, degree := range indegree {
		if degree == 0 {
			queue = append(queue, i)
		}
	}
	sort.Ints(queue)
	order := make([]int, 0, len(nodes))
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		order = append(order, current)
		for _, next := range adjacency[current] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
				sort.Ints(queue)
			}
		}
	}
	if len(order) != len(nodes) {
		return nil, fmt.Errorf("dependency graph contains a cycle")
	}

	projectEnd := cp.TotalMS
	for _, node := range nodes {
		if end := node.StartMS + node.DurationMS; end > projectEnd {
			projectEnd = end
		}
	}
	if projectEnd < 0 || math.IsNaN(projectEnd) || math.IsInf(projectEnd, 0) {
		return nil, fmt.Errorf("invalid project end %v", projectEnd)
	}

	latestFinish := make([]float64, len(nodes))
	for i := range nodes {
		latestFinish[i] = projectEnd
		if parentIndex := parent[i]; parentIndex >= 0 {
			parentEnd := nodes[parentIndex].StartMS + nodes[parentIndex].DurationMS
			if parentEnd < latestFinish[i] {
				latestFinish[i] = parentEnd
			}
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		current := order[i]
		for _, next := range adjacency[current] {
			nextLatestStart := latestFinish[next] - nodes[next].DurationMS
			if nextLatestStart < latestFinish[current] {
				latestFinish[current] = nextLatestStart
			}
		}
	}

	samples := make([]slackSample, 0, len(nodes))
	for i, node := range nodes {
		slack := latestFinish[i] - node.DurationMS - node.StartMS
		if math.IsNaN(slack) || math.IsInf(slack, 0) {
			return nil, fmt.Errorf("node %q produced invalid slack", node.ID)
		}
		samples = append(samples, slackSample{
			nodeID:  node.ID,
			slackMS: roundSlackMilliseconds(slack),
		})
	}
	return samples, nil
}

func slackPercentile(sorted []float64, percentile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 1 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func roundSlackMilliseconds(value float64) float64 {
	return math.Round(value*1e6) / 1e6
}

func slackSource(source string) string {
	if source == "" {
		return criticalPathSourceNone
	}
	return source
}
