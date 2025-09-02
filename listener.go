package dosqlite

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
)

var listenerConns = make(map[string]*listenerConn)
var listenerConnsMu sync.RWMutex

type listenerConn struct {
	conn      net.Conn
	writeChan chan string
	readChan  chan sendResp
	ctx       context.Context
	cancel    context.CancelCauseFunc
}

type sendResp struct {
	resp string
	err  error
}

type listener struct {
	addr string
	ln   net.Listener
}

// Ready waits for the listener to be ready or returns if there is already an
// active listener.
func (l *listener) Ready() error {
	listenerConnsMu.RLock()
	lis := listenerConns[l.addr]
	if lis == nil {
		listenerConnsMu.RUnlock()
		return fmt.Errorf("listener not found")
	}
	listenerConnsMu.RUnlock()
	if lis.conn != nil {
		return nil
	}
	<-lis.ctx.Done()
	if err := context.Cause(lis.ctx); err != context.Canceled {
		return err
	}
	return nil
}

// Next waits for the listener to be ready or for the next connection to be
// live.
func (l *listener) Next() error {
	listenerConnsMu.RLock()
	lis := listenerConns[l.addr]
	if lis == nil {
		listenerConnsMu.RUnlock()
		return fmt.Errorf("listener not found")
	}
	listenerConnsMu.RUnlock()
	<-lis.ctx.Done()
	if err := context.Cause(lis.ctx); err != context.Canceled {
		return err
	}
	return nil
}

// Send sends a message to the listener, consider removing before publishing.
// This should really only be used by the db driver.
func (l *listener) Send(msg string) (resp string, err error) {
	listenerConnsMu.RLock()
	lis := listenerConns[l.addr]
	listenerConnsMu.RUnlock()

	if lis == nil {
		return "", fmt.Errorf("listener not found")
	}

	return lis.Send(msg)
}

func (l *listener) Close() error {
	listenerConnsMu.Lock()
	defer listenerConnsMu.Unlock()
	_ = l.ln.Close()

	lis := listenerConns[l.addr]
	if lis == nil {
		return fmt.Errorf("listener not found")
	}

	lis.cancel(nil)
	if lis.conn != nil {
		_ = lis.conn.Close()
	}
	delete(listenerConns, l.addr)
	return nil
}

func AddListener(addr string) listener {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		panic(err)
	}

	actualAddr := ln.Addr().String()

	listenerConnsMu.Lock()
	ctx, cancel := context.WithCancelCause(context.Background())
	listenerConns[actualAddr] = &listenerConn{
		ctx:       ctx,
		cancel:    cancel,
		writeChan: make(chan string, 100),
		readChan:  make(chan sendResp, 100),
	}
	listenerConnsMu.Unlock()

	go func() {
		defer ln.Close()
		for {
			conn, err := ln.Accept()
			if err != nil {
				listenerConnsMu.Lock()
				lis := listenerConns[actualAddr]
				if lis != nil {
					lis.cancel(err)
				}
				listenerConnsMu.Unlock()
				return
			}
			listenerConnsMu.Lock()
			old := listenerConns[actualAddr]
			if old != nil {
				old.cancel(nil) // cancel the previous listener
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			l := &listenerConn{
				conn:      conn,
				writeChan: make(chan string, 100),
				readChan:  make(chan sendResp, 100),
				ctx:       ctx,
				cancel:    cancel,
			}
			listenerConns[actualAddr] = l
			go l.runLoop(ctx)
			listenerConnsMu.Unlock()
		}
	}()
	return listener{addr: actualAddr, ln: ln}
}

func (l *listenerConn) runLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-l.writeChan:
			if err := l.send(msg); err != nil {
				l.readChan <- sendResp{resp: "", err: err}
				if err == io.EOF {
					return
				}
				continue
			}
		}
	}
}

func (l *listenerConn) send(msg string) error {
	// Write length-prefixed message
	payload := []byte(msg)
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(payload)))

	if _, err := l.conn.Write(header); err != nil {
		return err
	}

	if _, err := l.conn.Write(payload); err != nil {
		return err
	}

	// Read length-prefixed response
	responseHeader := make([]byte, 4)
	if _, err := io.ReadFull(l.conn, responseHeader); err != nil {
		return err
	}

	length := binary.BigEndian.Uint32(responseHeader)
	responsePayload := make([]byte, length)
	if _, err := io.ReadFull(l.conn, responsePayload); err != nil {
		return err
	}

	l.readChan <- sendResp{resp: string(responsePayload), err: nil}
	return nil
}

func (l *listenerConn) Send(msg string) (resp string, err error) {
	if l.conn == nil {
		return "", fmt.Errorf("no active connection")
	}

	select {
	case l.writeChan <- msg:
	case <-l.ctx.Done():
		return "", context.Cause(l.ctx)
	}

	select {
	case r := <-l.readChan:
		return r.resp, r.err
	case <-l.ctx.Done():
		return "", context.Cause(l.ctx)
	}
}
