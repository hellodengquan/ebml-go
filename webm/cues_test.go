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

package webm

import (
	"bytes"
	"io"
	"testing"

	"github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/mkvcore"
)

// memFile is an in-memory io.WriteCloser and io.WriteSeeker.
type memFile struct {
	buf []byte
	pos int
}

func (f *memFile) Write(p []byte) (int, error) {
	end := f.pos + len(p)
	if end > len(f.buf) {
		f.buf = append(f.buf, make([]byte, end-len(f.buf))...)
	}
	copy(f.buf[f.pos:], p)
	f.pos = end
	return len(p), nil
}

func (f *memFile) Seek(offset int64, whence int) (int64, error) {
	var p int64
	switch whence {
	case io.SeekStart:
		p = offset
	case io.SeekCurrent:
		p = int64(f.pos) + offset
	case io.SeekEnd:
		p = int64(len(f.buf)) + offset
	}
	f.pos = int(p)
	return p, nil
}

func (f *memFile) Close() error { return nil }

// testTracks returns an audio track followed by a video track, exercising the
// case where the first track is not the main (video) track.
func testTracks() []TrackEntry {
	return []TrackEntry{
		{
			Name: "Audio", TrackNumber: 1, TrackUID: 1, CodecID: "A_OPUS", TrackType: 2,
			Audio: &Audio{SamplingFrequency: 48000, Channels: 2},
		},
		{
			Name: "Video", TrackNumber: 2, TrackUID: 2, CodecID: "V_VP8", TrackType: 1,
			Video: &Video{PixelWidth: 320, PixelHeight: 240},
		},
	}
}

func unmarshalMedia(t *testing.T, b []byte) (webmInfoDur, *Cues) {
	t.Helper()
	var result struct {
		Segment struct {
			Info struct {
				Duration float64 `ebml:"Duration"`
			} `ebml:"Info"`
			Cues *Cues `ebml:"Cues"`
		} `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(b), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	return webmInfoDur{result.Segment.Info.Duration}, result.Segment.Cues
}

type webmInfoDur struct{ duration float64 }

func TestBlockWriter_CuesReferenceMainKeyframes(t *testing.T) {
	f := &memFile{}
	ws, err := NewSimpleBlockWriter(f, testTracks(),
		mkvcore.WithSeekHead(true),
		mkvcore.WithCues(16384),
		mkvcore.WithMinMaxClusterDuration(2, 3000, 0x7FFF),
	)
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}
	for i := 0; i < 30; i++ {
		tc := int64(i) * 100
		ws[0].Write(true, tc, []byte{0x0a})
		ws[1].Write(i%10 == 0, tc, []byte{0x02})
	}
	ws[0].Close()
	ws[1].Close()

	_, cues := unmarshalMedia(t, f.buf)
	if cues == nil {
		t.Fatal("Cues not found")
	}
	if len(cues.CuePoint) != 3 {
		t.Errorf("expected 3 cue points at video keyframes, got %d", len(cues.CuePoint))
	}
	for i, cp := range cues.CuePoint {
		if len(cp.CueTrackPositions) != 1 {
			t.Fatalf("CuePoint[%d] has %d track positions, want 1", i, len(cp.CueTrackPositions))
		}
		if got := cp.CueTrackPositions[0].CueTrack; got != 2 {
			t.Errorf("CuePoint[%d] references track %d, want the video track 2", i, got)
		}
	}
}

func TestBlockWriter_DefaultSegmentInfoReusable(t *testing.T) {
	if DefaultSegmentInfo.Duration != 0 {
		t.Fatalf("DefaultSegmentInfo.Duration = %v before the test, want 0", DefaultSegmentInfo.Duration)
	}

	// First recording with Cues, using the package default segment info.
	f1 := &memFile{}
	ws, err := NewSimpleBlockWriter(f1, testTracks(),
		mkvcore.WithSeekHead(true), mkvcore.WithCues(4096))
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		ws[0].Write(true, int64(i)*100, []byte{0x0a})
		ws[1].Write(true, int64(i)*100, []byte{0x02})
	}
	ws[0].Close()
	ws[1].Close()

	info, _ := unmarshalMedia(t, f1.buf)
	if info.duration != 400 {
		t.Errorf("first recording Duration = %v, want 400", info.duration)
	}
	if DefaultSegmentInfo.Duration != 0 {
		t.Errorf("DefaultSegmentInfo.Duration leaked as %v", DefaultSegmentInfo.Duration)
	}

	// Second recording in the same process using the same default config.
	f2 := &memFile{}
	ws2, err := NewSimpleBlockWriter(f2, testTracks())
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		ws2[0].Write(true, int64(i)*100, []byte{0x0a})
		ws2[1].Write(true, int64(i)*100, []byte{0x02})
	}
	ws2[0].Close()
	ws2[1].Close()

	info2, _ := unmarshalMedia(t, f2.buf)
	if info2.duration != 400 {
		t.Errorf("second recording Duration = %v, want 400 (near-zero means config leaked)", info2.duration)
	}
}

func TestBlockWriter_AudioOnlyCues(t *testing.T) {
	f := &memFile{}
	tracks := []TrackEntry{{
		Name: "Audio", TrackNumber: 1, TrackUID: 1, CodecID: "A_OPUS", TrackType: 2,
		Audio: &Audio{SamplingFrequency: 48000, Channels: 2},
	}}
	ws, err := NewSimpleBlockWriter(f, tracks,
		mkvcore.WithSeekHead(true), mkvcore.WithCues(16384))
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}
	for i := 0; i < 1500; i++ {
		ws[0].Write(false, int64(i)*20, []byte{0x0a})
	}
	ws[0].Close()

	info, cues := unmarshalMedia(t, f.buf)
	if cues == nil {
		t.Fatal("audio-only recording must contain Cues")
	}
	if len(cues.CuePoint) < 2 {
		t.Fatalf("expected multiple cue points, got %d", len(cues.CuePoint))
	}
	for i, cp := range cues.CuePoint {
		if got := cp.CueTrackPositions[0].CueTrack; got != 1 {
			t.Errorf("CuePoint[%d] references track %d, want 1", i, got)
		}
	}
	if info.duration != 29980 {
		t.Errorf("Duration = %v, want 29980", info.duration)
	}
}
