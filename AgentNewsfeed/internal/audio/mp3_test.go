package audio

import (
	"bytes"
	"testing"
)

// frame is one layer III frame with header bytes b1 and b2 (version, layer,
// bit rate, sample rate, padding) and a payload filling its size.
func frame(b1, b2 byte, size int, payload string) []byte {
	out := make([]byte, size)
	copy(out, []byte{0xff, b1, b2, 0xc4})
	copy(out[4:], payload)
	return out
}

func TestParseMP3SkipsTagsAndInfoFramesAndCutsBetweenFrames(t *testing.T) {
	// MPEG-1, 64 kbit/s, 44.1 kHz: 208 bytes, or 209 with padding.
	var data []byte
	data = append(data, []byte("ID3\x04\x00\x00\x00\x00\x00\x05")...)
	data = append(data, "tag!!"...)
	data = append(data, frame(0xfb, 0x50, 208, "\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00Info")...)
	for i := range 10 {
		if i%2 == 0 {
			data = append(data, frame(0xfb, 0x52, 209, "padded")...)
		} else {
			data = append(data, frame(0xfb, 0x50, 208, "plain")...)
		}
	}
	data = append(data, "TAG"...)
	stream, err := parseMP3(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 10 || stream.frameMS < 26.12 || stream.frameMS > 26.13 {
		t.Fatalf("frames: %d of %.3f ms", len(stream.frames), stream.frameMS)
	}
	if stream.frameAt(-5) != 0 || stream.frameAt(39) != 1 || stream.frameAt(40) != 2 || stream.frameAt(1e6) != 10 {
		t.Fatal("frameAt does not round to the nearest frame boundary inside the stream")
	}
	clip := stream.clip(1, 3)
	if len(clip) != 208+209 || !bytes.HasPrefix(clip, []byte{0xff, 0xfb, 0x50}) || !bytes.Contains(clip[208:], []byte("padded")) {
		t.Fatalf("clip: %d bytes", len(clip))
	}
	if stream.clip(4, 4) != nil {
		t.Fatal("an empty range is no clip")
	}
}

func TestParseMP3RejectsWhatItCannotCut(t *testing.T) {
	mpeg2 := frame(0xf3, 0x80, 576/8*64000/22050, "")
	for name, data := range map[string][]byte{
		"not audio":           []byte("MP3:hello"),
		"only an info frame":  frame(0xfb, 0x50, 208, "Xing"),
		"sample rate changes": append(frame(0xfb, 0x50, 208, ""), mpeg2...),
		"layer II":            frame(0xfd, 0x50, 208, ""),
	} {
		if _, err := parseMP3(data); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	// MPEG-2 frames have half the samples.
	stream, err := parseMP3(append(mpeg2, mpeg2...))
	if err != nil || len(stream.frames) != 2 || stream.frameMS < 26.12 || stream.frameMS > 26.13 {
		t.Fatalf("MPEG-2: %+v %v", stream.frames, err)
	}
}
