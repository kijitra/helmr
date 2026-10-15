//go:build darwin && arm64

// A short-lived, offline experiment using the same VZ binding as Lima/vfkit.
// This is not the Helmr Worker backend and does not accept arbitrary VM configs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	vz "github.com/Code-Hex/vz/v3"
)

/*
#include <mach/mach_time.h>
static double continuous_seconds(void) {
 mach_timebase_info_data_t b; mach_timebase_info(&b);
 return (double)mach_continuous_time() * b.numer / b.denom / 1e9;
}
*/
import "C"

func continuous() float64 { return float64(C.continuous_seconds()) }

func ownership(volume string, fd int) error {
	root := fmt.Sprintf("/private/tmp/helmr-macos-%d", os.Getuid())
	run := filepath.Dir(volume)
	if filepath.Base(volume) != "volume" || filepath.Dir(run) != root || !strings.HasPrefix(filepath.Base(run), "run-") {
		return errors.New("private runner volume required")
	}
	var inherited, actual syscall.Stat_t
	if err := syscall.Fstat(fd, &inherited); err != nil {
		return err
	}
	if err := syscall.Lstat(filepath.Join(root, "lock"), &actual); err != nil {
		return err
	}
	if inherited.Ino != actual.Ino || inherited.Dev != actual.Dev || inherited.Uid != uint32(os.Getuid()) || inherited.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return errors.New("inherited admission lock required")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(root, "active.json"))
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return err
	}
	var marker struct {
		Run string `json:"run"`
	}
	if len(data) > 4096 || json.Unmarshal(data, &marker) != nil || marker.Run != filepath.Base(run) {
		return errors.New("durable ownership marker required")
	}
	return nil
}

func configuration(volume, mode, nbdURL string) (*vz.VirtualMachineConfiguration, []*os.File, error) {
	opts := []vz.LinuxBootLoaderOption{}
	if mode == "smoke" || strings.HasPrefix(mode, "checkpoint-") {
		opts = append(opts, vz.WithInitrd(filepath.Join(volume, "initramfs")), vz.WithCommandLine("console=hvc0 panic=-1"))
	}
	boot, err := vz.NewLinuxBootLoader(filepath.Join(volume, "kernel"), opts...)
	if err != nil {
		return nil, nil, err
	}
	config, err := vz.NewVirtualMachineConfiguration(boot, 1, 512*1024*1024)
	if err != nil {
		return nil, nil, err
	}
	var platformOptions []vz.GenericPlatformConfigurationOption
	if strings.HasPrefix(mode, "checkpoint-") {
		identifier, err := machineIdentifier(volume, mode)
		if err != nil {
			return nil, nil, err
		}
		platformOptions = append(platformOptions, vz.WithGenericMachineIdentifier(identifier))
	}
	platform, err := vz.NewGenericPlatformConfiguration(platformOptions...)
	if err != nil {
		return nil, nil, err
	}
	config.SetPlatformVirtualMachineConfiguration(platform)
	var disks []vz.StorageDeviceConfiguration
	for _, name := range []string{"scratch.raw", "computer.raw"} {
		var attachment vz.StorageDeviceAttachment
		var err error
		if (mode == "validate-nbd" || strings.HasPrefix(mode, "checkpoint-")) && name == "computer.raw" {
			// Configuration-only: no VM is constructed and no connection is opened.
			endpoint := nbdURL
			if endpoint == "" {
				endpoint = "nbd+unix:///?socket=" + filepath.Join(volume, "computer.sock")
			}
			var remote *vz.NetworkBlockDeviceStorageDeviceAttachment
			remote, err = vz.NewNetworkBlockDeviceStorageDeviceAttachment(endpoint, 3*time.Second, false, vz.DiskSynchronizationModeFull)
			if err == nil {
				attachment = remote
				go func() {
					for range remote.Connected() {
					}
				}()
				go func() {
					for e := range remote.DidEncounterError() {
						fmt.Fprintln(os.Stderr, "NBD attachment failed:", e)
						os.Exit(1)
					}
				}()
			}
		} else {
			attachment, err = vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(diskPath(volume, mode, name), false, vz.DiskImageCachingModeUncached, vz.DiskImageSynchronizationModeFull)
		}
		if err != nil {
			return nil, nil, err
		}
		disk, err := vz.NewVirtioBlockDeviceConfiguration(attachment)
		if err != nil {
			return nil, nil, err
		}
		disks = append(disks, disk)
	}
	config.SetStorageDevicesVirtualMachineConfiguration(disks)
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, err
	}
	files := []*os.File{os.NewFile(uintptr(fds[0]), "network"), os.NewFile(uintptr(fds[1]), "disconnected-peer")}
	// Neither endpoint is connected to host networking. No NAT, bridge, host share or forwarded ports.
	attachment, err := vz.NewFileHandleNetworkDeviceAttachment(files[0])
	if err != nil {
		return nil, files, err
	}
	nic, err := vz.NewVirtioNetworkDeviceConfiguration(attachment)
	if err != nil {
		return nil, files, err
	}
	mac, err := vz.NewMACAddress(net.HardwareAddr{2, 252, 0, 0, 0, 2})
	if err != nil {
		return nil, files, err
	}
	nic.SetMACAddress(mac)
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})
	socket, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, files, err
	}
	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{socket})
	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, files, err
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})
	serialFile, err := os.OpenFile(filepath.Join(volume, mode+"-serial.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, files, err
	}
	files = append(files, serialFile)
	serialAttachment, err := vz.NewFileSerialPortAttachment(filepath.Join(volume, mode+"-serial.log"), true)
	if err != nil {
		return nil, files, err
	}
	serial, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
	if err != nil {
		return nil, files, err
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serial})
	valid, err := config.Validate()
	if err != nil {
		return nil, files, err
	}
	if !valid {
		return nil, files, errors.New("invalid VM configuration")
	}
	valid, err = config.ValidateSaveRestoreSupport()
	if err != nil {
		return nil, files, err
	}
	if !valid {
		return nil, files, errors.New("save/restore unsupported")
	}
	return config, files, nil
}

func boundedCall(fn func() error, seconds float64, parentGone <-chan struct{}, signals <-chan os.Signal) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	deadline := continuous() + seconds
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-parentGone:
			return errors.New("controller gone; quarantine required")
		case <-signals:
			return errors.New("interrupted; quarantine required")
		case <-ticker.C:
			if continuous() >= deadline {
				return errors.New("framework deadline exceeded; quarantine required")
			}
		}
	}
}

func run(mode, volume string, fd int, parentGone <-chan struct{}) error {
	if err := ownership(volume, fd); err != nil {
		return err
	}
	var storage *fixtureDisk
	var endpoint string
	if strings.HasPrefix(mode, "checkpoint-") {
		var err error
		storage, err = newFixtureDisk(volume, mode)
		if err != nil {
			return err
		}
		endpoint, err = storage.device.NBDURL(context.Background())
		if err != nil {
			return err
		}
	}
	config, files, err := configuration(volume, mode, endpoint)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	if err != nil {
		return fmt.Errorf("full-device configuration: %w", err)
	}
	report := map[string]any{"mode": mode, "clean": true, "state": "not-started", "binding": "Code-Hex/vz v3.8.0", "save_restore_configuration_valid": true, "checkpoint_verified": false, "guest_vcpus": 1, "guest_memory_mib": 512}
	if mode == "validate" || mode == "validate-nbd" {
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	machine, err := vz.NewVirtualMachine(config)
	if err != nil {
		return err
	}
	// v3 lifecycle callbacks lack context cancellation; this helper's process is the boundary.
	// Drain state notifications to avoid the binding's unbounded notification queue growing.
	go func() {
		for range machine.StateChangedNotify() {
		}
	}()

	disconnects, err := machine.NetworkDeviceAttachmentWasDisconnected()
	if err != nil {
		return err
	}
	go func() {
		for range disconnects {
			fmt.Fprintln(os.Stderr, "unexpected network detach; quarantine required")
			os.Exit(1)
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	if strings.HasPrefix(mode, "checkpoint-") {
		return checkpoint(machine, config, files, mode, volume, parentGone, signals, storage)
	}
	if err = boundedCall(func() error { return machine.Start() }, 8, parentGone, signals); err != nil {
		return err
	}
	until := continuous() + 3
	for continuous() < until {
		select {
		case <-parentGone:
			return errors.New("controller gone; quarantine required")
		case <-signals:
			return errors.New("interrupted; quarantine required")
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Explicit hard VM stop of disposable test disks, NOT proof of guest fsync/shutdown.
	if err = boundedCall(machine.Stop, 3, parentGone, signals); err != nil {
		return err
	}
	if machine.State() != vz.VirtualMachineStateStopped {
		return errors.New("VM stop not confirmed")
	}
	runtime.KeepAlive(machine)
	runtime.KeepAlive(config)
	runtime.KeepAlive(files)
	report["state"] = "stopped"
	report["guest_clean_shutdown"] = false
	report["guest_application_verified"] = false
	return json.NewEncoder(os.Stdout).Encode(report)
}

func main() {
	if len(os.Args) != 4 || (os.Args[1] != "validate" && os.Args[1] != "validate-nbd" && os.Args[1] != "smoke" && os.Args[1] != "checkpoint-save" && os.Args[1] != "checkpoint-restore") || os.Getuid() == 0 {
		fmt.Fprintln(os.Stderr, "unprivileged runner invocation required")
		os.Exit(1)
	}
	// EOF before/during any native call must also end this process. The persistent
	// marker prevents retries until the supervisor/attachment state is reconciled.
	parentGone := make(chan struct{})
	go func() { var b [1]byte; _, _ = os.Stdin.Read(b[:]); close(parentGone); os.Exit(125) }()
	// Covers native configuration/constructor calls too, even if the runner dies.
	start := continuous()
	budget := float64(15)
	if strings.HasPrefix(os.Args[1], "checkpoint-") {
		budget = 30
	} // explicit per-process checkpoint budget
	go func() {
		for {
			time.Sleep(100 * time.Millisecond)
			if continuous()-start >= budget {
				os.Exit(124)
			}
		}
	}()

	fd, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = run(os.Args[1], os.Args[2], fd, parentGone); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
