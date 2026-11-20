// Copyright 2019 The ebml-go authors.
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

package webm

import (
	"github.com/at-wat/ebml-go/mkvcore"
)

const (
	// SubtitleTrackType is the Matroska TrackType value of subtitle tracks.
	SubtitleTrackType uint64 = 0x11
	// WebVTTCodecID is the Matroska CodecID of WebVTT subtitle tracks.
	WebVTTCodecID = "S_TEXT/WEBVTT"
)

// WebVTTTrackEntry returns a TrackEntry for a WebVTT subtitle track.
// Subtitle events are sparse and each of them has its own display duration,
// so write them with WriteWebVTTEvent, which stores them as BlockGroups with
// BlockDuration.
func WebVTTTrackEntry(trackNumber, trackUID uint64, name string) TrackEntry {
	return TrackEntry{
		Name:        name,
		TrackNumber: trackNumber,
		TrackUID:    trackUID,
		CodecID:     WebVTTCodecID,
		TrackType:   SubtitleTrackType,
	}
}

// WebVTTEvent is a WebVTT subtitle event stored in a WebM subtitle track.
// Start and Duration are in millisecond.
type WebVTTEvent struct {
	// Start is the display start time of the event.
	Start int64
	// Duration is the display duration of the event.
	Duration int64
	// Text is the payload of the event, i.e. the cue text.
	Text string
	// Keyframe reports whether the event can be decoded independently, i.e.
	// it doesn't reference another block. It is filled by ReadWebVTTEvent.
	Keyframe bool
}

// End returns the display end time of the event, i.e. Start + Duration.
func (e WebVTTEvent) End() int64 {
	return e.Start + e.Duration
}

// WriteWebVTTEvent writes the event to the given track writer as a BlockGroup
// with BlockDuration. The event keeps its start time and display duration when
// it is multiplexed with audio and video tracks, even if the start time is
// already behind the other tracks.
//
// A WebVTT event is a standalone cue and doesn't depend on other blocks, so it
// is always written as an independently decodable block. Use
// mkvcore.BlockGroupWriter.WriteBlockGroup directly to write a dependent
// block.
func WriteWebVTTEvent(w mkvcore.BlockGroupWriter, e WebVTTEvent) (int, error) {
	return w.WriteBlockGroup(true, e.Start, e.Duration, []byte(e.Text))
}

// ReadWebVTTEvent reads the next WebVTT event from the given track reader.
// It returns the event text, its display start time and duration, and whether
// the event can be decoded independently.
// io.EOF is returned after the last event.
func ReadWebVTTEvent(r mkvcore.BlockGroupReader) (WebVTTEvent, error) {
	b, keyframe, timestamp, duration, err := r.ReadBlockGroup()
	if err != nil {
		return WebVTTEvent{}, err
	}
	return WebVTTEvent{
		Start:    timestamp,
		Duration: duration,
		Text:     string(b),
		Keyframe: keyframe,
	}, nil
}
