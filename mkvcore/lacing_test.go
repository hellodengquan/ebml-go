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
	"testing"

	"github.com/at-wat/ebml-go"
)

func TestSelectLacingMode(t *testing.T) {
	frames := func(sizes ...int) [][]byte {
		var f [][]byte
		for _, s := range sizes {
			f = append(f, bytes.Repeat([]byte{0xAA}, s))
		}
		return f
	}
	cases := map[string]struct {
		frames [][]byte
		expect ebml.LacingMode
	}{
		"Empty":        {nil, ebml.LacingNo},
		"Single":       {frames(10), ebml.LacingNo},
		"Fixed":        {frames(10, 10, 10), ebml.LacingFixed},
		"XiphSmall":    {frames(10, 11, 12), ebml.LacingXiph},
		"XiphLarge":    {frames(255, 100, 510, 40), ebml.LacingXiph},
		"EBML":         {frames(800, 500, 100), ebml.LacingEBML},
		"EBMLEqualLen": {frames(800, 800, 700), ebml.LacingEBML},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			if got := selectLacingMode(c.frames); got != c.expect {
				t.Errorf("Expected lacing mode: %v, got: %v", c.expect, got)
			}
		})
	}
}

func TestSelectLacingMode_Smallest(t *testing.T) {
	// The selected representation must not be larger than the alternatives.
	for _, sizeList := range [][]int{
		{10, 11, 12},
		{255, 100, 510, 40},
		{800, 500, 100},
		{1, 2, 3, 4, 5, 6, 7, 8},
		{1000, 1000, 999, 1000, 998},
	} {
		var frames [][]byte
		for _, s := range sizeList {
			frames = append(frames, bytes.Repeat([]byte{0xAA}, s))
		}
		mode := selectLacingMode(frames)
		selected := lacingHeaderSize(mode, frames)
		for _, other := range []ebml.LacingMode{ebml.LacingXiph, ebml.LacingEBML} {
			if other == mode {
				continue
			}
			if n := lacingHeaderSize(other, frames); n < selected {
				t.Errorf("Frames %v: selected mode %v (%d bytes) is larger than %v (%d bytes)",
					sizeList, mode, selected, other, n)
			}
		}
	}
}

// lacingHeaderSize calculates the lacing header size for a test comparison.
func lacingHeaderSize(mode ebml.LacingMode, frames [][]byte) int {
	switch mode {
	case ebml.LacingNo:
		return 0
	case ebml.LacingFixed:
		return 1
	case ebml.LacingXiph:
		n := 1
		for _, f := range frames[:len(frames)-1] {
			n += len(f)/0xFF + 1
		}
		return n
	case ebml.LacingEBML:
		n := 1 + idLength(uint64(len(frames[0])))
		prev := int64(len(frames[0]))
		for _, f := range frames[1 : len(frames)-1] {
			cur := int64(len(f))
			n += vintLength(cur - prev)
			prev = cur
		}
		return n
	}
	return 0
}

func TestLaceFrameDurationTicks(t *testing.T) {
	cases := map[string]struct {
		durationNs    uint64
		timecodeScale uint64
		expect        int64
	}{
		"1msTo1ms":           {1000000, 1000000, 1},
		"20msTo1ms":          {20000000, 1000000, 20},
		"20msTo0.1ms":        {20000000, 100000, 200},
		"33333333nsTo1ms":    {33333333, 1000000, 33},
		"ZeroDuration":       {0, 1000000, 0},
		"ZeroScale":          {20000000, 0, 0},
		"TooLongForTimecode": {0x8000 * 1000000, 1000000, 0},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			if got := laceFrameDurationTicks(c.durationNs, c.timecodeScale); got != c.expect {
				t.Errorf("Expected %d ticks, got: %d", c.expect, got)
			}
		})
	}
}

func TestSegmentTimecodeScale(t *testing.T) {
	cases := map[string]struct {
		info   interface{}
		expect uint64
	}{
		"Nil": {nil, defaultTimecodeScale},
		"Struct": {
			&struct {
				TimecodeScale uint64
			}{TimecodeScale: 100000},
			100000,
		},
		"Map": {
			map[string]interface{}{"TimestampScale": uint64(100000)},
			100000,
		},
		"Empty": {
			&struct{}{},
			defaultTimecodeScale,
		},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			if got := segmentTimecodeScale(c.info); got != c.expect {
				t.Errorf("Expected %d, got: %d", c.expect, got)
			}
		})
	}
}

func TestTrackDefaultDuration(t *testing.T) {
	cases := map[string]struct {
		trackEntry interface{}
		expect     uint64
	}{
		"Struct": {
			&TrackEntry{DefaultDuration: 20000000},
			20000000,
		},
		"Map": {
			map[string]interface{}{"DefaultDuration": uint64(20000000)},
			20000000,
		},
		"Zero": {
			&TrackEntry{},
			0,
		},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			if got := trackDefaultDuration(c.trackEntry); got != c.expect {
				t.Errorf("Expected %d, got: %d", c.expect, got)
			}
		})
	}
}
