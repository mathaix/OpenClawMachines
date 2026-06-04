package cdpproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
)

// TargetResolver maps a VM's source IP to its browser CDP endpoint (host:port).
// Returns empty string if no browser target is known for the given VM.
type TargetResolver interface {
	GetBrowserTarget(vmIP string) string
}

// Proxy is a TCP forwarder that routes connections from MicroVMs to their
// browser's CDP endpoint. It performs no protocol parsing — just bidirectional
// byte copying.
type Proxy struct {
	resolver TargetResolver
	bindAddr string
	port     int
	listener net.Listener
}

// New creates a new CDP proxy bound to the given address and port.
func New(resolver TargetResolver, bindAddr string, port int) *Proxy {
	return &Proxy{
		resolver: resolver,
		bindAddr: bindAddr,
		port:     port,
	}
}

// Start listens on bindAddr:port, accepts TCP connections and forwards them
// to the browser target resolved from the source IP. It blocks until the
// context is cancelled.
func (p *Proxy) Start(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", p.bindAddr, p.port)

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("cdpproxy: listen %s: %w", addr, err)
	}
	p.listener = ln
	slog.Info("cdpproxy: listening", "addr", addr)

	// Close listener when context is cancelled.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Expected when listener is closed on context cancellation.
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("cdpproxy: accept error", "err", err)
			continue
		}
		go p.handleConn(conn)
	}
}

// handleConn forwards a single TCP connection to the browser target for the
// source VM, then copies bytes bidirectionally until either side closes.
func (p *Proxy) handleConn(src net.Conn) {
	defer func() { _ = src.Close() }()

	srcIP := extractIP(src.RemoteAddr().String())
	target := p.resolver.GetBrowserTarget(srcIP)
	if target == "" {
		slog.Warn("cdpproxy: no browser target", "srcIP", srcIP)
		return
	}

	dst, err := net.Dial("tcp", target)
	if err != nil {
		slog.Error("cdpproxy: dial target failed", "target", target, "srcIP", srcIP, "err", err)
		return
	}
	defer func() { _ = dst.Close() }()

	slog.Debug("cdpproxy: forwarding", "srcIP", srcIP, "target", target)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(src, dst)
		if tc, ok := src.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}()

	wg.Wait()
}

// extractIP splits the host from a host:port address string.
func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
