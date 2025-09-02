package dosqlite

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"golang.org/x/sync/errgroup"
)

// mockClient simulates a client that responds to length-prefixed messages
func mockClient(t *testing.T, conn net.Conn) {
	defer conn.Close()

	for {
		// Read length-prefixed message
		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header); err != nil {
			if err == io.EOF || errors.Is(err, net.ErrClosed) {
				return
			}
			t.Logf("Failed to read header: %v", err)
			return
		}

		length := binary.BigEndian.Uint32(header)
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			t.Logf("Failed to read payload: %v", err)
			return
		}

		// Send length-prefixed response
		responseBytes := []byte(payload)
		responseHeader := make([]byte, 4)
		binary.BigEndian.PutUint32(responseHeader, uint32(len(responseBytes)))

		if _, err := conn.Write(responseHeader); err != nil {
			t.Logf("Failed to write response header: %v", err)
			return
		}

		if _, err := conn.Write(responseBytes); err != nil {
			t.Logf("Failed to write response: %v", err)
			return
		}
	}
}

func TestListener_Driver(t *testing.T) {
	port := getAvailablePort(t)
	errg, _ := errgroup.WithContext(context.Background())
	errg.Go(func() error {
		driver := &Driver{}
		conn, err := driver.Open(fmt.Sprintf("dosqlite://127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("Failed to open connection: %v", err)
		}
		defer conn.Close()
		return nil
	})
	errg.Go(func() error {
		// Connect first client
		conn1, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("Failed to connect first client: %v", err)
		}

		// Start mock client that responds with "response1"
		go mockClient(t, conn1)
		return nil
	})
	if err := errg.Wait(); err != nil {
		t.Fatalf("Failed to wait for listener to be ready: %v", err)
	}
}

func TestListener_Lifecycle(t *testing.T) {
	// Shake out the flakes
	for i := 0; i < 10; i++ {
		// Start listener on a random port
		listener := AddListener("127.0.0.1:0")
		defer listener.Close()

		_, err := listener.Send("test")
		if err == nil {
			t.Error("Expected error when no client connected, got nil")
		}
		errg, _ := errgroup.WithContext(context.Background())
		errg.Go(func() error {
			return listener.Ready()
		})
		errg.Go(func() error {
			// Connect first client
			conn1, err := net.Dial("tcp", listener.addr)
			if err != nil {
				t.Fatalf("Failed to connect first client: %v", err)
			}

			// Start mock client that responds with "response1"
			go mockClient(t, conn1)
			return nil
		})
		if err := errg.Wait(); err != nil {
			t.Fatalf("Failed to wait for listener to be ready: %v", err)
		}

		// Test sending message to first client
		resp, err := listener.Send("hello1")
		if err != nil {
			t.Fatalf("Failed to send to first client: %v", err)
		}
		if resp != "hello1" {
			t.Errorf("Expected 'response1', got '%s'", resp)
		}
		var conn2 net.Conn
		errg, _ = errgroup.WithContext(context.Background())
		errg.Go(func() error {
			return listener.Next()
		})
		errg.Go(func() error {
			// Connect second client - this should replace the first
			conn2, err = net.Dial("tcp", listener.addr)
			if err != nil {
				panic(fmt.Errorf("Failed to connect second client: %v", err))
			}

			// Start mock client that responds with "response2"
			go mockClient(t, conn2)
			return nil
		})
		if err := errg.Wait(); err != nil {
			t.Fatalf("Failed to wait for listener to be ready: %v", err)
		}

		// Test sending message to second client
		resp, err = listener.Send("hello2")
		if err != nil {
			t.Fatalf("Failed to send to second client: %v", err)
		}
		if resp != "hello2" {
			t.Errorf("Expected 'hello2', got '%s'", resp)
		}

		// Close the second connection
		conn2.Close()
		{
			_, err := listener.Send("hello3")
			if err == nil {
				t.Error("Expected error when connection lost, got nil")
			}

		}
	}

}

func TestListener_MultipleListeners(t *testing.T) {
	// Start multiple listeners on different ports
	listener1 := AddListener("127.0.0.1:0")
	defer listener1.Close()

	listener2 := AddListener("127.0.0.1:0")
	defer listener2.Close()

	errg, _ := errgroup.WithContext(context.Background())
	errg.Go(func() error {
		return listener1.Ready()
	})
	errg.Go(func() error {
		// Connect to first listener
		conn1, err := net.Dial("tcp", listener1.addr)
		if err != nil {
			t.Fatalf("Failed to connect to first listener: %v", err)
		}
		go mockClient(t, conn1)
		return nil
	})

	errg.Go(func() error {
		return listener2.Ready()
	})
	errg.Go(func() error {
		// Connect to second listener
		conn2, err := net.Dial("tcp", listener2.addr)
		if err != nil {
			t.Fatalf("Failed to connect to second listener: %v", err)
		}
		go mockClient(t, conn2)
		return nil
	})
	if err := errg.Wait(); err != nil {
		t.Fatalf("Failed to wait for listeners to be ready: %v", err)
	}

	// Test that each listener works independently
	resp1, err := listener1.Send("test1")
	if err != nil {
		t.Fatalf("Failed to send to listener1: %v", err)
	}
	if resp1 != "test1" {
		t.Errorf("Expected 'test1', got '%s'", resp1)
	}

	resp2, err := listener2.Send("test2")
	if err != nil {
		t.Fatalf("Failed to send to listener2: %v", err)
	}
	if resp2 != "test2" {
		t.Errorf("Expected 'test2', got '%s'", resp2)
	}
}

func TestListener_ConcurrentSends(t *testing.T) {
	listener := AddListener("127.0.0.1:0")
	defer listener.Close()

	errg, _ := errgroup.WithContext(context.Background())
	errg.Go(func() error {
		return listener.Ready()
	})
	errg.Go(func() error {
		// Connect client
		conn, err := net.Dial("tcp", listener.addr)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		go mockClient(t, conn)
		return nil
	})
	if err := errg.Wait(); err != nil {
		t.Fatalf("Failed to wait for listener to be ready: %v", err)
	}

	// Send multiple messages concurrently
	eg, _ := errgroup.WithContext(context.Background())

	for i := 0; i < 10; i++ {
		msg := fmt.Sprintf("concurrent_response_%d", i)
		eg.Go(func() error {

			resp, err := listener.Send(msg)
			if err != nil {
				return err
			}
			if resp != msg {
				return fmt.Errorf("expected %s, got %s", msg, resp)
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		t.Fatalf("Error sending concurrent messages: %v", err)
	}
}

func TestListener_Close(t *testing.T) {
	listener := AddListener("127.0.0.1:0")

	errg, _ := errgroup.WithContext(context.Background())
	errg.Go(func() error {
		return listener.Ready()
	})
	errg.Go(func() error {
		// Connect client
		conn, err := net.Dial("tcp", listener.addr)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		go mockClient(t, conn)
		return nil
	})
	if err := errg.Wait(); err != nil {
		t.Fatalf("Failed to wait for listener to be ready: %v", err)
	}

	// Verify listener works
	resp, err := listener.Send("test")
	if err != nil {
		t.Fatalf("Failed to send before close: %v", err)
	}
	if resp != "test" {
		t.Errorf("Expected 'test', got '%s'", resp)
	}

	// Close the listener
	err = listener.Close()
	if err != nil {
		t.Fatalf("Failed to close listener: %v", err)
	}

	// Verify listener is removed from map
	listenerConnsMu.RLock()
	_, exists := listenerConns[listener.addr]
	listenerConnsMu.RUnlock()

	if exists {
		t.Error("Listener still exists in map after close")
	}
	{
		_, err := listener.Send("test")
		if err == nil {
			t.Error("Expected error after close, got nil")
		}
	}
}
