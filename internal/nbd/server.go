package nbd

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
)

const optionMagic uint64 = 0x49484156454f5054
const replyMagic uint64 = 0x3e889045565a9

// Serve owns conn through one fixed-newstyle negotiation and transmission.
// Cancellation closes the stream and interrupts blocked reads/writes; storage
// operations receive ctx. The caller joins this call before releasing the disk.
// One unnamed export supports FLUSH/TRIM, but no FUA or multi-connection claims.
func Serve(ctx context.Context, conn net.Conn, d BlockDevice, size int64) error {
	defer conn.Close()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); close(closed) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if size <= 0 {
		return errors.New("invalid export size")
	}
	return negotiate(ctx, conn, d, size)
}

// ServeOnce owns listener and accepts exactly one connection. It closes the
// listener on acceptance or cancellation and joins Serve before returning.
// Caller must create it in a private host directory before handing it to Claim.
func ServeOnce(ctx context.Context, listener net.Listener, d BlockDevice, size int64) error {
	defer listener.Close()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { listener.Close(); close(closed) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	listener.Close()
	return Serve(ctx, conn, d, size)
}

func negotiate(ctx context.Context, rw io.ReadWriter, d BlockDevice, size int64) error {
	var greeting [18]byte
	binary.BigEndian.PutUint64(greeting[:8], 0x4e42444d41474943)
	binary.BigEndian.PutUint64(greeting[8:16], optionMagic)
	binary.BigEndian.PutUint16(greeting[16:], 3) // FIXED_NEWSTYLE | NO_ZEROES
	if err := writeAll(rw, greeting[:]); err != nil {
		return err
	}
	var cf [4]byte
	if _, err := io.ReadFull(rw, cf[:]); err != nil {
		return err
	}
	flags := binary.BigEndian.Uint32(cf[:])
	if flags&1 == 0 || flags & ^uint32(3) != 0 {
		return errors.New("unsupported client flags")
	}
	// Bound both individual payloads and total negotiation work.
	for attempt := 0; attempt < 16; attempt++ {
		var hdr [16]byte
		if _, err := io.ReadFull(rw, hdr[:]); err != nil {
			return err
		}
		if binary.BigEndian.Uint64(hdr[:8]) != optionMagic {
			return errors.New("invalid option magic")
		}
		option := binary.BigEndian.Uint32(hdr[8:12])
		n := binary.BigEndian.Uint32(hdr[12:])
		if n > 4096 {
			return errors.New("option too large")
		}
		payload := make([]byte, int(n))
		if _, err := io.ReadFull(rw, payload); err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		switch option {
		case 1: // EXPORT_NAME has no option error reply; invalid names terminate.
			if len(payload) != 0 {
				return errors.New("unknown export")
			}
			var export [10]byte
			binary.BigEndian.PutUint64(export[:8], uint64(size))
			features := uint16(1 | 4 | 32) // HAS_FLAGS | SEND_FLUSH | SEND_TRIM
			binary.BigEndian.PutUint16(export[8:], features)
			if err := writeAll(rw, export[:]); err != nil {
				return err
			}
			if flags&2 == 0 {
				if err := writeAll(rw, make([]byte, 124)); err != nil {
					return err
				}
			}
			return ServeTransmission(ctx, rw, d, size)
		case 6, 7: // INFO and GO share bounded request/reply framing.
			accepted, err := exportInfo(rw, option, payload, size)
			if err != nil {
				return err
			}
			if accepted && option == 7 {
				return ServeTransmission(ctx, rw, d, size)
			}
		case 2:
			if n != 0 {
				return errors.New("invalid abort")
			}
			return optionReply(rw, option, 1)
		default:
			if err := optionReply(rw, option, 0x80000001); err != nil {
				return err
			}
		}
	}
	return errors.New("too many negotiation options")
}
func optionReply(w io.Writer, option, reply uint32) error {
	return optionReplyPayload(w, option, reply, nil)
}
func optionReplyPayload(w io.Writer, option, reply uint32, payload []byte) error {
	var b [20]byte
	binary.BigEndian.PutUint64(b[:8], replyMagic)
	binary.BigEndian.PutUint32(b[8:12], option)
	binary.BigEndian.PutUint32(b[12:16], reply)
	binary.BigEndian.PutUint32(b[16:], uint32(len(payload)))
	if err := writeAll(w, b[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

// exportInfo serves one unnamed export. Sizes are checked before slicing and
// unknown info requests are ignored per the NBD protocol. No FUA or concurrent
// connection promises are added by this negotiation path.
func exportInfo(w io.Writer, option uint32, payload []byte, size int64) (bool, error) {
	reject := func(code uint32) (bool, error) { return false, optionReply(w, option, code) }
	if len(payload) < 6 {
		return reject(0x80000003)
	} // ERR_INVALID
	length := binary.BigEndian.Uint32(payload[:4])
	if uint64(length) > uint64(len(payload)-6) {
		return reject(0x80000003)
	}
	countAt := 4 + int(length)
	count := int(binary.BigEndian.Uint16(payload[countAt : countAt+2]))
	if len(payload) != countAt+2+count*2 {
		return reject(0x80000003)
	}
	requested := make(map[uint16]bool, count)
	for i := 0; i < count; i++ {
		kind := binary.BigEndian.Uint16(payload[countAt+2+i*2:])
		if requested[kind] {
			return reject(0x80000003)
		}
		requested[kind] = true
	}
	if length != 0 {
		return reject(0x80000006)
	} // ERR_UNKNOWN export
	var export [12]byte // INFO_EXPORT (0), size and transmission flags.
	binary.BigEndian.PutUint64(export[2:10], uint64(size))
	binary.BigEndian.PutUint16(export[10:], 1|4|32)
	if err := optionReplyPayload(w, option, 3, export[:]); err != nil {
		return false, err
	}
	// Advertise our finite payload bound even when not requested; clients which
	// request this information commit to it on GO. Transmission also enforces it.
	var blocks [14]byte
	binary.BigEndian.PutUint16(blocks[:2], 3)
	binary.BigEndian.PutUint32(blocks[2:6], 1)
	binary.BigEndian.PutUint32(blocks[6:10], 4096)
	binary.BigEndian.PutUint32(blocks[10:], MaxRequest)
	if err := optionReplyPayload(w, option, 3, blocks[:]); err != nil {
		return false, err
	}
	if requested[1] {
		if err := optionReplyPayload(w, option, 3, []byte{0, 1}); err != nil {
			return false, err
		}
	}
	return true, optionReply(w, option, 1)
}
