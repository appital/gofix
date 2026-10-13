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
	"fmt"
	"math"
	"strconv"
	"sync"
)

// GroupItem interface is used to construct repeating group templates.
type GroupItem interface {
	// Tag returns the tag identifying this GroupItem.
	Tag() Tag

	// Read Parameter to Read is tagValues.  For most fields, only the first tagValue will be required.
	// The length of the slice extends from the tagValue mapped to the field to be read through the
	// following fields. This can be useful for GroupItems made up of repeating groups.
	//
	// The Read function returns the remaining tagValues not processed by the GroupItem. If there was a
	// problem reading the field, an error may be returned.
	Read([]TagValue) ([]TagValue, error)

	// Clone makes a copy of this GroupItem.
	Clone() GroupItem
}

type protoGroupElement struct {
	tag Tag
}

func (t protoGroupElement) Tag() Tag { return t.tag }
func (t protoGroupElement) Read(tv []TagValue) ([]TagValue, error) {
	if tv[0].tag == t.tag {
		return tv[1:], nil
	}

	return tv, nil
}

func (t protoGroupElement) Clone() GroupItem { return t }

// GroupElement returns a GroupItem made up of a single field.
func GroupElement(tag Tag) GroupItem {
	return protoGroupElement{tag: tag}
}

// GroupTemplate specifies the group item order for a RepeatingGroup.
type GroupTemplate []GroupItem

// Clone makes a copy of this GroupTemplate.
func (gt GroupTemplate) Clone() GroupTemplate {
	clone := make(GroupTemplate, len(gt))
	for i := range gt {
		clone[i] = gt[i].Clone()
	}

	return clone
}

// Group is a group of fields occurring in a repeating group.
type Group struct{ FieldMap }

// groupWithLock lets one allocation cover a Group and its lock.
type groupWithLock struct {
	Group
	lock sync.RWMutex
}

// newGroup returns an empty Group ordered by ordering.
func newGroup(ordering tagOrder) *Group {
	gl := new(groupWithLock)
	gl.initWithLock(ordering, &gl.lock)
	return &gl.Group
}

// RepeatingGroup is a FIX Repeating Group type.
type RepeatingGroup struct {
	tag      Tag
	template GroupTemplate
	groups   []*Group

	// order is the template's tag ordering, built on first use and shared by
	// every Group in this RepeatingGroup.
	order tagOrder
}

// NewRepeatingGroup returns an initilized RepeatingGroup instance.
func NewRepeatingGroup(tag Tag, template GroupTemplate) *RepeatingGroup {
	return &RepeatingGroup{
		tag:      tag,
		template: template,
	}
}

// Tag returns the Tag for this repeating Group.
func (f RepeatingGroup) Tag() Tag {
	return f.tag
}

// Clone makes a copy of this RepeatingGroup (tag, template).
func (f RepeatingGroup) Clone() GroupItem {
	return &RepeatingGroup{
		tag:      f.tag,
		template: f.template.Clone(),
	}
}

// Len returns the number of Groups in this RepeatingGroup.
func (f RepeatingGroup) Len() int {
	return len(f.groups)
}

// Get returns the ith group in this RepeatingGroup.
func (f RepeatingGroup) Get(i int) *Group {
	return f.groups[i]
}

// Add appends a new group to the RepeatingGroup and returns the new Group.
func (f *RepeatingGroup) Add() *Group {
	g := newGroup(f.groupTagOrder())

	// The entries of a group are usually alike, so expect as many fields as
	// the previous one has.
	if n := len(f.groups); n > 0 {
		g.spareHint = len(f.groups[n-1].tags)
	}

	f.groups = append(f.groups, g)
	return g
}

// Reset clears all groups from this RepeatingGroup, allowing the instance to be
// reused for a new message parse without reallocating the template and struct.
// This avoids the allocation overhead of calling NewRepeatingGroup for each parse.
func (f *RepeatingGroup) Reset() {
	f.groups = f.groups[:0]
}

// Write returns tagValues for all Items in the repeating group ordered by
// Group sequence and Group template order.
func (f RepeatingGroup) Write() []TagValue {
	tvs := make([]TagValue, 1)
	tvs[0].init(f.tag, []byte(strconv.Itoa(len(f.groups))))

	for _, group := range f.groups {
		tags := group.sortedTags()
		group.rwLock.RLock()
		for _, tag := range tags {
			if fields, ok := group.tagLookup[tag]; ok {
				tvs = append(tvs, fields...)
			}
		}
		group.rwLock.RUnlock()
	}

	return tvs
}

func (f RepeatingGroup) findItemInGroupTemplate(t Tag) (item GroupItem, ok bool) {
	for _, templateField := range f.template {
		if t == templateField.Tag() {
			// A single field holds no state, so it can be used as is; cloning
			// it would only box a copy into a new interface value.
			if _, isElement := templateField.(protoGroupElement); isElement {
				return templateField, true
			}
			return templateField.Clone(), true
		}
	}

	return
}

// groupTagOrder returns the ordering of the template's tags. It is built once
// per RepeatingGroup, since the template does not change.
func (f *RepeatingGroup) groupTagOrder() tagOrder {
	if f.order != nil {
		return f.order
	}

	tagMap := make(map[Tag]int, len(f.template))
	for i, f := range f.template {
		tagMap[f.Tag()] = i
	}

	f.order = func(i, j Tag) bool {
		orderi := math.MaxInt32
		orderj := math.MaxInt32

		if iIndex, ok := tagMap[i]; ok {
			orderi = iIndex
		}

		if jIndex, ok := tagMap[j]; ok {
			orderj = jIndex
		}

		return orderi < orderj
	}
	return f.order
}

func (f RepeatingGroup) delimiter() Tag {
	return f.template[0].Tag()
}

func (f RepeatingGroup) isDelimiter(t Tag) bool {
	return t == f.delimiter()
}

func (f *RepeatingGroup) Read(tv []TagValue) ([]TagValue, error) {
	expectedGroupSize, err := atoi(tv[0].value)
	if err != nil {
		return tv, err
	}

	if expectedGroupSize == 0 {
		return tv[1:], nil
	}

	tv = tv[1:cap(tv)]
	tagOrdering := f.groupTagOrder()
	var group *Group
	for len(tv) > 0 {
		gi, ok := f.findItemInGroupTemplate(tv[0].tag)
		if !ok {
			break
		}

		tvRange := tv
		if tv, err = gi.Read(tv); err != nil {
			return tv, err
		}

		if f.isDelimiter(gi.Tag()) {
			group = newGroup(tagOrdering)

			f.groups = append(f.groups, group)
		}

		// Fields before the first delimiter belong to no entry, so they are
		// dropped.
		if group == nil {
			continue
		}

		group.rwLock.Lock()
		group.ensureLookup()
		group.tagLookup[tvRange[0].tag] = tvRange
		group.appendTag(gi.Tag())
		group.rwLock.Unlock()
	}

	if len(f.groups) != expectedGroupSize {
		return tv, repeatingGroupFieldsOutOfOrder(f.tag, fmt.Sprintf("group %v: template is wrong or delimiter %v not found: expected %v groups, but found %v", f.tag, f.delimiter(), expectedGroupSize, len(f.groups)))
	}

	return tv, err
}
