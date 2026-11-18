package feature

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/factory24/flow-system/pkg/client"
)

func server(t *testing.T, body string, code int, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnabledReadsTheSwitchAndCachesIt(t *testing.T) {
	var hits int32
	srv := server(t, `{"success":true,"data":{"code":"feature.prepaidHold","enabled":true,"source":"platform"}}`, 200, &hits)
	r := New(client.New(nil), srv.URL+"/v1/settings")
	for i := 0; i < 3; i++ {
		if !r.Enabled(context.Background(), "prepaidHold", "biz1") {
			t.Fatal("switch is on")
		}
	}
	if !r.Enabled(context.Background(), "feature.prepaidHold", "biz1") {
		t.Fatal("the prefixed name reads the same switch")
	}
	if hits != 1 {
		t.Fatalf("one call inside the TTL, got %d", hits)
	}
}

func TestEnabledIsOffOnAnyFailure(t *testing.T) {
	var hits int32
	bad := server(t, `{"message":"boom"}`, 500, &hits)
	if New(client.New(nil), bad.URL+"/x").Enabled(context.Background(), "prepaidHold", "") {
		t.Fatal("a 500 reads as off")
	}
	off := server(t, `{"data":{"enabled":false}}`, 200, &hits)
	if New(client.New(nil), off.URL+"/y").Enabled(context.Background(), "cardTopup", "") {
		t.Fatal("an off switch reads off")
	}
	if New(client.New(nil), "http://127.0.0.1:1").Enabled(context.Background(), "cardTopup", "t") {
		t.Fatal("an unreachable service reads as off")
	}
	var none *Reader
	if none.Enabled(context.Background(), "x", "") || Enabled(context.Background(), "x", "") {
		t.Fatal("no reader reads as off")
	}
}
