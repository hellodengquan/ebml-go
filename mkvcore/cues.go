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
)

// Matroska TrackType values. See https://www.matroska.org/technical/elements.html
const (
	trackTypeVideo = 1
	trackTypeAudio = 2
)

// trackTypeOf returns the TrackType of the given TrackEntry value.
// TrackEntry is stored as an interface{} so reflection is used to support
// package-specific structs (e.g. webm.TrackEntry and mkvcore.TrackEntry).
// It returns 0 if the type cannot be determined.
func trackTypeOf(trackEntry interface{}) uint64 {
	if trackEntry == nil {
		return 0
	}
	v := reflect.ValueOf(trackEntry)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return 0
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0
	}
	f := v.FieldByName("TrackType")
	if !f.IsValid() {
		return 0
	}
	switch f.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return f.Uint()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(f.Int())
	}
	return 0
}

// selectCueTrack selects the track referenced by the CuePoints.
//
// When a video track is present, the CuePoints reference video keyframes so
// that every retainable seek point starts decoding at a decodable frame. The
// configured main track is preferred if it is a video track; otherwise the
// first video track is used. Audio-only recordings reference the configured or
// first track, since audio frames are independently decodable.
//
// configured is the main track number given by WithMaxKeyframeInterval or
// WithMinMaxClusterDuration.
func selectCueTrack(tracks []TrackDescription, configured uint64) (trackNumber uint64, requireKeyframe bool) {
	if configured != 0 {
		for _, t := range tracks {
			if t.TrackNumber == configured && trackTypeOf(t.TrackEntry) != trackTypeAudio {
				return configured, true
			}
		}
	}
	for _, t := range tracks {
		if trackTypeOf(t.TrackEntry) == trackTypeVideo {
			return t.TrackNumber, true
		}
	}
	for _, t := range tracks {
		if trackTypeOf(t.TrackEntry) == trackTypeAudio {
			return t.TrackNumber, false
		}
	}
	if len(tracks) > 0 {
		// Unknown track type: assume it needs keyframes to be decodable.
		return tracks[0].TrackNumber, true
	}
	return 0, true
}

// defaultAudioClusterDuration returns the Cluster duration used for audio-only
// recordings with Cues. Cues are only useful if the file is split into
// several Clusters; audio has no keyframes, so a periodic split is used when
// the caller did not configure a main track.
//
// The target is about five seconds, converted to timecode units using the
// TimecodeScale of the segment info (nanoseconds per unit). It is clamped to
// the range allowed for a Cluster duration.
func defaultAudioClusterDuration(segmentInfo interface{}) int64 {
	const target = 5 * 1e9 // 5 seconds in nanoseconds
	const fallback = 1000  // 1s if the scale is unknown (1ms timecode unit)
	const maxDuration = 0x7FFF

	scale := timecodeScaleOf(segmentInfo)
	if scale == 0 {
		return fallback
	}
	d := int64(target / scale)
	if d < 1 {
		d = 1
	}
	if d > maxDuration {
		d = maxDuration
	}
	return d
}

// timecodeScaleOf returns the TimecodeScale field of the segment info.
func timecodeScaleOf(segmentInfo interface{}) uint64 {
	if segmentInfo == nil {
		return 0
	}
	v := reflect.ValueOf(segmentInfo)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return 0
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0
	}
	f := v.FieldByName("TimecodeScale")
	if !f.IsValid() {
		return 0
	}
	switch f.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return f.Uint()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(f.Int())
	}
	return 0
}

// cloneSegmentInfo returns a shallow copy of the update target so that
// setting the placeholder Duration doesn't modify the caller-owned
// configuration. This allows the same config to be reused for another file.
func cloneSegmentInfo(info interface{}) interface{} {
	if info == nil {
		return nil
	}
	v := reflect.ValueOf(info)
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return info
		}
		c := reflect.New(v.Elem().Type())
		c.Elem().Set(v.Elem())
		return c.Interface()
	case reflect.Map:
		if v.IsNil() {
			return info
		}
		c := reflect.MakeMapWithSize(v.Type(), v.Len())
		for _, k := range v.MapKeys() {
			c.SetMapIndex(k, v.MapIndex(k))
		}
		return c.Interface()
	default:
		return info
	}
}
