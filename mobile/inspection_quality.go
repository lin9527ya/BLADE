package mobile

import "strings"

// InspectionStage describes what was actually readable. A missing or capped
// input is distinct from a completed check with zero results.
type InspectionStage struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Checked int    `json:"checked"`
	Total   int    `json:"total"`
	Note    string `json:"note,omitempty"`
}

func (m *PackageMetadata) ProtectReportMetadata() {
	for i := range m.CandidateURLs {
		m.CandidateURLs[i] = reportURL(m.CandidateURLs[i])
	}
	for i := range m.StaticFindings {
		f := &m.StaticFindings[i]
		if strings.Contains(f.Evidence, "://") {
			f.Evidence = reportURL(f.Evidence)
		}
	}
}

func (m *PackageMetadata) ReviewIncomplete() bool {
	for _, c := range m.Checks {
		if c.Status == "partial" || c.Status == "unavailable" || c.Status == "limited" || c.Status == "not_checked" || c.Status == "unknown" {
			return true
		}
	}
	return false
}

func countedStage(name string, checked, total int) InspectionStage {
	s := InspectionStage{Name: name, Checked: checked, Total: total, Status: "complete"}
	switch {
	case total == 0:
		s.Status = "not_present"
	case checked == 0:
		s.Status = "unavailable"
	case checked < total:
		s.Status = "partial"
	}
	return s
}
