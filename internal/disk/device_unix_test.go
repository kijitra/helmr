//go:build linux || darwin

package disk

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/helmrdotdev/helmr/internal/disk/blockformat"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type faultUnixSource struct {
	blockformat.RangeSource
	fail       bool
	cancelRace bool
	entered    chan struct{}
}

func (s *faultUnixSource) GetRange(ctx context.Context, digest string, size, offset, length int64) (io.ReadCloser, error) {
	if !s.fail {
		return s.RangeSource.GetRange(ctx, digest, size, offset, length)
	}
	close(s.entered)
	if s.cancelRace {
		<-ctx.Done()
	}
	return nil, errors.Join(blockformat.ErrIntegrity, io.EOF, ctx.Err())
}
func TestUnixDeviceFatalSourceFailureSurvivesCancellation(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrapped-eof", true: "cancel-race"}[race], func(t *testing.T) {
			cfg, _ := localVersionFixture(t)
			source := &faultUnixSource{RangeSource: cfg.BaseSource, cancelRace: race, entered: make(chan struct{})}
			cfg.BaseSource = source
			local, err := CreateLocalVersion(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			dir, err := os.MkdirTemp("", "vzd-")
			if err != nil {
				t.Fatal(err)
			}
			dir, err = filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			d, err := AttachUnixDevice(t.Context(), local, UnixDeviceConfig{Arena: dir, Owner: "fault", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			defer func() {
				select {
				case <-exited:
				default:
					close(exited)
				}
				d.Close(context.Background())
			}()
			source.fail = true
			if err = d.BindConsumer(exited); err != nil {
				t.Fatal(err)
			}
			conn, err := net.Dial("unix", filepath.Join(dir, "nbd.sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			versionNegotiate(t, conn, 1<<20)
			if _, err = conn.Write(request(0, 0, 7, 1, nil)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-source.entered:
			case <-time.After(time.Second):
				t.Fatal("source read did not start")
			}
			if race {
				close(exited)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err = d.Wait(ctx)
			var fatal *DeviceFailure
			if !errors.As(err, &fatal) || !errors.Is(err, io.EOF) {
				t.Fatalf("fatal source fault lost: %v", err)
			}
			if _, err = d.NBDURL(ctx); err == nil {
				t.Fatal("failed endpoint available")
			}
			if conn, err = net.Dial("unix", filepath.Join(dir, "nbd.sock")); err == nil {
				conn.Close()
				t.Fatal("fatal export accepts")
			}
		})
	}
}

type fatalUnixError struct{ error }

func (fatalUnixError) FatalDeviceError() bool { return true }
func (e fatalUnixError) Unwrap() error        { return e.error }

func TestUnixDeviceReconnectClassification(t *testing.T) {
	for _, err := range []error{fatalUnixError{io.EOF}, errors.Join(errors.New("integrity"), io.EOF), io.ErrUnexpectedEOF} {
		if reconnectableNBD(err) {
			t.Fatalf("fault permits reconnect: %v", err)
		}
	}
	if !reconnectableNBD(io.EOF) || !reconnectableNBD(nil) {
		t.Fatal("clean disconnect rejected")
	}
}

func TestUnixDeviceTruncatedWriteIsTerminal(t *testing.T) {
	d, _, dir := unixDeviceFixture(t)
	exited := make(chan struct{})
	if err := d.BindConsumer(exited); err != nil {
		t.Fatal(err)
	}
	defer func() { close(exited); d.Close(context.Background()) }()
	conn, err := net.Dial("unix", filepath.Join(dir, "nbd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	versionNegotiate(t, conn, 1<<20)
	if _, err = conn.Write(request(1, 0, 0, 4096, nil)); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = d.Wait(ctx); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial write: %v", err)
	}
	if _, err = d.NBDURL(ctx); err == nil {
		t.Fatal("failed endpoint available")
	}
	if conn, err = net.Dial("unix", filepath.Join(dir, "nbd.sock")); err == nil {
		conn.Close()
		t.Fatal("partial write export accepts")
	}
}

func TestUnixDeviceClaimCannotBeReused(t *testing.T) {
	d, _, dir := unixDeviceFixture(t)
	if err := d.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg, _ := localVersionFixture(t)
	local, err := CreateLocalVersion(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := AttachUnixDevice(t.Context(), local, UnixDeviceConfig{Arena: dir, Owner: "new-owner", Generation: 2})
	if err == nil {
		t.Fatal("stale claim reused")
	}
	if rejected == nil {
		t.Fatal("failed attach dropped ownership")
	}
	if err = rejected.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func unixDeviceFixture(t *testing.T) (*UnixDevice, *LocalVersion, string) {
	t.Helper()
	cfg, _ := localVersionFixture(t)
	local, err := CreateLocalVersion(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "vzd-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	device, err := AttachUnixDevice(t.Context(), local, UnixDeviceConfig{Arena: dir, Owner: "test-owner", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	return device, local, dir
}
func TestUnixDeviceRequiresExclusionAndRetainsClaim(t *testing.T) {
	d, _, dir := unixDeviceFixture(t)
	if _, err := d.NBDURL(t.Context()); err == nil {
		t.Fatal("unbound endpoint released")
	}
	exited := make(chan struct{})
	if err := d.BindConsumer(exited); err != nil {
		t.Fatal(err)
	}
	if err := d.BindConsumer(make(chan struct{})); err == nil {
		t.Fatal("second consumer bound")
	}
	if _, err := d.NBDURL(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(t.Context()); err == nil {
		t.Fatal("live consumer released")
	}
	if _, err := d.LinkInto(t.Context(), dir, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("pretended to be block device")
	}
	close(exited)
	if err := d.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "nbd-owner.json")); err != nil {
		t.Fatal("claim lost", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "nbd.sock")); err != nil {
		t.Fatal("endpoint evidence lost", err)
	}
	if _, err := net.Dial("unix", filepath.Join(dir, "nbd.sock")); err == nil {
		t.Fatal("closed export accepts")
	}
}
func TestUnixDevicePreBindCleanup(t *testing.T) {
	d, local, _ := unixDeviceFixture(t)
	if err := d.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := local.ReadAt(t.Context(), make([]byte, 1), 0); err == nil {
		t.Fatal("local disk retained after close")
	}
}
func TestUnixDeviceReconnectPreservesWritesAndCapture(t *testing.T) {
	d, local, dir := unixDeviceFixture(t)
	exited := make(chan struct{})
	defer func() { close(exited); d.Close(t.Context()) }()
	if err := d.BindConsumer(exited); err != nil {
		t.Fatal(err)
	}
	for _, value := range []byte{8, 9} {
		conn, err := net.Dial("unix", filepath.Join(dir, "nbd.sock"))
		if err != nil {
			t.Fatal(err)
		}
		versionNegotiate(t, conn, 1<<20)
		for _, raw := range [][]byte{request(1, 0, 7, 1, []byte{value}), request(3, 0, 0, 0, nil)} {
			if _, err = conn.Write(raw); err != nil {
				t.Fatal(err)
			}
			var reply [16]byte
			if _, err = io.ReadFull(conn, reply[:]); err != nil {
				t.Fatal(err)
			}
			if binary.BigEndian.Uint32(reply[4:]) != 0 {
				t.Fatal("I/O failure")
			}
		}
		cut, err := d.Capture(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		cut.Release()
		if got := localByte(t, local); got != value {
			t.Fatal("lost write", got)
		}
		conn.Write(request(2, 0, 0, 0, nil))
		conn.Close()
	}
}
func TestUnixDeviceCancellationJoinsBlockedConnection(t *testing.T) {
	for _, phase := range []string{"accept", "negotiation", "transmission"} {
		t.Run(phase, func(t *testing.T) {
			d, _, dir := unixDeviceFixture(t)
			exited := make(chan struct{})
			d.BindConsumer(exited)
			if phase != "accept" {
				conn, err := net.Dial("unix", filepath.Join(dir, "nbd.sock"))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if phase == "transmission" {
					versionNegotiate(t, conn, 1<<20)
				} else {
					versionHello(t, conn)
				}
			}
			close(exited)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := d.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestUnixDeviceProtocolFailureForbidsReconnect(t *testing.T) {
	d, _, dir := unixDeviceFixture(t)
	exited := make(chan struct{})
	d.BindConsumer(exited)
	defer func() { close(exited); d.Close(t.Context()) }()
	conn, err := net.Dial("unix", filepath.Join(dir, "nbd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	versionHello(t, conn)
	conn.Write([]byte{0, 0, 0, 0})
	conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = d.Wait(ctx); err == nil {
		t.Fatal("invalid protocol returned success")
	}
	if _, err = d.NBDURL(t.Context()); err == nil {
		t.Fatal("failed endpoint available")
	}
	if conn, err = net.Dial("unix", filepath.Join(dir, "nbd.sock")); err == nil {
		conn.Close()
		t.Fatal("failed export accepts")
	}
}
