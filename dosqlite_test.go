package dosqlite

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type WorkerServer struct {
	cmd       *exec.Cmd
	URL       string
	port      int
	stderrBuf *bytes.Buffer

	ctx    context.Context
	cancel context.CancelCauseFunc
}

func getAvailablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Failed to get available port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

type Opt func(cmd *WorkerServer)

func NewWorkerServer(t *testing.T, opts ...Opt) *WorkerServer {
	t.Helper()
	port := getAvailablePort(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	ws := &WorkerServer{
		URL:       fmt.Sprintf("http://localhost:%d", port),
		port:      port,
		stderrBuf: &bytes.Buffer{},
		ctx:       ctx,
		cancel:    cancel,
	}

	t.Cleanup(func() {
		ws.Close()
	})

	ws.cmd = exec.Command("deno", "task", "start")
	ws.cmd.Dir = filepath.Join(".", "test")
	ws.cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))
	for _, opt := range opts {
		opt(ws)
	}
	// Someone will explain to be one day why I can't just assign the buffer to
	// cmd.Std(out|err)
	stdoutPipe, _ := ws.cmd.StdoutPipe()
	stderrPipe, _ := ws.cmd.StderrPipe()
	go func() { _, _ = io.Copy(ws.stderrBuf, stderrPipe) }()
	go func() { _, _ = io.Copy(ws.stderrBuf, stdoutPipe) }()
	if err := ws.cmd.Start(); err != nil {
		t.Fatalf("failed to start worker: %v", err)
	}

	go func() {
		ws.cancel(ws.cmd.Wait())
	}()

	if err := ws.waitForWorker(); err != nil {
		t.Fatalf("Failed to wait for worker: %v", err)
	}

	return ws
}

func (ws *WorkerServer) waitForWorker() error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		select {
		case <-ws.ctx.Done():
			if err := context.Cause(ws.ctx); err != nil {
				return fmt.Errorf("worker process exited early:\n%s \n%s", err, ws.stderrBuf.String())
			}
			return fmt.Errorf("worker process exited unexpectedly")
		case <-ctx.Done():
			return fmt.Errorf("worker failed to start after 5 seconds:\n%s", ws.stderrBuf.String())
		case <-ticker.C:
			conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", ws.port))
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}

func (ws *WorkerServer) Close() {
	if ws.cmd != nil && ws.cmd.Process != nil {
		_ = ws.cmd.Process.Kill()
		<-ws.ctx.Done()
	}
}

func TestTCPConnection(t *testing.T) {
	goPort := getAvailablePort(t)
	server := NewWorkerServer(t, func(ws *WorkerServer) {
		ws.cmd.Env = append(ws.cmd.Env, fmt.Sprintf("GO_PORT=%d", goPort))
	})

	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", goPort))
	if err != nil {
		t.Fatalf("Failed to listen on port %d: %v", goPort, err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("hello\n"))
			_, _ = io.Copy(os.Stdout, conn)
		}
	}()

	res, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("Failed to get worker: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}
	fmt.Println(string(body))
}
