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
	"testing"

	"github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/internal/buffercloser"
)

type lacingTestInfo struct {
	TimecodeScale uint64 `ebml:"TimecodeScale"`
}

func newLacingTestTrack(number uint64, defaultDuration uint64) TrackDescription {
	return TrackDescription{
		TrackNumber: number,
		TrackEntry: &TrackEntry{
			TrackNumber:     number,
			TrackUID:        number,
			TrackType:       2,
			CodecID:         "A_OPUS",
			DefaultDuration: defaultDuration,
		},
	}
}

func readLacingTestSegment(t *testing.T, b []byte) []simpleBlockCluster {
	t.Helper()
	var result struct {
		Segment struct {
			Tracks  flexTracks           `ebml:"Tracks"`
			Cluster []simpleBlockCluster `ebml:"Cluster,size=unknown"`
		} `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(b), &result); err != nil {
		t.Fatalf("Failed to Unmarshal resultant binary: '%v'", err)
	}
	return result.Segment.Cluster
}

func TestBlockWriter_WithLacing(t *testing.T) {
	const timecodeScale = 1000000  // 1ms
	const frameDuration = 20000000 // 20ms
	const ticks = frameDuration / timecodeScale

	t.Run("RoundTrip", func(t *testing.T) {
		frameSizes := []int{10, 10, 10, 10, 10}
		testcases := map[string]struct {
			scale    uint64
			duration uint64
			sizes    []int
			frames   [][]byte
		}{
			"FixedSize": {
				scale: timecodeScale, duration: frameDuration, sizes: frameSizes,
			},
			"Xiph": {
				scale: timecodeScale, duration: frameDuration,
				sizes: []int{255, 100, 510, 40},
			},
			"EBML": {
				scale: timecodeScale, duration: frameDuration,
				sizes: []int{800, 500, 100},
			},
			"NonDefaultTimecodeScale": {
				scale: 100000, duration: frameDuration,
				sizes: []int{7, 9, 11},
			},
		}
		for name, tc := range testcases {
			tc := tc
			t.Run(name, func(t *testing.T) {
				var frames [][]byte
				for i, n := range tc.sizes {
					f := make([]byte, n)
					for j := range f {
						f[j] = byte(i + 1)
					}
					frames = append(frames, f)
				}
				ticks := int64(tc.duration / tc.scale)

				buf := buffercloser.New()
				ws, err := NewSimpleBlockWriter(
					buf,
					[]TrackDescription{newLacingTestTrack(1, tc.duration)},
					WithEBMLHeader(nil),
					WithSegmentInfo(&lacingTestInfo{TimecodeScale: tc.scale}),
					WithSeekHead(false),
					WithLacing(1),
				)
				if err != nil {
					t.Fatalf("Failed to create BlockWriter: '%v'", err)
				}
				for i, f := range frames {
					if _, err := ws[0].Write(true, int64(i)*ticks, f); err != nil {
						t.Fatalf("Failed to Write: '%v'", err)
					}
				}
				ws[0].Close()
				<-buf.Closed()

				// All the frames must be packed in one block.
				clusters := readLacingTestSegment(t, buf.Bytes())
				if len(clusters) == 0 {
					t.Fatal("No cluster is written")
				}
				var blocks []ebml.Block
				for _, c := range clusters {
					blocks = append(blocks, c.SimpleBlock...)
				}
				if len(blocks) != 1 {
					t.Fatalf("Expected 1 block, got %d", len(blocks))
				}
				if blocks[0].Lacing == ebml.LacingNo {
					t.Error("Expected laced block")
				}
				if len(blocks[0].Data) != len(frames) {
					t.Errorf("Expected %d frames in the block, got %d", len(frames), len(blocks[0].Data))
				}
				for i := range frames {
					if i >= len(blocks[0].Data) {
						break
					}
					if !bytes.Equal(frames[i], blocks[0].Data[i]) {
						t.Errorf("Frame %d is broken, expected %v, got %v", i, frames[i], blocks[0].Data[i])
					}
				}

				// The reader must return each frame with its original timestamp.
				rs, err := NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
				if err != nil {
					t.Fatalf("Failed to create BlockReader: '%v'", err)
				}
				for i, f := range frames {
					b, keyframe, timestamp, err := rs[0].Read()
					if err != nil {
						t.Fatalf("Failed to Read frame %d: '%v'", i, err)
					}
					if !bytes.Equal(b, f) {
						t.Errorf("Frame %d data mismatch, expected %v, got %v", i, f, b)
					}
					if !keyframe {
						t.Errorf("Frame %d keyframe flag is lost", i)
					}
					if expect := int64(i) * ticks; timestamp != expect {
						t.Errorf("Frame %d timestamp: expected %d, got %d", i, expect, timestamp)
					}
				}
				if _, _, _, err := rs[0].Read(); err != io.EOF {
					t.Errorf("Expected EOF, got: '%v'", err)
				}
				rs[0].Close()
			})
		}
	})

	t.Run("NoLacing", func(t *testing.T) {
		frames := [][]byte{{0x01}, {0x02, 0x03}, {0x04, 0x05, 0x06}}
		write := func(opts ...BlockWriterOption) []byte {
			buf := buffercloser.New()
			ws, err := NewSimpleBlockWriter(
				buf,
				[]TrackDescription{newLacingTestTrack(1, frameDuration)},
				append([]BlockWriterOption{
					WithEBMLHeader(nil),
					WithSegmentInfo(&lacingTestInfo{TimecodeScale: timecodeScale}),
					WithSeekHead(false),
				}, opts...)...,
			)
			if err != nil {
				t.Fatalf("Failed to create BlockWriter: '%v'", err)
			}
			for i, f := range frames {
				if _, err := ws[0].Write(true, int64(i)*ticks, f); err != nil {
					t.Fatalf("Failed to Write: '%v'", err)
				}
			}
			ws[0].Close()
			<-buf.Closed()
			return buf.Bytes()
		}

		// A track not enabled for lacing must be written as before.
		if !bytes.Equal(write(), write(WithLacing(2))) {
			t.Error("Output with lacing disabled on the track must be unchanged")
		}

		blocks := readLacingTestSegment(t, write())[0].SimpleBlock
		if len(blocks) != len(frames) {
			t.Fatalf("Expected %d blocks, got %d", len(frames), len(blocks))
		}
		for _, b := range blocks {
			if b.Lacing != ebml.LacingNo || len(b.Data) != 1 {
				t.Error("Expected simple block without lacing")
			}
		}
	})

	t.Run("SplitOnDiscontinuity", func(t *testing.T) {
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{newLacingTestTrack(1, frameDuration)},
			WithEBMLHeader(nil),
			WithSegmentInfo(&lacingTestInfo{TimecodeScale: timecodeScale}),
			WithSeekHead(false),
			WithLacing(1),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		// Continuous frames at 0, 20, then a gap, then 60, 80.
		for _, tc := range []int64{0, 20, 60, 80} {
			if _, err := ws[0].Write(true, tc, []byte{byte(tc)}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
		}
		ws[0].Close()
		<-buf.Closed()

		var blocks []ebml.Block
		for _, c := range readLacingTestSegment(t, buf.Bytes()) {
			blocks = append(blocks, c.SimpleBlock...)
		}
		if len(blocks) != 2 {
			t.Fatalf("Expected 2 blocks at the discontinuity, got %d", len(blocks))
		}
		if len(blocks[0].Data) != 2 || len(blocks[1].Data) != 2 {
			t.Errorf("Unexpected frame packing: %d and %d frames", len(blocks[0].Data), len(blocks[1].Data))
		}
		rs, err := NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Failed to create BlockReader: '%v'", err)
		}
		for i, tc := range []int64{0, 20, 60, 80} {
			if _, _, timestamp, err := rs[0].Read(); err != nil {
				t.Fatalf("Failed to Read frame %d: '%v'", i, err)
			} else if timestamp != tc {
				t.Errorf("Frame %d timestamp: expected %d, got %d", i, tc, timestamp)
			}
		}
		rs[0].Close()
	})

	t.Run("SplitOnKeyframeChange", func(t *testing.T) {
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{newLacingTestTrack(1, frameDuration)},
			WithEBMLHeader(nil),
			WithSegmentInfo(&lacingTestInfo{TimecodeScale: timecodeScale}),
			WithSeekHead(false),
			WithLacing(1),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		for i, kf := range []bool{true, true, false, false, true} {
			if _, err := ws[0].Write(kf, int64(i)*ticks, []byte{byte(i)}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
		}
		ws[0].Close()
		<-buf.Closed()

		var blocks []ebml.Block
		for _, c := range readLacingTestSegment(t, buf.Bytes()) {
			blocks = append(blocks, c.SimpleBlock...)
		}
		if len(blocks) != 3 {
			t.Fatalf("Expected 3 blocks on keyframe change, got %d", len(blocks))
		}
		expect := []struct {
			keyframe bool
			n        int
		}{{true, 2}, {false, 2}, {true, 1}}
		for i, e := range expect {
			if blocks[i].Keyframe != e.keyframe || len(blocks[i].Data) != e.n {
				t.Errorf("Block %d: expected keyframe=%v with %d frames, got keyframe=%v with %d frames",
					i, e.keyframe, e.n, blocks[i].Keyframe, len(blocks[i].Data))
			}
		}
	})

	t.Run("FlushOnClusterBoundary", func(t *testing.T) {
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{
				newLacingTestTrack(1, frameDuration),                      // audio, laced
				{TrackNumber: 2, TrackEntry: &TrackEntry{TrackNumber: 2}}, // video
			},
			WithEBMLHeader(nil),
			WithSegmentInfo(&lacingTestInfo{TimecodeScale: timecodeScale}),
			WithSeekHead(false),
			WithLacing(1),
			// Every video keyframe after the minimum duration starts a new cluster.
			WithMinMaxClusterDuration(2, 0, 0x7FFF),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}

		// Video keyframes at 0 and 100, audio frames every 20ms.
		// The first frame must be the video keyframe so that the first cluster
		// starts on the main track.
		if _, err := ws[1].Write(true, 0, []byte{0xB0}); err != nil {
			t.Fatalf("Failed to Write: '%v'", err)
		}
		for i := 0; i < 10; i++ {
			audioTc := int64(i) * ticks
			if _, err := ws[0].Write(true, audioTc, []byte{0xA0 + byte(i)}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
			if audioTc == 100 {
				if _, err := ws[1].Write(true, audioTc, []byte{0xB1}); err != nil {
					t.Fatalf("Failed to Write: '%v'", err)
				}
			}
		}
		ws[0].Close()
		ws[1].Close()
		<-buf.Closed()

		clusters := readLacingTestSegment(t, buf.Bytes())
		// Discard the empty finalization cluster.
		var dataClusters []simpleBlockCluster
		for _, c := range clusters {
			if len(c.SimpleBlock) > 0 {
				dataClusters = append(dataClusters, c)
			}
		}
		if len(dataClusters) != 2 {
			t.Fatalf("Expected 2 clusters, got %d", len(dataClusters))
		}
		if dataClusters[0].Timecode != 0 || dataClusters[1].Timecode != 100 {
			t.Errorf("Unexpected cluster timecodes: %d, %d",
				dataClusters[0].Timecode, dataClusters[1].Timecode)
		}

		// The laced audio block of each cluster must stay in its cluster.
		if len(dataClusters[0].SimpleBlock) != 2 {
			t.Fatalf("Expected 2 blocks in the first cluster, got %d", len(dataClusters[0].SimpleBlock))
		}
		audioBlock := dataClusters[0].SimpleBlock[1]
		if len(audioBlock.Data) != 6 {
			t.Errorf("Expected 6 audio frames in the first cluster, got %d", len(audioBlock.Data))
		}
		if audioBlock.Timecode != 0 {
			t.Errorf("Expected audio block timecode 0, got %d", audioBlock.Timecode)
		}
		// The audio frames after the cluster switch must be in the second cluster.
		audioBlock2 := dataClusters[1].SimpleBlock[1]
		if len(audioBlock2.Data) != 4 {
			t.Errorf("Expected 4 audio frames in the second cluster, got %d", len(audioBlock2.Data))
		}
		if audioBlock2.Timecode != 20 {
			t.Errorf("Expected audio block timecode 20, got %d", audioBlock2.Timecode)
		}

		rs, err := NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Failed to create BlockReader: '%v'", err)
		}
		// The video track is not read in this test.
		rs[1].Close()
		var audioTimestamps []int64
		for {
			_, _, ts, err := rs[0].Read()
			if err != nil {
				break
			}
			audioTimestamps = append(audioTimestamps, ts)
		}
		rs[0].Close()
		if len(audioTimestamps) != 10 {
			t.Fatalf("Expected 10 audio frames, got %d", len(audioTimestamps))
		}
		for i, ts := range audioTimestamps {
			if expect := int64(i) * ticks; ts != expect {
				t.Errorf("Audio frame %d timestamp: expected %d, got %d", i, expect, ts)
			}
		}
	})

	t.Run("FlushOnClose", func(t *testing.T) {
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{newLacingTestTrack(1, frameDuration)},
			WithEBMLHeader(nil),
			WithSegmentInfo(&lacingTestInfo{TimecodeScale: timecodeScale}),
			WithSeekHead(false),
			WithLacing(1),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		// Frames are still pending when the writer is closed.
		for i := 0; i < 3; i++ {
			if _, err := ws[0].Write(true, int64(i)*ticks, []byte{byte(i)}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
		}
		ws[0].Close()
		<-buf.Closed()

		rs, err := NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Failed to create BlockReader: '%v'", err)
		}
		n := 0
		for {
			b, _, ts, err := rs[0].Read()
			if err != nil {
				break
			}
			if !bytes.Equal(b, []byte{byte(n)}) || ts != int64(n)*ticks {
				t.Errorf("Frame %d is broken: data=%v timestamp=%d", n, b, ts)
			}
			n++
		}
		rs[0].Close()
		if n != 3 {
			t.Errorf("Expected 3 frames, got %d", n)
		}
	})

	t.Run("MaxFramesPerBlock", func(t *testing.T) {
		const n = 300
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{newLacingTestTrack(1, frameDuration)},
			WithEBMLHeader(nil),
			WithSegmentInfo(&lacingTestInfo{TimecodeScale: timecodeScale}),
			WithSeekHead(false),
			WithLacing(1),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		for i := 0; i < n; i++ {
			if _, err := ws[0].Write(true, int64(i)*ticks, []byte{byte(i)}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
		}
		ws[0].Close()
		<-buf.Closed()

		clusters := readLacingTestSegment(t, buf.Bytes())
		var blocks []ebml.Block
		for _, c := range clusters {
			blocks = append(blocks, c.SimpleBlock...)
		}
		if len(blocks) != 2 {
			t.Fatalf("Expected 2 blocks, got %d", len(blocks))
		}
		if len(blocks[0].Data) != maxLacedFrames {
			t.Errorf("Expected %d frames in the first block, got %d", maxLacedFrames, len(blocks[0].Data))
		}
		if len(blocks[1].Data) != n-maxLacedFrames {
			t.Errorf("Expected %d frames in the second block, got %d", n-maxLacedFrames, len(blocks[1].Data))
		}
	})

	t.Run("WithoutDefaultDuration", func(t *testing.T) {
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{newLacingTestTrack(1, 0)},
			WithEBMLHeader(nil),
			WithSegmentInfo(&lacingTestInfo{TimecodeScale: timecodeScale}),
			WithSeekHead(false),
			WithLacing(1),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		for i := 0; i < 3; i++ {
			if _, err := ws[0].Write(true, int64(i)*ticks, []byte{byte(i)}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
		}
		ws[0].Close()
		<-buf.Closed()

		blocks := readLacingTestSegment(t, buf.Bytes())[0].SimpleBlock
		if len(blocks) != 3 {
			t.Errorf("Expected no lacing without DefaultDuration, got %d blocks", len(blocks))
		}
	})

	t.Run("WithCues", func(t *testing.T) {
		buf := newWriteAtBuffer()
		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{newLacingTestTrack(1, frameDuration)},
			WithEBMLHeader(nil),
			WithSegmentInfo(&lacingTestInfo{TimecodeScale: timecodeScale}),
			WithSeekHead(true),
			WithCues(4096),
			WithLacing(1),
			WithMinMaxClusterDuration(1, 0, 0x7FFF),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		for i := 0; i < 4; i++ {
			if _, err := ws[0].Write(true, int64(i)*0x8000, []byte{byte(i)}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
		}
		ws[0].Close()
		<-buf.Closed()

		data := buf.Bytes()
		var result struct {
			Segment cuesTestSegment `ebml:"Segment,size=unknown"`
		}
		if err := ebml.Unmarshal(bytes.NewReader(data), &result); err != nil {
			t.Fatalf("Failed to Unmarshal: '%v'", err)
		}
		if result.Segment.Cues == nil || len(result.Segment.Cues.CuePoint) == 0 {
			t.Fatal("Expected Cues to be present")
		}

		segmentIdx := bytes.Index(data, []byte{0x18, 0x53, 0x80, 0x67})
		if segmentIdx < 0 {
			t.Fatal("Segment element not found")
		}
		segmentDataStart := segmentIdx + 4 + 8
		clusterElementID := []byte{0x1F, 0x43, 0xB6, 0x75}
		for i, cp := range result.Segment.Cues.CuePoint {
			pos := segmentDataStart + int(cp.CueTrackPositions[0].CueClusterPosition)
			if pos+4 > len(data) || !bytes.Equal(data[pos:pos+4], clusterElementID) {
				t.Errorf("CuePoint[%d] does not point to a Cluster", i)
			}
		}
	})
}

func TestBlockWriter_WithLacing_EncodedHeader(t *testing.T) {
	// The block header of the laced block must use the smallest representation.
	testcases := map[string]struct {
		sizes []int
		mode  ebml.LacingMode
		// header is the lacing header byte sequence following the flags.
		header []byte
	}{
		"Fixed": {
			sizes:  []int{10, 10, 10},
			mode:   ebml.LacingFixed,
			header: []byte{0x02},
		},
		"Xiph": {
			sizes:  []int{255, 100, 510, 40},
			mode:   ebml.LacingXiph,
			header: []byte{0x03, 0xFF, 0x00, 0x64, 0xFF, 0xFF, 0x00},
		},
		"EBML": {
			sizes:  []int{800, 500, 100},
			mode:   ebml.LacingEBML,
			header: []byte{0x02, 0x43, 0x20, 0x5E, 0xD3},
		},
	}
	for name, tc := range testcases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			frames := make([][]byte, len(tc.sizes))
			for i, n := range tc.sizes {
				frames[i] = bytes.Repeat([]byte{byte(i + 1)}, n)
			}

			buf := buffercloser.New()
			ws, err := NewSimpleBlockWriter(
				buf,
				[]TrackDescription{newLacingTestTrack(1, 20000000)},
				WithEBMLHeader(nil),
				WithSegmentInfo(&lacingTestInfo{TimecodeScale: 1000000}),
				WithSeekHead(false),
				WithLacing(1),
			)
			if err != nil {
				t.Fatalf("Failed to create BlockWriter: '%v'", err)
			}
			for i, f := range frames {
				if _, err := ws[0].Write(true, int64(i)*20, f); err != nil {
					t.Fatalf("Failed to Write: '%v'", err)
				}
			}
			ws[0].Close()
			<-buf.Closed()

			data := buf.Bytes()
			// SimpleBlock flags: keyframe (0x80) | lacing mode << 1.
			header := append([]byte{0x80 | byte(tc.mode)<<1}, tc.header...)
			if !bytes.Contains(data, header) {
				t.Errorf("Expected lacing header % X is not found", header)
			}

			// The block must be decodable.
			var result struct {
				Segment struct {
					Cluster []simpleBlockCluster `ebml:"Cluster,size=unknown"`
				} `ebml:"Segment,size=unknown"`
			}
			if err := ebml.Unmarshal(bytes.NewReader(data), &result); err != nil {
				t.Fatalf("Failed to Unmarshal: '%v'", err)
			}
			var blocks []ebml.Block
			for _, c := range result.Segment.Cluster {
				blocks = append(blocks, c.SimpleBlock...)
			}
			if len(blocks) != 1 {
				t.Fatalf("Expected 1 block, got %d", len(blocks))
			}
			if blocks[0].Lacing != tc.mode {
				t.Errorf("Expected lacing mode %v, got %v", tc.mode, blocks[0].Lacing)
			}
			for i := range frames {
				if !bytes.Equal(frames[i], blocks[0].Data[i]) {
					t.Errorf("Frame %d is broken", i)
				}
			}
		})
	}
}
