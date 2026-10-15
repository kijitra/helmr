//go:build linux && arm64

// Dedicated checkpoint oracle. Runs only as PID 1 inside a disposable Linux VM.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type response struct {
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

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	for {
		time.Sleep(time.Second)
	}
}
func must(err error) {
	if err != nil {
		die(err)
	}
}

func child() error {
	var disks [2]*os.File
	for i, name := range []string{"/dev/vda", "/dev/vdb"} {
		f, err := os.OpenFile(name, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		disks[i] = f
		defer f.Close()
	}
	var boot [8]byte
	if _, err := disks[1].ReadAt(boot[:], 0); err != nil {
		return err
	}
	count := binary.LittleEndian.Uint64(boot[:]) + 1
	binary.LittleEndian.PutUint64(boot[:], count)
	for _, f := range disks {
		if _, err := f.WriteAt(boot[:], 0); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw[strings.LastIndex(string(raw), ")")+2:]))
	if len(fields) < 20 {
		return fmt.Errorf("process stat missing")
	}
	state := response{Nonce: hex.EncodeToString(nonce[:]), PID: os.Getpid(), Start: fields[19], BootCount: count}
	// Keep the very same open file description across save and restore.
	if _, err = disks[0].Seek(4096, io.SeekStart); err != nil {
		return err
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 128), 1024)
	for scanner.Scan() {
		command := scanner.Text()
		state.Error = ""
		switch command {
		case "advance":
			if state.Frozen {
				state.Error = "workload is frozen"
				break
			}
			state.Sequence++
			var record [64]byte
			binary.LittleEndian.PutUint64(record[:8], state.Sequence)
			sum := sha256.Sum256(record[:32])
			copy(record[32:], sum[:])
			if _, err = disks[0].Write(record[:]); err != nil {
				return err
			}
			if _, err = disks[1].WriteAt(record[:], 4096+int64(state.Sequence-1)*64); err != nil {
				return err
			}
		case "freeze", "flush":
			if command == "freeze" {
				state.Frozen = true
			}
			for _, f := range disks {
				if err = f.Sync(); err != nil {
					return err
				}
			}
		case "continue":
			if !state.Frozen {
				state.Error = "not frozen"
				break
			}
			state.Frozen = false
		case "status":
		default:
			state.Error = "unknown command"
		}
		state.Offset, err = disks[0].Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if state.Sequence > 0 {
			for i, f := range disks {
				var record [64]byte
				if _, err = f.ReadAt(record[:], 4096+int64(state.Sequence-1)*64); err != nil {
					return err
				}
				sum := sha256.Sum256(record[:32])
				if string(sum[:]) != string(record[32:]) {
					return fmt.Errorf("disk checksum mismatch")
				}
				state.DiskSequence[i] = binary.LittleEndian.Uint64(record[:8])
			}
		}
		if err = json.NewEncoder(os.Stdout).Encode(state); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func initProcess() error {
	if os.Getpid() != 1 {
		return fmt.Errorf("fixture must be guest PID 1")
	}
	for _, m := range []struct{ name, typ string }{{"/dev", "devtmpfs"}, {"/proc", "proc"}, {"/sys", "sysfs"}} {
		if err := os.MkdirAll(m.name, 0755); err != nil {
			return err
		}
		if err := unix.Mount(m.typ, m.name, m.typ, 0, ""); err != nil {
			return err
		}
	}
	console, err := os.OpenFile("/dev/hvc0", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	for _, fd := range []int{0, 1, 2} {
		if int(console.Fd()) != fd {
			if err = unix.Dup3(int(console.Fd()), fd, 0); err != nil {
				return err
			}
		}
	}
	defer console.Close()
	for _, name := range []string{"virtio_blk", "vsock", "vmw_vsock_virtio_transport_common", "vmw_vsock_virtio_transport"} {
		b, err := os.ReadFile("/modules/" + name + ".ko")
		if err != nil {
			return err
		}
		if err = unix.InitModule(b, ""); err != nil && err != unix.EEXIST {
			return fmt.Errorf("module %s: %w", name, err)
		}
	}
	c := exec.Command("/init", "--child")
	c.Stderr = os.Stderr
	input, err := c.StdinPipe()
	if err != nil {
		return err
	}
	output, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err = c.Start(); err != nil {
		return err
	}
	go func() { die(fmt.Errorf("oracle child exited: %v", c.Wait())) }()
	reader := bufio.NewReaderSize(output, 4096)
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err = unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: 10789}); err != nil {
		return err
	}
	if err = unix.Listen(fd, 1); err != nil {
		return err
	}
	fmt.Println("HELMR_CHECKPOINT_ORACLE_READY")
	for {
		conn, _, err := unix.Accept4(fd, unix.SOCK_CLOEXEC)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(conn), "vsock")
		scanner := bufio.NewScanner(io.LimitReader(file, 1025))
		scanner.Buffer(make([]byte, 128), 1024)
		if scanner.Scan() {
			cmd := scanner.Text()
			if cmd != "flush" && cmd != "advance" && cmd != "freeze" && cmd != "continue" && cmd != "status" {
				file.Close()
				continue
			}
			if _, err = fmt.Fprintln(input, cmd); err != nil {
				file.Close()
				return err
			}
			line, err := reader.ReadSlice('\n')
			if err != nil {
				file.Close()
				return err
			}
			if _, err = file.Write(line); err != nil {
				file.Close()
				return err
			}
		}
		file.Close()
	}
}
func main() {
	if len(os.Args) == 2 && os.Args[1] == "--child" {
		must(child())
		return
	}
	must(initProcess())
}
