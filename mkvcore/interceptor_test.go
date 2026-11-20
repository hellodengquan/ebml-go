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
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestMultiTrackBlockSorterMaxPackets(t *testing.T) {
	for name, c := range map[string]struct {
		rule     BlockSorterRule
		expected []frame
	}{
		"DropOutdated": {
			BlockSorterDropOutdated,
			[]frame{
				{trackNumber: 1, keyframe: false, timestamp: 9, b: []byte{3}},
				{trackNumber: 0, keyframe: false, timestamp: 10, b: []byte{1}},
				{trackNumber: 0, keyframe: false, timestamp: 11, b: []byte{2}},
				{trackNumber: 0, keyframe: false, timestamp: 16, b: []byte{4}},
				{trackNumber: 0, keyframe: false, timestamp: 17, b: []byte{5}},
				{trackNumber: 0, keyframe: false, timestamp: 18, b: []byte{6}},
				{trackNumber: 1, keyframe: false, timestamp: 18, b: []byte{8}},
			},
		},
		"WriteOutdated": {
			BlockSorterWriteOutdated,
			[]frame{
				{trackNumber: 1, keyframe: false, timestamp: 9, b: []byte{3}},
				{trackNumber: 0, keyframe: false, timestamp: 10, b: []byte{1}},
				{trackNumber: 0, keyframe: false, timestamp: 11, b: []byte{2}},
				{trackNumber: 0, keyframe: false, timestamp: 16, b: []byte{4}},
				{trackNumber: 1, keyframe: false, timestamp: 15, b: []byte{7}},
				{trackNumber: 0, keyframe: false, timestamp: 17, b: []byte{5}},
				{trackNumber: 0, keyframe: false, timestamp: 18, b: []byte{6}},
				{trackNumber: 1, keyframe: false, timestamp: 18, b: []byte{8}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			wg := sync.WaitGroup{}

			f, err := NewMultiTrackBlockSorter(WithMaxDelayedPackets(2), WithSortRule(c.rule))
			if err != nil {
				t.Errorf("Failed to create MultiTrackBlockSorter: %v", err)
			}

			chOut := make(chan *frame)
			ch := []chan *frame{
				make(chan *frame),
				make(chan *frame),
			}

			w := []BlockWriter{
				&filterWriter{0, chOut},
				&filterWriter{1, chOut},
			}
			r := []BlockReader{
				&filterReader{ch[0]},
				&filterReader{ch[1]},
			}

			var frames []frame
			wg.Add(1)
			go func() {
				for f := range chOut {
					frames = append(frames, *f)
				}
				wg.Done()
			}()

			go func() {
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 10, b: []byte{1}}
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 11, b: []byte{2}}
				time.Sleep(time.Millisecond)
				ch[1] <- &frame{trackNumber: 1, keyframe: false, timestamp: 9, b: []byte{3}}
				time.Sleep(time.Millisecond)
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 16, b: []byte{4}}
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 17, b: []byte{5}}
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 18, b: []byte{6}}
				time.Sleep(time.Millisecond)
				ch[1] <- &frame{trackNumber: 1, keyframe: false, timestamp: 15, b: []byte{7}} // drop due to maxDelay=2
				ch[1] <- &frame{trackNumber: 1, keyframe: false, timestamp: 18, b: []byte{8}}
				close(ch[0])
				close(ch[1])
			}()

			f.Intercept(r, w)

			close(chOut)
			wg.Wait()

			if !reflect.DeepEqual(c.expected, frames) {
				t.Errorf("Unexpected sort result, \nexpected: %v, \n     got: %v", c.expected, frames)
			}
		})
	}
}

func TestMultiTrackBlockSorterTimescale(t *testing.T) {
	for name, c := range map[string]struct {
		rule     BlockSorterRule
		expected []frame
	}{
		"DropOutdated": {
			BlockSorterDropOutdated,
			[]frame{
				{trackNumber: 0, keyframe: false, timestamp: 100, b: []byte{1}},
				{trackNumber: 0, keyframe: false, timestamp: 110, b: []byte{2}},
				{trackNumber: 1, keyframe: false, timestamp: 150, b: []byte{3}},
				{trackNumber: 0, keyframe: false, timestamp: 160, b: []byte{4}},
				{trackNumber: 0, keyframe: false, timestamp: 170, b: []byte{5}},
				{trackNumber: 0, keyframe: false, timestamp: 200, b: []byte{6}},
				{trackNumber: 1, keyframe: false, timestamp: 210, b: []byte{8}},
			},
		},
		"WriteOutdated": {
			BlockSorterWriteOutdated,
			[]frame{
				{trackNumber: 0, keyframe: false, timestamp: 100, b: []byte{1}},
				{trackNumber: 0, keyframe: false, timestamp: 110, b: []byte{2}},
				{trackNumber: 1, keyframe: false, timestamp: 150, b: []byte{3}},
				{trackNumber: 1, keyframe: false, timestamp: 90, b: []byte{7}},
				{trackNumber: 0, keyframe: false, timestamp: 160, b: []byte{4}},
				{trackNumber: 0, keyframe: false, timestamp: 170, b: []byte{5}},
				{trackNumber: 0, keyframe: false, timestamp: 200, b: []byte{6}},
				{trackNumber: 1, keyframe: false, timestamp: 210, b: []byte{8}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			wg := sync.WaitGroup{}

			f, err := NewMultiTrackBlockSorter(WithMaxTimescaleDelay(100), WithSortRule(c.rule))
			if err != nil {
				t.Errorf("Failed to create MultiTrackBlockSorter: %v", err)
			}

			chOut := make(chan *frame)
			ch := []chan *frame{
				make(chan *frame),
				make(chan *frame),
			}

			w := []BlockWriter{
				&filterWriter{0, chOut},
				&filterWriter{1, chOut},
			}
			r := []BlockReader{
				&filterReader{ch[0]},
				&filterReader{ch[1]},
			}

			var frames []frame
			wg.Add(1)
			go func() {
				for f := range chOut {
					frames = append(frames, *f)
				}
				wg.Done()
			}()

			go func() {
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 100, b: []byte{1}}
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 110, b: []byte{2}}
				time.Sleep(time.Millisecond)
				ch[1] <- &frame{trackNumber: 1, keyframe: false, timestamp: 150, b: []byte{3}}
				time.Sleep(time.Millisecond)
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 160, b: []byte{4}}
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 170, b: []byte{5}}
				ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: 200, b: []byte{6}}
				time.Sleep(time.Millisecond)
				ch[1] <- &frame{trackNumber: 1, keyframe: false, timestamp: 90, b: []byte{7}} // maybe dropped due to WithMaxTimescaleDelay=100
				ch[1] <- &frame{trackNumber: 1, keyframe: false, timestamp: 210, b: []byte{8}}
				close(ch[0])
				close(ch[1])
			}()

			f.Intercept(r, w)

			close(chOut)
			wg.Wait()

			if !reflect.DeepEqual(c.expected, frames) {
				t.Errorf("Unexpected sort result, \nexpected: %v, \n     got: %v", c.expected, frames)
			}
		})
	}
}

func BenchmarkMultiTrackBlockSorter(b *testing.B) {
	f, err := NewMultiTrackBlockSorter(WithMaxDelayedPackets(2), WithSortRule(BlockSorterDropOutdated))
	if err != nil {
		b.Errorf("Failed to create MultiTrackBlockSorter: %v", err)
	}

	chOut := make(chan *frame)
	ch := []chan *frame{
		make(chan *frame),
		make(chan *frame),
	}

	w := []BlockWriter{
		&filterWriter{0, chOut},
		&filterWriter{1, chOut},
	}
	r := []BlockReader{
		&filterReader{ch[0]},
		&filterReader{ch[1]},
	}

	go func() {
		for out := range chOut {
			// Drain output channel
			_ = out
		}
	}()

	go func() {
		for i := 0; i < b.N; i++ {
			ch[0] <- &frame{trackNumber: 0, keyframe: false, timestamp: int64(i), b: []byte{1, 2, 3, 4}}
			ch[1] <- &frame{trackNumber: 1, keyframe: false, timestamp: int64(i) + 5, b: []byte{2, 3, 4, 5}}
		}
		close(ch[0])
		close(ch[1])
	}()

	b.ResetTimer()
	f.Intercept(r, w)

	close(chOut)
}

func TestMultiTrackBlockSorter_FailingOptions(t *testing.T) {
	errDummy := errors.New("an error")

	cases := map[string]struct {
		opts []MultiTrackBlockSorterOption
		err  error
	}{
		"SingleOptionPackets": {
			opts: []MultiTrackBlockSorterOption{
				WithMaxDelayedPackets(2),
			},
			err: nil,
		},
		"SingleOptionTimeScape": {
			opts: []MultiTrackBlockSorterOption{
				WithMaxTimescaleDelay(2),
			},
			err: nil,
		},
		"MultiOptionPacketsAndTimeScale": {
			opts: []MultiTrackBlockSorterOption{
				WithMaxDelayedPackets(2),
				WithMaxTimescaleDelay(100),
				WithSortRule(BlockSorterDropOutdated),
			},
			err: nil,
		},
		"FailingOption": {
			opts: []MultiTrackBlockSorterOption{
				WithSortRule(BlockSorterDropOutdated),
			},
			err: errDummy,
		},
		"ErroredOption": {
			opts: []MultiTrackBlockSorterOption{
				func(*MultiTrackBlockSorterOptions) error {
					return errDummy
				},
			},
			err: errDummy,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewMultiTrackBlockSorter(c.opts...)
			if c.err == nil && err != nil {
				t.Errorf("Expecting no error but got: '%v'", err)
			}
			if c.err != nil && err == nil {
				t.Errorf("Expected error but didn't get one: '%v'", name)
			}
		})
	}
}

type dummyInterceptor struct{}

func (dummyInterceptor) Intercept([]BlockReader, []BlockWriter) {
	panic("unimplemented")
}

func TestMustBlockInterceptor(t *testing.T) {
	t.Run("OK", func(t *testing.T) {
		i := &dummyInterceptor{}
		ret := MustBlockInterceptor(i, nil)
		if ret != i {
			t.Error("MustBlockInterceptor must return the interceptor on success")
		}
	})
	t.Run("Error", func(t *testing.T) {
		i := &dummyInterceptor{}
		err := errors.New("dummy error")

		defer func() {
			if r := recover(); r == nil {
				t.Error("MustBlockInterceptor must panic on failure")
			}
		}()

		_ = MustBlockInterceptor(i, err)
	})
}
