package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNewRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name    string
		address string
		handler http.Handler
		option  Option
		wantErr error
	}{
		{name: "empty address", handler: http.NotFoundHandler(), wantErr: ErrEmptyAddress},
		{name: "nil handler", address: ":8080", wantErr: ErrNilHandler},
		{name: "invalid shutdown timeout", address: ":8080", handler: http.NotFoundHandler(), option: WithShutdownTimeout(0), wantErr: ErrInvalidShutdownTimeout},
		{name: "nil dependency", address: ":8080", handler: http.NotFoundHandler(), option: WithDependency(nil), wantErr: ErrNilDependency},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := []Option{}
			if test.option != nil {
				options = append(options, test.option)
			}
			_, err := New(test.address, test.handler, options...)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("New() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
		})
	}
}

func TestServeShutsDownAndClosesDependencies(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	dependency := &testCloser{}
	appServer, err := New(listener.Addr().String(), http.NotFoundHandler(), WithShutdownTimeout(time.Second), WithDependency(dependency))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- appServer.Serve(ctx, listener)
	}()
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not finish after cancellation")
	}

	if dependency.closed != 1 {
		t.Errorf("dependency close count = %d, want 1", dependency.closed)
	}
}

func TestRunReturnsListenErrorAndClosesDependencies(t *testing.T) {
	dependency := &testCloser{}
	appServer, err := New("invalid-address", http.NotFoundHandler(), WithDependency(dependency))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = appServer.Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want listen error")
	}
	if dependency.closed != 1 {
		t.Errorf("dependency close count = %d, want 1", dependency.closed)
	}
}

func TestRunRejectsNilContext(t *testing.T) {
	appServer, err := New(":8080", http.NotFoundHandler())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := appServer.Run(nil); !errors.Is(err, ErrNilContext) {
		t.Fatalf("Run(nil) error = %v, want ErrNilContext", err)
	}
}

type testCloser struct {
	closed int
}

func (c *testCloser) Close() error {
	c.closed++
	return nil
}
