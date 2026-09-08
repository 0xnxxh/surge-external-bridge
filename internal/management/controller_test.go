package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestConnectionWebSocketDeliversLargeSnapshots(t *testing.T) {
	application, server, endpoint := testManagementServer(t, "management-token-1234567890")
	defer application.Close()

	connections := make([]map[string]any, 100)
	for i := range connections {
		connections[i] = map[string]any{
			"id": fmt.Sprintf("connection-%d", i),
			"metadata": map[string]any{
				"host":     strings.Repeat("a", 600) + ".example",
				"password": "upstream-secret",
			},
		}
	}
	payload, err := json.Marshal(map[string]any{"connections": connections, "uploadTotal": 42})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) <= 32768 {
		t.Fatal("fixture must exceed the WebSocket library's default message limit")
	}

	// Exercise the same private Unix-socket transport used by the embedded core.
	socket := filepath.Join(application.DataDir(), "upstream.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" || r.Header.Get("Authorization") != "Bearer private-controller-secret" {
			http.Error(w, "unexpected upstream request", http.StatusForbidden)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for range 2 {
			if err := conn.Write(r.Context(), websocket.MessageText, payload); err != nil {
				return
			}
		}
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	}))
	_ = upstream.Listener.Close()
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()
	server.core = newControllerFacade(socket, "private-controller-secret")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := http.Header{"Authorization": {"Bearer management-token-1234567890"}, "Origin": {endpoint}}
	conn, _, err := websocket.Dial(ctx, strings.Replace(endpoint, "http://", "ws://", 1)+"/api/mihomo/connections", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// Browsers do not impose the Go client's 32 KiB default.
	conn.SetReadLimit(1 << 20)
	for i := range 2 {
		_, received, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("large connection snapshot %d was interrupted: %v", i, err)
		}
		var snapshot struct {
			Connections []struct {
				ID       string            `json:"id"`
				Metadata map[string]string `json:"metadata"`
			} `json:"connections"`
			UploadTotal int `json:"uploadTotal"`
		}
		if err := json.Unmarshal(received, &snapshot); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Connections) != 100 || snapshot.Connections[99].ID != "connection-99" || snapshot.UploadTotal != 42 {
			t.Fatalf("large snapshot was truncated or changed: connections=%d uploadTotal=%d", len(snapshot.Connections), snapshot.UploadTotal)
		}
		if snapshot.Connections[0].Metadata["password"] != "<redacted>" || strings.Contains(string(received), "upstream-secret") {
			t.Fatal("large snapshot bypassed connection metadata redaction")
		}
	}
}
