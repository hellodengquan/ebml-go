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
	"reflect"

	"github.com/at-wat/ebml-go"
)

const (
	// maxLacedFrames is the maximum number of frames that can be stored in one block.
	// Matroska stores the number of frames minus one in a single byte.
	maxLacedFrames = 255

	// defaultTimecodeScale is the Segment.Info.TimecodeScale in nanoseconds
	// used when it is not available from the caller provided segment info.
	defaultTimecodeScale = 1000000 // 1ms
)

// pendingLace accumulates frames of a track that will be written as one laced block.
type pendingLace struct {
	trackNumber uint64
	// timecode is the timecode of the first frame relative to the cluster timecode.
	timecode int16
	keyframe bool
	lastTs   int64
	frames   [][]byte
}

// selectLacingMode returns the lacing mode producing the smallest block header
// for the given frames.
func selectLacingMode(frames [][]byte) ebml.LacingMode {
	switch len(frames) {
	case 0, 1:
		return ebml.LacingNo
	}

	sameSize := true
	for _, f := range frames[1:] {
		if len(f) != len(frames[0]) {
			sameSize = false
			break
		}
	}
	if sameSize {
		// Fixed lacing only stores the number of frames.
		return ebml.LacingFixed
	}

	// Xiph lacing stores the size of each frame except the last one
	// as 255-byte carries followed by a terminating byte.
	xiphSize := 1
	for _, f := range frames[:len(frames)-1] {
		xiphSize += len(f)/0xFF + 1
	}

	// EBML lacing stores the first frame size as an element ID and
	// the size differences of the following frames as signed VINTs.
	ebmlSize := 1 + idLength(uint64(len(frames[0])))
	prev := int64(len(frames[0]))
	for _, f := range frames[1 : len(frames)-1] {
		cur := int64(len(f))
		ebmlSize += vintLength(cur - prev)
		prev = cur
	}

	if ebmlSize < xiphSize {
		return ebml.LacingEBML
	}
	return ebml.LacingXiph
}

// idLength returns the encoded length of the element ID representation
// of v (used for the first frame size of EBML lacing).
func idLength(v uint64) int {
	switch {
	case v < 0x80:
		return 1
	case v < 0x4000:
		return 2
	case v < 0x200000:
		return 3
	case v < 0x10000000:
		return 4
	case v < 0x800000000:
		return 5
	case v < 0x40000000000:
		return 6
	case v < 0x2000000000000:
		return 7
	default:
		return 8
	}
}

// vintLength returns the encoded length of the signed VINT representation of v.
func vintLength(v int64) int {
	switch {
	case -0x3F <= v && v <= 0x3F:
		return 1
	case -0x1FFF <= v && v <= 0x1FFF:
		return 2
	case -0xFFFFF <= v && v <= 0xFFFFF:
		return 3
	case -0x7FFFFFF <= v && v <= 0x7FFFFFF:
		return 4
	case -0x3FFFFFFFF <= v && v <= 0x3FFFFFFFF:
		return 5
	case -0x1FFFFFFFFFF <= v && v <= 0x1FFFFFFFFFF:
		return 6
	case -0xFFFFFFFFFFFF <= v && v <= 0xFFFFFFFFFFFF:
		return 7
	case -0x7FFFFFFFFFFFFF <= v && v <= 0x7FFFFFFFFFFFFF:
		return 8
	default:
		// Out of the VINT range. This is never selected as the smallest mode.
		return 9
	}
}

// laceFrameDurationTicks converts the frame duration in nanoseconds to the
// timecode scale unit, rounding to the nearest tick.
func laceFrameDurationTicks(durationNs, timecodeScale uint64) int64 {
	if durationNs == 0 || timecodeScale == 0 {
		return 0
	}
	d := durationNs / timecodeScale
	if durationNs%timecodeScale >= timecodeScale/2 {
		d++
	}
	if d > 0x7FFF {
		// A frame longer than the int16 relative timecode range cannot be laced.
		return 0
	}
	return int64(d)
}

// trackDefaultDuration returns TrackEntry.DefaultDuration of the given track entry in nanoseconds.
// The track entry may be a struct (e.g. mkvcore.TrackEntry or webm.TrackEntry),
// a pointer to it, or a map decoded by the EBML unmarshaler.
func trackDefaultDuration(trackEntry interface{}) uint64 {
	return uint64Field(trackEntry, "DefaultDuration")
}

// segmentTimecodeScale returns Segment.Info.TimecodeScale of the given segment info in nanoseconds.
// The segment info may be a struct, a pointer to it, or a map decoded by the EBML unmarshaler.
func segmentTimecodeScale(segmentInfo interface{}) uint64 {
	if v := uint64Field(segmentInfo, "TimecodeScale", "TimestampScale"); v > 0 {
		return v
	}
	return defaultTimecodeScale
}

// uint64Field extracts an unsigned integer field from a struct, pointer or map.
func uint64Field(v interface{}, names ...string) uint64 {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && (rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface) {
		if rv.IsNil() {
			return 0
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return 0
	}

	for _, name := range names {
		var f reflect.Value
		switch rv.Kind() {
		case reflect.Struct:
			f = rv.FieldByName(name)
		case reflect.Map:
			if rv.Type().Key().Kind() != reflect.String {
				continue
			}
			f = rv.MapIndex(reflect.ValueOf(name).Convert(rv.Type().Key()))
		default:
			continue
		}
		if !f.IsValid() {
			continue
		}
		if f.Kind() == reflect.Interface {
			if f.IsNil() {
				continue
			}
			f = f.Elem()
		}
		if !f.CanInterface() {
			continue
		}
		switch x := f.Interface().(type) {
		case uint64:
			return x
		case uint32:
			return uint64(x)
		case uint:
			return uint64(x)
		case int64:
			if x > 0 {
				return uint64(x)
			}
		case int:
			if x > 0 {
				return uint64(x)
			}
		case float64:
			if x > 0 {
				return uint64(x)
			}
		}
	}
	return 0
}
