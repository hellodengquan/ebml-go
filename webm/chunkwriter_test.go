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
	"sync"
	"testing"

	"github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/internal/buffercloser"
	"github.com/at-wat/ebml-go/mkvcore"
)

func TestChunkWriter_NewSimpleChunkWriter(t *testing.T) {
	// The main track is inferred from the video track's TrackType and the
	// default header/info/interceptor are applied.
	buf := buffercloser.New()

	tracks := []TrackEntry{
		{
			TrackNumber: 1,
			TrackUID:    1,
			CodecID:     "A_OPUS",
			TrackType:   2,
			Audio:       &Audio{SamplingFrequency: 48000, Channels: 2},
		},
		{
			TrackNumber: 2,
			TrackUID:    2,
			CodecID:     "V_VP8",
			TrackType:   1,
			Video:       &Video{PixelWidth: 320, PixelHeight: 240},
		},
	}

	var mu sync.Mutex
	var chunks []mkvcore.Chunk
	handler := func(c mkvcore.Chunk) error {
		mu.Lock()
		defer mu.Unlock()
		chunks = append(chunks, c)
		return nil
	}

	ws, err := NewSimpleChunkWriter(buf, tracks, handler)
	if err != nil {
		t.Fatalf("Failed to create ChunkWriter: %v", err)
	}
	for _, f := range []struct {
		track    int
		keyframe bool
		tc       int64
	}{
		{1, true, 0}, // video keyframe
		{0, true, 5},
		{1, false, 10},
		{1, true, 100},
	} {
		if _, err := ws[f.track].Write(f.keyframe, f.tc, []byte{byte(f.tc)}); err != nil {
			t.Fatalf("Failed to Write: %v", err)
		}
	}
	for _, w := range ws {
		w.Close()
	}

	mu.Lock()
	defer mu.Unlock()
	if len(chunks) != 3 {
		t.Fatalf("Number of chunks: got %d, want 3", len(chunks))
	}
	if !chunks[0].Initialization {
		t.Error("The first chunk must be the initialization segment")
	}
	var init struct {
		Header EBMLHeader `ebml:"EBML"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(chunks[0].Data), &init); err != nil {
		t.Fatal(err)
	}
	if init.Header.DocType != "webm" {
		t.Errorf("The default WebM header is not applied: %+v", init.Header)
	}
	if chunks[1].StartTime != 0 || chunks[2].StartTime != 100 {
		t.Errorf("The video track is not used as the main track: %d, %d",
			chunks[1].StartTime, chunks[2].StartTime)
	}
}

func TestChunkWriter_NewChunkWriter(t *testing.T) {
	buf := buffercloser.New()

	tracks := []TrackEntry{
		{
			Name:        "Video",
			TrackNumber: 1,
			TrackUID:    12345,
			CodecID:     "V_VP8",
			TrackType:   1,
			Video: &Video{
				PixelWidth:  320,
				PixelHeight: 240,
			},
		},
		{
			Name:        "Audio",
			TrackNumber: 2,
			TrackUID:    54321,
			CodecID:     "A_OPUS",
			TrackType:   2,
			Audio: &Audio{
				SamplingFrequency: 48000.0,
				Channels:          2,
			},
		},
	}

	var mu sync.Mutex
	var chunks []mkvcore.Chunk
	handler := func(c mkvcore.Chunk) error {
		mu.Lock()
		defer mu.Unlock()
		chunks = append(chunks, c)
		return nil
	}

	ws, err := NewChunkWriter(buf, tracks, 1, 0, 0x7FFF, handler)
	if err != nil {
		t.Fatalf("Failed to create ChunkWriter: %v", err)
	}
	if len(ws) != len(tracks) {
		t.Fatalf("Number of the returned writer (%d) must be same as the number of TrackEntry (%d)",
			len(ws), len(tracks))
	}

	for _, f := range []struct {
		track    int
		keyframe bool
		tc       int64
	}{
		{0, true, 0},
		{1, true, 5},
		{0, false, 10},
		{0, true, 100},
		{1, true, 105},
	} {
		if _, err := ws[f.track].Write(f.keyframe, f.tc, []byte{byte(f.tc)}); err != nil {
			t.Fatalf("Failed to Write: %v", err)
		}
	}
	for _, w := range ws {
		w.Close()
	}

	mu.Lock()
	defer mu.Unlock()

	if len(chunks) != 3 {
		t.Fatalf("Number of chunks: got %d, want 3 (1 init + 2 media)", len(chunks))
	}
	if !chunks[0].Initialization {
		t.Error("The first chunk must be the initialization segment")
	}
	if chunks[1].Initialization || chunks[2].Initialization {
		t.Error("The media segments must not be marked as initialization")
	}
	if chunks[1].StartTime != 0 || chunks[2].StartTime != 100 {
		t.Errorf("Unexpected segment start times: %d, %d", chunks[1].StartTime, chunks[2].StartTime)
	}
	if chunks[1].EndTime != chunks[2].StartTime {
		t.Errorf("Segment time ranges are not contiguous: %d != %d",
			chunks[1].EndTime, chunks[2].StartTime)
	}

	// The initialization segment carries the EBML header, Info and Tracks.
	var init struct {
		Header  EBMLHeader `ebml:"EBML"`
		Segment struct {
			Info   Info   `ebml:"Info"`
			Tracks Tracks `ebml:"Tracks"`
		} `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(chunks[0].Data), &init); err != nil {
		t.Fatalf("Failed to unmarshal the initialization segment: %v", err)
	}
	if init.Header != *DefaultEBMLHeader {
		t.Errorf("Unexpected EBML header: %+v", init.Header)
	}
	if init.Segment.Info.TimecodeScale != DefaultSegmentInfo.TimecodeScale {
		t.Errorf("Unexpected Segment.Info: %+v", init.Segment.Info)
	}
	if len(init.Segment.Tracks.TrackEntry) != len(tracks) {
		t.Errorf("Number of tracks: got %d, want %d",
			len(init.Segment.Tracks.TrackEntry), len(tracks))
	}

	// Concatenating the chunks yields a WebM stream readable by this library,
	// and the library's block reader delivers the original frames.
	var all []byte
	for _, c := range chunks {
		all = append(all, c.Data...)
	}
	if !bytes.Equal(all, buf.Bytes()) {
		t.Error("Concatenated chunks differ from the stream written to the io.WriteCloser")
	}

	var result struct {
		Header  EBMLHeader `ebml:"EBML"`
		Segment Segment    `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(all), &result); err != nil {
		t.Fatalf("Failed to unmarshal concatenated output: %v", err)
	}
	if len(result.Segment.Cluster) != 2 {
		t.Fatalf("Number of clusters: got %d, want 2", len(result.Segment.Cluster))
	}
	for i, c := range result.Segment.Cluster {
		if len(c.SimpleBlock) == 0 {
			t.Errorf("Cluster %d has no block", i)
			continue
		}
		if c.SimpleBlock[0].TrackNumber != 1 || !c.SimpleBlock[0].Keyframe {
			t.Errorf("Cluster %d does not start with a video keyframe", i)
		}
	}
}
