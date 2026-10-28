package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// A completed turn can still have more steps behind the cursor. The tail must
// report that backlog and keep every returned page bounded by limit.
func TestConversationTailDrainsCompletedBacklog(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.pg
	conv, err := pg.CreateConversation("tail-pagination-test", "tail", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pg.Exec(`DELETE FROM conversation_activities WHERE conversation_id=$1`, conv.ID)
		_, _ = pg.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID)
	})
	for i := 0; i < 5; i++ {
		if _, err := pg.AppendConvActivity(conv.ID, db.Activity{Kind: "tool_use", Summary: fmt.Sprintf("step-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	type tail struct {
		Items []struct {
			Seq int64 `json:"seq"`
		} `json:"items"`
		Cursor  int64 `json:"cursor"`
		Running bool  `json:"running"`
		HasMore bool  `json:"hasMore"`
	}
	read := func(since int64) tail {
		w := request(http.MethodGet, fmt.Sprintf("/api/conversations/%d/messages?since=%d&limit=2", conv.ID, since), "")
		if w.Code != 200 {
			t.Fatalf("tail since=%d: %d %s", since, w.Code, w.Body)
		}
		var out tail
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v %s", err, w.Body)
		}
		return out
	}

	first := read(0)
	if len(first.Items) != 2 || !first.HasMore || first.Running {
		t.Fatalf("first page = %+v, want 2 items, hasMore, not running", first)
	}
	if first.Cursor != first.Items[1].Seq {
		t.Fatalf("cursor=%d must be the last RETURNED id=%d (lookahead must not advance it)", first.Cursor, first.Items[1].Seq)
	}

	second := read(first.Cursor)
	if len(second.Items) != 2 || !second.HasMore {
		t.Fatalf("second page = %+v, want 2 items and more", second)
	}
	if second.Items[0].Seq <= first.Cursor {
		t.Fatalf("second page repeated id %d at cursor %d", second.Items[0].Seq, first.Cursor)
	}

	last := read(second.Cursor)
	if len(last.Items) != 1 || last.HasMore {
		t.Fatalf("last page = %+v, want the remaining item and no more", last)
	}
	if last.Items[0].Seq <= second.Cursor {
		t.Fatalf("last page id %d not after cursor %d", last.Items[0].Seq, second.Cursor)
	}
}
