package server

import (
	"io"
	"testing"
)

func TestLogSinkCountsDroppedPersistence(t *testing.T) {
	sink := &logSinkT{
		cap:  10,
		subs: map[chan LogLine]struct{}{},
		out:  io.Discard,
		dbCh: make(chan dbWriteReq, 1),
	}
	sink.add(LogLine{Level: "info", Text: "first"})
	sink.add(LogLine{Level: "info", Text: "dropped"})
	stats := sink.DBStats()
	if stats["db_queue_dropped"] != 1 {
		t.Fatalf("db_queue_dropped=%d, want 1", stats["db_queue_dropped"])
	}
}
