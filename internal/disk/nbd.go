//go:build linux || darwin

package disk

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"syscall"

	"github.com/helmrdotdev/helmr/internal/nbd"
)

// ServeNBDTransmission serves one negotiated private connection. The caller
// exclusively owns the connection and disk, closes the connection on cancellation,
// and waits for this call before releasing the disk. Flush is host-local only.
func (p *LocalVersion) ServeNBDTransmission(ctx context.Context, stream io.ReadWriter) error {
	p.life.RLock()
	size := p.head.Root.LogicalBytes
	p.life.RUnlock()
	return nbd.ServeTransmission(ctx, stream, versionDevice{LocalVersion: p}, size)
}

type versionDevice struct {
	*LocalVersion
	stats        *versionDeviceStats
	writeThrough bool
}

type versionDeviceStats struct{ writes, flushes, completedFlushes, trims atomic.Uint64 }

// UnixDeviceStats contains operation counts only, never guest data or keys.
type UnixDeviceStats struct {
	Writes           uint64 `json:"writes"`
	Flushes          uint64 `json:"flushes"`
	CompletedFlushes uint64 `json:"completed_flushes"`
	Trims            uint64 `json:"trims"`
}

func versionDeviceError(err error) error {
	if errors.Is(err, ErrVersionBufferFull) || errors.Is(err, ErrVersionStagingFull) {
		return errors.Join(syscall.ENOSPC, err)
	}
	return err
}

// Split at block boundaries so any admitted dirty budget can serve the export.
// Buffer pressure commits locally and retries the unapplied block once; staged
// storage exhaustion remains an error. No remote publication is triggered.
func (d versionDevice) WriteAt(ctx context.Context, b []byte, off int64) (int, error) {
	if d.stats != nil {
		d.stats.writes.Add(1)
	}
	d.request.Lock()
	defer d.request.Unlock()
	total := 0
	for len(b) > 0 {
		n := min(len(b), 4096-int(off%4096))
		_, err := d.LocalVersion.WriteAt(ctx, b[:n], off)
		if errors.Is(err, ErrVersionBufferFull) {
			if _, err = d.LocalVersion.Flush(ctx); err == nil {
				_, err = d.LocalVersion.WriteAt(ctx, b[:n], off)
			}
		}
		if err != nil {
			return total, versionDeviceError(err)
		}
		total += n
		off += int64(n)
		b = b[n:]
	}
	if d.writeThrough {
		if _, err := d.LocalVersion.Flush(ctx); err != nil {
			return total, versionDeviceError(err)
		}
	}
	return total, nil
}
func (d versionDevice) Trim(ctx context.Context, off int64, n int) error {
	if d.stats != nil {
		d.stats.trims.Add(1)
	}
	d.request.Lock()
	defer d.request.Unlock()
	for n > 0 {
		length := min(n, 4096-int(off%4096))
		err := d.LocalVersion.Trim(ctx, off, length)
		if errors.Is(err, ErrVersionBufferFull) {
			if _, err = d.LocalVersion.Flush(ctx); err == nil {
				err = d.LocalVersion.Trim(ctx, off, length)
			}
		}
		if err != nil {
			return versionDeviceError(err)
		}
		n -= length
		off += int64(length)
	}
	if d.writeThrough {
		_, err := d.LocalVersion.Flush(ctx)
		return versionDeviceError(err)
	}
	return nil
}
func (d versionDevice) Flush(ctx context.Context) error {
	if d.stats != nil {
		d.stats.flushes.Add(1)
	}
	d.request.Lock()
	defer d.request.Unlock()
	_, err := d.LocalVersion.Flush(ctx)
	if err == nil && d.stats != nil {
		d.stats.completedFlushes.Add(1)
	}
	return versionDeviceError(err)
}

// ServeNBD owns a private listener for one exclusive device connection, including
// negotiation and cancellation. It returns after all connection I/O has stopped;
// the caller remains responsible for device/consumer exclusion before disk Close.
func (p *LocalVersion) ServeNBD(ctx context.Context, listener net.Listener) error {
	p.life.RLock()
	size := p.head.Root.LogicalBytes
	p.life.RUnlock()
	return nbd.ServeOnce(ctx, listener, versionDevice{LocalVersion: p}, size)
}

// ServeNBDConnection owns one already-accepted private connection, including
// bounded negotiation. A lifecycle owner may accept sequential reconnects only
// while the same consumer and LocalVersion remain exclusively owned.
func (p *LocalVersion) ServeNBDConnection(ctx context.Context, conn net.Conn) error {
	p.life.RLock()
	size, closed := p.head.Root.LogicalBytes, p.closed
	p.life.RUnlock()
	if closed {
		conn.Close()
		return errors.New("local version is closed")
	}
	return nbd.Serve(ctx, conn, versionDevice{LocalVersion: p}, size)
}

// VZ may expose a writethrough virtio disk and omit NBD FLUSH for guest fsync.
// Unix exports therefore persist each full mutation before successful reply.
// The request mutex spans mutation and durable root installation. Linux block
// attachments retain their existing buffered WRITE and explicit FLUSH behavior.
func (p *LocalVersion) serveUnixConnection(ctx context.Context, conn net.Conn, stats *versionDeviceStats) error {
	p.life.RLock()
	size, closed := p.head.Root.LogicalBytes, p.closed
	p.life.RUnlock()
	if closed {
		conn.Close()
		return errors.New("local version is closed")
	}
	return nbd.Serve(ctx, conn, versionDevice{LocalVersion: p, stats: stats, writeThrough: true}, size)
}
