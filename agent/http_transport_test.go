package agent

import (
	"testing"
	"time"
)

func TestToolSetReusesAndClosesHTTPTransports(t *testing.T) {
	ts := NewToolSet(nil, "worker")
	c1, err := ts.buildHTTPClient(nil, true, true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := ts.buildHTTPClient(nil, true, true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Transport != c2.Transport {
		t.Fatal("same proxy/TLS configuration created a new transport")
	}
	c3, err := ts.buildHTTPClient(nil, true, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if c3.Transport == c1.Transport {
		t.Fatal("different TLS mode must use a different transport")
	}
	ts.CloseHTTP()
	if ts.httpPool == nil || len(ts.httpPool.transports) != 0 {
		t.Fatal("CloseHTTP did not release the pool")
	}
}
