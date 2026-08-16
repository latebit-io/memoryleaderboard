package memory

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/latebit-io/demarkus/protocol"
	"github.com/quic-go/quic-go"
)

func TestMarkReadClientHonorsCancelledContext(t *testing.T) {
	client := newMarkReadClient(true)
	t.Cleanup(client.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Request(ctx, "127.0.0.1:1", protocol.Request{Verb: protocol.VerbFetch, Path: "/doc.md"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestMarkReadClientCancelsBlockedResponse(t *testing.T) {
	listener, err := quic.ListenAddr("127.0.0.1:0", testServerTLS(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverCtx, stopServer := context.WithCancel(context.Background())
	t.Cleanup(stopServer)
	received := make(chan struct{})
	go func() {
		conn, err := listener.Accept(serverCtx)
		if err != nil {
			return
		}
		stream, err := conn.AcceptStream(serverCtx)
		if err != nil {
			return
		}
		if _, err := protocol.ParseRequest(stream); err != nil {
			return
		}
		close(received)
		<-serverCtx.Done()
	}()

	client := newMarkReadClient(true)
	t.Cleanup(client.Close)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-received:
			cancel()
		case <-time.After(time.Second):
			cancel()
		}
	}()

	started := time.Now()
	_, err = client.Request(ctx, listener.Addr().String(), protocol.Request{Verb: protocol.VerbFetch, Path: "/doc.md"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("blocked response cancellation took %s", elapsed)
	}
}

func TestMarkReadCancellationKeepsSharedConnection(t *testing.T) {
	listener, err := quic.ListenAddr("127.0.0.1:0", testServerTLS(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverCtx, stopServer := context.WithCancel(context.Background())
	t.Cleanup(stopServer)
	blocked := make(chan struct{})
	go func() {
		conn, err := listener.Accept(serverCtx)
		if err != nil {
			return
		}
		for {
			stream, err := conn.AcceptStream(serverCtx)
			if err != nil {
				return
			}
			go func() {
				req, err := protocol.ParseRequest(stream)
				if err != nil {
					return
				}
				if req.Path == "/blocked.md" {
					close(blocked)
					<-serverCtx.Done()
					return
				}
				_, _ = (protocol.Response{Status: protocol.StatusOK, Body: "ready"}).WriteTo(stream)
				_ = stream.Close()
			}()
		}
	}()

	client := newMarkReadClient(true)
	t.Cleanup(client.Close)
	blockedCtx, cancelBlocked := context.WithCancel(context.Background())
	blockedErr := make(chan error, 1)
	go func() {
		_, err := client.Request(blockedCtx, listener.Addr().String(), protocol.Request{Verb: protocol.VerbFetch, Path: "/blocked.md"})
		blockedErr <- err
	}()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("server did not receive blocked request")
	}

	resp, err := client.Request(context.Background(), listener.Addr().String(), protocol.Request{Verb: protocol.VerbFetch, Path: "/ready.md"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != protocol.StatusOK || resp.Body != "ready" {
		t.Fatalf("response = %+v", resp)
	}
	cancelBlocked()
	if err := <-blockedErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked error = %v, want context.Canceled", err)
	}
}

func testServerTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{protocol.ALPN}}
}
