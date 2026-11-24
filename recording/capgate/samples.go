package capgate

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/gogpu/gg/text"
)

// FaultSampleResult is one fault-injection sample and whether the gate caught
// it with the expected finding code.
type FaultSampleResult struct {
	Type       string           `json:"type"` // "fault_sample"
	Schema     string           `json:"schema"`
	Sample     string           `json:"sample"`
	Subject    string           `json:"subject"`
	ExpectCode string           `json:"expect_code"`
	Caught     bool             `json:"caught"`
	CaseID     string           `json:"case_id,omitempty"`
	Result     *CaseResult      `json:"case_result,omitempty"`
	Stability  *StabilityResult `json:"stability,omitempty"`
}

// RunFaultSamples executes every injected fault and reports detection.
//
// caseByID must be the built matrix so sample CaseIDs resolve; pass nil to
// use BuildMatrix(MatrixOptions{Face: face}).
func RunFaultSamples(face text.Face, caseByID map[string]Case) []FaultSampleResult {
	if caseByID == nil {
		mx := BuildMatrix(MatrixOptions{Face: face})
		caseByID = make(map[string]Case, len(mx))
		for _, c := range mx {
			caseByID[c.ID] = c
		}
	}

	out := make([]FaultSampleResult, 0, len(FaultSamples()))
	for _, fs := range FaultSamples() {
		r := FaultSampleResult{
			Type: "fault_sample", Schema: SchemaVersion,
			Sample: fs.Name, Subject: fs.Subject.Name, ExpectCode: fs.ExpectCode,
			CaseID: fs.CaseID,
		}

		if fs.StabilityOnly {
			st := AuditStability(fs.Subject)
			r.Stability = &st
			r.Caught = st.Finding != nil && st.Finding.Code == fs.ExpectCode
			out = append(out, r)
			continue
		}

		c, ok := caseByID[fs.CaseID]
		if !ok {
			r.Caught = false
			r.Result = &CaseResult{Verdict: "missing_matrix_case", CaseID: fs.CaseID}
			out = append(out, r)
			continue
		}
		cr := RunCase(fs.Subject, c, face)
		r.Result = &cr
		for _, f := range cr.Findings {
			if f.Code == fs.ExpectCode {
				r.Caught = true
				break
			}
		}
		out = append(out, r)
	}
	return out
}

// FaultSamplesCaught reports whether every injected fault was caught.
func FaultSamplesCaught(rs []FaultSampleResult) bool {
	for _, r := range rs {
		if !r.Caught {
			return false
		}
	}
	return true
}

// WriteFaultInjectionJSONL writes one JSON object per fault sample.
func WriteFaultInjectionJSONL(w io.Writer, face text.Face) error {
	enc := json.NewEncoder(w)
	for _, r := range RunFaultSamples(face, nil) {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// FaultInjectionJSONL returns the reproducible JSONL bytes for the fault
// fleet. Two calls with a nil face produce byte-identical output.
func FaultInjectionJSONL(face text.Face) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteFaultInjectionJSONL(&buf, face); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
