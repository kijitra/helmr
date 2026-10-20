//go:build darwin && arm64

package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"time"

	vz "github.com/Code-Hex/vz/v3"
)

type observation struct {
	Nonce        string    `json:"nonce"`
	Sequence     uint64    `json:"sequence"`
	Frozen       bool      `json:"frozen"`
	PID          int       `json:"pid"`
	Start        string    `json:"start"`
	BootCount    uint64    `json:"boot_count"`
	Offset       int64     `json:"offset"`
	DiskSequence [2]uint64 `json:"disk_sequence"`
	Error        string    `json:"error,omitempty"`
}

func diskPath(volume, mode, name string) string {
	if mode == "checkpoint-restore" {
		return filepath.Join(volume, "restore", name)
	}
	return filepath.Join(volume, name)
}
func machineIdentifier(volume, mode string) (*vz.GenericMachineIdentifier, error) {
	path := filepath.Join(volume, "machine-id")
	if mode == "checkpoint-restore" {
		b, err := readLimited(path, 4096)
		if err != nil {
			return nil, err
		}
		if len(b) > 4096 {
			return nil, errors.New("machine identifier too large")
		}
		return vz.NewGenericMachineIdentifierWithData(b)
	}
	id, err := vz.NewGenericMachineIdentifier()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(id.DataRepresentation())
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	return id, errors.Join(err, closeErr)
}

func exchange(machine *vz.VirtualMachine, command string) (observation, error) {
	devices := machine.SocketDevices()
	if len(devices) != 1 {
		return observation{}, errors.New("expected one vsock device")
	}
	conn, err := devices[0].Connect(10789)
	if err != nil {
		return observation{}, err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return observation{}, err
	}
	if _, err = fmt.Fprintln(conn, command); err != nil {
		return observation{}, err
	}
	line, err := bufio.NewReaderSize(io.LimitReader(conn, 4097), 4097).ReadSlice('\n')
	if err != nil || len(line) > 4096 {
		return observation{}, errors.New("guest response missing or oversized")
	}
	var reply observation
	if err = json.Unmarshal(line, &reply); err != nil {
		return reply, err
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	if len(reply.Nonce) != 64 || reply.PID <= 1 || reply.Start == "" || reply.BootCount != 1 {
		return reply, errors.New("guest oracle invalid or cold-booted")
	}
	return reply, nil
}
func checkpoint(machine *vz.VirtualMachine, config *vz.VirtualMachineConfiguration, files []*os.File, mode, volume string, parentGone <-chan struct{}, signals <-chan os.Signal, storage *fixtureDisk) error {
	call := func(fn func() error, seconds float64) error { return boundedCall(fn, seconds, parentGone, signals) }
	query := func(command string) (observation, error) {
		results := make(chan observation, 1)
		err := call(func() error {
			result, e := exchange(machine, command)
			if e == nil {
				results <- result
			}
			return e
		}, 3)
		if err != nil {
			return observation{}, err
		}
		return <-results, nil
	}
	report := map[string]any{"mode": mode, "clean": true, "state": "stopped", "binding": "Code-Hex/vz v3.8.0", "guest_vcpus": 1, "guest_memory_mib": 512, "disk": "localversion-private-unix-nbd", "guest_clean_shutdown": false}
	if mode == "checkpoint-save" {
		if err := call(func() error { return machine.Start() }, 8); err != nil {
			return fmt.Errorf("start checkpoint VM: %w", err)
		}
		// The guest is a pinned fixture. Wait for its explicit userspace marker
		// before Connect, whose native callback has no cancellation API.
		until := continuous() + 5
		for {
			b, err := readLimited(filepath.Join(volume, mode+"-serial.log"), 1024*1024)
			if err != nil {
				return err
			}
			if len(b) > 1024*1024 {
				return errors.New("serial log exceeded bound")
			}
			if containsReady(b) {
				break
			}
			if continuous() >= until {
				return errors.New("guest fixture readiness deadline")
			}
			time.Sleep(25 * time.Millisecond)
		}
		if _, err := query("advance"); err != nil {
			return err
		}
		flushed, err := query("flush")
		if err != nil {
			return err
		}
		if flushed.Frozen || flushed.Sequence != 1 {
			return errors.New("online flush oracle mismatch")
		}
		// Capture while guest is runnable; only the final RAM checkpoint freezes.
		cut, err := storage.device.Capture(context.Background())
		if err != nil {
			return err
		}
		report["online_capture_root"] = cut.Root()
		defer cut.Release()
		if machine.State() != vz.VirtualMachineStateRunning {
			return errors.New("online capture paused VM")
		}
		if _, err := query("advance"); err != nil {
			return err
		}
		frozen, err := query("freeze")
		if err != nil {
			return err
		}
		if !frozen.Frozen || frozen.Sequence != 2 || frozen.Offset != 4224 || frozen.DiskSequence != [2]uint64{2, 2} {
			return errors.New("guest freeze oracle mismatch")
		}
		if err = storage.verifyOnlineCut(cut); err != nil {
			return err
		}
		report["online_cut_preserved_after_later_flush"] = true
		cut.Release()
		if err = call(machine.Pause, 3); err != nil {
			return err
		}
		if machine.State() != vz.VirtualMachineStatePaused {
			return errors.New("pause not confirmed")
		}
		if err = call(func() error { return machine.SaveMachineStateToPath(filepath.Join(volume, "state.vz")) }, 8); err != nil {
			return err
		}
		report["frozen"] = frozen
		live := make([]byte, 128)
		if _, err := storage.local.ReadAt(context.Background(), live, 4096); err != nil {
			return err
		}
		report["live_disk_sequences_before_stop"] = []uint64{binary.LittleEndian.Uint64(live[:8]), binary.LittleEndian.Uint64(live[64:72])}
		report["nbd_operations_before_stop"] = storage.device.Statistics()
	} else {
		if err := call(func() error {
			return machine.RestoreMachineStateFromURL(filepath.Join(volume, "checkpoint", "state.vz"))
		}, 8); err != nil {
			return err
		}
		if machine.State() != vz.VirtualMachineStatePaused {
			return errors.New("restore did not return paused")
		}
		report["restored_paused"] = true
		if err := call(machine.Resume, 3); err != nil {
			return err
		}
		frozen, err := query("status")
		if err != nil {
			return err
		}
		raw, err := readLimited(filepath.Join(volume, "checkpoint", "oracle.json"), 4096)
		if err != nil {
			return err
		}
		if len(raw) > 4096 {
			return errors.New("oracle too large")
		}
		var expected observation
		if err = json.Unmarshal(raw, &expected); err != nil {
			return err
		}
		// Comparison only; expected RAM nonce/state is never sent into the guest.
		if !frozen.Frozen || !reflect.DeepEqual(frozen, expected) {
			return errors.New("restored RAM/process/FD/disk state does not match")
		}
		report["frozen"] = frozen
		if _, err = query("continue"); err != nil {
			return err
		}
		advanced, err := query("advance")
		if err != nil {
			return err
		}
		if advanced.Nonce != frozen.Nonce || advanced.PID != frozen.PID || advanced.Start != frozen.Start || advanced.Sequence != 3 || advanced.Offset != 4288 || advanced.DiskSequence != [2]uint64{3, 3} || advanced.Frozen {
			return errors.New("continuation oracle mismatch")
		}
		if _, err = query("freeze"); err != nil {
			return err
		}
		report["continued"] = advanced
	}
	if err := call(machine.Stop, 3); err != nil {
		return err
	}
	if machine.State() != vz.VirtualMachineStateStopped {
		return errors.New("stop not confirmed")
	}
	if err := storage.stopped(); err != nil {
		return err
	}
	report["localversion_flush_reopen_verified"] = true
	runtime.KeepAlive(machine)
	runtime.KeepAlive(config)
	runtime.KeepAlive(files)
	return json.NewEncoder(os.Stdout).Encode(report)
}
func containsReady(b []byte) bool {
	return strings.Contains(string(b), "HELMR_CHECKPOINT_ORACLE_READY")
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid bounded file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(b) > int(limit) {
		return nil, errors.New("file exceeded bound")
	}
	return b, err
}
