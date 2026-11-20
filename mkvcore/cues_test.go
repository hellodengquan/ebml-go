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
	"math"
	"testing"

	"github.com/at-wat/ebml-go"
)

func TestSelectCueTrack(t *testing.T) {
	audio := &TrackEntry{TrackNumber: 1, TrackType: trackTypeAudio}
	video := &TrackEntry{TrackNumber: 2, TrackType: trackTypeVideo}

	cases := []struct {
		name             string
		tracks           []TrackDescription
		configured       uint64
		wantTrack        uint64
		wantRequireKeyfr bool
	}{
		{
			name:             "AudioFirstVideoSecond",
			tracks:           []TrackDescription{{1, audio}, {2, video}},
			wantTrack:        2,
			wantRequireKeyfr: true,
		},
		{
			name:             "VideoFirst",
			tracks:           []TrackDescription{{2, video}, {1, audio}},
			wantTrack:        2,
			wantRequireKeyfr: true,
		},
		{
			name:             "AudioOnly",
			tracks:           []TrackDescription{{1, audio}},
			wantTrack:        1,
			wantRequireKeyfr: false,
		},
		{
			name:             "ConfiguredVideo",
			tracks:           []TrackDescription{{1, audio}, {2, video}},
			configured:       2,
			wantTrack:        2,
			wantRequireKeyfr: true,
		},
		{
			name:             "ConfiguredAudioFallsBackToVideo",
			tracks:           []TrackDescription{{1, audio}, {2, video}},
			configured:       1,
			wantTrack:        2,
			wantRequireKeyfr: true,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			track, requireKeyframe := selectCueTrack(c.tracks, c.configured)
			if track != c.wantTrack || requireKeyframe != c.wantRequireKeyfr {
				t.Errorf("selectCueTrack() = (%d, %v), want (%d, %v)",
					track, requireKeyframe, c.wantTrack, c.wantRequireKeyfr)
			}
		})
	}
}

func TestCloneSegmentInfo(t *testing.T) {
	orig := &durationInfo{TimecodeScale: 1000000}
	cloned, ok := cloneSegmentInfo(orig).(*durationInfo)
	if !ok {
		t.Fatalf("cloneSegmentInfo returned %T", cloneSegmentInfo(orig))
	}
	if cloned == orig {
		t.Fatal("cloneSegmentInfo returned the same pointer")
	}
	cloned.SetDuration(42)
	if orig.Duration != 0 {
		t.Errorf("original was modified: Duration = %v", orig.Duration)
	}
	if cloned.Duration != 42 {
		t.Errorf("clone not updated: Duration = %v", cloned.Duration)
	}
}

// parsedBlock is a SimpleBlock decoded from the output.
type parsedBlock struct {
	track    uint64
	timecode int16
	keyframe bool
}

// parsedCluster is a Cluster decoded from the output.
type parsedCluster struct {
	absPos   uint64
	timecode uint64
	blocks   []parsedBlock
}

// parseMedia parses the Segment of the output and returns the segment data
// start and all Clusters with their absolute positions.
func parseMedia(t *testing.T, buf []byte) (uint64, []parsedCluster) {
	t.Helper()
	layout, ok := parseHeaderLayout(buf)
	if !ok {
		t.Fatal("failed to parse header layout")
	}
	clusters := []parsedCluster{}
	clusterID := ebml.ElementCluster.Bytes()

	p := layout.segmentStart
	for p < uint64(len(buf)) {
		id, _, ok := decodeElementIDAt(buf, p)
		if !ok {
			t.Fatalf("failed to decode element at %d", p)
		}
		if bytes.Equal(id, clusterID) {
			start, _, ok := elementContentAt(buf, p)
			if !ok {
				t.Fatalf("failed to decode cluster content at %d", p)
			}
			c, next := parseCluster(t, buf, p, start)
			clusters = append(clusters, c)
			p = next
			continue
		}
		// All other Segment children have a known size.
		_, end, ok := elementContentAt(buf, p)
		if !ok || end <= p {
			break
		}
		p = end
	}
	return layout.segmentStart, clusters
}

// parseCluster parses the Cluster starting at pos (ID at pos, content at start)
// and returns it plus the position of the next top-level element.
func parseCluster(t *testing.T, buf []byte, pos, start uint64) (parsedCluster, uint64) {
	t.Helper()
	c := parsedCluster{absPos: pos}
	clusterID := ebml.ElementCluster.Bytes()
	p := start
	for p < uint64(len(buf)) {
		id, idLen, ok := decodeElementIDAt(buf, p)
		if !ok {
			break
		}
		// The next Cluster terminates the current one (unknown-size Cluster).
		if bytes.Equal(id, clusterID) {
			return c, p
		}
		contentStart, contentEnd, ok := elementContentAt(buf, p)
		if !ok {
			break
		}
		switch {
		case bytes.Equal(id, ebml.ElementTimecode.Bytes()):
			var v uint64
			for i := contentStart; i < contentEnd; i++ {
				v = v<<8 | uint64(buf[i])
			}
			c.timecode = v
		case bytes.Equal(id, ebml.ElementSimpleBlock.Bytes()):
			data := buf[contentStart:contentEnd]
			if len(data) >= 4 {
				b := parsedBlock{
					track:    uint64(data[0] & 0x7f),
					timecode: int16(data[1])<<8 | int16(data[2]),
					keyframe: data[3]&0x80 != 0,
				}
				c.blocks = append(c.blocks, b)
			}
		}
		p = contentEnd
		if contentEnd <= contentStart-uint64(idLen) {
			break
		}
	}
	return c, uint64(len(buf))
}

// cueInfo is a CuePoint decoded from the output.
type cueInfo struct {
	time        uint64
	track       uint64
	clusterPos  uint64
	blockNumber uint64
}

func readCues(t *testing.T, buf []byte) ([]cueInfo, float64, bool) {
	t.Helper()
	var result struct {
		Segment struct {
			Info struct {
				Duration float64 `ebml:"Duration"`
			} `ebml:"Info"`
			Cues *struct {
				CuePoint []struct {
					CueTime           uint64 `ebml:"CueTime"`
					CueTrackPositions []struct {
						CueTrack           uint64 `ebml:"CueTrack"`
						CueClusterPosition uint64 `ebml:"CueClusterPosition"`
						CueBlockNumber     uint64 `ebml:"CueBlockNumber"`
					} `ebml:"CueTrackPositions"`
				} `ebml:"CuePoint"`
			} `ebml:"Cues"`
		} `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(buf), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result.Segment.Cues == nil {
		return nil, result.Segment.Info.Duration, false
	}
	var cues []cueInfo
	for _, cp := range result.Segment.Cues.CuePoint {
		for _, p := range cp.CueTrackPositions {
			cues = append(cues, cueInfo{
				time:        cp.CueTime,
				track:       p.CueTrack,
				clusterPos:  p.CueClusterPosition,
				blockNumber: p.CueBlockNumber,
			})
		}
	}
	return cues, result.Segment.Info.Duration, true
}

// resolveCue returns the block referenced by the cue, failing the test if the
// cue does not point at a valid Cluster or block.
func resolveCue(t *testing.T, clusters []parsedCluster, segStart uint64, cue cueInfo) parsedBlock {
	t.Helper()
	abs := segStart + cue.clusterPos
	var cluster *parsedCluster
	for i := range clusters {
		if clusters[i].absPos == abs {
			cluster = &clusters[i]
			break
		}
	}
	if cluster == nil {
		t.Fatalf("cue at %d: no Cluster starts at position %d", cue.time, abs)
	}
	idx := int(cue.blockNumber)
	if idx == 0 {
		idx = 1 // EBML default
	}
	idx--
	if idx < 0 || idx >= len(cluster.blocks) {
		t.Fatalf("cue at %d: block number %d out of range (%d blocks)",
			cue.time, cue.blockNumber, len(cluster.blocks))
	}
	return cluster.blocks[idx]
}

func TestBlockWriter_CuePointsReferenceVideoKeyframes(t *testing.T) {
	// Audio is the first track; the Cues must reference the video track so a
	// seeking player lands on a decodable keyframe.
	tracks := []TrackDescription{
		{TrackNumber: 1, TrackEntry: &TrackEntry{TrackNumber: 1, TrackType: trackTypeAudio}},
		{TrackNumber: 2, TrackEntry: &TrackEntry{TrackNumber: 2, TrackType: trackTypeVideo}},
	}

	cases := map[string]struct {
		opts []BlockWriterOption
		// wantMidClusterCues requires at least one cue point whose block
		// number is not the first block of its Cluster, exercising
		// CueBlockNumber.
		wantMidClusterCues bool
	}{
		"DefaultClusterCadence": {},
		// Force several video keyframes into one Cluster to exercise
		// CueBlockNumber for frames that do not start a Cluster.
		"KeyframesInOneCluster": {
			opts:               []BlockWriterOption{WithMinMaxClusterDuration(2, 3000, 0x7FFF)},
			wantMidClusterCues: true,
		},
	}

	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			buf := newSeekableBuffer()
			opts := append([]BlockWriterOption{
				WithEBMLHeader(nil),
				WithSegmentInfo(&durationInfo{TimecodeScale: 1000000}),
				WithSeekHead(true),
				WithCues(16384),
			}, c.opts...)
			ws, err := NewSimpleBlockWriter(buf, tracks, opts...)
			if err != nil {
				t.Fatalf("Failed to create BlockWriter: %v", err)
			}

			const nFrames = 50
			videoKeyframes := map[int64]bool{}
			for i := 0; i < nFrames; i++ {
				tc := int64(i) * 100
				if _, err := ws[0].Write(true, tc, []byte{0x0a}); err != nil {
					t.Fatalf("Failed to Write: %v", err)
				}
				keyframe := i%10 == 0
				if keyframe {
					videoKeyframes[tc] = true
				}
				if _, err := ws[1].Write(keyframe, tc, []byte{0x02}); err != nil {
					t.Fatalf("Failed to Write: %v", err)
				}
			}
			ws[0].Close()
			ws[1].Close()
			<-buf.Closed()

			data := buf.Bytes()
			segStart, clusters := parseMedia(t, data)
			cues, _, hasCues := readCues(t, data)
			if !hasCues {
				t.Fatal("Cues not found")
			}
			if len(cues) != len(videoKeyframes) {
				t.Errorf("expected %d cue points (one per video keyframe), got %d",
					len(videoKeyframes), len(cues))
			}

			seen := map[int64]bool{}
			midCluster := 0
			for _, cue := range cues {
				if cue.track != 2 {
					t.Errorf("cue at %d references track %d, want the video track 2", cue.time, cue.track)
				}
				blk := resolveCue(t, clusters, segStart, cue)
				if blk.track != 2 {
					t.Errorf("cue at %d resolves to track %d, want video track 2", cue.time, blk.track)
				}
				if !blk.keyframe {
					t.Errorf("cue at %d resolves to a non-keyframe block", cue.time)
				}
				if cue.blockNumber > 1 {
					midCluster++
				}
				seen[int64(cue.time)] = true
			}
			for tc := range videoKeyframes {
				if !seen[tc] {
					t.Errorf("video keyframe at %d has no cue point", tc)
				}
			}
			if c.wantMidClusterCues && midCluster == 0 {
				t.Error("no cue point uses CueBlockNumber > 1; mid-Cluster keyframes are not indexed")
			}
		})
	}
}

func TestBlockWriter_CuePointsAudioOnly(t *testing.T) {
	tracks := []TrackDescription{
		{TrackNumber: 1, TrackEntry: &TrackEntry{TrackNumber: 1, TrackType: trackTypeAudio}},
	}

	buf := newSeekableBuffer()
	ws, err := NewSimpleBlockWriter(buf, tracks,
		WithEBMLHeader(nil),
		WithSegmentInfo(&durationInfo{TimecodeScale: 1000000}),
		WithSeekHead(true),
		WithCues(16384),
	)
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}

	const nFrames = 1500 // 30s at 20ms
	lastTc := int64(0)
	for i := 0; i < nFrames; i++ {
		lastTc = int64(i) * 20
		if _, err := ws[0].Write(false, lastTc, []byte{0x0a}); err != nil {
			t.Fatalf("Failed to Write: %v", err)
		}
	}
	ws[0].Close()
	<-buf.Closed()

	data := buf.Bytes()
	segStart, clusters := parseMedia(t, data)
	cues, duration, hasCues := readCues(t, data)
	if !hasCues {
		t.Fatal("audio-only recording must contain Cues")
	}
	if len(cues) < 2 {
		t.Fatalf("expected multiple cue points for a 30s audio recording, got %d", len(cues))
	}
	for _, cue := range cues {
		if cue.track != 1 {
			t.Errorf("cue at %d references track %d, want 1", cue.time, cue.track)
		}
		blk := resolveCue(t, clusters, segStart, cue)
		if blk.track != 1 {
			t.Errorf("cue at %d resolves to track %d, want 1", cue.time, blk.track)
		}
	}
	// CueTimes increase and cover the media.
	for i := 1; i < len(cues); i++ {
		if cues[i].time <= cues[i-1].time {
			t.Errorf("CueTime not increasing: %d then %d", cues[i-1].time, cues[i].time)
		}
	}
	if cues[len(cues)-1].time == 0 {
		t.Error("last cue time is zero; audio cues do not span the media")
	}
	if duration != float64(lastTc) {
		t.Errorf("Duration = %v, want %v", duration, float64(lastTc))
	}
}

func TestBlockWriter_DurationConfigNotMutated(t *testing.T) {
	// A single config shared across recordings, mimicking webm.DefaultSegmentInfo.
	shared := &durationInfo{TimecodeScale: 1000000}
	tracks := []TrackDescription{
		{TrackNumber: 1, TrackEntry: &TrackEntry{TrackNumber: 1, TrackType: trackTypeVideo}},
	}

	writeFile := func(opts ...BlockWriterOption) ([]byte, float64) {
		t.Helper()
		buf := newSeekableBuffer()
		ws, err := NewSimpleBlockWriter(buf, tracks,
			append([]BlockWriterOption{
				WithEBMLHeader(nil),
				WithSegmentInfo(shared),
			}, opts...)...,
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: %v", err)
		}
		for i := 0; i < 10; i++ {
			if _, err := ws[0].Write(true, int64(i)*100, []byte{0x01}); err != nil {
				t.Fatalf("Failed to Write: %v", err)
			}
		}
		ws[0].Close()
		<-buf.Closed()
		_, duration, _ := readCues(t, buf.Bytes())
		return buf.Bytes(), duration
	}

	// First recording: Cues enabled.
	if _, duration := writeFile(WithSeekHead(true), WithCues(4096)); duration != 900 {
		t.Errorf("first recording Duration = %v, want 900", duration)
	}
	if shared.Duration != 0 {
		t.Errorf("shared config was mutated: Duration = %v", shared.Duration)
	}

	// Second recording in the same process without Cues: the header Duration
	// must reflect the media, not the leaked placeholder.
	if _, duration := writeFile(); duration != 900 {
		t.Errorf("second recording Duration = %v, want 900", duration)
	}
	if shared.Duration != 0 {
		t.Errorf("shared config was mutated by the second recording: Duration = %v", shared.Duration)
	}

	// A recording with no frames must not produce NaN or a tiny placeholder.
	buf := newSeekableBuffer()
	ws, err := NewSimpleBlockWriter(buf, tracks,
		WithEBMLHeader(nil), WithSegmentInfo(shared), WithSeekHead(true), WithCues(4096))
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}
	ws[0].Close()
	<-buf.Closed()
	_, duration, _ := readCues(t, buf.Bytes())
	if duration != 0 || math.IsNaN(duration) {
		t.Errorf("empty recording Duration = %v, want 0", duration)
	}
}

func TestBlockWriter_PlainSeekableDuration(t *testing.T) {
	tracks := []TrackDescription{{TrackNumber: 1}}

	buf := newSeekableBuffer()
	ws, err := NewSimpleBlockWriter(buf, tracks,
		WithEBMLHeader(nil),
		WithSegmentInfo(&durationInfo{TimecodeScale: 1000000}),
	)
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := ws[0].Write(true, 1000+int64(i)*100, []byte{0x01}); err != nil {
			t.Fatalf("Failed to Write: %v", err)
		}
	}
	ws[0].Close()
	<-buf.Closed()

	_, duration, _ := readCues(t, buf.Bytes())
	if duration != 400 {
		t.Errorf("Duration = %v, want 400", duration)
	}
}
