package report

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

//go:embed template.html
var templateHTML string

// Render returns a self-contained HTML document. The JSON payload is embedded
// directly, so the file works offline and can be copied as one artifact.
func Render(data Data) ([]byte, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal report data: %w", err)
	}
	// Keep a literal </script> inside a string from terminating the script
	// element. JSON string escaping makes this reversible by the browser.
	payloadText := strings.ReplaceAll(string(payload), "</", "<\\/")
	title := strings.TrimSpace(data.Title)
	if title == "" {
		title = "CANN eBPF 性能报告"
	}
	out := strings.ReplaceAll(templateHTML, "__TITLE__", html.EscapeString(title))
	out = strings.ReplaceAll(out, "__DATA__", payloadText)
	return []byte(out), nil
}
