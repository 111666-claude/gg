// Command capgate runs the rendering-backend capability consistency gate.
//
// It replays the command matrix against the in-memory reference double and
// the pure-CPU raster backend (no GPU required), writes reproducible JSONL
// reports, and runs the fault-injection fleet proving the gate catches
// silent dropped draws and the other capability/behavior deviations.
//
// Usage:
//
//	go run ./cmd/capgate -out artifacts/capgate
//
// Exit status is non-zero if a reference backend has error-severity findings
// or any injected fault is not caught.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gogpu/gg/recording"
	"github.com/gogpu/gg/recording/backends/raster"
	"github.com/gogpu/gg/recording/capgate"
)

func main() {
	out := flag.String("out", "artifacts/capgate", "directory for JSONL reports")
	scale := flag.Float64("scale", 1, "raster backend device scale")
	flag.Parse()

	if err := run(*out, *scale); err != nil {
		fmt.Fprintf(os.Stderr, "capgate: %v\n", err)
		os.Exit(1)
	}
}

func run(outDir string, scale float64) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	matrix := capgate.BuildMatrix(capgate.MatrixOptions{})

	// Reference subjects: the all-capable double and the real CPU raster.
	subjects := []capgate.Subject{
		capgate.PerfectSubject(),
		{
			Name: "raster-cpu",
			New: func() recording.Backend {
				return capgate.NewInstrumented("raster-cpu", raster.NewBackendWithScale(scale))
			},
		},
	}

	report := capgate.Run(subjects, matrix, nil)

	reportPath := filepath.Join(outDir, "report.jsonl")
	f, err := os.Create(reportPath)
	if err != nil {
		return err
	}
	if err := report.WriteJSONL(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	faultBytes, err := capgate.FaultInjectionJSONL(nil)
	if err != nil {
		return err
	}
	faultPath := filepath.Join(outDir, "fault-injection.jsonl")
	if err := os.WriteFile(faultPath, faultBytes, 0o644); err != nil {
		return err
	}

	faultResults := capgate.RunFaultSamples(nil, nil)
	caught := capgate.FaultSamplesCaught(faultResults)

	fmt.Printf("capability gate %s\n", capgate.SchemaVersion)
	fmt.Printf("matrix cases/subject : %d\n", len(matrix))
	fmt.Printf("subjects             : %d\n", report.Summary.Subjects)
	fmt.Printf("case results         : %d\n", report.Summary.CaseResults)
	fmt.Printf("verdicts             : %v\n", report.Summary.ByVerdict)
	fmt.Printf("findings by code     : %v\n", report.Summary.FindingsByCode)
	fmt.Printf("error findings       : %d\n", report.Summary.ErrorFindings)
	fmt.Printf("fault samples caught : %d/%d\n", countCaught(faultResults), len(faultResults))
	fmt.Printf("wrote %s\n", reportPath)
	fmt.Printf("wrote %s\n", faultPath)

	if report.Summary.ErrorFindings != 0 {
		return fmt.Errorf("reference gate has %d error findings", report.Summary.ErrorFindings)
	}
	if !caught {
		return fmt.Errorf("fault injection fleet contains an uncaught defect")
	}
	fmt.Println("GATE PASS: reference backends consistent; all injected faults caught")
	return nil
}

func countCaught(rs []capgate.FaultSampleResult) int {
	n := 0
	for _, r := range rs {
		if r.Caught {
			n++
		}
	}
	return n
}
