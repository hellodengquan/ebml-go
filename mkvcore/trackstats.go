// Copyright 2026 The ebml-go authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mkvcore

import (
	"bytes"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/at-wat/ebml-go"
)

// simpleTag represents a Matroska SimpleTag element.
type simpleTag struct {
	TagName     string `ebml:"TagName"`
	TagString   string `ebml:"TagString"`
	TagLanguage string `ebml:"TagLanguage,omitempty"`
	TagDefault  uint64 `ebml:"TagDefault,omitempty"`
}

// tagTargets represents a Matroska Targets element scoped to a single track.
type tagTargets struct {
	TargetTypeValue uint64   `ebml:"TargetTypeValue"`
	TargetType      string   `ebml:"TargetType"`
	TagTrackUID     []uint64 `ebml:"TagTrackUID"`
}

// trackStatTag represents a Matroska Tag element carrying per-track statistics.
type trackStatTag struct {
	Targets   tagTargets  `ebml:"Targets"`
	SimpleTag []simpleTag `ebml:"SimpleTag"`
}

// tagsElement represents the top-level Matroska Tags element.
type tagsElement struct {
	Tag []trackStatTag `ebml:"Tag"`
}

// Track statistics tag names defined by the Matroska specification.
const (
	tagNameNumberOfFrames = "NUMBER_OF_FRAMES"
	tagNameNumberOfBytes  = "NUMBER_OF_BYTES"
	tagNameDuration       = "DURATION"
)

// trackStats accumulates the actually recorded statistics of a single track.
type trackStats struct {
	trackUID  uint64
	frameCnt  uint64
	payload   uint64
	firstTime int64
	lastTime  int64
	hasFrame  bool
}

// add records one frame that has actually been written into the stream.
// timestamp is in the timecode scale of the Segment (milliseconds by default).
func (s *trackStats) add(timestamp int64, payloadBytes int) {
	if !s.hasFrame {
		s.firstTime = timestamp
		s.hasFrame = true
	}
	s.lastTime = timestamp
	s.frameCnt++
	s.payload += uint64(payloadBytes)
}

// durationNanos returns the recorded track duration in nanoseconds.
// The timecode delta is multiplied by the Segment TimecodeScale (nanoseconds
// per timecode unit), so the result stays an exact integer nanosecond value.
func (s *trackStats) durationNanos(timecodeScale uint64) int64 {
	if !s.hasFrame {
		return 0
	}
	return (s.lastTime - s.firstTime) * int64(timecodeScale)
}

// formatDurationNanos renders a nanosecond duration in the Matroska
// DURATION tag format: HH:MM:SS.nnnnnnnnn (9 fractional digits, nanosecond).
func formatDurationNanos(nanos int64) string {
	neg := nanos < 0
	if neg {
		nanos = -nanos
	}
	sec := nanos / 1e9
	frac := nanos % 1e9
	hours := sec / 3600
	sec %= 3600
	minutes := sec / 60
	sec %= 60
	const digits = "0123456789"
	var b [30]byte
	i := len(b)
	for d := 0; d < 9; d++ {
		i--
		b[i] = digits[frac%10]
		frac /= 10
	}
	i--
	b[i] = '.'
	for d := 0; d < 2; d++ {
		i--
		b[i] = digits[sec%10]
		sec /= 10
	}
	i--
	b[i] = ':'
	for d := 0; d < 2; d++ {
		i--
		b[i] = digits[minutes%10]
		minutes /= 10
	}
	i--
	b[i] = ':'
	switch {
	case hours == 0:
		i--
		b[i] = '0'
		i--
		b[i] = '0'
	case hours < 100:
		// Pad hours to two digits, matching the HH:MM:SS shape used by the
		// Matroska DURATION tag examples.
		i--
		b[i] = digits[hours%10]
		i--
		b[i] = digits[hours/10]
	default:
		for hours > 0 {
			i--
			b[i] = digits[hours%10]
			hours /= 10
		}
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// marshalTrackStats builds the top-level Tags element from the accumulated
// per-track statistics.
func marshalTrackStats(stats map[uint64]*trackStats, orderedUIDs []uint64, timecodeScale uint64) tagsElement {
	tags := tagsElement{}
	for _, uid := range orderedUIDs {
		s := stats[uid]
		duration := "00:00:00.000000000"
		if s != nil {
			duration = formatDurationNanos(s.durationNanos(timecodeScale))
		}
		var frames, bytes uint64
		if s != nil {
			frames = s.frameCnt
			bytes = s.payload
		}
		tags.Tag = append(tags.Tag, trackStatTag{
			Targets: tagTargets{
				TargetTypeValue: 6, // track level
				TargetType:      "track",
				TagTrackUID:     []uint64{uid},
			},
			SimpleTag: []simpleTag{
				{TagName: tagNameNumberOfFrames, TagString: strconv.FormatUint(frames, 10)},
				{TagName: tagNameNumberOfBytes, TagString: strconv.FormatUint(bytes, 10)},
				{TagName: tagNameDuration, TagString: duration},
			},
		})
	}
	return tags
}

// extractTrackUID reads the TrackUID field (ebml:"TrackUID") of a TrackEntry
// struct via reflection. It works with both mkvcore.TrackEntry and the
// TrackEntry types of the upper-level webm/mkv packages.
// The second return value reports whether the field exists.
func extractTrackUID(trackEntry interface{}) (uint64, bool) {
	v := reflect.ValueOf(trackEntry)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0, false
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("ebml")
		name := tag
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			name = tag[:comma]
		}
		if name == "" {
			name = t.Field(i).Name
		}
		if name == "TrackUID" {
			fv := v.Field(i)
			if fv.Kind() >= reflect.Uint8 && fv.Kind() <= reflect.Uint64 {
				return fv.Uint(), true
			}
			return 0, false
		}
	}
	return 0, false
}

// extractTimecodeScale reads the TimecodeScale field (ebml:"TimecodeScale")
// of a Segment.Info struct via reflection. It returns (0, false) when the
// field does not exist, letting the caller apply a default value.
func extractTimecodeScale(segmentInfo interface{}) (uint64, bool) {
	v := reflect.ValueOf(segmentInfo)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0, false
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("ebml")
		name := tag
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			name = tag[:comma]
		}
		if name == "" {
			name = t.Field(i).Name
		}
		if name == "TimecodeScale" {
			fv := v.Field(i)
			if fv.Kind() >= reflect.Uint8 && fv.Kind() <= reflect.Uint64 {
				return fv.Uint(), true
			}
			return 0, false
		}
	}
	return 0, false
}

// writeTrackStats marshals the top-level Tags element, appends it after the
// last Cluster, and patches the Tags SeekPosition inside the header SeekHead.
//
// w is the streaming output writer (its Total() gives the current absolute
// file position); writerAt is used only for the in-place SeekHead patch.
// segmentDataStart is the absolute position of the Segment contents (the
// SeekHead origin), tagsSeekPositionPos is the absolute file offset of the
// SeekPosition value reserved for Tags in the SeekHead and
// tagsSeekPositionLen is that value's byte width.
func writeTrackStats(
	writerAt io.WriterAt,
	w *writerWithSizeCount,
	stats map[uint64]*trackStats,
	orderedUIDs []uint64,
	timecodeScale uint64,
	segmentDataStart, tagsSeekPositionPos, tagsSeekPositionLen uint64,
	marshalOpts []ebml.MarshalOption,
	onFatal func(error),
) {
	tags := marshalTrackStats(stats, orderedUIDs, timecodeScale)

	var buf bytes.Buffer
	if err := ebml.Marshal(&struct {
		Tags tagsElement `ebml:"Tags"`
	}{Tags: tags}, &buf, marshalOpts...); err != nil {
		if onFatal != nil {
			onFatal(err)
		}
		return
	}

	// Absolute position of the Tags element: right after all Clusters.
	tagsAbsPos := uint64(w.Total())
	if _, err := w.Write(buf.Bytes()); err != nil {
		if onFatal != nil {
			onFatal(err)
		}
		return
	}

	// A zero patch offset means the SeekPosition slot was never located;
	// writing there would corrupt the beginning of the file.
	if tagsSeekPositionPos == 0 || tagsSeekPositionLen == 0 {
		if onFatal != nil {
			onFatal(ErrTrackStatsSeekPositionNotFound)
		}
		return
	}

	// SeekPosition is relative to the Segment data start (the SeekHead).
	seekPosition := tagsAbsPos - segmentDataStart
	if tagsSeekPositionLen < 8 && seekPosition >= (uint64(1)<<(8*tagsSeekPositionLen)) {
		// The reserved SeekPosition field cannot represent the offset
		// (e.g. a narrow field on a very large file).
		if onFatal != nil {
			onFatal(ErrTrackStatsSeekPositionOverflow)
		}
		return
	}
	posBuf := make([]byte, tagsSeekPositionLen)
	putBigEndianAtWidth(posBuf, seekPosition)
	if _, err := writerAt.WriteAt(posBuf, int64(tagsSeekPositionPos)); err != nil {
		if onFatal != nil {
			onFatal(err)
		}
	}
}

// putBigEndianAtWidth writes v into b using exactly len(b) bytes, big-endian.
func putBigEndianAtWidth(b []byte, v uint64) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}
