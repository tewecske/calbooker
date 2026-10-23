// Package server owns HTTP process lifecycle and shutdown ordering.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const defaultShutdownTimeout = 10 * time.Second

var (
	// ErrEmptyAddress identifies a server without a listen address.
	ErrEmptyAddress = errors.New("server: empty listen address")
	// ErrNilHandler identifies a server without an HTTP handler.
	ErrNilHandler = errors.New("server: nil http handler")
	// ErrNilContext identifies a missing lifecycle context.
	ErrNilContext = errors.New("server: nil context")
	// ErrNilListener identifies a missing listener.
	ErrNilListener = errors.New("server: nil listener")
	// ErrInvalidShutdownTimeout identifies a non-positive shutdown timeout.
	ErrInvalidShutdownTimeout = errors.New("server: invalid shutdown timeout")
	// ErrNilDependency identifies a nil dependency closer.
	ErrNilDependency = errors.New("server: nil dependency")
)

// Option configures a Server during construction.
type Option func(*Server) error

// Server owns the HTTP listener and dependencies that must close after it.
type Server struct {
	httpServer      *http.Server
	shutdownTimeout time.Duration
	dependencies    []io.Closer
	closeOnce       sync.Once
	closeErr        error
}

// New validates server inputs and returns an explicitly wired HTTP server.
func New(address string, handler http.Handler, options ...Option) (*Server, error) {
	if address == "" {
		return nil, ErrEmptyAddress
	}
	if handler == nil {
		return nil, ErrNilHandler
	}

	server := &Server{
		httpServer: &http.Server{
			Addr:              address,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			// Bound slow clients. WriteTimeout leaves room for requests that
			// hash passwords or send mail through the relay.
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 45 * time.Second,
			IdleTimeout:  2 * time.Minute,
		},
		shutdownTimeout: defaultShutdownTimeout,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(server); err != nil {
			return nil, err
		}
	}
	return server, nil
}

// WithShutdownTimeout sets the maximum time allowed for HTTP shutdown.
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(server *Server) error {
		if timeout <= 0 {
			return fmt.Errorf("%w: must be positive", ErrInvalidShutdownTimeout)
		}
		server.shutdownTimeout = timeout
		return nil
	}
}

// WithDependency registers a resource that closes after HTTP shutdown.
func WithDependency(dependency io.Closer) Option {
	return func(server *Server) error {
		if dependency == nil {
			return ErrNilDependency
		}
		server.dependencies = append(server.dependencies, dependency)
		return nil
	}
}

// Run listens on the configured TCP address until context cancellation or a
// server error. Context cancellation triggers bounded graceful shutdown.
func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}

	listener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return errors.Join(fmt.Errorf("listen: %w", err), s.closeDependencies())
	}
	return s.Serve(ctx, listener)
}

// Serve runs the server on listener until context cancellation or a server
// error. Caller transfers listener ownership to Server.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if ctx == nil {
		return ErrNilContext
	}
	if listener == nil {
		return errors.Join(ErrNilListener, s.closeDependencies())
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		return errors.Join(normalizeServeError(err), s.closeDependencies())
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		shutdownErr := s.Shutdown(shutdownContext)
		cancel()

		serveErr := <-serveErr
		if err := normalizeServeError(serveErr); err != nil {
			return errors.Join(shutdownErr, err)
		}
		return shutdownErr
	}
}

// Shutdown stops accepting requests, waits for active requests up to ctx's
// deadline, and then closes registered dependencies.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.Join(ErrNilContext, s.closeDependencies())
	}
	return errors.Join(s.httpServer.Shutdown(ctx), s.closeDependencies())
}

func (s *Server) closeDependencies() error {
	s.closeOnce.Do(func() {
		for _, dependency := range s.dependencies {
			if err := dependency.Close(); err != nil {
				s.closeErr = errors.Join(s.closeErr, err)
			}
		}
	})
	return s.closeErr
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve: %w", err)
}
