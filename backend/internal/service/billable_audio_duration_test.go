package service

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractBillableAudioDurationSeconds_WAVCeilsDuration(t *testing.T) {
	data := testWAVBytes(t, 16000, 2, 16000*2*3+1)

	seconds, ok := ExtractBillableAudioDurationSeconds(data)

	require.True(t, ok)
	require.Equal(t, 4, seconds)
}

func TestExtractBillableAudioDurationSeconds_Unsupported(t *testing.T) {
	seconds, ok := ExtractBillableAudioDurationSeconds([]byte("not-audio"))

	require.False(t, ok)
	require.Zero(t, seconds)
}

func TestExtractBillableAudioDurationSeconds_WebMDurationCeils(t *testing.T) {
	data := testWebMBytes(t, webmDurationConfig{duration: 3500, timecodeScale: 1000000})

	seconds, ok := ExtractBillableAudioDurationSeconds(data)

	require.True(t, ok)
	require.Equal(t, 4, seconds)
}

func TestExtractBillableAudioDurationSeconds_WebMClusterFallback(t *testing.T) {
	data := testWebMBytes(t, webmDurationConfig{
		timecodeScale:   1000000,
		clusterTimecode: 2100,
		blockTimecode:   700,
	})

	seconds, ok := ExtractBillableAudioDurationSeconds(data)

	require.True(t, ok)
	require.Equal(t, 3, seconds)
}

func TestExtractBillableAudioDurationSeconds_WebMShortAudioBillsOneSecond(t *testing.T) {
	data := testWebMBytes(t, webmDurationConfig{duration: 200, timecodeScale: 1000000})

	seconds, ok := ExtractBillableAudioDurationSeconds(data)

	require.True(t, ok)
	require.Equal(t, 1, seconds)
}

func TestExtractBillableAudioDurationSeconds_WebMWithoutAudioTrack(t *testing.T) {
	data := testWebMBytes(t, webmDurationConfig{duration: 3500, timecodeScale: 1000000, videoOnly: true})

	seconds, ok := ExtractBillableAudioDurationSeconds(data)

	require.False(t, ok)
	require.Zero(t, seconds)
}

func TestExtractBillableAudioDurationSeconds_DamagedWebM(t *testing.T) {
	seconds, ok := ExtractBillableAudioDurationSeconds([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x80})

	require.False(t, ok)
	require.Zero(t, seconds)
}

func testWAVBytes(t *testing.T, sampleRate uint32, bytesPerSample uint16, dataSize uint32) []byte {
	t.Helper()
	channels := uint16(1)
	bitsPerSample := bytesPerSample * 8
	byteRate := sampleRate * uint32(channels) * uint32(bytesPerSample)
	blockAlign := channels * bytesPerSample

	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, []byte("RIFF")))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(36)+dataSize))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, []byte("WAVE")))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, []byte("fmt ")))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(16)))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint16(1)))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, channels))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, sampleRate))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, byteRate))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, blockAlign))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, bitsPerSample))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, []byte("data")))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, dataSize))
	buf.Write(bytes.Repeat([]byte{0}, int(dataSize)))
	return buf.Bytes()
}

type webmDurationConfig struct {
	duration        float64
	timecodeScale   uint64
	clusterTimecode uint64
	blockTimecode   int16
	videoOnly       bool
}

func testWebMBytes(t *testing.T, cfg webmDurationConfig) []byte {
	t.Helper()
	scale := cfg.timecodeScale
	if scale == 0 {
		scale = 1000000
	}
	infoChildren := append([]byte{}, ebmlUIntElement(0x2AD7B1, scale)...)
	if cfg.duration > 0 {
		infoChildren = append(infoChildren, ebmlFloat64Element(0x4489, cfg.duration)...)
	}
	segmentChildren := append([]byte{}, ebmlElement(0x1549A966, infoChildren)...)
	segmentChildren = append(segmentChildren, ebmlElement(0x1654AE6B, testTrackEntryBytes(cfg.videoOnly))...)
	if cfg.clusterTimecode > 0 || cfg.blockTimecode > 0 {
		clusterChildren := append([]byte{}, ebmlUIntElement(0xE7, cfg.clusterTimecode)...)
		clusterChildren = append(clusterChildren, ebmlElement(0xA3, testSimpleBlockBytes(cfg.blockTimecode))...)
		segmentChildren = append(segmentChildren, ebmlElement(0x1F43B675, clusterChildren)...)
	}
	var out []byte
	out = append(out, ebmlElement(0x1A45DFA3, []byte{0x42, 0x86, 0x81, 0x01})...)
	out = append(out, ebmlElement(0x18538067, segmentChildren)...)
	return out
}

func testTrackEntryBytes(videoOnly bool) []byte {
	trackType := uint64(2)
	codecID := "A_OPUS"
	if videoOnly {
		trackType = 1
		codecID = "V_VP9"
	}
	children := append([]byte{}, ebmlUIntElement(0xD7, 1)...)
	children = append(children, ebmlUIntElement(0x73C5, 1)...)
	children = append(children, ebmlUIntElement(0x83, trackType)...)
	children = append(children, ebmlStringElement(0x86, codecID)...)
	if !videoOnly {
		audioChildren := append([]byte{}, ebmlFloat64Element(0xB5, 48000)...)
		audioChildren = append(audioChildren, ebmlUIntElement(0x9F, 1)...)
		children = append(children, ebmlElement(0xE1, audioChildren)...)
	}
	return ebmlElement(0xAE, children)
}

func ebmlElement(id uint64, payload []byte) []byte {
	out := append([]byte{}, ebmlID(id)...)
	out = append(out, ebmlSize(uint64(len(payload)))...)
	out = append(out, payload...)
	return out
}

func ebmlUIntElement(id uint64, value uint64) []byte {
	payload := minimalBigEndianUInt(value)
	return ebmlElement(id, payload)
}

func ebmlFloat64Element(id uint64, value float64) []byte {
	var payload [8]byte
	binary.BigEndian.PutUint64(payload[:], mathFloat64bits(value))
	return ebmlElement(id, payload[:])
}

func ebmlStringElement(id uint64, value string) []byte {
	return ebmlElement(id, []byte(value))
}

func ebmlID(id uint64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], id)
	i := 0
	for i < len(raw)-1 && raw[i] == 0 {
		i++
	}
	return raw[i:]
}

func ebmlSize(size uint64) []byte {
	if size < 0x7f {
		return []byte{byte(0x80 | size)}
	}
	if size < 0x3fff {
		return []byte{byte(0x40 | (size >> 8)), byte(size)}
	}
	if size < 0x1fffff {
		return []byte{byte(0x20 | (size >> 16)), byte(size >> 8), byte(size)}
	}
	return []byte{byte(0x10 | (size >> 24)), byte(size >> 16), byte(size >> 8), byte(size)}
}

func minimalBigEndianUInt(value uint64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], value)
	i := 0
	for i < len(raw)-1 && raw[i] == 0 {
		i++
	}
	return raw[i:]
}

func testSimpleBlockBytes(blockTimecode int16) []byte {
	return []byte{
		0x81,
		byte(blockTimecode >> 8),
		byte(blockTimecode),
		0x00,
		0x00,
	}
}

func mathFloat64bits(f float64) uint64 {
	return binary.BigEndian.Uint64(mustBinaryFloat64(f))
}

func mustBinaryFloat64(f float64) []byte {
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.BigEndian, f); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
