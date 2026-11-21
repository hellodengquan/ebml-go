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
	"reflect"

	"github.com/at-wat/ebml-go"
)

// Chunk is a self-contained part of a chunked (segmented) output.
//
// Chunked output is intended for browser MSE (Media Source Extensions) and
// DASH/HLS live streaming, where the player receives an initialization segment
// followed by a sequence of independently appendable media segments.
//
// Concatenating the initialization segment and all media segments in the
// delivery order results in a regular, valid WebM stream readable by this
// library.
//
// The structure follows the WebM byte stream format: the initialization
// segment is the EBML header followed by the Segment header, Info and Tracks,
// and each media segment is a single Cluster element. The Cluster timecode is
// on the same timeline as the initialization segment, so media segments can be
// appended in any order to the playback buffer.
type Chunk struct {
	// Initialization is true for the initialization segment, which contains
	// the EBML header, Segment header, Info and Tracks, and false for media
	// segments.
	Initialization bool

	// Data is the complete EBML data of the chunk. It can be appended to a
	// browser SourceBuffer as-is. The slice is not reused by the writer; the
	// owner may retain it, but must not modify it.
	Data []byte

	// StartTime is the timestamp of the first frame of the media segment
	// (which is a main track keyframe) in the same unit as the timestamp
	// passed to BlockWriter.Write. It is zero for the initialization segment.
	StartTime int64

	// EndTime is the timestamp at which the next media segment starts, in the
	// same unit as StartTime. For the last media segment, it is the timestamp
	// of the last frame it contains. It is zero for the initialization
	// segment.
	//
	// The time ranges of adjacent media segments are therefore contiguous:
	// the EndTime of a segment is equal to the StartTime of the next one, and
	// they do not overlap.
	EndTime int64
}

// ChunkHandler receives the initialization segment and each complete media
// segment. See WithChunkHandler.
//
// The initialization segment is passed synchronously from
// NewSimpleBlockWriter; media segments are passed from the writer goroutine.
// The handler must not modify the chunk data. The returned error is treated as
// fatal: NewSimpleBlockWriter returns it for the initialization segment, and
// the fatal error handler receives it for media segments.
type ChunkHandler func(chunk Chunk) error

// inferMainTrackNumber returns the track number of the first video track
// described by tracks, or 0 if there is none.
//
// It lets the chunked output work without an explicit main track configuration
// when the track entries carry their TrackType (as webm.TrackEntry does).
func inferMainTrackNumber(tracks []TrackDescription) uint64 {
	for _, t := range tracks {
		if trackTypeOf(t.TrackEntry) == TrackTypeVideo {
			return t.TrackNumber
		}
	}
	return 0
}

// Track types defined by the Matroska specification.
const (
	TrackTypeVideo    = 1
	TrackTypeAudio    = 2
	TrackTypeSubtitle = 17
)

// trackTypeOf extracts TrackType from a TrackEntry of an arbitrary struct type.
func trackTypeOf(entry interface{}) uint64 {
	v := reflect.ValueOf(entry)
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
	switch {
	case f.CanUint():
		return f.Uint()
	case f.CanInt():
		return uint64(f.Int())
	}
	return 0
}

// chunkCluster is a Cluster element with a known size so that each media
// segment is a complete, standalone EBML element.
//
// PrevSize is intentionally omitted: a media segment may be appended to a
// SourceBuffer at any position, where the size of the previous cluster is
// unknown and irrelevant.
type chunkCluster struct {
	Timecode    uint64       `ebml:"Timecode"`
	SimpleBlock []ebml.Block `ebml:"SimpleBlock"`
}

// chunkWriter converts the interleaved frame stream into a sequence of media
// segments, each of which consists of exactly one Cluster element.
//
// Policy:
//   - A media segment starts only at a keyframe of the main track, so every
//     media segment is independently decodable.
//   - The main track keyframe interval can exceed the maximum cluster
//     duration configured by WithMinMaxClusterDuration / WithMaxKeyframeInterval.
//     In that case the segment is kept open until the next keyframe and
//     ErrChunkDurationExceeded is reported through the error handler. Cutting
//     the cluster at a non-keyframe would produce a media segment which is not
//     independently decodable.
//   - A SimpleBlock timecode is a signed 16-bit value relative to the cluster
//     timecode, so no frame may be more than 0x7FFF timecode units away from
//     the cluster timecode. If waiting for the next keyframe would exceed this
//     hard limit, the frames which cannot be represented are dropped and
//     ErrChunkFrameOutOfRange is reported through the error handler; the next
//     main track keyframe starts a new media segment.
//   - Frames which arrive before the first main track keyframe are held until
//     the keyframe arrives. Frames sharing its timestamp (e.g. audio frames of
//     the same instant) join the media segment it opens; strictly older frames
//     are dropped and ErrChunkFrameBeforeKeyframe is reported.
type chunkWriter struct {
	ch      <-chan *frame
	closed  <-chan struct{}
	options *BlockWriterOptions
	w       *writerWithSizeCount

	started        bool
	origin         int64
	chunkStart     int64
	frames         []*frame
	pending        []*frame // Frames received before the first media segment.
	overflow       bool
	maxReported    bool
	preKeyReported bool
}

func (c *chunkWriter) run() {
	for {
		select {
		case <-c.closed:
			c.finalize()
			c.w.Close()
			return
		case f := <-c.ch:
			if err := c.add(f); err != nil {
				if c.options.onFatal != nil {
					c.options.onFatal(err)
				}
				c.w.Close()
				return
			}
		}
	}
}

func (c *chunkWriter) notifyError(err error) {
	if c.options.onError != nil {
		c.options.onError(err)
	}
}

func (c *chunkWriter) add(f *frame) error {
	isMainKeyframe := f.trackNumber == c.options.mainTrackNumber && f.keyframe

	if !c.started {
		if !isMainKeyframe {
			// A media segment must start with a main track keyframe. Hold the
			// frame until then rather than dropping it: it usually shares its
			// timestamp with the opening keyframe (e.g. audio frames of the
			// same instant arrive first when the audio track is declared
			// before the video track).
			// Only the frames at the newest timestamp can still share the
			// keyframe timestamp; older ones are dropped to bound the buffer.
			if n := len(c.pending); n > 0 && c.pending[n-1].timestamp < f.timestamp {
				c.pending = c.pending[:0]
				if !c.preKeyReported {
					c.notifyError(ErrChunkFrameBeforeKeyframe)
					c.preKeyReported = true
				}
			}
			c.pending = append(c.pending, f)
			return nil
		}
		if len(c.pending) > 0 {
			// Frames strictly older than the opening keyframe cannot belong to
			// any independently decodable media segment.
			kept := c.pending[:0]
			for _, pf := range c.pending {
				if pf.timestamp >= f.timestamp {
					kept = append(kept, pf)
				} else if !c.preKeyReported {
					c.notifyError(ErrChunkFrameBeforeKeyframe)
					c.preKeyReported = true
				}
			}
			c.pending = kept
		}
		c.started = true
		c.origin = f.timestamp
		c.chunkStart = f.timestamp
		// The opening keyframe must be the first block of its media segment.
		c.frames = append([]*frame{f}, c.pending...)
		c.pending = nil
		return nil
	}

	if isMainKeyframe {
		tc := f.timestamp - c.chunkStart
		// A main track keyframe starts a new media segment when the current
		// one is long enough, when the keyframe is beyond the SimpleBlock
		// timecode range, or when the previous frames were already dropped
		// for the range overflow.
		if c.overflow || tc > 0x7FFF || tc >= c.options.minClusterDuration {
			// Frames with the same timestamp as the keyframe were delivered
			// before the keyframe (e.g. an audio track declared before the
			// video track) but belong to the new segment. Frames before it
			// stay in the current segment.
			var old, tied []*frame
			for _, pf := range c.frames {
				if pf.timestamp < f.timestamp {
					old = append(old, pf)
				} else {
					tied = append(tied, pf)
				}
			}
			if len(old) > 0 {
				if err := c.flush(c.chunkStart, f.timestamp, old); err != nil {
					return err
				}
				c.chunkStart = f.timestamp
				// The keyframe must be the first block of its media segment.
				c.frames = append([]*frame{f}, tied...)
				c.maxReported = false
				c.overflow = false
				return nil
			}
			// No frame precedes the keyframe in the current segment (e.g.
			// minClusterDuration == 0 and the keyframe has the same timestamp
			// as the segment start). Keep the frames in one segment; it still
			// starts with the keyframe which opened it.
			c.frames = append(c.frames, f)
			c.overflow = false
			return nil
		}
	}

	if c.overflow {
		// Waiting for a main track keyframe to open an independently
		// decodable media segment.
		return nil
	}

	tc := f.timestamp - c.chunkStart
	switch {
	case tc < 0:
		// The frame is older than the media segment start. Writing it with a
		// negative relative timecode would place content before the segment
		// start and make the time ranges of the segments overlap.
		c.notifyError(ErrIgnoreOldFrame)
		return nil
	case tc > 0x7FFF:
		// Waiting for the next keyframe would overflow the signed 16-bit
		// SimpleBlock timecode. Drop frames until the next main track
		// keyframe instead of writing a corrupt cluster.
		c.notifyError(ErrChunkFrameOutOfRange)
		c.overflow = true
		return nil
	}

	c.frames = append(c.frames, f)

	if !c.maxReported && tc > c.options.maxClusterDuration {
		// The keyframe interval is longer than the configured maximum cluster
		// duration. The segment is kept open to remain independently
		// decodable and the condition is reported.
		c.notifyError(ErrChunkDurationExceeded)
		c.maxReported = true
	}
	return nil
}

func (c *chunkWriter) finalize() {
	if !c.started {
		if len(c.pending) > 0 && !c.preKeyReported {
			c.notifyError(ErrChunkFrameBeforeKeyframe)
		}
		c.pending = nil
		return
	}
	if len(c.frames) == 0 {
		return
	}
	end := c.frames[len(c.frames)-1].timestamp
	if err := c.flush(c.chunkStart, end, c.frames); err != nil {
		if c.options.onFatal != nil {
			c.options.onFatal(err)
		}
	}
	c.frames = nil
}

// flush serializes one media segment and delivers it to the handler.
func (c *chunkWriter) flush(startTime, endTime int64, frames []*frame) error {
	clusterTimecode := startTime - c.origin
	blocks := make([]ebml.Block, 0, len(frames))
	for _, f := range frames {
		tc := f.timestamp - startTime
		if tc < 0 || tc > 0x7FFF {
			// Defensive: frames are range-checked in add. Dropping here keeps
			// the cluster valid rather than writing an out-of-range timecode.
			c.notifyError(ErrChunkFrameOutOfRange)
			continue
		}
		blocks = append(blocks, ebml.Block{
			TrackNumber: f.trackNumber,
			Timecode:    int16(tc),
			Keyframe:    f.keyframe,
			Data:        [][]byte{f.b},
		})
	}
	if len(blocks) == 0 {
		return nil
	}

	v := struct {
		Cluster chunkCluster `ebml:"Cluster"`
	}{
		Cluster: chunkCluster{
			Timecode:    uint64(clusterTimecode),
			SimpleBlock: blocks,
		},
	}

	var buf bytes.Buffer
	if err := ebml.Marshal(&v, &buf, c.options.marshalOpts...); err != nil {
		return err
	}
	data := buf.Bytes()

	if _, err := c.w.Write(data); err != nil {
		return err
	}
	return c.options.chunkHandler(Chunk{
		Data:      data,
		StartTime: startTime,
		EndTime:   endTime,
	})
}
