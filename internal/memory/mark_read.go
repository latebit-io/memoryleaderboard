package memory

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus/protocol"
	"github.com/quic-go/quic-go"
)

const (
	markRequestTimeout = 10 * time.Second
	markMaxAttempts    = 5
	markRetryDelay     = 100 * time.Millisecond
)

// markReadClient supplies context-aware reads until the shared client exposes
// caller-context methods. One QUIC connection is pooled per host.
type markReadClient struct {
	tlsConfig  *tls.Config
	quicConfig *quic.Config
	mu         sync.Mutex
	conns      map[string]*quic.Conn
}

func newMarkReadClient(insecure bool) *markReadClient {
	return &markReadClient{
		tlsConfig: &tls.Config{
			InsecureSkipVerify: insecure,
			MinVersion:         tls.VersionTLS13,
			NextProtos:         []string{protocol.ALPN},
		},
		quicConfig: &quic.Config{KeepAlivePeriod: 25 * time.Second},
		conns:      make(map[string]*quic.Conn),
	}
}

func (c *markReadClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for host, conn := range c.conns {
		_ = conn.CloseWithError(0, "")
		delete(c.conns, host)
	}
}

func (c *markReadClient) Request(ctx context.Context, host string, req protocol.Request) (protocol.Response, error) {
	var lastErr error
	for attempt := range markMaxAttempts {
		if err := ctx.Err(); err != nil {
			return protocol.Response{}, err
		}

		attemptCtx, cancel := context.WithTimeout(ctx, markRequestTimeout)
		conn, err := c.getConn(attemptCtx, host)
		if err == nil {
			var response protocol.Response
			response, err = requestOnConn(attemptCtx, conn, req)
			if err == nil {
				cancel()
				return response, nil
			}
		}
		cancel()
		if ctx.Err() != nil {
			return protocol.Response{}, ctx.Err()
		}

		lastErr = err
		if attempt == markMaxAttempts-1 || !isTransientReadError(err) {
			return protocol.Response{}, err
		}
		if conn != nil && conn.Context().Err() != nil {
			c.removeConn(host, conn)
		}
		if err := waitForRetry(ctx); err != nil {
			return protocol.Response{}, err
		}
	}
	return protocol.Response{}, lastErr
}

func requestOnConn(ctx context.Context, conn *quic.Conn, req protocol.Request) (protocol.Response, error) {
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("open stream: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			stream.CancelRead(0)
			stream.CancelWrite(0)
		}
		_ = stream.Close()
	}()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stream.CancelRead(0)
			stream.CancelWrite(0)
		case <-done:
		}
	}()
	defer close(done)

	if _, err := req.WriteTo(stream); err != nil {
		return protocol.Response{}, contextError(ctx, fmt.Errorf("send request: %w", err))
	}
	if err := stream.Close(); err != nil {
		return protocol.Response{}, contextError(ctx, fmt.Errorf("close request: %w", err))
	}
	resp, err := protocol.ParseResponse(stream)
	if err != nil {
		return protocol.Response{}, contextError(ctx, fmt.Errorf("read response: %w", err))
	}
	complete = true
	return resp, nil
}

func (c *markReadClient) getConn(ctx context.Context, host string) (*quic.Conn, error) {
	c.mu.Lock()
	conn := c.conns[host]
	c.mu.Unlock()
	if conn != nil && conn.Context().Err() == nil {
		return conn, nil
	}

	tlsConfig := c.tlsConfig.Clone()
	if serverName, _, err := net.SplitHostPort(host); err == nil {
		tlsConfig.ServerName = serverName
	} else {
		tlsConfig.ServerName = host
	}
	conn, err := quic.DialAddr(ctx, host, tlsConfig, c.quicConfig)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", host, err)
	}

	c.mu.Lock()
	if existing := c.conns[host]; existing != nil && existing.Context().Err() == nil {
		c.mu.Unlock()
		_ = conn.CloseWithError(0, "")
		return existing, nil
	}
	c.conns[host] = conn
	c.mu.Unlock()
	return conn, nil
}

func (c *markReadClient) removeConn(host string, failed *quic.Conn) {
	if failed == nil {
		return
	}
	c.mu.Lock()
	if c.conns[host] == failed {
		delete(c.conns, host)
	}
	c.mu.Unlock()
	_ = failed.CloseWithError(0, "retry")
}

func waitForRetry(ctx context.Context) error {
	timer := time.NewTimer(markRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func contextError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}

func isTransientReadError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	message := err.Error()
	return errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(message, "no recent network activity") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "connection reset") ||
		strings.HasSuffix(message, "EOF")
}
