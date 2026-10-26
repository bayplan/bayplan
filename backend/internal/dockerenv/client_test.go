package dockerenv

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func frame(stream byte, payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	return append(hdr, payload...)
}

// demux unwraps Docker's multiplexed format (stdout and stderr in the same
// stream with an 8-byte header) and concatenates the payloads.
func TestDemux(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(1, "hello "))
	in.Write(frame(2, "err\n"))
	in.Write(frame(1, "world\n"))
	var out bytes.Buffer
	if err := demux(&in, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello err\nworld\n" {
		t.Fatalf("got %q", out.String())
	}
}

// A stream cut mid-header (the container died) is a normal end, not an
// error: what was read is already written.
func TestDemuxTruncatedHeaderIsEOF(t *testing.T) {
	in := bytes.NewBuffer(append(frame(1, "ok"), 1, 0, 0))
	var out bytes.Buffer
	if err := demux(in, &out); err != nil {
		t.Fatalf("expected nil with a truncated header, got %v", err)
	}
	if out.String() != "ok" {
		t.Fatalf("got %q", out.String())
	}
}

// A truncated payload IS an error: the header promised more bytes.
func TestDemuxTruncatedPayloadIsError(t *testing.T) {
	f := frame(1, "abcdef")
	in := bytes.NewBuffer(f[:len(f)-2])
	var out bytes.Buffer
	if err := demux(in, &out); err == nil {
		t.Fatal("expected an error with a truncated payload")
	}
}
