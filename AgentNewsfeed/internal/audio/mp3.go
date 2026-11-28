package audio

import (
	"bytes"
	"errors"
)

// mp3Stream is an MP3 file split into its audio frames, so that it can be cut
// between frames without decoding. Every frame of a stream lasts frameMS.
type mp3Stream struct {
	data    []byte
	frames  []mp3Frame
	frameMS float64
}

type mp3Frame struct{ offset, size int }

var errNotMP3 = errors.New("not a constant-format MPEG audio layer III stream")

// MPEG audio layer III bit rates in kbit/s by version (MPEG-1, then MPEG-2 and
// 2.5) and index, and sample rates in Hz by version and index.
var (
	mp3BitRates = [2][16]int{
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	}
	mp3SampleRates = map[byte][3]int{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}
)

// parseMP3 finds the audio frames of an MP3 file, as ElevenLabs returns it:
// an optional ID3v2 tag, then layer III frames of one version and sample rate.
// A leading Xing, Info or VBRI frame describes the whole file rather than
// holding audio, so it is left out; trailing data that isn't a frame, such as
// an ID3v1 tag, ends the stream.
func parseMP3(data []byte) (mp3Stream, error) {
	pos := 0
	if len(data) >= 10 && string(data[:3]) == "ID3" {
		size := int(data[6]&0x7f)<<21 | int(data[7]&0x7f)<<14 | int(data[8]&0x7f)<<7 | int(data[9]&0x7f)
		pos = 10 + size
		if data[5]&0x10 != 0 {
			pos += 10
		}
	}
	stream := mp3Stream{data: data}
	var version byte
	var sampleRate int
	for pos+4 <= len(data) {
		h := data[pos : pos+4]
		if h[0] != 0xff || h[1]&0xe0 != 0xe0 {
			break
		}
		v, layer := (h[1]>>3)&3, (h[1]>>1)&3
		rates, known := mp3SampleRates[v]
		rateIndex, bitIndex := (h[2]>>2)&3, h[2]>>4
		if !known || layer != 1 || rateIndex == 3 {
			break
		}
		table, samples := 1, 576
		if v == 3 {
			table, samples = 0, 1152
		}
		bitRate, rate := mp3BitRates[table][bitIndex]*1000, rates[rateIndex]
		if bitRate == 0 {
			break
		}
		if len(stream.frames) == 0 {
			version, sampleRate = v, rate
			stream.frameMS = float64(samples) * 1000 / float64(rate)
		} else if v != version || rate != sampleRate {
			return mp3Stream{}, errNotMP3
		}
		size := samples/8*bitRate/rate + int((h[2]>>1)&1)
		if size < 4 || pos+size > len(data) {
			break
		}
		stream.frames = append(stream.frames, mp3Frame{pos, size})
		pos += size
	}
	if len(stream.frames) > 0 && isInfoFrame(data[stream.frames[0].offset:][:stream.frames[0].size]) {
		stream.frames = stream.frames[1:]
	}
	if len(stream.frames) == 0 {
		return mp3Stream{}, errNotMP3
	}
	return stream, nil
}

// isInfoFrame reports whether a frame carries a Xing, Info or VBRI header,
// which sits right after the side information, at most 36 bytes in.
func isInfoFrame(frame []byte) bool {
	head := frame[:min(len(frame), 48)]
	return bytes.Contains(head, []byte("Xing")) || bytes.Contains(head, []byte("Info")) || bytes.Contains(head, []byte("VBRI"))
}

// frameAt is the index of the frame boundary closest to ms, within the stream.
func (s mp3Stream) frameAt(ms float64) int {
	return min(max(int(ms/s.frameMS+0.5), 0), len(s.frames))
}

// clip returns frames [from, to) as an MP3 file of their own.
func (s mp3Stream) clip(from, to int) []byte {
	if from >= to {
		return nil
	}
	start, last := s.frames[from], s.frames[to-1]
	return bytes.Clone(s.data[start.offset : last.offset+last.size])
}
