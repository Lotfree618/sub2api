package service

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
)

const (
	ebmlIDHeader        = 0x1A45DFA3
	ebmlIDSegment       = 0x18538067
	ebmlIDInfo          = 0x1549A966
	ebmlIDTimecodeScale = 0x2AD7B1
	ebmlIDDuration      = 0x4489
	ebmlIDTracks        = 0x1654AE6B
	ebmlIDTrackEntry    = 0xAE
	ebmlIDTrackNumber   = 0xD7
	ebmlIDTrackType     = 0x83
	ebmlIDCodecID       = 0x86
	ebmlIDAudio         = 0xE1
	ebmlIDCluster       = 0x1F43B675
	ebmlIDClusterTime   = 0xE7
	ebmlIDSimpleBlock   = 0xA3
	ebmlIDBlockGroup    = 0xA0
	ebmlIDBlock         = 0xA1

	webmDefaultTimecodeScale = uint64(1000000)
)

func ExtractBillableAudioDurationSeconds(data []byte) (int, bool) {
	if len(data) == 0 {
		return 0, false
	}
	return parseAudioDurationSeconds(data)
}

func ceilPositiveSeconds(value float64) int {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return int(math.Ceil(value))
}

func parseAudioDurationSeconds(data []byte) (int, bool) {
	switch {
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")):
		return parseWAVDurationSeconds(data)
	case len(data) >= 10 && bytes.HasPrefix(data, []byte("ID3")),
		len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0:
		return parseMP3DurationSeconds(data)
	case len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")):
		return parseMP4DurationSeconds(data)
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return parseWebMDurationSeconds(data)
	default:
		return 0, false
	}
}

func parseWAVDurationSeconds(data []byte) (int, bool) {
	var byteRate uint32
	var dataSize uint32
	for offset := 12; offset+8 <= len(data); {
		chunkID := string(data[offset : offset+4])
		chunkSize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		payload := offset + 8
		next := payload + int(chunkSize)
		if next > len(data) {
			return 0, false
		}
		switch chunkID {
		case "fmt ":
			if chunkSize >= 16 {
				byteRate = binary.LittleEndian.Uint32(data[payload+8 : payload+12])
			}
		case "data":
			dataSize = chunkSize
		}
		if byteRate > 0 && dataSize > 0 {
			return ceilPositiveSeconds(float64(dataSize) / float64(byteRate)), true
		}
		offset = next + int(chunkSize%2)
	}
	return 0, false
}

func parseMP3DurationSeconds(data []byte) (int, bool) {
	offset := skipID3v2(data)
	totalSeconds := 0.0
	frames := 0
	for offset+4 <= len(data) {
		frameLen, samples, sampleRate, ok := parseMP3FrameHeader(data[offset : offset+4])
		if !ok || frameLen <= 0 || offset+frameLen > len(data) {
			offset++
			continue
		}
		totalSeconds += float64(samples) / float64(sampleRate)
		frames++
		offset += frameLen
	}
	if frames == 0 {
		return 0, false
	}
	return ceilPositiveSeconds(totalSeconds), true
}

func skipID3v2(data []byte) int {
	if len(data) < 10 || !bytes.HasPrefix(data, []byte("ID3")) {
		return 0
	}
	size := int(data[6]&0x7f)<<21 | int(data[7]&0x7f)<<14 | int(data[8]&0x7f)<<7 | int(data[9]&0x7f)
	if 10+size >= len(data) {
		return 0
	}
	return 10 + size
}

func parseMP3FrameHeader(h []byte) (frameLen int, samples int, sampleRate int, ok bool) {
	if len(h) < 4 || h[0] != 0xff || h[1]&0xe0 != 0xe0 {
		return 0, 0, 0, false
	}
	versionID := (h[1] >> 3) & 0x03
	layerID := (h[1] >> 1) & 0x03
	bitrateIdx := (h[2] >> 4) & 0x0f
	sampleIdx := (h[2] >> 2) & 0x03
	padding := int((h[2] >> 1) & 0x01)
	if versionID == 1 || layerID == 0 || bitrateIdx == 0 || bitrateIdx == 15 || sampleIdx == 3 {
		return 0, 0, 0, false
	}
	sampleRate = mp3SampleRate(versionID, sampleIdx)
	bitrate := mp3BitrateKbps(versionID, layerID, bitrateIdx)
	if sampleRate <= 0 || bitrate <= 0 {
		return 0, 0, 0, false
	}
	samples = mp3SamplesPerFrame(versionID, layerID)
	if layerID == 3 {
		frameLen = (12*bitrate*1000/sampleRate + padding) * 4
	} else if layerID == 1 && versionID != 3 {
		frameLen = 72*bitrate*1000/sampleRate + padding
	} else {
		frameLen = 144*bitrate*1000/sampleRate + padding
	}
	return frameLen, samples, sampleRate, true
}

func mp3SampleRate(versionID byte, sampleIdx byte) int {
	base := []int{44100, 48000, 32000}[sampleIdx]
	switch versionID {
	case 3:
		return base
	case 2:
		return base / 2
	case 0:
		return base / 4
	default:
		return 0
	}
}

func mp3BitrateKbps(versionID, layerID, idx byte) int {
	tables := map[byte]map[byte][]int{
		3: {
			3: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
			2: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
			1: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
		},
		2: {
			3: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
			2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
			1: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		},
		0: {
			3: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
			2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
			1: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		},
	}
	return tables[versionID][layerID][idx]
}

func mp3SamplesPerFrame(versionID, layerID byte) int {
	switch layerID {
	case 3:
		return 384
	case 2:
		return 1152
	case 1:
		if versionID == 3 {
			return 1152
		}
		return 576
	default:
		return 0
	}
}

func parseMP4DurationSeconds(data []byte) (int, bool) {
	duration, ok := scanMP4Duration(data, 0)
	if !ok {
		return 0, false
	}
	return ceilPositiveSeconds(duration), true
}

func scanMP4Duration(data []byte, depth int) (float64, bool) {
	if depth > 8 {
		return 0, false
	}
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		boxType := string(data[offset+4 : offset+8])
		header := 8
		switch size {
		case 1:
			if offset+16 > len(data) {
				return 0, false
			}
			size = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			header = 16
		case 0:
			size = uint64(len(data) - offset)
		}
		if size < uint64(header) || offset+int(size) > len(data) {
			return 0, false
		}
		payload := data[offset+header : offset+int(size)]
		if boxType == "mvhd" || boxType == "mdhd" {
			if duration, ok := parseMP4TimeBox(payload); ok {
				return duration, true
			}
		}
		if isMP4ContainerBox(boxType) {
			if duration, ok := scanMP4Duration(payload, depth+1); ok {
				return duration, true
			}
		}
		offset += int(size)
	}
	return 0, false
}

func parseMP4TimeBox(payload []byte) (float64, bool) {
	if len(payload) < 20 {
		return 0, false
	}
	version := payload[0]
	if version == 1 {
		if len(payload) < 32 {
			return 0, false
		}
		timescale := binary.BigEndian.Uint32(payload[20:24])
		duration := binary.BigEndian.Uint64(payload[24:32])
		if timescale == 0 || duration == 0 {
			return 0, false
		}
		return float64(duration) / float64(timescale), true
	}
	timescale := binary.BigEndian.Uint32(payload[12:16])
	duration := binary.BigEndian.Uint32(payload[16:20])
	if timescale == 0 || duration == 0 {
		return 0, false
	}
	return float64(duration) / float64(timescale), true
}

func isMP4ContainerBox(boxType string) bool {
	switch boxType {
	case "moov", "trak", "mdia", "minf", "stbl", "edts":
		return true
	default:
		return false
	}
}

type ebmlParsedElement struct {
	id           uint64
	payloadStart int
	payloadEnd   int
	next         int
}

type webmDurationState struct {
	timecodeScale uint64
	duration      float64
	hasDuration   bool
	audioTracks   map[uint64]struct{}
	blockMaxTicks map[uint64]int64
}

type webmTrackEntry struct {
	number   uint64
	typ      uint64
	codecID  string
	hasAudio bool
}

func parseWebMDurationSeconds(data []byte) (int, bool) {
	first, ok := readEBMLElement(data, 0, len(data))
	if !ok || first.id != ebmlIDHeader {
		return 0, false
	}

	state := &webmDurationState{
		timecodeScale: webmDefaultTimecodeScale,
		audioTracks:   map[uint64]struct{}{},
		blockMaxTicks: map[uint64]int64{},
	}
	for offset := first.next; offset < len(data); {
		el, ok := readEBMLElement(data, offset, len(data))
		if !ok {
			return 0, false
		}
		if el.id == ebmlIDSegment {
			if !scanWebMSegment(data[el.payloadStart:el.payloadEnd], state) {
				return 0, false
			}
			break
		}
		offset = el.next
	}
	if len(state.audioTracks) == 0 {
		return 0, false
	}
	if state.hasDuration {
		return webMTicksToBillableSeconds(state.duration, state.timecodeScale)
	}

	var maxTicks int64
	for track := range state.audioTracks {
		if ticks := state.blockMaxTicks[track]; ticks > maxTicks {
			maxTicks = ticks
		}
	}
	if maxTicks <= 0 {
		return 0, false
	}
	return webMTicksToBillableSeconds(float64(maxTicks), state.timecodeScale)
}

func scanWebMSegment(segment []byte, state *webmDurationState) bool {
	for offset := 0; offset < len(segment); {
		el, ok := readEBMLElement(segment, offset, len(segment))
		if !ok {
			return false
		}
		payload := segment[el.payloadStart:el.payloadEnd]
		switch el.id {
		case ebmlIDInfo:
			if !scanWebMInfo(payload, state) {
				return false
			}
		case ebmlIDTracks:
			if !scanWebMTracks(payload, state) {
				return false
			}
		case ebmlIDCluster:
			if !scanWebMCluster(payload, state) {
				return false
			}
		}
		offset = el.next
	}
	return true
}

func scanWebMInfo(info []byte, state *webmDurationState) bool {
	for offset := 0; offset < len(info); {
		el, ok := readEBMLElement(info, offset, len(info))
		if !ok {
			return false
		}
		payload := info[el.payloadStart:el.payloadEnd]
		switch el.id {
		case ebmlIDTimecodeScale:
			scale, ok := parseEBMLUInt(payload)
			if !ok || scale == 0 {
				return false
			}
			state.timecodeScale = scale
		case ebmlIDDuration:
			duration, ok := parseEBMLFloat(payload)
			if ok && duration > 0 {
				state.duration = duration
				state.hasDuration = true
			}
		}
		offset = el.next
	}
	return true
}

func scanWebMTracks(tracks []byte, state *webmDurationState) bool {
	for offset := 0; offset < len(tracks); {
		el, ok := readEBMLElement(tracks, offset, len(tracks))
		if !ok {
			return false
		}
		if el.id == ebmlIDTrackEntry {
			entry, ok := parseWebMTrackEntry(tracks[el.payloadStart:el.payloadEnd])
			if !ok {
				return false
			}
			if entry.number > 0 && entry.isAudio() {
				state.audioTracks[entry.number] = struct{}{}
			}
		}
		offset = el.next
	}
	return true
}

func parseWebMTrackEntry(data []byte) (webmTrackEntry, bool) {
	var entry webmTrackEntry
	for offset := 0; offset < len(data); {
		el, ok := readEBMLElement(data, offset, len(data))
		if !ok {
			return entry, false
		}
		payload := data[el.payloadStart:el.payloadEnd]
		switch el.id {
		case ebmlIDTrackNumber:
			value, ok := parseEBMLUInt(payload)
			if !ok {
				return entry, false
			}
			entry.number = value
		case ebmlIDTrackType:
			value, ok := parseEBMLUInt(payload)
			if !ok {
				return entry, false
			}
			entry.typ = value
		case ebmlIDCodecID:
			entry.codecID = strings.TrimSpace(string(payload))
		case ebmlIDAudio:
			entry.hasAudio = true
		}
		offset = el.next
	}
	return entry, true
}

func (e webmTrackEntry) isAudio() bool {
	return e.typ == 2 || e.hasAudio || strings.HasPrefix(strings.ToUpper(e.codecID), "A_")
}

func scanWebMCluster(cluster []byte, state *webmDurationState) bool {
	var clusterTimecode uint64
	for offset := 0; offset < len(cluster); {
		el, ok := readEBMLElement(cluster, offset, len(cluster))
		if !ok {
			return false
		}
		if el.id == ebmlIDClusterTime {
			value, ok := parseEBMLUInt(cluster[el.payloadStart:el.payloadEnd])
			if !ok {
				return false
			}
			clusterTimecode = value
			break
		}
		offset = el.next
	}

	for offset := 0; offset < len(cluster); {
		el, ok := readEBMLElement(cluster, offset, len(cluster))
		if !ok {
			return false
		}
		payload := cluster[el.payloadStart:el.payloadEnd]
		switch el.id {
		case ebmlIDSimpleBlock:
			recordWebMBlockTimestamp(payload, clusterTimecode, state)
		case ebmlIDBlockGroup:
			if !scanWebMBlockGroup(payload, clusterTimecode, state) {
				return false
			}
		}
		offset = el.next
	}
	return true
}

func scanWebMBlockGroup(group []byte, clusterTimecode uint64, state *webmDurationState) bool {
	for offset := 0; offset < len(group); {
		el, ok := readEBMLElement(group, offset, len(group))
		if !ok {
			return false
		}
		if el.id == ebmlIDBlock {
			recordWebMBlockTimestamp(group[el.payloadStart:el.payloadEnd], clusterTimecode, state)
		}
		offset = el.next
	}
	return true
}

func recordWebMBlockTimestamp(block []byte, clusterTimecode uint64, state *webmDurationState) {
	track, relative, ok := parseWebMBlockHeader(block)
	if !ok {
		return
	}
	ticks := int64(clusterTimecode) + int64(relative)
	if ticks <= 0 {
		return
	}
	if ticks > state.blockMaxTicks[track] {
		state.blockMaxTicks[track] = ticks
	}
}

func parseWebMBlockHeader(block []byte) (track uint64, relativeTimecode int16, ok bool) {
	track, trackLen, ok := readEBMLVIntValue(block, 0, len(block))
	if !ok || track == 0 || trackLen+3 > len(block) {
		return 0, 0, false
	}
	return track, int16(binary.BigEndian.Uint16(block[trackLen : trackLen+2])), true
}

func webMTicksToBillableSeconds(ticks float64, scale uint64) (int, bool) {
	if scale == 0 {
		return 0, false
	}
	seconds := ceilPositiveSeconds(ticks * float64(scale) / 1_000_000_000)
	if seconds <= 0 {
		return 0, false
	}
	return seconds, true
}

func readEBMLElement(data []byte, offset int, limit int) (ebmlParsedElement, bool) {
	var out ebmlParsedElement
	if offset < 0 || offset >= limit || limit > len(data) {
		return out, false
	}
	id, idLen, ok := readEBMLID(data, offset, limit)
	if !ok {
		return out, false
	}
	size, sizeLen, unknown, ok := readEBMLSize(data, offset+idLen, limit)
	if !ok {
		return out, false
	}
	payloadStart := offset + idLen + sizeLen
	payloadEnd := limit
	if !unknown {
		if size > uint64(limit-payloadStart) {
			return out, false
		}
		payloadEnd = payloadStart + int(size)
	}
	return ebmlParsedElement{
		id:           id,
		payloadStart: payloadStart,
		payloadEnd:   payloadEnd,
		next:         payloadEnd,
	}, true
}

func readEBMLID(data []byte, offset int, limit int) (uint64, int, bool) {
	if offset >= limit {
		return 0, 0, false
	}
	first := data[offset]
	if first == 0 {
		return 0, 0, false
	}
	length := ebmlVIntLength(first)
	if length == 0 || length > 4 || offset+length > limit {
		return 0, 0, false
	}
	value := uint64(0)
	for i := 0; i < length; i++ {
		value = (value << 8) | uint64(data[offset+i])
	}
	return value, length, true
}

func readEBMLSize(data []byte, offset int, limit int) (size uint64, length int, unknown bool, ok bool) {
	value, length, ok := readEBMLVIntValue(data, offset, limit)
	if !ok {
		return 0, 0, false, false
	}
	maxValue := uint64(1)<<(uint(length)*7) - 1
	return value, length, value == maxValue, true
}

func readEBMLVIntValue(data []byte, offset int, limit int) (uint64, int, bool) {
	if offset >= limit {
		return 0, 0, false
	}
	first := data[offset]
	if first == 0 {
		return 0, 0, false
	}
	length := ebmlVIntLength(first)
	if length == 0 || offset+length > limit {
		return 0, 0, false
	}
	mask := byte(0xFF >> length)
	value := uint64(first & mask)
	for i := 1; i < length; i++ {
		value = (value << 8) | uint64(data[offset+i])
	}
	return value, length, true
}

func ebmlVIntLength(first byte) int {
	for length := 1; length <= 8; length++ {
		if first&(0x80>>uint(length-1)) != 0 {
			return length
		}
	}
	return 0
}

func parseEBMLUInt(data []byte) (uint64, bool) {
	if len(data) == 0 || len(data) > 8 {
		return 0, false
	}
	var value uint64
	for _, b := range data {
		value = (value << 8) | uint64(b)
	}
	return value, true
}

func parseEBMLFloat(data []byte) (float64, bool) {
	switch len(data) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(data))), true
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(data)), true
	default:
		return 0, false
	}
}
