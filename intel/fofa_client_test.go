package intel

import "testing"

// 一次查询内有多个请求：client 必须复用，不能每个请求都 clone 一份 Transport。
func TestFOFAClientReused(t *testing.T) {
	f := &fofa{cfg: Config{Key: "k"}}
	a, err := f.client()
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.client()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("client must be built once per provider instance")
	}
}
