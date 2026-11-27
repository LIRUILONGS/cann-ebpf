package report

// ReportViews is the stable JSON boundary for the M1/M2 visual views.
// Each view carries its own availability and reason so the HTML can explain
// an unavailable panel without fabricating data.
type ReportViews struct {
	LatencyHeatmap   LatencyHeatmapData   `json:"latency_heatmap"`
	SubsecondHeatmap SubsecondHeatmapData `json:"subsecond_heatmap"`
	AggregateBands   AggregateBandsData   `json:"aggregate_bands"`
	TailCohort       TailCohortData       `json:"tail_cohort"`
}
