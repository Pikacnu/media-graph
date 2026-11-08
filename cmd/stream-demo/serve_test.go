package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShutdownServerWithActiveStream(t *testing.T) {
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	finished := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	server.Config.BaseContext = func(net.Listener) context.Context { return requests }
	server.Start()
	defer server.Close()

	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	cancelRequests()
	if err := shutdownServer(server.Config); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("stream handler did not exit after cancellation")
	}
}
