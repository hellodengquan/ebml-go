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

package mkvcore

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"

	"github.com/at-wat/ebml-go"
)

// ErrIgnoreOldFrame means that a frame has too old timestamp and ignored.
var ErrIgnoreOldFrame = errors.New("too old frame")

type blockWriter struct {
	trackNumber uint64
	f           chan *frame
	wg          *sync.WaitGroup
	fin         chan struct{}
}

type frame struct {
	trackNumber uint64
	keyframe    bool
	timestamp   int64
	b           []byte
}

func (w *blockWriter) Write(keyframe bool, timestamp int64, b []byte) (int, error) {
	w.f <- &frame{
		trackNumber: w.trackNumber,
		keyframe:    keyframe,
		timestamp:   timestamp,
		b:           b,
	}
	return len(b), nil
}

func (w *blockWriter) Close() error {
	w.wg.Done()

	// If it is the last writer, block until closing output writer.
	w.fin <- struct{}{}

	return nil
}

// TrackDescription stores track number and its TrackEntry struct.
type TrackDescription struct {
	TrackNumber uint64
	TrackEntry  interface{}
}

// NewSimpleBlockWriter creates BlockWriteCloser for each track specified as tracks argument.
// Blocks will be written to the writer as EBML SimpleBlocks.
// Given io.WriteCloser will be closed automatically; don't close it by yourself.
// Frames written to each track must be sorted by their timestamp.
//
// Use WithLacing to pack consecutive frames of a track into one block.
func NewSimpleBlockWriter(w0 io.WriteCloser, tracks []TrackDescription, opts ...BlockWriterOption) ([]BlockWriteCloser, error) {
	options := &BlockWriterOptions{
		BlockReadWriterOptions: BlockReadWriterOptions{
			onFatal: func(err error) {
				panic(err)
			},
		},
		ebmlHeader:         nil,
		segmentInfo:        nil,
		interceptor:        nil,
		seekHead:           false,
		minClusterDuration: 0,
		maxClusterDuration: 0x7FFF,
	}
	for _, o := range opts {
		if err := o.ApplyToBlockWriterOptions(options); err != nil {
			return nil, err
		}
	}

	// Validate Cues requirements
	var writerAt io.WriterAt
	if options.cuesReservedSize > 0 {
		if !options.seekHead {
			return nil, ErrCuesRequiresSeekHead
		}
		var ok bool
		writerAt, ok = w0.(io.WriterAt)
		if !ok {
			seeker, ok := w0.(io.WriteSeeker)
			if !ok {
				return nil, ErrCuesRequiresSeeker
			}
			writerAt = &writerAtBySeeker{
				WriteSeeker: seeker,
			}
		}
	}

	w := &writerWithSizeCount{w: w0}

	header := flexHeader{
		Header: options.ebmlHeader,
		Segment: flexSegment{
			Info: options.segmentInfo,
		},
	}
	for _, t := range tracks {
		header.Segment.Tracks.TrackEntry = append(header.Segment.Tracks.TrackEntry, t.TrackEntry)
	}

	// When Cues are enabled and segmentInfo supports it, set a placeholder
	// Duration so the marshaler includes the element in the output.
	// We'll overwrite it with the real value at finalization.
	if options.cuesReservedSize > 0 {
		if ds, ok := options.segmentInfo.(durationSettable); ok {
			ds.SetDuration(math.SmallestNonzeroFloat64)
		}
	}

	var durationElementPos uint64
	var segmentDataStart uint64
	if options.seekHead {
		var err error
		segmentDataStart, durationElementPos, err = setSeekHead(&header, options.cuesReservedSize > 0, options.marshalOpts...)
		if err != nil {
			return nil, err
		}
	}
	if err := ebml.Marshal(&header, w, options.marshalOpts...); err != nil {
		return nil, err
	}

	// Reserve space for Cues by writing a Void element
	var cuesReservedStart uint64
	var posAfterHeader uint64
	if options.cuesReservedSize > 0 {
		cuesReservedStart = uint64(w.Size())
		ve := voidElement(options.cuesReservedSize)
		if _, err := w.Write(ve); err != nil {
			return nil, err
		}
	}
	posAfterHeader = uint64(w.Size())

	w.Clear()

	ch := make(chan *frame)
	fin := make(chan struct{}, len(tracks)-1)
	wg := sync.WaitGroup{}
	var ws []BlockWriteCloser
	var fw []BlockWriter
	var fr []BlockReader

	for _, t := range tracks {
		wg.Add(1)
		var chSrc chan *frame
		if options.interceptor == nil {
			chSrc = ch
		} else {
			chSrc = make(chan *frame)
			fr = append(fr, &filterReader{chSrc})
			fw = append(fw, &filterWriter{t.TrackNumber, ch})
		}
		ws = append(ws, &blockWriter{
			trackNumber: t.TrackNumber,
			f:           chSrc,
			wg:          &wg,
			fin:         fin,
		})
	}

	filterFlushed := make(chan struct{})
	if options.interceptor != nil {
		go func() {
			options.interceptor.Intercept(fr, fw)
			close(filterFlushed)
		}()
	} else {
		close(filterFlushed)
	}

	closed := make(chan struct{})
	go func() {
		wg.Wait()
		for _, c := range fr {
			c.(*filterReader).close()
		}
		<-filterFlushed
		close(closed)
	}()

	go func() {
		const invalidTimestamp = int64(0x7FFFFFFFFFFFFFFF)
		tc0 := invalidTimestamp
		tc1 := invalidTimestamp
		lastTc := int64(0)

		// Cues tracking state
		runningPos := posAfterHeader
		var cuePoints []cuePoint

		// Lacing state. A track entry is present only when frame packing is
		// enabled and its frame duration can be represented in the timecode scale.
		lacingDurations := make(map[uint64]int64)
		timecodeScale := segmentTimecodeScale(options.segmentInfo)
		for _, t := range tracks {
			if !options.lacingEnabled(t.TrackNumber) {
				continue
			}
			if ticks := laceFrameDurationTicks(trackDefaultDuration(t.TrackEntry), timecodeScale); ticks > 0 {
				lacingDurations[t.TrackNumber] = ticks
			}
		}
		pending := make(map[uint64]*pendingLace)

		writeBlock := func(b *ebml.Block) bool {
			blk := struct {
				Block ebml.Block `ebml:"SimpleBlock"`
			}{
				Block: *b,
			}
			if err := ebml.Marshal(&blk, w, options.marshalOpts...); err != nil {
				if options.onFatal != nil {
					options.onFatal(err)
				}
				return false
			}
			return true
		}

		// flushLace writes the frames accumulated for the track as one block.
		flushLace := func(trackNumber uint64) bool {
			p := pending[trackNumber]
			if p == nil {
				return true
			}
			delete(pending, trackNumber)
			return writeBlock(&ebml.Block{
				TrackNumber: p.trackNumber,
				Timecode:    p.timecode,
				Keyframe:    p.keyframe,
				Lacing:      selectLacingMode(p.frames),
				Data:        p.frames,
			})
		}
		flushAllLace := func() bool {
			for _, t := range tracks {
				if !flushLace(t.TrackNumber) {
					return false
				}
			}
			return true
		}

		defer func() {
			// Flush the frames still pending before closing the last cluster.
			flushAllLace()

			// Finalize WebM
			if tc0 == invalidTimestamp {
				// No data written
				tc0 = 0
			}
			cluster := struct {
				Cluster simpleBlockCluster `ebml:"Cluster,size=unknown"`
			}{
				Cluster: simpleBlockCluster{
					Timecode: uint64(lastTc - tc0),
					PrevSize: uint64(w.Size()),
				},
			}
			if err := ebml.Marshal(&cluster, w, options.marshalOpts...); err != nil {
				if options.onFatal != nil {
					options.onFatal(err)
				}
			}

			// Write Cues to the reserved space
			if options.cuesReservedSize > 0 && len(cuePoints) > 0 {
				writeCuesToReserved(
					writerAt, cuePoints, options.marshalOpts,
					cuesReservedStart, options.cuesReservedSize,
					options.onFatal,
				)
			}

			// Overwrite the placeholder Duration with the real value.
			// Duration element layout: 2-byte ID (0x44 0x89) + 1-byte VINT (0x88) + 8-byte float64.
			if durationElementPos > 0 && writerAt != nil {
				duration := float64(lastTc - tc0)
				var buf [8]byte
				binary.BigEndian.PutUint64(buf[:], math.Float64bits(duration))
				if _, err := writerAt.WriteAt(buf[:], int64(durationElementPos)+3); err != nil {
					if options.onFatal != nil {
						options.onFatal(err)
					}
				}
			}

			w.Close()
			<-fin // read one data to release blocked Close()
		}()

	L_WRITE:
		for {
			select {
			case <-closed:
				break L_WRITE
			case f := <-ch:
				if tc0 == invalidTimestamp {
					tc0 = f.timestamp
				}
				lastTc = f.timestamp
				tc := f.timestamp - tc1
				if tc1 == invalidTimestamp ||
					tc >= options.maxClusterDuration ||
					(f.trackNumber == options.mainTrackNumber && tc >= options.minClusterDuration && f.keyframe) {
					// Flush pending laced frames into the previous cluster before
					// starting the new one.
					if !flushAllLace() {
						return
					}

					// Create new Cluster
					tc1 = f.timestamp
					tc = 0

					// Collect CuePoint before Clear
					if options.cuesReservedSize > 0 {
						runningPos += uint64(w.Size())
						cuePoints = append(cuePoints, cuePoint{
							CueTime: uint64(tc1 - tc0),
							CueTrackPositions: []cueTrackPosition{{
								CueTrack:           f.trackNumber,
								CueClusterPosition: runningPos - segmentDataStart,
							}},
						})
					}

					cluster := struct {
						Cluster simpleBlockCluster `ebml:"Cluster,size=unknown"`
					}{
						Cluster: simpleBlockCluster{
							Timecode: uint64(tc1 - tc0),
							PrevSize: uint64(w.Size()),
						},
					}
					w.Clear()
					if err := ebml.Marshal(&cluster, w, options.marshalOpts...); err != nil {
						if options.onFatal != nil {
							options.onFatal(err)
						}
						return
					}
				}
				if tc <= -0x7FFF {
					// Ignore too old frame
					if options.onError != nil {
						options.onError(ErrIgnoreOldFrame)
					}
					continue
				}

				if ticks, ok := lacingDurations[f.trackNumber]; ok {
					p := pending[f.trackNumber]
					if p != nil &&
						(len(p.frames) >= maxLacedFrames ||
							f.timestamp-p.lastTs != ticks ||
							f.keyframe != p.keyframe) {
						// The frame cannot be packed with the pending ones.
						if !flushLace(f.trackNumber) {
							return
						}
						p = nil
					}
					if p == nil {
						p = &pendingLace{
							trackNumber: f.trackNumber,
							timecode:    int16(tc),
							keyframe:    f.keyframe,
						}
						pending[f.trackNumber] = p
					}
					p.frames = append(p.frames, f.b)
					p.lastTs = f.timestamp
					continue
				}

				// Write SimpleBlock to the file
				if !writeBlock(&ebml.Block{
					TrackNumber: f.trackNumber,
					Timecode:    int16(tc),
					Keyframe:    f.keyframe,
					Data:        [][]byte{f.b},
				}) {
					return
				}
			}
		}
	}()

	return ws, nil
}

// voidElement creates an EBML Void element of exactly totalSize bytes.
// Always uses 8-byte VINT for simplicity; callers must ensure totalSize >= 9.
func voidElement(totalSize int) []byte {
	buf := make([]byte, totalSize)
	buf[0] = 0xEC // Void Element ID
	dataSize := uint64(totalSize - 9)
	buf[1] = 0x01
	buf[2] = byte(dataSize >> 48)
	buf[3] = byte(dataSize >> 40)
	buf[4] = byte(dataSize >> 32)
	buf[5] = byte(dataSize >> 24)
	buf[6] = byte(dataSize >> 16)
	buf[7] = byte(dataSize >> 8)
	buf[8] = byte(dataSize)
	return buf
}

// writeCuesToReserved marshals Cues and writes them into the reserved Void space.
func writeCuesToReserved(
	writerAt io.WriterAt,
	cuePoints []cuePoint,
	marshalOpts []ebml.MarshalOption,
	reservedStart uint64, reservedSize int,
	onFatal func(error),
) {
	var buf bytes.Buffer
	var cuesBytes []byte
	var remaining int

	for {
		cuesData := struct {
			Cues cues `ebml:"Cues"`
		}{
			Cues: cues{CuePoint: cuePoints},
		}
		buf.Reset()
		if err := ebml.Marshal(&cuesData, &buf, marshalOpts...); err != nil {
			if onFatal != nil {
				onFatal(err)
			}
			return
		}

		cuesBytes = buf.Bytes()
		remaining = reservedSize - len(cuesBytes)
		if remaining == 0 || remaining >= 9 {
			break
		}

		if len(cuePoints) == 1 {
			// The space is too small and no cue can be written.
			cuesBytes = nil
			break
		}

		// Cues don't fit, or leftover space is too small for a Void element
		// (need 9 bytes minimum: 1-byte ID + 8-byte VINT).
		// Downsample the cues to fit the space and retry.
		cuePoints2 := make([]cuePoint, 0, len(cuePoints)/2)
		for i := 0; i < len(cuePoints); i += 2 {
			cuePoints2 = append(cuePoints2, cuePoints[i])
		}
		cuePoints = cuePoints2
	}

	if remaining > 0 {
		cuesBytes = append(cuesBytes, voidElement(remaining)...)
	}
	if _, err := writerAt.WriteAt(cuesBytes, int64(reservedStart)); err != nil {
		if onFatal != nil {
			onFatal(err)
		}
	}
}
