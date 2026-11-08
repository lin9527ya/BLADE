package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestActivityStreamReplaysOutOfOrderPublish(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	task := s.m.ResolveTask("")
	if task == nil {
		t.Fatal("missing fixture task")
	}
	before, err := task.Store.ActivityMaxID()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(s.streamActivity))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"?task="+task.ID+"&since="+strconv.FormatInt(before, 10), nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Header Flush occurs after subscription and initial DB replay.
	first, err := task.Store.AppendActivity(db.Activity{Worker: "work#1", Kind: "text", Summary: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := task.Store.AppendActivity(db.Activity{Worker: "work#2", Kind: "text", Summary: "second"})
	if err != nil {
		t.Fatal(err)
	}
	s.engine.Broadcaster().Publish(task.ID, db.Activity{ID: second})
	s.engine.Broadcaster().Publish(task.ID, db.Activity{ID: first})
	scan := bufio.NewScanner(resp.Body)
	var ids []int64
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "id: ") {
			id, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
			if len(ids) == 2 {
				break
			}
		}
	}
	if len(ids) != 2 || ids[0] != first || ids[1] != second {
		t.Fatalf("out-of-order publish lost rows: ids=%v expected=[%d %d], scan=%v", ids, first, second, scan.Err())
	}
	cancel()
	// Reconnection from the first emitted id still replays the second record.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	req2, _ := http.NewRequestWithContext(ctx2, "GET", srv.URL+"?task="+task.ID, nil)
	req2.Header.Set("Last-Event-ID", strconv.FormatInt(first, 10))
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	scan = bufio.NewScanner(resp2.Body)
	for scan.Scan() {
		if strings.HasPrefix(scan.Text(), "id: ") {
			if scan.Text() != "id: "+strconv.FormatInt(second, 10) {
				t.Fatalf("reconnect id: %s", scan.Text())
			}
			return
		}
	}
	t.Fatalf("reconnect did not replay: %v", scan.Err())
}
