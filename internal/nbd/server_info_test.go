package nbd

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func infoRequest(name string, requested ...uint16) []byte {
	b := make([]byte, 6+len(name)+2*len(requested))
	binary.BigEndian.PutUint32(b[:4], uint32(len(name)))
	copy(b[4:], name)
	binary.BigEndian.PutUint16(b[4+len(name):], uint16(len(requested)))
	for i, r := range requested {
		binary.BigEndian.PutUint16(b[6+len(name)+2*i:], r)
	}
	return b
}
func option(code uint32, b []byte) []byte { return negotiationOption(code, uint32(len(b)), b) }
func transmissionDisconnect() []byte {
	b := make([]byte, 28)
	binary.BigEndian.PutUint32(b, 0x25609513)
	binary.BigEndian.PutUint16(b[6:], 2)
	return b
}
func parseReplies(t *testing.T, raw []byte) [][]byte {
	t.Helper()
	var replies [][]byte
	for len(raw) > 0 {
		if len(raw) < 20 || binary.BigEndian.Uint64(raw) != replyMagic {
			t.Fatal("bad reply framing")
		}
		n := int(binary.BigEndian.Uint32(raw[16:20]))
		if n > len(raw)-20 {
			t.Fatal("truncated reply")
		}
		replies = append(replies, raw[:20+n])
		raw = raw[20+n:]
	}
	return replies
}
func TestInfoThenGoHasSameInformationAndEntersTransmission(t *testing.T) {
	wire := []byte{0, 0, 0, 3}
	wire = append(wire, option(6, infoRequest("", 0, 3, 1, 999))...)
	wire = append(wire, option(7, infoRequest("", 0, 3, 1, 999))...)
	wire = append(wire, transmissionDisconnect()...)
	stream := &negotiationStream{Reader: bytes.NewReader(wire)}
	if err := negotiate(t.Context(), stream, nil, 4096); err != nil {
		t.Fatal(err)
	}
	replies := parseReplies(t, stream.output.Bytes()[18:])
	if len(replies) != 8 {
		t.Fatalf("got %d replies", len(replies))
	}
	for i := 0; i < 4; i++ {
		a, b := replies[i], replies[i+4]
		if binary.BigEndian.Uint32(a[8:]) != 6 || binary.BigEndian.Uint32(b[8:]) != 7 || !bytes.Equal(a[12:], b[12:]) {
			t.Fatal("INFO/GO mismatch")
		}
	}
	if binary.BigEndian.Uint64(replies[0][22:]) != 4096 || binary.BigEndian.Uint16(replies[0][30:]) != 37 {
		t.Fatal("wrong export metadata")
	}
	if binary.BigEndian.Uint32(replies[1][30:]) != MaxRequest {
		t.Fatal("payload limit not advertised")
	}
	if binary.BigEndian.Uint32(replies[3][12:]) != 1 || len(replies[3]) != 20 {
		t.Fatal("missing final ACK")
	}
}
func TestMalformedInfoNeverEntersTransmission(t *testing.T) {
	cases := [][]byte{nil, {0, 0, 0, 0, 0}, {255, 255, 255, 255, 0, 0}, infoRequest("unknown"), infoRequest("", 3, 3), append(infoRequest(""), 0)}
	for _, payload := range cases {
		t.Run(string(payload), func(t *testing.T) {
			wire := append([]byte{0, 0, 0, 3}, option(7, payload)...)
			wire = append(wire, option(2, nil)...)
			stream := &negotiationStream{Reader: bytes.NewReader(wire)}
			if err := negotiate(t.Context(), stream, nil, 4096); err != nil {
				t.Fatal(err)
			}
			replies := parseReplies(t, stream.output.Bytes()[18:])
			if len(replies) != 2 || binary.BigEndian.Uint32(replies[0][12:])&0x80000000 == 0 {
				t.Fatal("malformed request accepted")
			}
		})
	}
}
func TestGoWritesThroughExistingTransmission(t *testing.T) {
	wire := append([]byte{0, 0, 0, 3}, option(7, infoRequest("", 3))...)
	header := make([]byte, 28)
	binary.BigEndian.PutUint32(header, 0x25609513)
	binary.BigEndian.PutUint16(header[6:], 1)
	binary.BigEndian.PutUint32(header[24:], 3)
	wire = append(wire, header...)
	wire = append(wire, 1, 2, 3)
	wire = append(wire, transmissionDisconnect()...)
	stream := &negotiationStream{Reader: bytes.NewReader(wire)}
	disk := &framingDisk{data: make([]byte, 4096)}
	if err := negotiate(t.Context(), stream, disk, 4096); err != nil {
		t.Fatal(err)
	}
	if disk.writes != 1 || !bytes.Equal(disk.data[:3], []byte{1, 2, 3}) {
		t.Fatal("GO failed to enter transmission")
	}
}
func TestGoDoesNotEmitLegacyZeroPadding(t *testing.T) {
	wire := append([]byte{0, 0, 0, 1}, option(7, infoRequest(""))...)
	wire = append(wire, transmissionDisconnect()...)
	stream := &negotiationStream{Reader: bytes.NewReader(wire)}
	if err := negotiate(t.Context(), stream, nil, 4096); err != nil {
		t.Fatal(err)
	}
	if len(parseReplies(t, stream.output.Bytes()[18:])) != 3 {
		t.Fatal("unexpected padding")
	}
}
func TestTruncatedInfoFails(t *testing.T) {
	stream := &negotiationStream{Reader: bytes.NewReader(append([]byte{0, 0, 0, 3}, negotiationOption(7, 6, []byte{0})...))}
	if err := negotiate(t.Context(), stream, nil, 4096); err != io.ErrUnexpectedEOF {
		t.Fatalf("got %v", err)
	}
}
