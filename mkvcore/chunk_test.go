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
	"errors"
	"math/rand"
	"reflect"
	"sync"
	"testing"

	"github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/internal/buffercloser"
)

type testCluster struct {
	Timecode    uint64       `ebml:"Timecode"`
	SimpleBlock []ebml.Block `ebml:"SimpleBlock"`
}

type testSegment struct {
	Cluster []testCluster `ebml:"Cluster"`
}

type chunkCollector struct {
	mu     sync.Mutex
	chunks []Chunk
}

func (c *chunkCollector) handle(chunk Chunk) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Copy the data because the writer may reuse it.
	data := make([]byte, len(chunk.Data))
	copy(data, chunk.Data)
	chunk.Data = data
	c.chunks = append(c.chunks, chunk)
	return nil
}

func (c *chunkCollector) get() []Chunk {
	c.mu.Lock()
	defer c.mu.Unlock()
	ret := make([]Chunk, len(c.chunks))
	copy(ret, c.chunks)
	return ret
}

func (c *chunkCollector) init() Chunk {
	for _, ch := range c.get() {
		if ch.Initialization {
			return ch
		}
	}
	return Chunk{}
}

func (c *chunkCollector) media() []Chunk {
	var ret []Chunk
	for _, ch := range c.get() {
		if !ch.Initialization {
			ret = append(ret, ch)
		}
	}
	return ret
}

func TestChunkWriter_InitAndMediaSegments(t *testing.T) {
	buf := buffercloser.New()
	collector := &chunkCollector{}

	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}, {TrackNumber: 2}},
		WithEBMLHeader(&struct {
			DocType string `ebml:"EBMLDocType"`
		}{DocType: "webm"}),
		WithSegmentInfo(&struct {
			TimecodeScale uint64 `ebml:"TimecodeScale"`
		}{TimecodeScale: 1000000}),
		WithMinMaxClusterDuration(1, 0, 0x7FFF),
		WithChunkHandler(collector.handle),
	)
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}

	frames := []struct {
		track    int
		keyframe bool
		tc       int64
		b        byte
	}{
		{0, true, 0, 0x01},   // segment 1 starts
		{1, true, 5, 0x02},   // audio
		{0, false, 10, 0x03}, // video
		{1, true, 15, 0x04},  // audio
		{0, true, 20, 0x05},  // segment 2 starts
		{1, true, 25, 0x06},  // audio broadcast
		{0, false, 30, 0x07}, // video
		{0, true, 40, 0x08},  // segment 3 starts
	}
	for _, f := range frames {
		if _, err := ws[f.track].Write(f.keyframe, f.tc, []byte{f.b}); err != nil {
			t.Fatalf("Failed to Write: %v", err)
		}
	}
	for _, w := range ws {
		w.Close()
	}

	if err := checkChunks(t, buf.Bytes(), collector, frames); err != nil {
		t.Fatal(err)
	}
}

func checkChunks(t *testing.T, stream []byte, collector *chunkCollector, frames []struct {
	track    int
	keyframe bool
	tc       int64
	b        byte
}) error {
	t.Helper()

	init := collector.init()
	if len(init.Data) == 0 {
		t.Fatal("Initialization segment is not delivered")
	}

	media := collector.media()
	if got, want := len(media), 3; got != want {
		t.Fatalf("Number of media segments: got %d, want %d", got, want)
	}

	// Time ranges must be contiguous and not overlap.
	for i := 0; i < len(media); i++ {
		if media[i].StartTime != []int64{0, 20, 40}[i] {
			t.Errorf("Segment %d StartTime: got %d", i, media[i].StartTime)
		}
		if i > 0 && media[i-1].EndTime != media[i].StartTime {
			t.Errorf("Segment %d EndTime (%d) != segment %d StartTime (%d)",
				i-1, media[i-1].EndTime, i, media[i].StartTime)
		}
	}
	if media[len(media)-1].EndTime < media[len(media)-1].StartTime {
		t.Errorf("Last segment EndTime %d is before StartTime %d",
			media[len(media)-1].EndTime, media[len(media)-1].StartTime)
	}

	// Every media segment starts with a keyframe of the main track.
	for i, seg := range media {
		var cluster struct {
			Cluster []testCluster `ebml:"Cluster"`
		}
		if err := ebml.Unmarshal(bytes.NewReader(seg.Data), &cluster); err != nil {
			t.Fatalf("Failed to unmarshal media segment %d: %v", i, err)
		}
		if len(cluster.Cluster) != 1 {
			t.Fatalf("Media segment %d must contain exactly one cluster, got %d",
				i, len(cluster.Cluster))
		}
		blocks := cluster.Cluster[0].SimpleBlock
		if len(blocks) == 0 {
			t.Fatalf("Media segment %d has no block", i)
		}
		if blocks[0].TrackNumber != 1 || !blocks[0].Keyframe {
			t.Errorf("Media segment %d does not start with a main track keyframe: %+v",
				i, blocks[0])
		}
	}

	// Concatenating all chunks yields a valid WebM readable by this library.
	combined := append([]byte{}, init.Data...)
	for _, seg := range media {
		combined = append(combined, seg.Data...)
	}
	if !bytes.Equal(combined, stream) {
		t.Errorf("Concatenated chunks differ from the stream written to the io.WriteCloser")
	}

	var result struct {
		Segment testSegment `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(combined), &result); err != nil {
		t.Fatalf("Failed to unmarshal concatenated output: %v", err)
	}

	// The frames must be preserved: same order, data and absolute timestamps.
	var got []struct {
		track    int
		keyframe bool
		tc       int64
		b        byte
	}
	for _, cluster := range result.Segment.Cluster {
		if len(cluster.SimpleBlock) == 0 {
			continue
		}
		for _, blk := range cluster.SimpleBlock {
			got = append(got, struct {
				track    int
				keyframe bool
				tc       int64
				b        byte
			}{
				track:    int(blk.TrackNumber - 1),
				keyframe: blk.Keyframe,
				tc:       int64(cluster.Timecode) + int64(blk.Timecode),
				b:        blk.Data[0][0],
			})
		}
	}
	want := make([]struct {
		track    int
		keyframe bool
		tc       int64
		b        byte
	}, len(frames))
	for i, f := range frames {
		want[i] = struct {
			track    int
			keyframe bool
			tc       int64
			b        byte
		}{track: f.track, keyframe: f.keyframe, tc: f.tc, b: f.b}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Unexpected frames,\nexpected: %+v\n     got: %+v", want, got)
	}
	return nil
}

func TestChunkWriter_SameTimestampTiedToNextSegment(t *testing.T) {
	buf := buffercloser.New()
	collector := &chunkCollector{}

	// Audio track is declared first: with the same timestamp as a video
	// keyframe, the audio frame arrives first but belongs to the new segment.
	sorter, err := NewMultiTrackBlockSorter(WithMaxDelayedPackets(16))
	if err != nil {
		t.Fatal(err)
	}
	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}, {TrackNumber: 2}},
		WithEBMLHeader(&struct {
			DocType string `ebml:"EBMLDocType"`
		}{DocType: "webm"}),
		WithSegmentInfo(&struct {
			TimecodeScale uint64 `ebml:"TimecodeScale"`
		}{TimecodeScale: 1000000}),
		WithBlockInterceptor(sorter),
		WithMinMaxClusterDuration(2, 0, 0x7FFF), // Main track is the second one.
		WithChunkHandler(collector.handle),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ws[1].Write(true, 0, []byte{0x01}); err != nil { // video keyframe
		t.Fatal(err)
	}
	if _, err := ws[0].Write(true, 10, []byte{0x02}); err != nil { // audio
		t.Fatal(err)
	}
	if _, err := ws[0].Write(true, 20, []byte{0x03}); err != nil { // audio = next keyframe time
		t.Fatal(err)
	}
	if _, err := ws[1].Write(true, 20, []byte{0x04}); err != nil { // video keyframe
		t.Fatal(err)
	}
	for _, w := range ws {
		w.Close()
	}

	media := collector.media()
	if len(media) != 2 {
		t.Fatalf("Number of media segments: got %d, want 2", len(media))
	}
	if media[0].EndTime != 20 || media[1].StartTime != 20 {
		t.Fatalf("Expected a boundary at 20: got %d..%d and %d..%d",
			media[0].StartTime, media[0].EndTime, media[1].StartTime, media[1].EndTime)
	}

	var first struct {
		Cluster []testCluster `ebml:"Cluster"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(media[0].Data), &first); err != nil {
		t.Fatal(err)
	}
	// The audio frame at 20 must not be in the previous segment.
	for _, blk := range first.Cluster[0].SimpleBlock {
		if blk.TrackNumber == 1 && int64(first.Cluster[0].Timecode)+int64(blk.Timecode) == 20 {
			t.Errorf("Frame at the segment boundary is in the previous segment")
		}
	}
}

func TestChunkWriter_MainKeyframeIntervalLongerThanMaxDuration(t *testing.T) {
	buf := buffercloser.New()
	collector := &chunkCollector{}
	var errs []error
	var mu sync.Mutex

	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}},
		WithEBMLHeader(nil),
		WithSegmentInfo(nil),
		WithMinMaxClusterDuration(1, 500, 1000),
		WithChunkHandler(collector.handle),
		WithOnErrorHandler(func(err error) {
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Keyframe interval is 5000, much larger than the max duration 1000.
	for _, f := range []struct {
		keyframe bool
		tc       int64
	}{
		{true, 0},
		{false, 1000},
		{false, 2000},
		{true, 5000},
		{false, 6000},
	} {
		if _, err := ws[0].Write(f.keyframe, f.tc, []byte{byte(f.tc)}); err != nil {
			t.Fatal(err)
		}
	}
	ws[0].Close()

	media := collector.media()
	if len(media) != 2 {
		t.Fatalf("Number of media segments: got %d, want 2", len(media))
	}
	if media[0].StartTime != 0 || media[0].EndTime != 5000 || media[1].StartTime != 5000 {
		t.Errorf("Unexpected segment ranges: %+v", media)
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, err := range errs {
		if errors.Is(err, ErrChunkDurationExceeded) {
			found = true
		}
	}
	if !found {
		t.Errorf("ErrChunkDurationExceeded is not reported: %v", errs)
	}

	// Both segments must still start with a keyframe and be decodable.
	for i, seg := range media {
		var c struct {
			Cluster []testCluster `ebml:"Cluster"`
		}
		if err := ebml.Unmarshal(bytes.NewReader(seg.Data), &c); err != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
		if b := c.Cluster[0].SimpleBlock[0]; b.TrackNumber != 1 || !b.Keyframe {
			t.Errorf("segment %d does not start with a keyframe", i)
		}
	}
}

func TestChunkWriter_SimpleBlockTimecodeRangeOverflow(t *testing.T) {
	buf := buffercloser.New()
	collector := &chunkCollector{}
	var errs []error
	var mu sync.Mutex

	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}},
		WithEBMLHeader(nil),
		WithSegmentInfo(nil),
		WithMinMaxClusterDuration(1, 0x7FFF, 0x7FFF),
		WithChunkHandler(collector.handle),
		WithOnErrorHandler(func(err error) {
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// The keyframe interval exceeds the 16-bit SimpleBlock timecode range.
	for _, f := range []struct {
		keyframe bool
		tc       int64
	}{
		{true, 0},
		{false, 0x4000},
		{false, 0x8000}, // overflow
		{false, 0x9000}, // dropped
		{true, 0x10000},
		{false, 0x10010},
	} {
		if _, err := ws[0].Write(f.keyframe, f.tc, []byte{byte(f.tc)}); err != nil {
			t.Fatal(err)
		}
	}
	ws[0].Close()

	media := collector.media()
	if len(media) != 2 {
		t.Fatalf("Number of media segments: got %d, want 2", len(media))
	}
	if media[0].StartTime != 0 || media[1].StartTime != 0x10000 {
		t.Errorf("Unexpected segment starts: %d, %d", media[0].StartTime, media[1].StartTime)
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, err := range errs {
		if errors.Is(err, ErrChunkFrameOutOfRange) {
			found = true
		}
	}
	if !found {
		t.Errorf("ErrChunkFrameOutOfRange is not reported: %v", errs)
	}

	// The emitted clusters must remain valid; no relative timecode may
	// overflow int16.
	for i, seg := range media {
		var c struct {
			Cluster []testCluster `ebml:"Cluster"`
		}
		if err := ebml.Unmarshal(bytes.NewReader(seg.Data), &c); err != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
		for _, b := range c.Cluster[0].SimpleBlock {
			if b.Timecode < 0 || b.Timecode > 0x7FFF {
				t.Errorf("segment %d has an out-of-range block timecode %d", i, b.Timecode)
			}
		}
	}
}

func TestChunkWriter_FramesBeforeFirstKeyframe(t *testing.T) {
	buf := buffercloser.New()
	collector := &chunkCollector{}
	var errs []error
	var mu sync.Mutex

	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}, {TrackNumber: 2}},
		WithEBMLHeader(nil),
		WithSegmentInfo(nil),
		WithMinMaxClusterDuration(2, 0, 0x7FFF), // Track 1 is audio, track 2 is the main track.
		WithChunkHandler(collector.handle),
		WithOnErrorHandler(func(err error) {
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ws[0].Write(false, 5, []byte{0x01}); err != nil { // audio strictly before the keyframe
		t.Fatal(err)
	}
	if _, err := ws[1].Write(true, 10, []byte{0x02}); err != nil { // main track keyframe
		t.Fatal(err)
	}
	if _, err := ws[0].Write(false, 20, []byte{0x03}); err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		w.Close()
	}

	var result struct {
		Segment testSegment `ebml:"Segment,size=unknown"`
	}
	var combined []byte
	combined = append(combined, collector.init().Data...)
	for _, seg := range collector.media() {
		combined = append(combined, seg.Data...)
	}
	if err := ebml.Unmarshal(bytes.NewReader(combined), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Segment.Cluster) != 1 {
		t.Fatalf("Number of clusters: got %d, want 1", len(result.Segment.Cluster))
	}
	blocks := result.Segment.Cluster[0].SimpleBlock
	if len(blocks) != 2 {
		t.Fatalf("Number of blocks: got %d, want 2", len(blocks))
	}
	if blocks[0].Data[0][0] != 0x02 {
		t.Errorf("The segment does not start with the main track keyframe")
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, err := range errs {
		if errors.Is(err, ErrChunkFrameBeforeKeyframe) {
			found = true
		}
	}
	if !found {
		t.Errorf("ErrChunkFrameBeforeKeyframe is not reported: %v", errs)
	}
}

func TestChunkWriter_FrameBeforeKeyframeAtSameTimestampIsKept(t *testing.T) {
	// A frame at the same timestamp as the opening keyframe (e.g. an audio
	// frame delivered first when the audio track is declared before the video
	// track) must be kept in the media segment opened by the keyframe.
	buf := buffercloser.New()
	collector := &chunkCollector{}

	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}, {TrackNumber: 2}},
		WithEBMLHeader(nil),
		WithSegmentInfo(nil),
		WithMinMaxClusterDuration(2, 0, 0x7FFF),
		WithChunkHandler(collector.handle),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws[0].Write(true, 10, []byte{0x01}); err != nil { // audio at the keyframe time
		t.Fatal(err)
	}
	if _, err := ws[1].Write(true, 10, []byte{0x02}); err != nil { // main keyframe
		t.Fatal(err)
	}
	for _, w := range ws {
		w.Close()
	}

	media := collector.media()
	if len(media) != 1 {
		t.Fatalf("Number of media segments: got %d, want 1", len(media))
	}
	var c struct {
		Cluster []testCluster `ebml:"Cluster"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(media[0].Data), &c); err != nil {
		t.Fatal(err)
	}
	blocks := c.Cluster[0].SimpleBlock
	if len(blocks) != 2 {
		t.Fatalf("Number of blocks: got %d, want 2 (the tied frame must be kept)", len(blocks))
	}
	if blocks[0].TrackNumber != 2 || !blocks[0].Keyframe {
		t.Errorf("The media segment does not start with the main track keyframe: %+v", blocks[0])
	}
	if media[0].StartTime != 10 {
		t.Errorf("Unexpected segment start: %d", media[0].StartTime)
	}
}

func TestChunkWriter_ReadBackWithBlockReader(t *testing.T) {
	// Concatenated chunks must be readable through the library's block reader
	// with the same frames and timestamps.
	buf := buffercloser.New()
	collector := &chunkCollector{}

	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{
			{TrackNumber: 1, TrackEntry: &TrackEntry{TrackNumber: 1, TrackType: TrackTypeAudio}},
			{TrackNumber: 2, TrackEntry: &TrackEntry{TrackNumber: 2, TrackType: TrackTypeVideo}},
		},
		WithEBMLHeader(&struct {
			DocType string `ebml:"EBMLDocType"`
		}{DocType: "webm"}),
		WithSegmentInfo(&struct {
			TimecodeScale uint64 `ebml:"TimecodeScale"`
		}{TimecodeScale: 1000000}),
		WithMinMaxClusterDuration(1, 0, 0x7FFF),
		WithChunkHandler(collector.handle),
	)
	if err != nil {
		t.Fatal(err)
	}

	type written struct {
		track    uint64
		keyframe bool
		tc       int64
		b        byte
	}
	var writes []written
	for _, f := range []struct {
		track    int
		keyframe bool
		tc       int64
	}{
		{0, true, 0},
		{1, true, 5},
		{0, false, 10},
		{1, true, 15},
		{0, true, 20},
		{1, true, 25},
		{0, false, 30},
		{0, true, 40},
	} {
		b := byte(f.tc)
		if _, err := ws[f.track].Write(f.keyframe, f.tc, []byte{b}); err != nil {
			t.Fatal(err)
		}
		writes = append(writes, written{track: uint64(f.track + 1), keyframe: f.keyframe, tc: f.tc, b: b})
	}
	for _, w := range ws {
		w.Close()
	}

	var combined []byte
	combined = append(combined, collector.init().Data...)
	for _, seg := range collector.media() {
		combined = append(combined, seg.Data...)
	}

	rs, err := NewSimpleBlockReader(bytes.NewReader(combined))
	if err != nil {
		t.Fatal(err)
	}

	type read struct {
		track    uint64
		keyframe bool
		tc       int64
		b        byte
	}
	results := [2][]read{}
	var wg sync.WaitGroup
	wg.Add(len(rs))
	for i, r := range rs {
		go func(i int, r BlockReadCloserWithTrackEntry) {
			defer wg.Done()
			for {
				b, keyframe, tc, err := r.Read()
				if err != nil {
					return
				}
				results[i] = append(results[i], read{
					track:    r.TrackEntry().TrackNumber,
					keyframe: keyframe,
					tc:       tc,
					b:        b[0],
				})
			}
		}(i, r)
	}
	wg.Wait()

	// Group the written frames by track, preserving order.
	want := map[uint64][]read{}
	for _, w := range writes {
		want[w.track] = append(want[w.track], read{
			track: w.track, keyframe: w.keyframe, tc: w.tc, b: w.b,
		})
	}
	for i, r := range rs {
		n := r.TrackEntry().TrackNumber
		if !reflect.DeepEqual(results[i], want[n]) {
			t.Errorf("Track %d read back mismatch,\nexpected: %+v\n     got: %+v", n, want[n], results[i])
		}
	}
}

func TestChunkWriter_SegmentationInvariants(t *testing.T) {
	// Randomized frames across two tracks must always satisfy:
	//  - each media segment is exactly one Cluster starting at a main track
	//    keyframe,
	//  - block timecodes are inside the signed 16-bit range,
	//  - adjacent segment ranges are contiguous and non-overlapping,
	//  - frames are not lost or duplicated when reordering is bounded.
	rng := rand.New(rand.NewSource(1))
	collector := &chunkCollector{}

	buf := buffercloser.New()
	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}, {TrackNumber: 2}},
		WithEBMLHeader(nil),
		WithSegmentInfo(nil),
		WithMinMaxClusterDuration(2, 500, 1000), // Main track = 2 (video).
		WithChunkHandler(collector.handle),
	)
	if err != nil {
		t.Fatal(err)
	}

	type written struct {
		track    uint64
		keyframe bool
		tc       int64
	}
	var writes []written

	tc := int64(0)
	for i := 0; i < 40; i++ {
		// Video frames, with keyframes roughly every 1s.
		if i%8 == 0 {
			if _, err := ws[1].Write(true, tc, []byte{byte(i)}); err != nil {
				t.Fatal(err)
			}
			writes = append(writes, written{2, true, tc})
		} else {
			if _, err := ws[1].Write(false, tc, []byte{byte(i)}); err != nil {
				t.Fatal(err)
			}
			writes = append(writes, written{2, false, tc})
		}
		// Audio frame at the same timestamp.
		if _, err := ws[0].Write(true, tc, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
		writes = append(writes, written{1, true, tc})

		tc += 125 + rng.Int63n(10)
	}
	for _, w := range ws {
		w.Close()
	}

	var combined []byte
	combined = append(combined, collector.init().Data...)
	for _, seg := range collector.media() {
		combined = append(combined, seg.Data...)
	}

	var result struct {
		Segment testSegment `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(combined), &result); err != nil {
		t.Fatalf("Concatenated output is not readable: %v", err)
	}

	media := collector.media()
	if len(result.Segment.Cluster) != len(media) {
		t.Fatalf("Number of clusters in the stream (%d) differs from the delivered media segments (%d)",
			len(result.Segment.Cluster), len(media))
	}

	var totalBlocks int
	for i, cluster := range result.Segment.Cluster {
		if len(cluster.SimpleBlock) == 0 {
			t.Fatalf("Cluster %d is empty", i)
		}
		first := cluster.SimpleBlock[0]
		if first.TrackNumber != 2 || !first.Keyframe {
			t.Errorf("Cluster %d does not start with a main track keyframe: %+v", i, first)
		}
		for _, b := range cluster.SimpleBlock {
			if b.Timecode < 0 || b.Timecode > 0x7FFF {
				t.Errorf("Cluster %d has an out-of-range block timecode %d", i, b.Timecode)
			}
		}
		totalBlocks += len(cluster.SimpleBlock)
	}

	// Audio and video frames of the same instant belong to the same segment,
	// and no frame is lost.
	if totalBlocks != len(writes) {
		t.Errorf("Number of blocks: got %d, want %d", totalBlocks, len(writes))
	}

	for i := 1; i < len(media); i++ {
		if media[i-1].EndTime != media[i].StartTime {
			t.Errorf("Segment %d EndTime (%d) != segment %d StartTime (%d)",
				i-1, media[i-1].EndTime, i, media[i].StartTime)
		}
		if media[i].StartTime <= media[i-1].StartTime {
			t.Errorf("Segment start time is not increasing: %d then %d",
				media[i-1].StartTime, media[i].StartTime)
		}
	}
}

func TestChunkWriter_WithSeekHead(t *testing.T) {
	// A SeekHead is part of the initialization segment. Its positions are
	// relative to the start of the Segment data, which is still the prefix of
	// the concatenated stream.
	buf := buffercloser.New()
	collector := &chunkCollector{}

	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{
			{TrackNumber: 1, TrackEntry: &TrackEntry{TrackNumber: 1}},
		},
		WithEBMLHeader(&struct {
			DocType string `ebml:"EBMLDocType"`
		}{DocType: "webm"}),
		WithSegmentInfo(&struct {
			TimecodeScale uint64 `ebml:"TimecodeScale"`
		}{TimecodeScale: 1000000}),
		WithSeekHead(true),
		WithMinMaxClusterDuration(1, 0, 0x7FFF),
		WithChunkHandler(collector.handle),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws[0].Write(true, 0, []byte{0x01}); err != nil {
		t.Fatal(err)
	}
	ws[0].Close()

	init := collector.init()
	var h struct {
		Segment struct {
			SeekHead struct {
				Seek []struct {
					SeekID       []byte `ebml:"SeekID"`
					SeekPosition uint64 `ebml:"SeekPosition"`
				} `ebml:"Seek"`
			} `ebml:"SeekHead"`
			Info    struct{}   `ebml:"Info"`
			Tracks  struct{}   `ebml:"Tracks"`
			Cluster []struct{} `ebml:"Cluster"`
		} `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(init.Data), &h); err != nil {
		t.Fatalf("Failed to unmarshal initialization segment: %v", err)
	}
	if len(h.Segment.SeekHead.Seek) == 0 {
		t.Fatal("SeekHead is not in the initialization segment")
	}
	if len(h.Segment.Cluster) != 0 {
		t.Error("The initialization segment must not contain a Cluster")
	}

	// Positions must address the actual elements in the concatenated stream.
	var all []byte
	all = append(all, init.Data...)
	for _, seg := range collector.media() {
		all = append(all, seg.Data...)
	}
	// Verify the whole concatenation is still readable.
	var result struct {
		Segment testSegment `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(all), &result); err != nil {
		t.Fatalf("Concatenated output is not readable: %v", err)
	}
}

func TestChunkWriter_RequiresMainTrack(t *testing.T) {
	buf := buffercloser.New()
	_, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}},
		WithChunkHandler(func(Chunk) error { return nil }),
	)
	if !errors.Is(err, ErrChunkRequiresMainTrack) {
		t.Errorf("Expected error: %v, got: %v", ErrChunkRequiresMainTrack, err)
	}
}

func TestChunkWriter_MainTrackNotFound(t *testing.T) {
	buf := buffercloser.New()
	_, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}},
		WithMinMaxClusterDuration(2, 0, 0x7FFF),
		WithChunkHandler(func(Chunk) error { return nil }),
	)
	if !errors.Is(err, ErrChunkMainTrackNotFound) {
		t.Errorf("Expected error: %v, got: %v", ErrChunkMainTrackNotFound, err)
	}
}

func TestChunkWriter_RejectsCues(t *testing.T) {
	buf := buffercloser.New()
	_, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}},
		WithMinMaxClusterDuration(1, 0, 0x7FFF),
		WithCues(64),
		WithChunkHandler(func(Chunk) error { return nil }),
	)
	if !errors.Is(err, ErrChunkWithCues) {
		t.Errorf("Expected error: %v, got: %v", ErrChunkWithCues, err)
	}
}

func TestChunkWriter_HandlerErrorIsFatal(t *testing.T) {
	t.Run("InitializationSegment", func(t *testing.T) {
		buf := buffercloser.New()
		errDummy := errors.New("an error")
		_, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{{TrackNumber: 1}},
			WithEBMLHeader(nil),
			WithSegmentInfo(nil),
			WithMinMaxClusterDuration(1, 0, 0x7FFF),
			WithChunkHandler(func(Chunk) error { return errDummy }),
		)
		if !errors.Is(err, errDummy) {
			t.Errorf("Expected error: %v, got: %v", errDummy, err)
		}
	})

	t.Run("MediaSegment", func(t *testing.T) {
		buf := buffercloser.New()
		errDummy := errors.New("an error")
		chFatal := make(chan error, 1)

		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{{TrackNumber: 1}},
			WithEBMLHeader(nil),
			WithSegmentInfo(nil),
			WithMinMaxClusterDuration(1, 0, 0x7FFF),
			WithChunkHandler(func(c Chunk) error {
				if c.Initialization {
					return nil
				}
				return errDummy
			}),
			WithOnFatalHandler(func(err error) { chFatal <- err }),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ws[0].Write(true, 0, []byte{0x01}); err != nil {
			t.Fatal(err)
		}
		ws[0].Close()

		select {
		case err := <-chFatal:
			if !errors.Is(err, errDummy) {
				t.Errorf("Expected error: %v, got: %v", errDummy, err)
			}
		default:
			t.Error("Handler error is not reported as fatal")
		}
	})
}

// goldenNonChunkedOutput is the byte stream produced by the continuous (non
// chunked) writer for the input used in TestBlockWriter_NonChunkedOutputUnchanged.
// It is recorded from the writer before the chunked output feature was added.
var goldenNonChunkedOutput = []byte{
	0x1a, 0x45, 0xdf, 0xa3, 0x87, 0x42, 0x82, 0x84, 0x77, 0x65, 0x62, 0x6d,
	0x18, 0x53, 0x80, 0x67, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0x15, 0x49, 0xa9, 0x66, 0x87, 0x2a, 0xd7, 0xb1, 0x83, 0x0f, 0x42, 0x40,
	0x16, 0x54, 0xae, 0x6b, 0x80,
	0x1f, 0x43, 0xb6, 0x75, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xe7, 0x81, 0x00,
	0xa3, 0x85, 0x81, 0x00, 0x00, 0x80, 0x00,
	0xa3, 0x85, 0x81, 0x00, 0x0a, 0x00, 0x0a,
	0x1f, 0x43, 0xb6, 0x75, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xe7, 0x81, 0x14,
	0xab, 0x81, 0x1d,
	0xa3, 0x85, 0x81, 0x00, 0x00, 0x80, 0x14,
	0x1f, 0x43, 0xb6, 0x75, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xe7, 0x81, 0x14,
	0xab, 0x81, 0x19,
}

func TestBlockWriter_NonChunkedOutputUnchanged(t *testing.T) {
	// Not enabling chunked output must produce the same continuous stream as
	// before the chunked output feature was added.
	buf := buffercloser.New()
	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{{TrackNumber: 1}},
		WithEBMLHeader(&struct {
			DocType string `ebml:"EBMLDocType"`
		}{DocType: "webm"}),
		WithSegmentInfo(&struct {
			TimecodeScale uint64 `ebml:"TimecodeScale"`
		}{TimecodeScale: 1000000}),
		WithMinMaxClusterDuration(1, 0, 0x7FFF),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		keyframe bool
		tc       int64
	}{{true, 0}, {false, 10}, {true, 20}} {
		if _, err := ws[0].Write(f.keyframe, f.tc, []byte{byte(f.tc)}); err != nil {
			t.Fatal(err)
		}
	}
	ws[0].Close()

	if !bytes.Equal(buf.Bytes(), goldenNonChunkedOutput) {
		t.Errorf("Non-chunked output changed,\nexpected: %+v\n     got: %+v",
			goldenNonChunkedOutput, buf.Bytes())
	}
}
