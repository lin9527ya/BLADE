package db

import (
	"strings"
	"testing"
)

func TestDeliverableVersionAcceptanceAndDiff(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	detail, err := d.CreateDeliverable(DeliverableInput{
		Name: "deliverable test", ReportMD: "# v1\nhello\n", Note: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteDeliverable(detail.Deliverable.ID) //nolint:errcheck
	if detail.Deliverable.LatestVersion != 1 || len(detail.Versions) != 1 {
		t.Fatalf("initial detail = %+v", detail)
	}
	v2, err := d.AddDeliverableVersion(detail.Deliverable.ID, "# v2\nhello\nworld\n", "second", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if v2.Version != 2 || v2.SHA256 == "" {
		t.Fatalf("v2 = %+v", v2)
	}
	acceptance, err := d.AcceptDeliverable(detail.Deliverable.ID, v2.ID, "client", "signed")
	if err != nil {
		t.Fatal(err)
	}
	if acceptance.Version != 2 || acceptance.AcceptedBy != "client" {
		t.Fatalf("acceptance = %+v", acceptance)
	}
	updated, err := d.GetDeliverable(detail.Deliverable.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Deliverable.Status != DeliverableStatusAccepted || updated.Deliverable.LatestVersion != 2 || updated.Deliverable.AcceptanceCount != 1 {
		t.Fatalf("updated deliverable = %+v", updated.Deliverable)
	}
	diff, err := d.DeliverableDiff(detail.Deliverable.ID, detail.Versions[0].ID, v2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+world") {
		t.Fatalf("diff missing addition: %s", diff)
	}
}
