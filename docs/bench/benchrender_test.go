//go:build benchrender

package bench

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	benchStartMarker = "<!-- bench:embed:start -->"
	benchEndMarker   = "<!-- bench:embed:end -->"
)

var benchLinePattern = regexp.MustCompile(`^BenchmarkDriverDispatch_(?:Local|Integration)/(.+)-\d+\s+(\d+)\s+([0-9.]+)\s+ns/op\s+(\d+)\s+B/op\s+(\d+)\s+allocs/op$`)
var benchNamePattern = regexp.MustCompile(`^BenchmarkDriverDispatch_(?:Local|Integration)/(.+)-\d+`)
var benchMetricsPattern = regexp.MustCompile(`^(\d+)\s+([0-9.]+)\s+ns/op\s+(\d+)\s+B/op\s+(\d+)\s+allocs/op$`)

type benchRow struct {
	Set      string
	Driver   string
	N        int
	NsOp     string
	BOp      int
	AllocsOp int
}

func TestRenderBenchmarks(t *testing.T) {
	root := findRepoRoot(t)

	localOutput := runBenchCommand(t, root, commandSpec{
		args: []string{
			"test", "./...",
			"-run=^$",
			"-bench", "BenchmarkDriverDispatch_Local",
		},
		env: map[string]string{
			"GOCACHE": "/tmp/queue-gocache",
		},
	})

	rows := parseRows(t, localOutput, "Local")

	if integrationBackends := os.Getenv("INTEGRATION_BACKEND"); integrationBackends != "" {
		integrationOutput := runBenchCommand(t, root, commandSpec{
			args: []string{
				"test", "./...",
				"-tags", "integration",
				"-run=^$",
				"-bench", "BenchmarkDriverDispatch_Integration",
			},
			env: map[string]string{
				"INTEGRATION_BACKEND": integrationBackends,
				"GOCACHE":             "/tmp/queue-gocache",
			},
		})
		rows = append(rows, parseRows(t, integrationOutput, "Integration")...)
	}

	rendered := renderTable(rows)
	readmePath := filepath.Join(root, "README.md")
	readmeRaw, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	updated, err := replaceBenchBlock(string(readmeRaw), rendered)
	if err != nil {
		t.Fatalf("replace bench block: %v", err)
	}
	if err := os.WriteFile(readmePath, []byte(updated), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
}

type commandSpec struct {
	args []string
	env  map[string]string
}

func runBenchCommand(t *testing.T, root string, spec commandSpec) string {
	t.Helper()
	cmd := exec.Command("go", spec.args...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	for k, v := range spec.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %q failed: %v\n%s", strings.Join(spec.args, " "), err, string(out))
	}
	return string(out)
}

func parseRows(t *testing.T, benchOutput, set string) []benchRow {
	t.Helper()
	rows := make([]benchRow, 0)
	scanner := bufio.NewScanner(strings.NewReader(benchOutput))
	pendingDriver := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		m := benchLinePattern.FindStringSubmatch(line)
		if m != nil {
			n, err := strconv.Atoi(m[2])
			if err != nil {
				t.Fatalf("parse N from %q: %v", line, err)
			}
			bop, err := strconv.Atoi(m[4])
			if err != nil {
				t.Fatalf("parse B/op from %q: %v", line, err)
			}
			allocs, err := strconv.Atoi(m[5])
			if err != nil {
				t.Fatalf("parse allocs/op from %q: %v", line, err)
			}
			rows = append(rows, benchRow{
				Set:      set,
				Driver:   m[1],
				N:        n,
				NsOp:     m[3],
				BOp:      bop,
				AllocsOp: allocs,
			})
			pendingDriver = ""
			continue
		}

		// Some drivers may log while benchmark runs. In that case, go test can
		// emit the benchmark name on one line and the metrics on a later line.
		// Capture that split form as well.
		if m := benchNamePattern.FindStringSubmatch(line); m != nil {
			pendingDriver = m[1]
			continue
		}

		if pendingDriver == "" {
			continue
		}
		m = benchMetricsPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("parse N from %q: %v", line, err)
		}
		bop, err := strconv.Atoi(m[3])
		if err != nil {
			t.Fatalf("parse B/op from %q: %v", line, err)
		}
		allocs, err := strconv.Atoi(m[4])
		if err != nil {
			t.Fatalf("parse allocs/op from %q: %v", line, err)
		}
		rows = append(rows, benchRow{
			Set:      set,
			Driver:   pendingDriver,
			N:        n,
			NsOp:     m[2],
			BOp:      bop,
			AllocsOp: allocs,
		})
		pendingDriver = ""
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan benchmark output: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("no benchmark rows parsed for set %q", set)
	}
	return rows
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
	for i, set := range sets {
		groupRows := grouped[set]
		sort.Slice(groupRows, func(i, j int) bool {
			return groupRows[i].Driver < groupRows[j].Driver
		})
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString("### ")
		buf.WriteString(set)
		buf.WriteString("\n\n")
		buf.WriteString("| Driver | N | ns/op | B/op | allocs/op |\n")
		buf.WriteString("|:------|---:|-----:|-----:|---------:|\n")
		for _, row := range groupRows {
			buf.WriteString(fmt.Sprintf("| %s | %d | %s | %d | %d |\n", row.Driver, row.N, row.NsOp, row.BOp, row.AllocsOp))
		}
	}
	return strings.TrimSpace(buf.String())
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

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("find repo root from %q: %v", wd, err)
	}
	return root
}
