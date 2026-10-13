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
	"sort"
	"sync"
	"time"
)

// field stores a slice of TagValues.
type field []TagValue

func fieldTag(f field) Tag {
	return f[0].tag
}

func initField(f field, tag Tag, value []byte) {
	f[0].init(tag, value)
}

func writeField(f field, buffer *bytes.Buffer) {
	for _, tv := range f {
		buffer.Write(tv.bytes)
	}
}

// tagOrder true if tag i should occur before tag j.
type tagOrder func(i, j Tag) bool

type tagSort struct {
	tags    []Tag
	compare tagOrder
}

func (t tagSort) Len() int           { return len(t.tags) }
func (t tagSort) Swap(i, j int)      { t.tags[i], t.tags[j] = t.tags[j], t.tags[i] }
func (t tagSort) Less(i, j int) bool { return t.compare(t.tags[i], t.tags[j]) }

// FieldMap is a collection of fix fields that make up a fix message.
type FieldMap struct {
	// tagLookup is created on first write, so that it can be sized for the
	// fields about to be added. Reads of a nil map are safe.
	tagLookup map[Tag]field
	tagSort
	rwLock *sync.RWMutex

	// spare is storage for fields set one at a time, carved off in chunks so
	// that a Set does not allocate a field of its own. When spareHint is set,
	// it sizes the first chunk instead of firstSpareChunk.
	spare     []TagValue
	spareHint int
}

// Chunk sizes for FieldMap.spare. Chunks double from the first size up to
// the last, so a small section stays small and a large one allocates rarely.
const (
	firstSpareChunk = 4
	maxSpareChunk   = 32
)

// firstTagsCap is the capacity an empty tags slice starts at.
const firstTagsCap = 8

// ascending tags.
func normalFieldOrder(i, j Tag) bool { return i < j }

func (m *FieldMap) init() {
	m.initWithOrdering(normalFieldOrder)
}

func (m *FieldMap) initWithOrdering(ordering tagOrder) {
	m.initWithLock(ordering, &sync.RWMutex{})
}

func (m *FieldMap) initWithLock(ordering tagOrder, lock *sync.RWMutex) {
	m.rwLock = lock
	m.compare = ordering
}

// ensureLookup creates tagLookup if this is the first write.
func (m *FieldMap) ensureLookup() {
	if m.tagLookup == nil {
		m.tagLookup = make(map[Tag]field)
	}
}

// reserve sizes an empty FieldMap's storage for about n fields. It does
// nothing to a FieldMap that already has storage, so that a reused one keeps
// whatever it has grown to.
func (m *FieldMap) reserve(n int, tags []Tag) {
	if m.tagLookup == nil {
		m.tagLookup = make(map[Tag]field, n)
	}
	if cap(m.tags) == 0 {
		m.tags = tags
	}
}

// appendTag records a new tag, skipping the smallest growth steps of an empty
// tags slice.
func (m *FieldMap) appendTag(tag Tag) {
	if cap(m.tags) == 0 {
		m.tags = make([]Tag, 0, firstTagsCap)
	}
	m.tags = append(m.tags, tag)
}

// newField returns storage for a single field.
func (m *FieldMap) newField() field {
	if len(m.spare) == 0 {
		size := firstSpareChunk
		switch n := len(m.tags); {
		case m.spareHint > 0:
			size, m.spareHint = min(m.spareHint, maxSpareChunk), 0
		case n > size:
			size = min(n, maxSpareChunk)
		}
		m.spare = make([]TagValue, size)
	}

	// Cap the field at one element so that nothing can append into its
	// neighbour.
	f := field(m.spare[:1:1])
	m.spare = m.spare[1:]
	return f
}

// Tags returns all of the Field Tags in this FieldMap.
func (m FieldMap) Tags() []Tag {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	tags := make([]Tag, 0, len(m.tagLookup))
	for t := range m.tagLookup {
		tags = append(tags, t)
	}

	return tags
}

// Get parses out a field in this FieldMap. Returned reject may indicate the field is not present, or the field value is invalid.
func (m FieldMap) Get(parser Field) MessageRejectError {
	return m.GetField(parser.Tag(), parser)
}

// Has returns true if the Tag is present in this FieldMap.
func (m FieldMap) Has(tag Tag) bool {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	_, ok := m.tagLookup[tag]
	return ok
}

// GetField parses of a field with Tag tag. Returned reject may indicate the field is not present, or the field value is invalid.
func (m FieldMap) GetField(tag Tag, parser FieldValueReader) MessageRejectError {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	f, ok := m.tagLookup[tag]
	if !ok {
		return ConditionallyRequiredFieldMissing(tag)
	}

	if err := parser.Read(f[0].value); err != nil {
		return IncorrectDataFormatForValue(tag)
	}

	return nil
}

// GetBytes is a zero-copy GetField wrapper for []bytes fields.
func (m FieldMap) GetBytes(tag Tag) ([]byte, MessageRejectError) {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	f, ok := m.tagLookup[tag]
	if !ok {
		return nil, ConditionallyRequiredFieldMissing(tag)
	}

	return f[0].value, nil
}

// getBytesNoLock is a lock free zero-copy GetField wrapper for []bytes fields.
func (m FieldMap) getBytesNoLock(tag Tag) ([]byte, MessageRejectError) {
	f, ok := m.tagLookup[tag]
	if !ok {
		return nil, ConditionallyRequiredFieldMissing(tag)
	}

	return f[0].value, nil
}

// GetBool is a GetField wrapper for bool fields.
func (m FieldMap) GetBool(tag Tag) (bool, MessageRejectError) {
	var val FIXBoolean
	if err := m.GetField(tag, &val); err != nil {
		return false, err
	}
	return bool(val), nil
}

// GetInt is a GetField wrapper for int fields.
func (m FieldMap) GetInt(tag Tag) (int, MessageRejectError) {
	bytes, err := m.GetBytes(tag)
	if err != nil {
		return 0, err
	}

	var val FIXInt
	if val.Read(bytes) != nil {
		err = IncorrectDataFormatForValue(tag)
	}

	return int(val), err
}

// GetInt is a lock free GetField wrapper for int fields.
func (m FieldMap) getIntNoLock(tag Tag) (int, MessageRejectError) {
	bytes, err := m.getBytesNoLock(tag)
	if err != nil {
		return 0, err
	}

	var val FIXInt
	if val.Read(bytes) != nil {
		err = IncorrectDataFormatForValue(tag)
	}

	return int(val), err
}

// GetTime is a GetField wrapper for utc timestamp fields.
func (m FieldMap) GetTime(tag Tag) (t time.Time, err MessageRejectError) {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	bytes, err := m.getBytesNoLock(tag)
	if err != nil {
		return
	}

	var val FIXUTCTimestamp
	if val.Read(bytes) != nil {
		err = IncorrectDataFormatForValue(tag)
	}

	return val.Time, err
}

// GetString is a GetField wrapper for string fields.
func (m FieldMap) GetString(tag Tag) (string, MessageRejectError) {
	var val FIXString
	if err := m.GetField(tag, &val); err != nil {
		return "", err
	}
	return string(val), nil
}

// GetGroup is a Get function specific to Group Fields.
func (m FieldMap) GetGroup(parser FieldGroupReader) MessageRejectError {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	f, ok := m.tagLookup[parser.Tag()]
	if !ok {
		return ConditionallyRequiredFieldMissing(parser.Tag())
	}

	if _, err := parser.Read(f); err != nil {
		if msgRejErr, ok := err.(MessageRejectError); ok {
			return msgRejErr
		}
		return IncorrectDataFormatForValue(parser.Tag())
	}

	return nil
}

// SetField sets the field with Tag tag.
func (m *FieldMap) SetField(tag Tag, field FieldValueWriter) *FieldMap {
	return m.SetBytes(tag, field.Write())
}

// SetBytes sets bytes.
func (m *FieldMap) SetBytes(tag Tag, value []byte) *FieldMap {
	f := m.getOrCreate(tag)
	initField(f, tag, value)
	return m
}

// SetBool is a SetField wrapper for bool fields.
func (m *FieldMap) SetBool(tag Tag, value bool) *FieldMap {
	return m.SetField(tag, FIXBoolean(value))
}

// SetInt is a SetField wrapper for int fields.
func (m *FieldMap) SetInt(tag Tag, value int) *FieldMap {
	v := FIXInt(value)
	return m.SetBytes(tag, v.Write())
}

// SetString is a SetField wrapper for string fields.
func (m *FieldMap) SetString(tag Tag, value string) *FieldMap {
	return m.SetBytes(tag, []byte(value))
}

// Remove removes a tag from field map.
func (m *FieldMap) Remove(tag Tag) {
	m.rwLock.Lock()
	defer m.rwLock.Unlock()

	delete(m.tagLookup, tag)
}

// Clear purges all fields from field map.
func (m *FieldMap) Clear() {
	m.rwLock.Lock()
	defer m.rwLock.Unlock()

	m.tags = m.tags[0:0]
	for k := range m.tagLookup {
		delete(m.tagLookup, k)
	}
}

func (m *FieldMap) clearNoLock() {
	m.tags = m.tags[0:0]
	for k := range m.tagLookup {
		delete(m.tagLookup, k)
	}
}

// CopyInto overwrites the given FieldMap with this one.
func (m *FieldMap) CopyInto(to *FieldMap) {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	to.tagLookup = make(map[Tag]field, len(m.tagLookup))
	clones := make([]TagValue, len(m.tagLookup))
	i := 0
	for tag, f := range m.tagLookup {
		clone := field(clones[i : i+1 : i+1])
		clone[0] = f[0]
		to.tagLookup[tag] = clone
		i++
	}
	to.tags = make([]Tag, len(m.tags))
	copy(to.tags, m.tags)
	to.compare = m.compare
}

func (m *FieldMap) add(f field) {
	m.ensureLookup()

	t := fieldTag(f)
	if _, ok := m.tagLookup[t]; !ok {
		m.appendTag(t)
	}

	m.tagLookup[t] = f
}

func (m *FieldMap) getOrCreate(tag Tag) field {
	m.rwLock.Lock()
	defer m.rwLock.Unlock()

	if f, ok := m.tagLookup[tag]; ok {
		f = f[:1]
		return f
	}

	m.ensureLookup()

	f := m.newField()
	m.tagLookup[tag] = f
	m.appendTag(tag)
	return f
}

// Set is a setter for fields.
func (m *FieldMap) Set(field FieldWriter) *FieldMap {
	f := m.getOrCreate(field.Tag())
	initField(f, field.Tag(), field.Write())
	return m
}

// SetGroup is a setter specific to group fields.
func (m *FieldMap) SetGroup(field FieldGroupWriter) *FieldMap {
	m.rwLock.Lock()
	defer m.rwLock.Unlock()

	m.ensureLookup()
	_, ok := m.tagLookup[field.Tag()]
	if !ok {
		m.appendTag(field.Tag())
	}
	m.tagLookup[field.Tag()] = field.Write()
	return m
}

func (m *FieldMap) sortedTags() []Tag {
	sort.Sort(m)
	return m.tags
}

func (m FieldMap) write(buffer *bytes.Buffer) {
	m.rwLock.Lock()
	defer m.rwLock.Unlock()

	for _, tag := range m.sortedTags() {
		if f, ok := m.tagLookup[tag]; ok {
			writeField(f, buffer)
		}
	}
}

func (m FieldMap) total() int {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	total := 0
	for _, fields := range m.tagLookup {
		for _, tv := range fields {
			switch tv.tag {
			case tagCheckSum: // Tag does not contribute to total.
			default:
				total += tv.total()
			}
		}
	}

	return total
}

func (m FieldMap) length() int {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	length := 0
	for _, fields := range m.tagLookup {
		for _, tv := range fields {
			switch tv.tag {
			case tagBeginString, tagBodyLength, tagCheckSum: // Tags do not contribute to length.
			default:
				length += tv.length()
			}
		}
	}

	return length
}
