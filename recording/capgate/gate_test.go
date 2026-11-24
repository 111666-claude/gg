package capgate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gogpu/gg/recording"
)

// TestMatrixCoversAllRequiredAreas ensures the command matrix actually spans
// every mandated feature family and every blend mode.
func TestMatrixCoversAllRequiredAreas(t *testing.T) {
	cases := BuildMatrix(MatrixOptions{})
	wantCategories := map[string]bool{
		"path": false, "roundrect": false, "clip": false, "image": false,
		"text": false, "gradient": false, "blend": false, "state": false,
	}
	blendModes := map[recording.BlendMode]bool{}
	ids := map[string]bool{}
	for _, c := range cases {
		if _, ok := wantCategories[c.Category]; ok {
			wantCategories[c.Category] = true
		}
		if ids[c.ID] {
			t.Errorf("duplicate matrix case id %q", c.ID)
		}
		ids[c.ID] = true
		if c.Category == "blend" {
			blendModes[c.Blend] = true
		}
		if c.Build == nil {
			t.Errorf("case %s has nil Build", c.ID)
		}
	}
	for cat, seen := range wantCategories {
		if !seen {
			t.Errorf("matrix missing category %q", cat)
		}
	}
	for _, m := range recording.AllBlendModes() {
		if !blendModes[m] {
			t.Errorf("matrix missing blend mode %d (%s)", m, BlendModeName(m))
		}
	}
	if got, want := len(blendModes), len(recording.AllBlendModes()); got != want {
		t.Errorf("blend cases = %d, want %d", got, want)
	}
}

// TestPerfectSpyPassesMatrix: a backend that honestly declares and performs
// everything must produce no error-severity findings and must not mutate the
// recording.
func TestPerfectSpyPassesMatrix(t *testing.T) {
	cases := BuildMatrix(MatrixOptions{})
	subj := PerfectSubject()
	for _, c := range cases {
		res := RunCase(subj, c, nil)
		switch res.Verdict {
		case VerdictPass, VerdictPassFallback:
		default:
			t.Errorf("case %s: unexpected verdict %s err=%q findings=%v",
				c.ID, res.Verdict, res.PlaybackError, codes(res.Findings))
		}
		if !res.RecordingUnmodified {
			t.Errorf("case %s: recording/resources mutated by perfect spy", c.ID)
		}
		if res.StackChecked && !res.StackBalanced {
			t.Errorf("case %s: stack not balanced", c.ID)
		}
		for _, f := range res.Findings {
			if f.Severity == SeverityError {
				t.Errorf("case %s: unexpected error finding %s: %s", c.ID, f.Code, f.Message)
			}
		}
	}
}

// TestPerfectSpyStableCapabilities verifies the reference profile is stable.
func TestPerfectSpyStableCapabilities(t *testing.T) {
	st := AuditStability(PerfectSubject())
	if !st.Stable || st.Finding != nil {
		t.Fatalf("reference capabilities should be stable, got profiles=%v", st.Profiles)
	}
}

// TestFaultInjectionAllCaught is the key proof that the gate catches every
// injected defect class, notably the silent dropped draw.
func TestFaultInjectionAllCaught(t *testing.T) {
	results := RunFaultSamples(nil, nil)
	if len(results) != len(FaultSamples()) {
		t.Fatalf("got %d sample results, want %d", len(results), len(FaultSamples()))
	}
	for _, r := range results {
		if !r.Caught {
			t.Errorf("gate failed to catch %q (expected code %s)", r.Sample, r.ExpectCode)
			continue
		}
		t.Logf("caught %-42s via %s", r.Sample, r.ExpectCode)
	}
	if !FaultSamplesCaught(results) {
		t.Fatal("not every fault injection was caught")
	}
}

// TestSilentDropExplicitlyReported makes the headline guarantee explicit:
// a declared capability with no error but a missing call yields silent_drop
// with a locatable case/subject report.
func TestSilentDropExplicitlyReported(t *testing.T) {
	cases := BuildMatrix(MatrixOptions{})
	byID := map[string]Case{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	for _, id := range []string{"image/draw", "blend/multiply-legacy"} {
		c := byID[id]
		subj := Subject{
			Name:    "explicit-drop",
			Profile: profilePtr(FullCapabilities()),
			New:     func() recording.Backend { return NewSilentDropBackend(dropOpFor(id)) },
		}
		res := RunCase(subj, c, nil)
		if res.Verdict != VerdictSilentDrop {
			t.Fatalf("%s: verdict=%s, want silent_drop", id, res.Verdict)
		}
		var found bool
		for _, f := range res.Findings {
			if f.Code == CodeSilentDrop && strings.Contains(f.Message, "silently dropped") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: missing locatable silent_drop finding: %+v", id, res.Findings)
		}
	}
}

func dropOpFor(caseID string) string {
	if strings.HasPrefix(caseID, "blend/") {
		return OpSetBlendMode
	}
	return OpDrawImage
}

// TestFaultJSONLReproducible proves the JSONL fault summary is byte-for-byte
// reproducible across runs in the same environment and is valid JSONL.
func TestFaultJSONLReproducible(t *testing.T) {
	a, err := FaultInjectionJSONL(nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := FaultInjectionJSONL(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("fault injection JSONL is not reproducible")
	}

	lines := bytes.Split(bytes.TrimRight(a, "\n"), []byte("\n"))
	if len(lines) != len(FaultSamples()) {
		t.Fatalf("JSONL lines = %d, want %d", len(lines), len(FaultSamples()))
	}
	for i, ln := range lines {
		var rec map[string]any
		if err := json.Unmarshal(ln, &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v", i+1, err)
		}
		if rec["type"] != "fault_sample" {
			t.Fatalf("line %d type=%v", i+1, rec["type"])
		}
		if rec["caught"] != true {
			t.Fatalf("line %d sample not caught: %s", i+1, ln)
		}
	}
}

// TestCheckedInFaultSampleIsCurrent verifies the reviewed JSONL sample under
// testdata/ parses, reports every fault caught, and matches the generator.
//
// Regenerate with:
//
//	go test ./recording/capgate/ -run TestCheckedInFaultSampleIsCurrent -update
var updateGolden = boolEnv("CAPGATE_UPDATE_GOLDEN")

func boolEnv(k string) bool {
	return os.Getenv(k) == "1" || strings.EqualFold(os.Getenv(k), "true")
}

func TestCheckedInFaultSampleIsCurrent(t *testing.T) {
	golden := goldenPath(t, "fault-injection.jsonl")
	generated, err := FaultInjectionJSONL(nil)
	if err != nil {
		t.Fatal(err)
	}

	if updateGolden {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, generated, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated golden %s", golden)
	}

	onDisk, err := os.ReadFile(golden)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden sample missing (%s); regenerate with CAPGATE_UPDATE_GOLDEN=1", golden)
		}
		t.Fatal(err)
	}
	if !bytes.Equal(normalizeNL(onDisk), normalizeNL(generated)) {
		t.Fatalf("checked-in fault sample is stale; regenerate with CAPGATE_UPDATE_GOLDEN=1")
	}

	// Every expected code must appear and every sample caught.
	text := string(onDisk)
	for _, fs := range FaultSamples() {
		if !strings.Contains(text, fs.ExpectCode) {
			t.Errorf("golden sample missing code %q", fs.ExpectCode)
		}
	}
}

func normalizeNL(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }

func goldenPath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file path")
	}
	return filepath.Join(filepath.Dir(thisFile), "testdata", name)
}

func codes(fs []Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Code
	}
	return out
}
