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
	"testing"
)

func TestTrackStatsAdd(t *testing.T) {
	s := &trackStats{trackUID: 7}
	if s.hasFrame {
		t.Fatal("new trackStats must not report a frame")
	}

	s.add(100, 4)
	s.add(140, 2)
	s.add(200, 1)

	if s.frameCnt != 3 {
		t.Errorf("frameCnt: got %d, want 3", s.frameCnt)
	}
	if s.payload != 7 {
		t.Errorf("payload: got %d, want 7", s.payload)
	}
	if s.firstTime != 100 || s.lastTime != 200 {
		t.Errorf("time range: got %d..%d, want 100..200", s.firstTime, s.lastTime)
	}
}

func TestTrackStatsDurationNanos(t *testing.T) {
	// No frames: duration must be zero regardless of TimecodeScale.
	empty := &trackStats{}
	if got := empty.durationNanos(1000000); got != 0 {
		t.Errorf("empty duration: got %d, want 0", got)
	}

	// Default 1 ms TimecodeScale: 150 units -> 150,000,000 ns.
	s := &trackStats{}
	s.add(100, 1)
	s.add(250, 1)
	if got := s.durationNanos(1000000); got != 150000000 {
		t.Errorf("duration (1ms scale): got %d, want 150000000", got)
	}

	// A different TimecodeScale must still produce an exact integer result.
	// 150 units * 2,500 ns -> 375,000 ns.
	if got := s.durationNanos(2500); got != 375000 {
		t.Errorf("duration (custom scale): got %d, want 375000", got)
	}
}

func TestFormatDurationNanos(t *testing.T) {
	cases := map[string]struct {
		nanos int64
		want  string
	}{
		"Zero":              {0, "00:00:00.000000000"},
		"Nanoseconds":       {42, "00:00:00.000000042"},
		"Microseconds":      {123456, "00:00:00.000123456"},
		"Milliseconds":      {123456789, "00:00:00.123456789"},
		"Seconds":           {1*1e9 + 500000000, "00:00:01.500000000"},
		"Minutes":           {75*1e9 + 250000000, "00:01:15.250000000"},
		"Hours":             {3723*1e9 + 1, "01:02:03.000000001"},
		"HoursRollOver":     {3723 * 1e9, "01:02:03.000000000"},
		"MoreThanTwoDigits": {100 * 3600 * 1e9, "100:00:00.000000000"},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			if got := formatDurationNanos(c.nanos); got != c.want {
				t.Errorf("formatDurationNanos(%d): got %q, want %q", c.nanos, got, c.want)
			}
		})
	}
}

func TestExtractTrackUID(t *testing.T) {
	type tagged struct {
		TrackNumber uint64 `ebml:"TrackNumber"`
		TrackUID    uint64 `ebml:"TrackUID"`
	}
	type untagged struct {
		TrackNumber uint64
		TrackUID    uint64
	}
	type noUID struct {
		TrackNumber uint64 `ebml:"TrackNumber"`
	}

	t.Run("TaggedStruct", func(t *testing.T) {
		uid, ok := extractTrackUID(&tagged{TrackNumber: 1, TrackUID: 42})
		if !ok || uid != 42 {
			t.Errorf("got (%d, %v), want (42, true)", uid, ok)
		}
	})
	t.Run("UntaggedStruct", func(t *testing.T) {
		// mkvcore.TrackEntry uses field names without ebml tags.
		uid, ok := extractTrackUID(untagged{TrackNumber: 1, TrackUID: 99})
		if !ok || uid != 99 {
			t.Errorf("got (%d, %v), want (99, true)", uid, ok)
		}
	})
	t.Run("MissingField", func(t *testing.T) {
		if _, ok := extractTrackUID(&noUID{TrackNumber: 1}); ok {
			t.Error("expected ok=false for TrackEntry without TrackUID")
		}
	})
	t.Run("NilPointer", func(t *testing.T) {
		var p *tagged
		if _, ok := extractTrackUID(p); ok {
			t.Error("expected ok=false for nil pointer")
		}
	})
	t.Run("NonStruct", func(t *testing.T) {
		if _, ok := extractTrackUID(123); ok {
			t.Error("expected ok=false for non-struct")
		}
	})
}

func TestExtractTimecodeScale(t *testing.T) {
	type info struct {
		TimecodeScale uint64 `ebml:"TimecodeScale"`
		MuxingApp     string `ebml:"MuxingApp,omitempty"`
	}
	t.Run("Present", func(t *testing.T) {
		scale, ok := extractTimecodeScale(&info{TimecodeScale: 2500})
		if !ok || scale != 2500 {
			t.Errorf("got (%d, %v), want (2500, true)", scale, ok)
		}
	})
	t.Run("Absent", func(t *testing.T) {
		if _, ok := extractTimecodeScale(&struct{}{}); ok {
			t.Error("expected ok=false for struct without TimecodeScale")
		}
	})
	t.Run("Nil", func(t *testing.T) {
		if _, ok := extractTimecodeScale(nil); ok {
			t.Error("expected ok=false for nil info")
		}
	})
}

func TestMarshalTrackStats(t *testing.T) {
	s1 := &trackStats{trackUID: 111}
	s1.add(0, 4)
	s1.add(33, 1)
	s2 := &trackStats{trackUID: 222}
	s2.add(10, 2)
	stats := map[uint64]*trackStats{
		111: s1,
		222: s2,
	}

	tags := marshalTrackStats(stats, []uint64{111, 222}, 1000000)
	if len(tags.Tag) != 2 {
		t.Fatalf("expected 2 Tag elements, got %d", len(tags.Tag))
	}

	// Order follows the input UID order.
	if got := tags.Tag[0].Targets.TagTrackUID; len(got) != 1 || got[0] != 111 {
		t.Errorf("first Tag UID: got %v, want [111]", got)
	}
	if tags.Tag[0].Targets.TargetTypeValue != 6 || tags.Tag[0].Targets.TargetType != "track" {
		t.Errorf("unexpected Targets: %+v", tags.Tag[0].Targets)
	}

	want := map[uint64]map[string]string{
		111: {
			"NUMBER_OF_FRAMES": "2",
			"NUMBER_OF_BYTES":  "5",
			"DURATION":         "00:00:00.033000000",
		},
		222: {
			"NUMBER_OF_FRAMES": "1",
			"NUMBER_OF_BYTES":  "2",
			"DURATION":         "00:00:00.000000000",
		},
	}
	for _, tg := range tags.Tag {
		uid := tg.Targets.TagTrackUID[0]
		if len(tg.SimpleTag) != 3 {
			t.Fatalf("track %d: expected 3 SimpleTags, got %d", uid, len(tg.SimpleTag))
		}
		for _, st := range tg.SimpleTag {
			if st.TagString != want[uid][st.TagName] {
				t.Errorf("track %d %s: got %q, want %q", uid, st.TagName, st.TagString, want[uid][st.TagName])
			}
		}
	}
}

func TestMarshalTrackStatsEmptyTracks(t *testing.T) {
	// A track that never received a frame still gets zeroed statistics.
	tags := marshalTrackStats(map[uint64]*trackStats{}, []uint64{5}, 1000000)
	if len(tags.Tag) != 1 {
		t.Fatalf("expected 1 Tag, got %d", len(tags.Tag))
	}
	tg := tags.Tag[0]
	if tg.Targets.TagTrackUID[0] != 5 {
		t.Errorf("UID: got %d, want 5", tg.Targets.TagTrackUID[0])
	}
	values := map[string]string{}
	for _, st := range tg.SimpleTag {
		values[st.TagName] = st.TagString
	}
	if values["NUMBER_OF_FRAMES"] != "0" || values["NUMBER_OF_BYTES"] != "0" {
		t.Errorf("expected zeroed counters, got %v", values)
	}
	if values["DURATION"] != "00:00:00.000000000" {
		t.Errorf("expected zero duration, got %q", values["DURATION"])
	}
}
