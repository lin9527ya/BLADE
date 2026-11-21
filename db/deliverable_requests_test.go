package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAggregateReceiptCommitsOnceAndRejectsChangedParameters(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	owner := fmt.Sprintf("aggregate-fixture-%d", time.Now().UnixNano())
	key := "same-operation"
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM aggregate_deliverable_requests WHERE owner=$1`, owner) })
	ids := make(chan int64, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			v, _, err := d.CreateAggregateDeliverable(t.Context(), DeliverableInput{Name: owner, ReportMD: "fixture"}, owner, key, "sha")
			if err != nil {
				errs <- err
				return
			}
			ids <- v.Deliverable.ID
		})
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var id int64
	for got := range ids {
		if id == 0 {
			id = got
		}
		if id != got {
			t.Fatal("one operation created multiple deliverables")
		}
	}
	if id == 0 {
		t.Fatal("no result")
	}
	t.Cleanup(func() { _, _ = d.DeleteDeliverable(id) })
	if _, _, err := d.CreateAggregateDeliverable(t.Context(), DeliverableInput{Name: "different"}, owner, key, "changed"); !errors.Is(err, ErrDeliverableRequestConflict) {
		t.Fatalf("changed parameters accepted: %v", err)
	}
	if _, err := d.DeleteDeliverable(id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.AggregateRequest(t.Context(), owner, key, "sha"); !errors.Is(err, ErrDeliverableRequestGone) {
		t.Fatalf("deleted result regenerated: %v", err)
	}
}

func TestBoundReportReadsAndDeliverableCreationHonorCancellation(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.WithContext(ctx).ListDeliverables(); !errors.Is(err, context.Canceled) {
		t.Fatalf("default query ignored cancellation: %v", err)
	}
	if _, err := d.CreateDeliverableContext(ctx, DeliverableInput{Name: "must not commit"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("creation ignored cancellation: %v", err)
	}
}
