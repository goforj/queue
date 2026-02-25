//go:build benchrender
// +build benchrender

package bench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	benchStartMarker = "<!-- bench:embed:start -->"
	benchEndMarker   = "<!-- bench:embed:end -->"
	benchRowsFile    = "benchmarks_rows.json"
)

var (
	benchLinePattern    = regexp.MustCompile(`^BenchmarkDriverDispatch_(?:Local|Integration)/(.+)-\d+\s+(\d+)\s+([0-9.]+)\s+ns/op\s+(\d+)\s+B/op\s+(\d+)\s+allocs/op$`)
	benchNamePattern    = regexp.MustCompile(`^BenchmarkDriverDispatch_(?:Local|Integration)/(.+)-\d+`)
	benchMetricsPattern = regexp.MustCompile(`^(\d+)\s+([0-9.]+)\s+ns/op\s+(\d+)\s+B/op\s+(\d+)\s+allocs/op$`)
)

type benchRow struct {
	Set      string  `json:"set"`
	Driver   string  `json:"driver"`
	N        int     `json:"n"`
	NsOp     float64 `json:"ns_op"`
	BOp      int     `json:"b_op"`
	AllocsOp int     `json:"allocs_op"`
}

// RenderBenchmarks runs queue benchmark suites, writes a benchmark snapshot, and
// injects a throughput-focused dashboard into README.md.
func RenderBenchmarks() {
	root := findRepoRoot()
	rowsPath := filepath.Join(root, "docs", "bench", benchRowsFile)

	var rows []benchRow
	if os.Getenv("BENCH_RENDER_ONLY") == "1" {
		loaded, err := loadBenchmarkRows(rowsPath)
		if err != nil {
			panic(fmt.Errorf("render-only mode requires snapshot at %s: %w", rowsPath, err))
		}
		rows = loaded
	} else {
		rows = runBenchmarks(root)
		if err := saveBenchmarkRows(rowsPath, rows); err != nil {
			panic(err)
		}
	}

	if err := writeDashboard(root, rows); err != nil {
		panic(err)
	}

	rendered := renderTable(rows)
	readmePath := filepath.Join(root, "README.md")
	readmeRaw, err := os.ReadFile(readmePath)
	if err != nil {
		panic(err)
	}
	updated, err := replaceBenchBlock(string(readmeRaw), rendered)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(readmePath, []byte(updated), 0o644); err != nil {
		panic(err)
	}
	fmt.Println("✔ Benchmarks dashboard updated")
}

func runBenchmarks(root string) []benchRow {
	localOutput := runBenchCommand(root, commandSpec{
		dir: root,
		args: []string{
			"test", "./...",
			"-run=^$",
			"-bench", "BenchmarkDriverDispatch_Local",
		},
		env: map[string]string{
			"GOCACHE": "/tmp/queue-gocache",
		},
	})

	rows := parseRows(localOutput, "Local")

	// Integration benches live in the separate /integration module.
	if integrationBackends := os.Getenv("INTEGRATION_BACKEND"); integrationBackends != "" {
		integrationRoot := filepath.Join(root, "integration")
		integrationOutput := runBenchCommand(root, commandSpec{
			dir: integrationRoot,
			args: []string{
				"test", "./...",
				"-tags=integration",
				"-run=^$",
				"-bench", "BenchmarkDriverDispatch_Integration",
			},
			env: map[string]string{
				"INTEGRATION_BACKEND": integrationBackends,
				"GOCACHE":             "/tmp/queue-gocache",
			},
		})
		rows = append(rows, parseRows(integrationOutput, "Integration")...)
	}

	return rows
}

type commandSpec struct {
	dir  string
	args []string
	env  map[string]string
}

func runBenchCommand(root string, spec commandSpec) string {
	cmd := exec.Command("go", spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = os.Environ()
	for k, v := range spec.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		panic(fmt.Errorf("run %q in %s failed: %w\n%s", strings.Join(spec.args, " "), strings.TrimPrefix(spec.dir, root+"/"), err, string(out)))
	}
	return string(out)
}

func parseRows(benchOutput, set string) []benchRow {
	rows := make([]benchRow, 0)
	scanner := bufio.NewScanner(strings.NewReader(benchOutput))
	pendingDriver := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if m := benchLinePattern.FindStringSubmatch(line); m != nil {
			rows = append(rows, parseBenchRow(set, m[1], m[2], m[3], m[4], m[5], line))
			pendingDriver = ""
			continue
		}
		if m := benchNamePattern.FindStringSubmatch(line); m != nil {
			pendingDriver = m[1]
			continue
		}
		if pendingDriver == "" {
			continue
		}
		m := benchMetricsPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows = append(rows, parseBenchRow(set, pendingDriver, m[1], m[2], m[3], m[4], line))
		pendingDriver = ""
	}
	if err := scanner.Err(); err != nil {
		panic(fmt.Errorf("scan benchmark output: %w", err))
	}
	if len(rows) == 0 {
		panic(fmt.Errorf("no benchmark rows parsed for set %q", set))
	}
	return rows
}

func parseBenchRow(set, driver, nStr, nsStr, bStr, allocStr, line string) benchRow {
	n, err := strconv.Atoi(nStr)
	if err != nil {
		panic(fmt.Errorf("parse N from %q: %w", line, err))
	}
	ns, err := strconv.ParseFloat(nsStr, 64)
	if err != nil {
		panic(fmt.Errorf("parse ns/op from %q: %w", line, err))
	}
	bop, err := strconv.Atoi(bStr)
	if err != nil {
		panic(fmt.Errorf("parse B/op from %q: %w", line, err))
	}
	allocs, err := strconv.Atoi(allocStr)
	if err != nil {
		panic(fmt.Errorf("parse allocs/op from %q: %w", line, err))
	}
	return benchRow{
		Set:      set,
		Driver:   driver,
		N:        n,
		NsOp:     ns,
		BOp:      bop,
		AllocsOp: allocs,
	}
}

func renderTable(rows []benchRow) string {
	grouped := map[string][]benchRow{}
	for _, row := range rows {
		grouped[row.Set] = append(grouped[row.Set], row)
	}
	sets := make([]string, 0, len(grouped))
	for set := range grouped {
		sets = append(sets, set)
	}
	sort.Strings(sets)

	var buf bytes.Buffer
	buf.WriteString("> Benchmark results focus on dispatch throughput (`BenchmarkDriverDispatch_*`). Lower `ns/op` means higher throughput. `ops/s` is derived as `1e9 / ns/op`.\n\n")
	buf.WriteString("### Latency (ns/op)\n\n")
	buf.WriteString("![Queue benchmark latency chart](docs/bench/benchmarks_ns.svg)\n\n")
	buf.WriteString("### Throughput (ops/s)\n\n")
	buf.WriteString("![Queue benchmark throughput chart](docs/bench/benchmarks_ops.svg)\n\n")
	buf.WriteString("### Allocated Bytes (B/op)\n\n")
	buf.WriteString("![Queue benchmark bytes chart](docs/bench/benchmarks_bytes.svg)\n\n")
	buf.WriteString("### Allocations (allocs/op)\n\n")
	buf.WriteString("![Queue benchmark allocations chart](docs/bench/benchmarks_allocs.svg)\n\n")
	buf.WriteString("### Tables\n\n")
	for i, set := range sets {
		groupRows := append([]benchRow(nil), grouped[set]...)
		sort.Slice(groupRows, func(i, j int) bool { return groupRows[i].NsOp < groupRows[j].NsOp })
		fastest := groupRows[0].NsOp

		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString("### ")
		buf.WriteString(displaySetLabel(set))
		buf.WriteString(" Dispatch Throughput\n\n")
		buf.WriteString("| Driver | ns/op | ops/s | Relative | B/op | allocs/op |\n")
		buf.WriteString("|:------|-----:|-----:|--------:|-----:|---------:|\n")
		for _, row := range groupRows {
			opsPerSec := 1e9 / row.NsOp
			relative := row.NsOp / fastest
			buf.WriteString(fmt.Sprintf(
				"| %s | %.0f | %.0f | %.2fx | %d | %d |\n",
				row.Driver, row.NsOp, opsPerSec, relative, row.BOp, row.AllocsOp,
			))
		}
	}
	return strings.TrimSpace(buf.String())
}

func writeDashboard(root string, rows []benchRow) error {
	if len(rows) == 0 {
		return nil
	}
	sorted := append([]benchRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Set != sorted[j].Set {
			return sorted[i].Set < sorted[j].Set
		}
		return sorted[i].NsOp < sorted[j].NsOp
	})
	if err := writeMetricSVG(root, "benchmarks_ns.svg", "Queue Dispatch Latency", "ns/op", false, sorted, func(r benchRow) float64 { return r.NsOp }); err != nil {
		return err
	}
	if err := writeMetricSVG(root, "benchmarks_ops.svg", "Queue Dispatch Throughput", "ops/s", true, sorted, func(r benchRow) float64 { return 1e9 / r.NsOp }); err != nil {
		return err
	}
	if err := writeMetricSVG(root, "benchmarks_bytes.svg", "Queue Dispatch Allocated Bytes", "B/op", false, sorted, func(r benchRow) float64 { return float64(r.BOp) }); err != nil {
		return err
	}
	return writeMetricSVG(root, "benchmarks_allocs.svg", "Queue Dispatch Allocations", "allocs/op", false, sorted, func(r benchRow) float64 { return float64(r.AllocsOp) })
}

func writeMetricSVG(root, fileName, title, unit string, higherBetter bool, rows []benchRow, valueFn func(benchRow) float64) error {
	const (
		width        = 1200
		topPad       = 90
		bottomPad    = 40
		leftPad      = 280
		rightPad     = 120
		rowHeight    = 34
		rowGap       = 12
		axisLabelGap = 8
	)
	height := topPad + bottomPad + len(rows)*(rowHeight+rowGap)
	plotW := width - leftPad - rightPad
	maxV := 0.0
	values := make([]float64, len(rows))
	for i, row := range rows {
		v := valueFn(row)
		values[i] = v
		if v > maxV {
			maxV = v
		}
	}
	if maxV <= 0 {
		maxV = 1
	}
	maxV *= 1.10

	var svg bytes.Buffer
	svg.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	svg.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`+"\n", width, height, width, height))
	svg.WriteString(`<rect width="100%" height="100%" fill="#111827"/>` + "\n")
	pref := "lower is better"
	if higherBetter {
		pref = "higher is better"
	}
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="38" text-anchor="middle" fill="#f9fafb" font-size="26" font-family="Arial, sans-serif">%s (%s)</text>`+"\n", width/2, xmlEscape(title), xmlEscape(unit)))
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="66" text-anchor="middle" fill="#9ca3af" font-size="16" font-family="Arial, sans-serif">%s</text>`+"\n", width/2, xmlEscape(pref)))

	axisX0 := leftPad
	// grid
	for i := 0; i <= 5; i++ {
		p := float64(i) / 5.0
		x := axisX0 + int(p*float64(plotW))
		v := p * maxV
		svg.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#374151" stroke-width="1"/>`+"\n", x, topPad-10, x, height-bottomPad))
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" fill="#d1d5db" font-size="13" font-family="Arial, sans-serif">%s</text>`+"\n", x, topPad-axisLabelGap, formatMetric(v)))
	}

	for i, row := range rows {
		y := topPad + i*(rowHeight+rowGap)
		centerY := y + rowHeight/2
		label := row.Driver
		if row.Set != "" {
			label = displaySetLabel(row.Set) + "/" + row.Driver
		}
		// track
		svg.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" rx="4" fill="#1f2937"/>`+"\n", axisX0, y, plotW, rowHeight))
		barW := int((values[i] / maxV) * float64(plotW))
		if barW < 1 && values[i] > 0 {
			barW = 1
		}
		color := "#60a5fa"
		if row.Set == "Integration" {
			color = "#34d399"
		}
		svg.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" rx="4" fill="%s"/>`+"\n", axisX0, y, barW, rowHeight, color))
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="end" fill="#e5e7eb" font-size="14" font-family="Arial, sans-serif">%s</text>`+"\n", leftPad-10, centerY+5, xmlEscape(label)))
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="start" fill="#f3f4f6" font-size="13" font-family="Arial, sans-serif">%s</text>`+"\n", axisX0+barW+8, centerY+5, formatMetric(values[i])))
	}

	legendY := height - 14
	svg.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="12" height="12" fill="#60a5fa"/>`+"\n", leftPad, legendY-10))
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="#d1d5db" font-size="12" font-family="Arial, sans-serif">Local</text>`+"\n", leftPad+18, legendY))
	svg.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="12" height="12" fill="#34d399"/>`+"\n", leftPad+80, legendY-10))
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="#d1d5db" font-size="12" font-family="Arial, sans-serif">External</text>`+"\n", leftPad+98, legendY))

	svg.WriteString(`</svg>` + "\n")
	return os.WriteFile(filepath.Join(root, "docs", "bench", fileName), svg.Bytes(), 0o644)
}

func formatMetric(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("%.1fk", v/1_000)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func displaySetLabel(set string) string {
	switch set {
	case "Integration":
		return "External"
	default:
		return set
	}
}

func replaceBenchBlock(readme, rendered string) (string, error) {
	start := strings.Index(readme, benchStartMarker)
	end := strings.Index(readme, benchEndMarker)
	if start == -1 || end == -1 || end <= start {
		return "", fmt.Errorf("bench embed markers missing or malformed")
	}
	startContent := start + len(benchStartMarker)
	var out strings.Builder
	out.WriteString(readme[:startContent])
	out.WriteString("\n\n")
	out.WriteString(rendered)
	out.WriteString("\n\n")
	out.WriteString(readme[end:])
	return out.String(), nil
}

func findRepoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(fmt.Errorf("getwd: %w", err))
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		panic(fmt.Errorf("find repo root from %q: %w", wd, err))
	}
	return root
}

func saveBenchmarkRows(path string, rows []benchRow) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func loadBenchmarkRows(path string) ([]benchRow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []benchRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("empty benchmark row snapshot")
	}
	return rows, nil
}
