//go:build linux || darwin

package disk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type UnixDeviceConfig struct {
	Arena      string
	Owner      string
	Generation int64
}

// UnixDevice owns a LocalVersion and a private NBD endpoint, without allocating
// a host block device. Its caller retains the durable arena/capacity reservation
// until the VMM is excluded and Close has joined every server operation.
// The exited channel is an in-process capability, never inferred from a PID.
type UnixDevice struct {
	mu       sync.Mutex
	disk     *LocalVersion
	listener net.Listener
	endpoint string
	consumer <-chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
	serveErr error
	stats    versionDeviceStats
	closed   bool
}

// AttachUnixDevice transfers local ownership even on failure. Its non-nil
// result must be closed. An existing claim/socket is never removed or adopted.
// No connection is served until the lifecycle owner successfully binds itself.
func AttachUnixDevice(ctx context.Context, local *LocalVersion, cfg UnixDeviceConfig) (*UnixDevice, error) {
	if local == nil {
		return nil, errors.New("local version required")
	}
	d := &UnixDevice{disk: local}
	if err := ctx.Err(); err != nil {
		return d, err
	}
	canonical, err := filepath.EvalSymlinks(cfg.Arena)
	if err != nil || canonical != cfg.Arena || !filepath.IsAbs(cfg.Arena) {
		return d, errors.New("canonical private arena required")
	}
	info, err := os.Lstat(cfg.Arena)
	if err != nil {
		return d, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || owner.Uid != uint32(os.Getuid()) {
		return d, errors.New("owned private arena required")
	}
	if cfg.Owner == "" || len(cfg.Owner) > 128 || cfg.Generation <= 0 {
		return d, errors.New("owner and positive generation required")
	}
	socket := filepath.Join(cfg.Arena, "nbd.sock")
	if len(socket) > 100 {
		return d, errors.New("Unix NBD socket path exceeds portable bound")
	}
	raw, err := json.Marshal(struct {
		Owner      string `json:"owner"`
		Generation int64  `json:"generation"`
	}{cfg.Owner, cfg.Generation})
	if err != nil {
		return d, err
	}
	claim, err := os.OpenFile(filepath.Join(cfg.Arena, "nbd-owner.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return d, err
	}
	_, writeErr := claim.Write(raw)
	err = errors.Join(writeErr, claim.Sync(), claim.Close(), syncVersionDirectory(cfg.Arena))
	if err != nil {
		return d, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return d, err
	}
	// Keep endpoint/claim evidence after close. Only the backend's positively
	// identified arena cleanup may remove them after consumer exclusion.
	listener.SetUnlinkOnClose(false)
	d.listener = listener
	if err = os.Chmod(socket, 0600); err != nil {
		return d, err
	}
	d.endpoint = "nbd+unix:///?socket=" + url.QueryEscape(socket)
	return d, nil
}

func (d *UnixDevice) BindConsumer(exited <-chan struct{}) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.listener == nil || d.consumer != nil || exited == nil {
		return errors.New("device unavailable or consumer already bound")
	}
	select {
	case <-exited:
		return errors.New("consumer already exited")
	default:
	}
	d.consumer = exited
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.done = make(chan struct{})
	go func() {
		select {
		case <-exited:
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		defer close(d.done)
		defer d.listener.Close()
		stop := context.AfterFunc(ctx, func() { d.listener.Close() })
		defer stop()
		// Bound unexpected reconnects instead of allowing an unbounded retry storm.
		for attempt := 0; attempt < 4; attempt++ {
			conn, err := d.listener.Accept()
			if err != nil {
				d.serveErr = errors.Join(ctx.Err(), err)
				return
			}
			if ctx.Err() != nil {
				conn.Close()
				d.serveErr = ctx.Err()
				return
			}
			err = d.disk.serveUnixConnection(ctx, conn, &d.stats)
			var fatal interface{ FatalDeviceError() bool }
			if errors.As(err, &fatal) && fatal.FatalDeviceError() {
				d.serveErr = err
				return
			}
			if ctx.Err() != nil {
				d.serveErr = ctx.Err()
				return
			}
			// A clean disconnect or frame-boundary EOF can reconnect. Partial frames,
			// malformed requests and fatal storage faults end ownership of the export.
			if !reconnectableNBD(err) {
				d.serveErr = err
				return
			}
		}
		d.serveErr = errors.New("NBD reconnect budget exhausted")
	}()
	return nil
}
func (d *UnixDevice) NBDURL(ctx context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := d.live(); err != nil {
		return "", err
	}
	return d.endpoint, nil
}
func (d *UnixDevice) live() error {
	if d.closed || d.consumer == nil {
		return errors.New("bound device required")
	}
	select {
	case <-d.consumer:
		return errors.New("consumer exited")
	default:
	}
	select {
	case <-d.done:
		return fmt.Errorf("NBD export ended: %w", d.serveErr)
	default:
	}
	return nil
}
func (d *UnixDevice) LinkInto(context.Context, string, int, int) (string, error) {
	return "", errors.New("Unix NBD devices require the backend NBDURL capability, not a host block-device path")
}

// Capture follows acknowledged guest filesystem flush. LocalVersion's request
// mutex cuts between complete NBD writes/flushes; encoding proceeds online.
func (d *UnixDevice) Capture(ctx context.Context) (CapturedVersion, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.live(); err != nil {
		return nil, err
	}
	return d.disk.Capture(ctx)
}
func (d *UnixDevice) Wait(ctx context.Context) error {
	d.mu.Lock()
	done := d.done
	d.mu.Unlock()
	if done == nil {
		return errors.New("consumer not bound")
	}
	select {
	case <-done:
		return d.serveErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (d *UnixDevice) Close(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.consumer != nil {
		select {
		case <-d.consumer:
		default:
			return errors.New("consumer exclusion unproven; retain device")
		}
	}
	if d.cancel != nil {
		d.cancel()
		select {
		case <-d.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	} else if d.listener != nil {
		d.listener.Close()
	}
	if err := d.disk.Close(); err != nil {
		return err
	}
	d.closed = true
	return nil
}

// Only the exact sentinel emitted at a frame boundary is reconnectable.
// Storage errors may wrap EOF; they must still terminate the export.
func reconnectableNBD(err error) bool {
	var fatal interface{ FatalDeviceError() bool }
	if errors.As(err, &fatal) && fatal.FatalDeviceError() {
		return false
	}
	return err == nil || err == io.EOF
}

func (d *UnixDevice) Statistics() UnixDeviceStats {
	return UnixDeviceStats{Writes: d.stats.writes.Load(), Flushes: d.stats.flushes.Load(), CompletedFlushes: d.stats.completedFlushes.Load(), Trims: d.stats.trims.Load()}
}
