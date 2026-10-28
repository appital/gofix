// Copyright (c) quickfixengine.org  All rights reserved.
//
// This file may be distributed under the terms of the quickfixengine.org
// license as defined by quickfixengine.org and appearing in the file
// LICENSE included in the packaging of this file.
//
// This file is provided AS IS with NO WARRANTY OF ANY KIND, INCLUDING
// THE WARRANTY OF DESIGN, MERCHANTABILITY AND FITNESS FOR A
// PARTICULAR PURPOSE.
//
// See http://www.quickfixengine.org/LICENSE for licensing information.
//
// Contact ask@quickfixengine.org if any conditions of this licensing
// are not clear to you.

package gofix

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepeatingGroup_Add(t *testing.T) {
	f := RepeatingGroup{template: GroupTemplate{GroupElement(1)}}

	var tests = []struct {
		expectedLen int
	}{
		{1},
		{2},
	}

	for _, test := range tests {
		g := f.Add()
		if f.Len() != test.expectedLen {
			t.Errorf("Expected %v groups, got %v", test.expectedLen, f.Len())
		}

		g.SetField(Tag(1), FIXString("hello"))

		if !f.groups[test.expectedLen-1].Has(Tag(1)) {
			t.Errorf("expected tag in group %v", test.expectedLen)
		}

		var v FIXString
		require.Nil(t, f.groups[test.expectedLen-1].GetField(Tag(1), &v))

		if string(v) != "hello" {
			t.Errorf("expected hello in group %v", test.expectedLen)
		}
	}
}

func TestRepeatingGroup_Write(t *testing.T) {
	f1 := RepeatingGroup{tag: 10, template: GroupTemplate{
		GroupElement(1),
		GroupElement(2),
	}}

	f1.Add().SetField(Tag(1), FIXString("hello"))

	f2 := RepeatingGroup{tag: 11, template: GroupTemplate{
		GroupElement(1),
		GroupElement(2),
	}}
	f2.Add().SetField(Tag(1), FIXString("hello")).SetField(Tag(2), FIXString("world"))

	f3 := RepeatingGroup{tag: 12, template: GroupTemplate{
		GroupElement(1),
		GroupElement(2),
	}}
	f3.Add().SetField(Tag(1), FIXString("hello"))
	f3.Add().SetField(Tag(1), FIXString("world"))

	f4 := RepeatingGroup{tag: 13, template: GroupTemplate{
		GroupElement(1),
		GroupElement(2),
	}}
	f4.Add().SetField(Tag(1), FIXString("hello")).SetField(Tag(2), FIXString("world"))
	f4.Add().SetField(Tag(1), FIXString("goodbye"))

	var tests = []struct {
		f        RepeatingGroup
		expected []byte
	}{
		{f1, []byte("10=1\x011=hello\x01")},
		{f2, []byte("11=1\x011=hello\x012=world\x01")},
		{f3, []byte("12=2\x011=hello\x011=world\x01")},
		{f4, []byte("13=2\x011=hello\x012=world\x011=goodbye\x01")},
	}

	for _, test := range tests {
		tvbytes := []byte{}
		for _, tv := range test.f.Write() {
			tvbytes = append(tvbytes, tv.bytes...)
		}

		if !bytes.Equal(test.expected, tvbytes) {
			t.Errorf("expected %s got %s", test.expected, tvbytes)
		}
	}
}

func TestRepeatingGroup_ReadError(t *testing.T) {
	singleFieldTemplate := GroupTemplate{GroupElement(1)}
	tests := []struct {
		tv               []TagValue
		expectedGroupNum int
	}{
		{
			[]TagValue{
				{value: []byte("1")},
				{tag: Tag(2), value: []byte("not in template")},
				{tag: Tag(1), value: []byte("hello")},
			}, 0},
		{
			[]TagValue{
				{value: []byte("2")},
				{tag: Tag(1), value: []byte("hello")},
				{tag: Tag(2), value: []byte("not in template")},
				{tag: Tag(1), value: []byte("hello")},
			}, 1}}

	for _, s := range tests {
		f := RepeatingGroup{template: singleFieldTemplate}
		_, err := f.Read(s.tv)
		if err == nil || len(f.groups) != s.expectedGroupNum {
			t.Errorf("Should have raised an error because expected group number is wrong: %v instead of %v", len(f.groups), s.expectedGroupNum)
		}
	}
}

func TestRepeatingGroup_Read(t *testing.T) {

	singleFieldTemplate := GroupTemplate{GroupElement(1)}
	multiFieldTemplate := GroupTemplate{GroupElement(1), GroupElement(2), GroupElement(3)}

	tests := []struct {
		groupTemplate    GroupTemplate
		tv               []TagValue
		expectedGroupTvs [][]TagValue
	}{
		{singleFieldTemplate, []TagValue{{value: []byte("0")}},
			[][]TagValue{}},
		{singleFieldTemplate, []TagValue{{value: []byte("1")}, {tag: Tag(1), value: []byte("hello")}},
			[][]TagValue{{TagValue{tag: Tag(1), value: []byte("hello")}}}},
		{singleFieldTemplate,
			[]TagValue{{value: []byte("1")},
				{tag: Tag(1), value: []byte("hello")},
				{tag: Tag(2), value: []byte("not in group")}},
			[][]TagValue{
				{TagValue{tag: Tag(1), value: []byte("hello")}}}},
		{singleFieldTemplate,
			[]TagValue{{value: []byte("2")},
				{tag: Tag(1), value: []byte("hello")},
				{tag: Tag(1), value: []byte("world")}},
			[][]TagValue{
				{TagValue{tag: Tag(1), value: []byte("hello")}},
				{TagValue{tag: Tag(1), value: []byte("world")}},
			}},
		{multiFieldTemplate,
			[]TagValue{
				{value: []byte("2")},
				{tag: Tag(1), value: []byte("hello")},
				{tag: Tag(1), value: []byte("goodbye")}, {tag: Tag(2), value: []byte("cruel")}, {tag: Tag(3), value: []byte("world")},
			},
			[][]TagValue{
				{TagValue{tag: Tag(1), value: []byte("hello")}},
				{TagValue{tag: Tag(1), value: []byte("goodbye")}, TagValue{tag: Tag(2), value: []byte("cruel")}, TagValue{tag: Tag(3), value: []byte("world")}},
			}},
		{multiFieldTemplate,
			[]TagValue{
				{value: []byte("3")},
				{tag: Tag(1), value: []byte("hello")},
				{tag: Tag(1), value: []byte("goodbye")}, {tag: Tag(2), value: []byte("cruel")}, {tag: Tag(3), value: []byte("world")},
				{tag: Tag(1), value: []byte("another")},
			},
			[][]TagValue{
				{TagValue{tag: Tag(1), value: []byte("hello")}},
				{TagValue{tag: Tag(1), value: []byte("goodbye")}, TagValue{tag: Tag(2), value: []byte("cruel")}, TagValue{tag: Tag(3), value: []byte("world")}},
				{TagValue{tag: Tag(1), value: []byte("another")}},
			}},
	}

	for _, test := range tests {
		f := RepeatingGroup{template: test.groupTemplate}

		_, err := f.Read(test.tv)
		if err != nil {
			t.Error(err)
		}
		if len(f.groups) != len(test.expectedGroupTvs) {
			t.Errorf("expected %v groups, got %v", len(test.expectedGroupTvs), len(f.groups))
		}

		for g, group := range f.groups {
			for _, expected := range test.expectedGroupTvs[g] {
				var actual FIXString
				require.Nil(t, group.GetField(expected.tag, &actual))
				require.NotNil(t, group.tags)
				require.Equal(t, len(group.tags), len(group.tagLookup))

				if !bytes.Equal(expected.value, []byte(actual)) {
					t.Errorf("%v, %v: expected %s, got %s", g, expected.tag, expected.value, actual)
				}
			}
		}
	}
}

func TestRepeatingGroup_ReadRecursive(t *testing.T) {
	singleFieldTemplate := GroupTemplate{GroupElement(4)}
	parentTemplate := GroupTemplate{GroupElement(2), NewRepeatingGroup(Tag(3), singleFieldTemplate), GroupElement(5)}

	f := NewRepeatingGroup(Tag(1), parentTemplate)
	_, err := f.Read([]TagValue{
		{value: []byte("2")},
		{tag: Tag(2), value: []byte("hello")},
		{tag: 3, value: []byte("1")}, {tag: 4, value: []byte("foo")},
		{tag: Tag(2), value: []byte("world")},
		{tag: 3, value: []byte("2")}, {tag: 4, value: []byte("foo")}, {tag: 4, value: []byte("bar")}, {tag: 5, value: []byte("fubar")},
	})
	require.Nil(t, err)

	assert.Equal(t, 2, f.Len())
}

func TestRepeatingGroup_ReadComplete(t *testing.T) {

	rawMsg := bytes.NewBufferString(withValidCheckSum("8=FIXT.1.1\x019=268\x0135=W\x0134=7118\x0149=TEST\x0152=20151027-18:41:52.698\x0156=TST\x0122=99\x0148=TSTX15\x01262=7\x01268=4\x01269=4\x01270=0.07499\x01272=20151027\x01273=18:41:52.698\x01269=7\x01270=0.07501\x01272=20151027\x01273=18:41:52.698\x01269=8\x01270=0.07494\x01272=20151027\x01273=18:41:52.698\x01269=B\x01271=60\x01272=20151027\x01273=18:41:52.698\x0110=163\x01"))

	msg := NewMessage()
	err := ParseMessage(msg, rawMsg)

	if err != nil {
		t.Error("Unexpected error, ", err)
	}

	template := GroupTemplate{
		GroupElement(269),
		GroupElement(270),
		GroupElement(271),
		GroupElement(272),
		GroupElement(273),
	}

	f := RepeatingGroup{tag: 268, template: template}
	err = msg.Body.GetGroup(&f)
	if err != nil {
		t.Error("Unexpected error, ", err)
	}

	if len(f.groups) != 4 {
		t.Errorf("expected %v groups, got %v", 4, len(f.groups))
	}

	expectedGroupTags := [][]Tag{
		{Tag(269), Tag(270), Tag(272), Tag(273)},
		{Tag(269), Tag(270), Tag(272), Tag(273)},
		{Tag(269), Tag(270), Tag(272), Tag(273)},
		{Tag(269), Tag(271), Tag(272), Tag(273)},
	}

	expectedGroupValues := [][]FIXString{
		{FIXString("4"), FIXString("0.07499"), FIXString("20151027"), FIXString("18:41:52.698")},
		{FIXString("7"), FIXString("0.07501"), FIXString("20151027"), FIXString("18:41:52.698")},
		{FIXString("8"), FIXString("0.07494"), FIXString("20151027"), FIXString("18:41:52.698")},
		{FIXString("B"), FIXString("60"), FIXString("20151027"), FIXString("18:41:52.698")},
	}

	for i, group := range f.groups {

		for j, tag := range expectedGroupTags[i] {
			if !group.Has(tag) {
				t.Errorf("expected %v in group %v", expectedGroupTags[i][j], i)
				continue
			}

			var actual FIXString
			if err := group.GetField(tag, &actual); err != nil {
				t.Errorf("error getting field %v from group %v", tag, i)
				continue
			}

			if expectedGroupValues[i][j] != actual {
				t.Errorf("Expected %v got %v", expectedGroupTags[i][j], actual)
			}
		}
	}
}

func TestRepeatingGroup_Reset(t *testing.T) {
	// Issue #758: RepeatingGroup should support Reset() to avoid reallocation
	template := GroupTemplate{GroupElement(1), GroupElement(2)}
	rg := NewRepeatingGroup(Tag(100), template)

	// Add two groups
	g1 := rg.Add()
	g1.SetField(Tag(1), FIXString("hello"))
	g1.SetField(Tag(2), FIXString("world"))

	g2 := rg.Add()
	g2.SetField(Tag(1), FIXString("foo"))
	g2.SetField(Tag(2), FIXString("bar"))

	require.Equal(t, 2, rg.Len())

	// Reset and reuse
	rg.Reset()
	require.Equal(t, 0, rg.Len())

	// Add new groups after reset
	g3 := rg.Add()
	g3.SetField(Tag(1), FIXString("reused"))
	g3.SetField(Tag(2), FIXString("instance"))

	require.Equal(t, 1, rg.Len())

	// Verify the new group has correct data
	var v1, v2 FIXString
	require.Nil(t, rg.groups[0].GetField(Tag(1), &v1))
	require.Nil(t, rg.groups[0].GetField(Tag(2), &v2))
	require.Equal(t, "reused", string(v1))
	require.Equal(t, "instance", string(v2))

	// Tag and template should be unchanged
	require.Equal(t, Tag(100), rg.Tag())
	require.Equal(t, 2, len(rg.template))
}

func TestRepeatingGroup_ResetMultipleCycles(t *testing.T) {
	template := GroupTemplate{GroupElement(1)}
	rg := NewRepeatingGroup(Tag(50), template)

	// Simulate multiple message parses without reallocation
	for cycle := range 3 {
		numGroups := cycle + 1
		for i := range numGroups {
			g := rg.Add()
			g.SetField(Tag(1), FIXString(fmt.Sprintf("cycle-%d-group-%d", cycle, i)))
		}

		require.Equal(t, numGroups, rg.Len())

		// Verify data
		for i := range numGroups {
			var v FIXString
			require.Nil(t, rg.groups[i].GetField(Tag(1), &v))
			require.Equal(t, fmt.Sprintf("cycle-%d-group-%d", cycle, i), string(v))
		}

		// Reset for next cycle
		rg.Reset()
		require.Equal(t, 0, rg.Len())
	}
}
