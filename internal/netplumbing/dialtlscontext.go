package netplumbing

// This file contains the implementation of Transport.DialTLSContext.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"
)

// DialTLSContext dials a TLS connection.
func (txp *Transport) DialTLSContext(
	ctx context.Context, network string, address string) (net.Conn, error) {
	conn, _, err := txp.dialTLSContext(ctx, network, address)
	return conn, err
}

// dialTLSContext is the internal entry point for dialing TLS
func (txp *Transport) dialTLSContext(
	ctx context.Context, network string, address string) (
	net.Conn, *tls.ConnectionState, error) {
	return txp.dialTLSContextWrapError(ctx, network, address)
}

// dialTLSContextWrapError wraps errors with ErrDialTLS.
func (txp *Transport) dialTLSContextWrapError(
	ctx context.Context, network string, address string) (
	net.Conn, *tls.ConnectionState, error) {
	conn, state, err := txp.dialTLSContextEmitLogs(ctx, network, address)
	if err != nil {
		return nil, nil, &ErrDialTLS{err}
	}
	return conn, state, nil
}

// ErrDialTLS is an error when dialing a TLS connection.
type ErrDialTLS struct {
	error
}

// Unwrap returns the wrapped error.
func (err *ErrDialTLS) Unwrap() error {
	return err.error
}

// dialTLSContextEmitLogs emits dialTLS-related logs.
func (txp *Transport) dialTLSContextEmitLogs(
	ctx context.Context, network string, address string) (
	net.Conn, *tls.ConnectionState, error) {
	log := txp.logger(ctx)
	log.Debugf("dialTLS: %s/%s...", address, network)
	conn, state, err := txp.dialTLSContextDialAndHandshake(ctx, network, address)
	if err != nil {
		log.Debugf("dialTLS: %s/%s... %s", address, network, err)
		return nil, nil, err
	}
	log.Debugf("dialTLS: %s/%s... ok", address, network)
	return conn, state, nil
}

// dialTLSContextDialAndHandshake dials and handshakes.
func (txp *Transport) dialTLSContextDialAndHandshake(
	ctx context.Context, network string, address string) (
	net.Conn, *tls.ConnectionState, error) {
	sni, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, nil, err
	}
	tcpConn, err := txp.DialContext(ctx, network, address)
	if err != nil {
		return nil, nil, err
	}
	tlsConfig := txp.tlsClientConfig(ctx)
	// TODO(bassosimone): implement this part
	//if tlsConfig.RootCAs == nil {
	//}
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = sni
	}
	if tlsConfig.NextProtos == nil && port == "443" {
		tlsConfig.NextProtos = []string{"h2", "http/1.1"}
	}
	if tlsConfig.NextProtos == nil && port == "853" {
		tlsConfig.NextProtos = []string{"dot"}
	}
	// Set the deadline so the handshake fails naturally for I/O timeout
	// rather than for a context timeout. The context may still fail, when
	// the user wants that. So, we can distinguish the case where there
	// is a timeout from the impatient-user case.
	tcpConn.SetDeadline(time.Now().Add(txp.tlsHandshakeTimeout()))
	tlsConn, state, err := txp.tlsHandshake(ctx, tcpConn, tlsConfig)
	if err != nil {
		tcpConn.Close() // we own the connection
		return nil, nil, err
	}
	tcpConn.SetDeadline(time.Time{})
	return tlsConn, state, nil
}

// tlsHandshake its the top-level interface for performing a TLS handshake.
func (txp *Transport) tlsHandshake(
	ctx context.Context, tcpConn net.Conn, config *tls.Config) (
	net.Conn, *tls.ConnectionState, error) {
	return txp.tlsHandshakeWrapError(ctx, tcpConn, config)
}

// tlsHandshakeWrapError wraps errors using ErrTLSHandshake,
func (txp *Transport) tlsHandshakeWrapError(
	ctx context.Context, tcpConn net.Conn, config *tls.Config) (
	net.Conn, *tls.ConnectionState, error) {
	tlsConn, state, err := txp.tlsHandshakeEmitLogs(ctx, tcpConn, config)
	if err != nil {
		return nil, nil, &ErrTLSHandshake{err}
	}
	return tlsConn, state, nil
}

// ErrTLSHandshake is an error during the TLS handshake.
type ErrTLSHandshake struct {
	error
}

// Unwrap returns the underlying error.
func (err *ErrTLSHandshake) Unwrap() error {
	return err.error
}

// tlsHandshakeEmitLogs emits logs related to the TLS handshake.
func (txp *Transport) tlsHandshakeEmitLogs(
	ctx context.Context, tcpConn net.Conn, config *tls.Config) (
	net.Conn, *tls.ConnectionState, error) {
	log := txp.logger(ctx)
	prefix := fmt.Sprintf("tlsHandshake: %s/%s sni=%s alpn=%s...", tcpConn.RemoteAddr().String(),
		tcpConn.RemoteAddr().Network(), config.ServerName, config.NextProtos)
	log.Debug(prefix)
	tlsConn, state, err := txp.tlsHandshakeMaybeTrace(ctx, tcpConn, config)
	if err != nil {
		log.Debugf("%s %s", prefix, err)
		return nil, nil, err
	}
	log.Debugf("%s %s", prefix, state.NegotiatedProtocol)
	return tlsConn, state, nil
}

// tlsHandshakeMaybeTrace enabling tracing if needed.
func (txp *Transport) tlsHandshakeMaybeTrace(
	ctx context.Context, tcpConn net.Conn, tlsConfig *tls.Config) (
	net.Conn, *tls.ConnectionState, error) {
	if th := ContextTraceHeader(ctx); th != nil {
		return txp.tlsHandshakeWithTraceHeader(ctx, tcpConn, tlsConfig, th)
	}
	return txp.tlsHandshakeMaybeOverride(ctx, tcpConn, tlsConfig)
}

// tlsHandshakeWithTraceHeader performs tls handshake tracing.
func (txp *Transport) tlsHandshakeWithTraceHeader(
	ctx context.Context, tcpConn net.Conn, tlsConfig *tls.Config,
	th *TraceHeader) (net.Conn, *tls.ConnectionState, error) {
	ev := &TLSHandshakeTrace{
		kind:          TraceKindTLSHandshake,
		LocalAddr:     tcpConn.LocalAddr().String(),
		RemoteAddr:    tcpConn.RemoteAddr().String(),
		SkipTLSVerify: tlsConfig.InsecureSkipVerify,
		NextProtos:    tlsConfig.NextProtos,
		StartTime:     time.Now(),
		Error:         nil,
	}
	if net.ParseIP(tlsConfig.ServerName) == nil {
		ev.ServerName = tlsConfig.ServerName
	}
	defer th.add(ev)
	tlsConn, state, err := txp.tlsHandshakeMaybeOverride(ctx, tcpConn, tlsConfig)
	ev.EndTime = time.Now()
	ev.Error = err
	if err != nil {
		return nil, nil, err
	}
	ev.Version = state.Version
	ev.CipherSuite = state.CipherSuite
	ev.NegotiatedProto = state.NegotiatedProtocol
	for _, c := range state.PeerCertificates {
		ev.PeerCerts = append(ev.PeerCerts, c.Raw)
	}
	return tlsConn, state, nil
}

// TLSHandshakeTrace is a measurement performed during a TLS handshake.
type TLSHandshakeTrace struct {
	// kind is the structure kind.
	kind string

	// LocalAddr is the local address.
	LocalAddr string

	// RemoteAddr is the remote address.
	RemoteAddr string

	// SkipTLSVerify indicates whether we disabled TLS verification.
	SkipTLSVerify bool

	// ServerName contains the configured server name.
	ServerName string

	// NextProtos contains the protocols for ALPN.
	NextProtos []string

	// StartTime is when we started the TLS handshake.
	StartTime time.Time

	// EndTime is when we're done.
	EndTime time.Time

	// Version contains the TLS version.
	Version uint16

	// CipherSuite contains the negotiated cipher suite.
	CipherSuite uint16

	// NegotiatedProto contains the negotiated proto.
	NegotiatedProto string

	// PeerCerts contains the peer certificates.
	PeerCerts [][]byte

	// Error contains the error.
	Error error
}

// Kind implements TraceEvent.Kind.
func (te *TLSHandshakeTrace) Kind() string {
	return te.kind
}

// tlsHandshakeMaybeOverride calls the overriden or the default TLSHandshaker
func (txp *Transport) tlsHandshakeMaybeOverride(
	ctx context.Context, tcpConn net.Conn, tlsConfig *tls.Config) (
	net.Conn, *tls.ConnectionState, error) {
	thp := txp.DefaultTLSHandshaker()
	if config := ContextConfig(ctx); config != nil && config.TLSHandshaker != nil {
		thp = config.TLSHandshaker
	}
	return thp.TLSHandshake(ctx, tcpConn, tlsConfig)
}

// DefaultTLSHandshaker is the TLSHandshaker used by default by this transport.
func (txp *Transport) DefaultTLSHandshaker() TLSHandshaker {
	return &StdlibTLSHandshaker{}
}

// StdlibTLSHandshaker uses the stdlib to perform the TLS handshake.
type StdlibTLSHandshaker struct{}

// TLSHandshake implements TLSHandshaker.TLSHandshake.
func (th *StdlibTLSHandshaker) TLSHandshake(
	ctx context.Context, tcpConn net.Conn,
	config *tls.Config) (net.Conn, *tls.ConnectionState, error) {
	tlsConn := tls.Client(tcpConn, config)
	errch := make(chan error, 1)
	go func() { errch <- tlsConn.Handshake() }()
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case err := <-errch:
		if err != nil {
			return nil, nil, err
		}
		state := tlsConn.ConnectionState()
		return tlsConn, &state, nil
	}
}
