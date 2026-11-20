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
	"fmt"
	"io"
	"sync"
)

// BlockInterceptor is a interface of block stream muxer.
type BlockInterceptor interface {
	// Intercept reads blocks of each track, filters, and writes.
	Intercept(r []BlockReader, w []BlockWriter)
}

// MustBlockInterceptor panics if creation of a BlockInterceptor fails, such as
// when the NewMultiTrackBlockSorter function fails.
func MustBlockInterceptor(interceptor BlockInterceptor, err error) BlockInterceptor {
	if err != nil {
		panic(err)
	}
	return interceptor
}

type filterWriter struct {
	trackNumber uint64
	ch          chan *frame
}

type filterReader struct {
	ch chan *frame
}

func (w *filterWriter) Write(keyframe bool, timestamp int64, b []byte) (int, error) {
	w.ch <- &frame{
		trackNumber: w.trackNumber,
		keyframe:    keyframe,
		timestamp:   timestamp,
		b:           b,
	}
	return len(b), nil
}

func (w *filterWriter) WriteBlockGroup(keyframe bool, timestamp, duration int64, b []byte) (int, error) {
	if duration < 0 {
		return 0, ErrInvalidBlockDuration
	}
	w.ch <- &frame{
		trackNumber: w.trackNumber,
		keyframe:    keyframe,
		timestamp:   timestamp,
		b:           b,
		duration:    duration,
		blockGroup:  true,
	}
	return len(b), nil
}

func (r *filterReader) Read() ([]byte, bool, int64, error) {
	frame, ok := <-r.ch
	if !ok {
		return nil, false, 0, io.EOF
	}
	return frame.b, frame.keyframe, frame.timestamp, nil
}

// ReadBlockGroup reads a frame with its block group information.
// It also returns frames written by Write; in that case duration is 0.
func (r *filterReader) ReadBlockGroup() ([]byte, bool, int64, int64, error) {
	frame, ok := <-r.ch
	if !ok {
		return nil, false, 0, 0, io.EOF
	}
	return frame.b, frame.keyframe, frame.timestamp, frame.duration, nil
}

// readFrame reads the whole frame including the internal block group flag.
func (r *filterReader) readFrame() (*frame, error) {
	frame, ok := <-r.ch
	if !ok {
		return nil, io.EOF
	}
	return frame, nil
}

// frameReader is implemented by internal readers that expose the whole frame,
// preserving information which is not a part of the public BlockReader API,
// e.g. whether a duration-less frame was written as a BlockGroup.
type frameReader interface {
	readFrame() (*frame, error)
}

func (r *filterReader) close() {
	close(r.ch)
}

// BlockSorterRule is a type of BlockSorter behaviour for outdated frame.
type BlockSorterRule int

// List of BlockSorterRules.
const (
	BlockSorterDropOutdated BlockSorterRule = iota
	BlockSorterWriteOutdated
)

// MultiTrackBlockSorterOption configures a MultiTrackBlockSorterOptions.
type MultiTrackBlockSorterOption func(*MultiTrackBlockSorterOptions) error

// MultiTrackBlockSorterOptions stores options for BlockWriter.
type MultiTrackBlockSorterOptions struct {
	maxDelayedPackets int
	rule              BlockSorterRule
	maxTimescaleDelay int64
}

// WithMaxDelayedPackets set the maximum number of packets that may be delayed
// within each track.
func WithMaxDelayedPackets(maxDelayedPackets int) MultiTrackBlockSorterOption {
	return func(o *MultiTrackBlockSorterOptions) error {
		o.maxDelayedPackets = maxDelayedPackets
		return nil
	}
}

// WithSortRule set the sort rule to apply to how packet ordering should be
// treated within the webm container.
func WithSortRule(rule BlockSorterRule) MultiTrackBlockSorterOption {
	return func(o *MultiTrackBlockSorterOptions) error {
		o.rule = rule
		return nil
	}
}

// WithMaxTimescaleDelay set the maximum allowed delay between tracks for a
// given timescale.
func WithMaxTimescaleDelay(maxTimescaleDelay int64) MultiTrackBlockSorterOption {
	return func(o *MultiTrackBlockSorterOptions) error {
		o.maxTimescaleDelay = maxTimescaleDelay
		return nil
	}
}

// NewMultiTrackBlockSorter creates BlockInterceptor, which sorts blocks on
// multiple tracks by timestamp. Either WithMaxDelayedPackets or
// WithMaxTimescaleDelay must be specified. If both are specified, then the
// first rule that is satisfied causes the packets to get written (thus a
// backlog of a max packets or max time scale will cause any older packets than
// the one satisfying the rule to be discarded). The index of TrackEntry sorts
// blocks with the same timestamp. Place the audio track before the video track
// to meet WebM Interceptor Guidelines.
func NewMultiTrackBlockSorter(opts ...MultiTrackBlockSorterOption) (BlockInterceptor, error) {
	applyOptions := []MultiTrackBlockSorterOption{
		WithMaxDelayedPackets(0),
		WithSortRule(BlockSorterDropOutdated),
		WithMaxTimescaleDelay(0),
	}
	applyOptions = append(applyOptions, opts...)

	options := &MultiTrackBlockSorterOptions{}
	for _, o := range applyOptions {
		if err := o(options); err != nil {
			return nil, err
		}
	}

	if options.maxDelayedPackets == 0 && options.maxTimescaleDelay == 0 {
		return nil, fmt.Errorf("must specify either WithMaxDelayedPackets(...) or WithMaxTimescaleDelay(...) with a non-0 value")
	}

	return &multiTrackBlockSorter{options: *options}, nil
}

type multiTrackBlockSorter struct {
	options MultiTrackBlockSorterOptions
}

func (s *multiTrackBlockSorter) Intercept(r []BlockReader, w []BlockWriter) {
	var wg sync.WaitGroup
	wg.Add(len(r))

	ch := make(chan *frame)
	for i, r := range r {
		fr, isFrameReader := r.(frameReader)
		gr, isGroupReader := r.(BlockGroupReader)
		go func(i int, r BlockReader, fr frameReader, gr BlockGroupReader, hasFrame, hasGroup bool) {
			for {
				var err error
				f := &frame{trackNumber: uint64(i)}
				switch {
				case hasFrame:
					// Fast path for the internal reader: it preserves the
					// block group flag even when the duration is 0.
					var ff *frame
					if ff, err = fr.readFrame(); err == nil {
						*f = *ff
						f.trackNumber = uint64(i)
					}
				case hasGroup:
					var duration int64
					f.b, f.keyframe, f.timestamp, duration, err = gr.ReadBlockGroup()
					// Frames carrying a BlockDuration must be written back as
					// a BlockGroup to keep the duration.
					f.duration = duration
					f.blockGroup = duration != 0
				default:
					f.b, f.keyframe, f.timestamp, err = r.Read()
				}
				if err != nil {
					wg.Done()
					return
				}
				ch <- f
			}
		}(i, r, fr, gr, isFrameReader, isGroupReader)
	}

	closed := make(chan struct{})
	go func() {
		wg.Wait()
		close(closed)
	}()

	var tDone int64
	buf := make([]*frameBuffer, len(r))
	for i := range buf {
		buf[i] = &frameBuffer{}
	}

	write := func(f *frame) {
		if f.blockGroup {
			if gw, ok := w[f.trackNumber].(BlockGroupWriter); ok {
				_, _ = gw.WriteBlockGroup(f.keyframe, f.timestamp, f.duration, f.b)
				return
			}
		}
		_, _ = w[f.trackNumber].Write(f.keyframe, f.timestamp, f.b)
	}

	flush := func(all bool) {
		nChReq := 1
		if !all {
			nChReq = len(r)
		}
		for {
			var largestTimestampDelta int64
			var tOldest int64
			var tNewest int64
			var nCh, nMax int
			var bOldest *frameBuffer
			var bNewest *frameBuffer
			for _, b := range buf {
				if n := b.Size(); n > 0 {
					nCh++
					if f := b.Head(); f.timestamp < tOldest || bOldest == nil {
						tOldest = f.timestamp
						bOldest = b
					}
					if f := b.Tail(); f.timestamp > tNewest || bNewest == nil {
						tNewest = f.timestamp
						bNewest = b

						tDiff := tNewest - tOldest
						if tDiff > largestTimestampDelta {
							largestTimestampDelta = tDiff
						}
					}
					if n > nMax {
						nMax = n
					}
				}
			}
			if nCh >= nChReq ||
				(nMax > s.options.maxDelayedPackets && s.options.maxDelayedPackets != 0) ||
				(largestTimestampDelta > s.options.maxTimescaleDelay && s.options.maxTimescaleDelay != 0) {
				fOldest := bOldest.Pop()
				write(fOldest)
				if fOldest.timestamp > tDone {
					tDone = fOldest.timestamp
				}
			} else {
				break
			}
		}
	}

	for {
		select {
		case d := <-ch:
			// Frames of a BlockGroup carry their own display duration and
			// sparse event time, e.g. subtitles. They must not be dropped even
			// when they are submitted after the interleaver has already
			// advanced past their start time.
			if d.blockGroup || d.timestamp >= tDone || s.options.rule == BlockSorterWriteOutdated {
				buf[d.trackNumber].Push(d)
				flush(false)
			}
		case <-closed:
			flush(true)
			return
		}
	}
}
