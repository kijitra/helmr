//go:build darwin && arm64

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"uuid"

	"github.com/helmrdotdev/helmr/internal/cas"
	"github.com/helmrdotdev/helmr/internal/disk"
	"github.com/helmrdotdev/helmr/internal/disk/blockformat"
)

// The native fixture now exercises the real encrypted LocalVersion transport.
// Raw files are only the bounded fixture seed/inspection format. They are not a
// production continuation layout or publication protocol.
type fixtureDisk struct {
	local    *disk.LocalVersion
	device   *disk.UnixDevice
	cfg      disk.LocalVersionConfig
	exited   chan struct{}
	excluded bool
	raw      string
}

func newFixtureDisk(volume, mode string) (*fixtureDisk, error) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	parent := filepath.Join(volume, mode+"-disk")
	if err := os.Mkdir(parent, 0700); err != nil {
		return nil, err
	}
	store, err := cas.NewFile(filepath.Join(parent, "base"))
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	kid := uuid.NewV7().String()
	writer := blockformat.Writer{Source: store, Sink: store, Scope: "synthetic-macos-fixture", ActiveKey: kid, Keys: map[string][]byte{kid: key}, PackLimit: blockformat.MinPackLimit}
	const capacity = 64 << 20
	locator, err := writer.Empty(ctx, capacity, 64)
	if err != nil {
		return nil, err
	}
	path := diskPath(volume, mode, "computer.raw")
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != capacity {
		return nil, errors.New("invalid fixture seed")
	}
	// Read fixed chunks and skip zero blocks; no unbounded archive or tree import.
	chunk := make([]byte, 1<<20)
	zero := make([]byte, 4096)
	for offset := int64(0); offset < capacity; offset += int64(len(chunk)) {
		if _, err = io.ReadFull(f, chunk); err != nil {
			return nil, err
		}
		changes := map[uint64][]byte{}
		for i := 0; i < len(chunk); i += 4096 {
			if !bytes.Equal(chunk[i:i+4096], zero) {
				changes[uint64((offset+int64(i))/4096)] = chunk[i : i+4096]
			}
		}
		if len(changes) > 0 {
			locator, err = writer.Capture(ctx, locator, capacity, changes)
			if err != nil {
				return nil, err
			}
		}
	}
	root, err := disk.NewVersionRoot(locator, capacity)
	if err != nil {
		return nil, err
	}
	cfg := disk.LocalVersionConfig{Directory: filepath.Join(parent, "local"), Base: root, BaseSource: store, Scope: writer.Scope, ActiveKey: kid, Keys: writer.Keys, DirtyBlocks: 8, StagedBytes: 32 << 20, PackLimit: blockformat.MinPackLimit}
	local, err := disk.CreateLocalVersion(ctx, cfg)
	if err != nil {
		return nil, err
	}
	arena := filepath.Join(parent, "nbd")
	if err = os.Mkdir(arena, 0700); err != nil {
		local.Close()
		return nil, err
	}
	device, err := disk.AttachUnixDevice(ctx, local, disk.UnixDeviceConfig{Arena: arena, Owner: mode, Generation: 1})
	if err != nil {
		if device != nil {
			device.Close(ctx)
		}
		return nil, err
	}
	d := &fixtureDisk{local: local, device: device, cfg: cfg, exited: make(chan struct{}), raw: path}
	if err = device.BindConsumer(d.exited); err != nil {
		device.Close(ctx)
		return nil, err
	}
	go func() {
		err := device.Wait(context.Background())
		select {
		case <-d.exited:
			if expectedExportStop(err) {
				return
			}
		default:
		}
		fmt.Fprintln(os.Stderr, "LocalVersion NBD export failed:", err)
		os.Exit(1)
	}()
	return d, nil
}

// Called only after native VM Stop completed and its state was checked. Closing
// on an error path does not assert exclusion: the helper process must exit and
// the supervisor retains the entire arena for reconciliation.
func (d *fixtureDisk) stopped() error {
	if !d.excluded {
		close(d.exited)
		d.excluded = true
	}
	if err := d.device.Close(context.Background()); err != nil {
		return err
	}
	if err := d.device.Wait(context.Background()); !expectedExportStop(err) {
		return fmt.Errorf("unexpected export termination: %w", err)
	}
	// Reopening discards all in-memory dirty state. A missing NBD FLUSH therefore
	// cannot be hidden by exporting the old live cache into the host oracle file.
	reopened, err := disk.OpenLocalVersion(context.Background(), d.cfg)
	if err != nil {
		return err
	}
	defer reopened.Close()
	f, err := os.OpenFile(d.raw, os.O_WRONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	chunk := make([]byte, 1<<20)
	for offset := int64(0); offset < 64<<20; offset += int64(len(chunk)) {
		if n, e := reopened.ReadAt(context.Background(), chunk, offset); e != nil || n != len(chunk) {
			return errors.Join(e, io.ErrUnexpectedEOF)
		}
		if n, e := f.WriteAt(chunk, offset); e != nil || n != len(chunk) {
			return errors.Join(e, io.ErrShortWrite)
		}
	}
	return f.Sync()
}

func expectedExportStop(err error) bool {
	var fatal interface{ FatalDeviceError() bool }
	return !(errors.As(err, &fatal) && fatal.FatalDeviceError()) && errors.Is(err, context.Canceled)
}

type fixtureSource struct{ local, base blockformat.RangeSource }

func (s fixtureSource) GetRange(ctx context.Context, digest string, size, offset, length int64) (io.ReadCloser, error) {
	r, err := s.local.GetRange(ctx, digest, size, offset, length)
	if errors.Is(err, os.ErrNotExist) {
		return s.base.GetRange(ctx, digest, size, offset, length)
	}
	return r, err
}
func (d *fixtureDisk) verifyOnlineCut(cut disk.CapturedVersion) error {
	local, err := cas.NewFile(filepath.Join(d.cfg.Directory, "objects"))
	if err != nil {
		return err
	}
	tree, err := disk.OpenVersion(context.Background(), fixtureSource{local, d.cfg.BaseSource}, d.cfg.Scope, d.cfg.Keys, cut.Root(), 64<<20)
	if err != nil {
		return err
	}
	raw, err := tree.ReadRange(context.Background(), 4096, 128)
	if err != nil {
		return err
	}
	checksum := sha256.Sum256(raw[:32])
	if binary.LittleEndian.Uint64(raw[:8]) != 1 || !bytes.Equal(raw[32:64], checksum[:]) || !bytes.Equal(raw[64:], make([]byte, 64)) {
		return errors.New("online cut lost seq1 or incorporated later seq2")
	}
	return nil
}
