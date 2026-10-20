//go:build linux || darwin

package disk

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

func unixMutation(t *testing.T, p *LocalVersion, cmd uint16, body []byte, n uint32, want uint32) {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	done := make(chan error, 1)
	stats := &versionDeviceStats{}
	go func() { done <- p.serveUnixConnection(t.Context(), server, stats) }()
	versionNegotiate(t, client, 1<<20)
	if _, err := client.Write(request(cmd, 0, 0, n, body)); err != nil {
		t.Fatal(err)
	}
	var reply [16]byte
	_, err := io.ReadFull(client, reply[:])
	if err != nil {
		t.Fatalf("missing complete NBD reply: %v", err)
	}
	if binary.BigEndian.Uint32(reply[4:8]) != want {
		t.Fatalf("mutation errno=%d want %d", binary.BigEndian.Uint32(reply[4:8]), want)
	}
	client.Close()
	<-done
	if stats.flushes.Load() != 0 {
		t.Fatal("test accidentally sent FLUSH")
	}
}

func TestUnixMutationsDurableWithoutClientFlush(t *testing.T) {
	for _, trim := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "trim"}[trim], func(t *testing.T) {
			cfg, _ := localVersionFixture(t)
			p, err := CreateLocalVersion(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if trim {
				unixMutation(t, p, 4, nil, 4096, 0)
			} else {
				unixMutation(t, p, 1, bytes.Repeat([]byte{8}, 4096), 4096, 0)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenLocalVersion(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			want := byte(8)
			if trim {
				want = 0
			}
			if got := localByte(t, reopened); got != want {
				t.Fatalf("acknowledged mutation lost on reopen: got %d want %d", got, want)
			}
		})
	}
}

func TestUnixMutationsNeverAcknowledgePersistenceFailure(t *testing.T) {
	for _, phase := range []string{"objects-staged", "root-synced", "root-renamed", "root-committed"} {
		for _, trim := range []bool{false, true} {
			t.Run(phase+map[bool]string{false: "-write", true: "-trim"}[trim], func(t *testing.T) {
				cfg, _ := localVersionFixture(t)
				p, err := CreateLocalVersion(t.Context(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				reached := false
				p.phase = func(at string) error {
					if at == phase {
						reached = true
						return syscall.EIO
					}
					return nil
				}
				if trim {
					unixMutation(t, p, 4, nil, 4096, 5)
				} else {
					unixMutation(t, p, 1, bytes.Repeat([]byte{8}, 4096), 4096, 5)
				}
				if !reached {
					t.Fatal("fault phase not reached")
				}
			})
		}
	}
}

func TestUnixMutationStagingLimit(t *testing.T) {
	cfg, _ := localVersionFixture(t)
	p, err := CreateLocalVersion(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.phase = func(phase string) error {
		if phase == "objects-staged" {
			return ErrVersionStagingFull
		}
		return nil
	}
	unixMutation(t, p, 1, bytes.Repeat([]byte{8}, 4096), 4096, 28)
}

func TestUnixWriteHoldsRequestUntilDurableCommit(t *testing.T) {
	cfg, _ := localVersionFixture(t)
	p, err := CreateLocalVersion(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	p.phase = func(phase string) error {
		if phase == "root-committed" {
			close(entered)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				return errors.New("test gate timeout")
			}
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := (versionDevice{LocalVersion: p, writeThrough: true}).WriteAt(context.Background(), []byte{8}, 7)
		done <- err
	}()
	<-entered
	if p.request.TryLock() {
		p.request.Unlock()
		close(release)
		t.Fatal("request boundary released before persistence acknowledgement")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
