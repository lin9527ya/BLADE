package server

import "testing"

func TestOperatorWritePolicyBlocksExecutableSurfaces(t *testing.T) {
	for _, path := range []string{
		"/api/cves", "/api/cves/import", "/api/cves/12/script",
		"/api/credentials", "/api/credentials/1/reveal", "/api/credentials/1",
		"/api/visibility/toggle", "/api/visibility/skill/toggle", "/api/triggers/1", "/api/custom-tools", "/api/tools/x", "/api/skills/x", "/api/mcp/x",
		"/api/intercept/rules", "/api/intercept/rules/always-allow",
	} {
		if operatorWriteAllowed(path) {
			t.Errorf("operator token must not write executable/admin surface %q", path)
		}
	}
	for _, path := range []string{
		"/api/tasks", "/api/tasks/1", "/api/findings/1", "/api/playbooks/1",
		"/api/approvals/1/decide", "/api/assets/1",
	} {
		if !operatorWriteAllowed(path) {
			t.Errorf("operator token should retain business-data write %q", path)
		}
	}
}
