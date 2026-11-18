package db

import "testing"

func TestValidateDetectionResult(t *testing.T) {
	tech, err := ValidateDetectionResult(StateDetectionTriggered, "t1046", "nmap 扫过，Suricata 告警", "high")
	if err != nil || tech != "T1046" {
		t.Fatalf("valid detection rejected: tech=%q err=%v", tech, err)
	}
	bad := []struct {
		name, result, technique, evidence, confidence string
	}{
		{"bad result", "detected", "T1046", "x", "high"},
		{"bad technique", StateDetectionTriggered, "not-a-technique", "x", "high"},
		{"no evidence", StateDetectionTriggered, "T1046", " ", "high"},
		{"no confidence", StateDetectionTriggered, "T1046", "x", ""},
		{"bad confidence", StateDetectionTriggered, "T1046", "x", "certain"},
	}
	for _, c := range bad {
		if _, err := ValidateDetectionResult(c.result, c.technique, c.evidence, c.confidence); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}
