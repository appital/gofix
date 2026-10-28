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
	"math"
	"sync"
	"time"

	"github.com/appital/gofix/datadictionary"
)

// Header is first section of a FIX Message.
type Header struct{ FieldMap }

// msgparser contains message parsing vars needed to parse a string into a message.
type msgParser struct {
	msg                     *Message
	transportDataDictionary *datadictionary.DataDictionary
	appDataDictionary       *datadictionary.DataDictionary
	msgDef                  *datadictionary.MessageDef
	rawBytes                []byte
	fieldIndex              int
	parsedFieldBytes        *TagValue
	trailerBytes            []byte
	foundBody               bool
	foundTrailer            bool

	// seen records the singleton tags that must appear at most once. A
	// bit is set for each tag that has been seen, so a duplicate can be
	// rejected without a map allocation.
	seen [4]uint64
}

// maxParsedFields bounds the number of fields a frame may declare. Together
// with the parser's byte limit this stops a frame of SOH bytes from turning
// into an allocation many times its own size.
const maxParsedFields = 1 << 16

// singletonTag reports whether tag must appear at most once in a message.
// These are the tags the session trusts for version, identity, sequencing and
// integrity decisions.
func singletonTag(tag Tag) bool {
	switch tag {
	case tagBeginString, tagBodyLength, tagMsgSeqNum, tagMsgType,
		tagSenderCompID, tagSenderSubID, tagSenderLocationID,
		tagTargetCompID, tagTargetSubID, tagTargetLocationID,
		tagSendingTime, tagCheckSum:
		return true
	}
	return false
}

// markSeen records a singleton tag and reports an error if it was already
// present.
func (mp *msgParser) markSeen(tag Tag) error {
	if !singletonTag(tag) || tag < 0 || tag >= Tag(len(mp.seen)*64) {
		return nil
	}

	word, bit := int(tag)/64, uint64(1)<<(uint(tag)%64)
	if mp.seen[word]&bit != 0 {
		return parseError{OrigError: fmt.Sprintf("duplicate tag %d", tag)}
	}
	mp.seen[word] |= bit

	return nil
}

// sessionBodyTag reports whether tag is a body field the session itself acts
// on. None of them is a repeating group member in any FIX version, so a
// repeat is a duplicate even when no data dictionary says so.
func sessionBodyTag(tag Tag) bool {
	switch tag {
	case tagBeginSeqNo, tagEndSeqNo, tagNewSeqNo, tagGapFillFlag,
		tagHeartBtInt, tagTestReqID, tagResetSeqNumFlag,
		tagNextExpectedMsgSeqNum, tagDefaultApplVerID:
		return true
	}
	return false
}

// rejectMessage records the first reason the session must reject the message
// for. Parsing carries on, so the rest of the message is still available.
func (mp *msgParser) rejectMessage(reject MessageRejectError) {
	if mp.msg.parseReject == nil {
		mp.msg.parseReject = reject
	}
}

// addHeaderField adds a header field, rejecting a duplicate singleton tag. A
// header tag after the body, or a repeated one, is recorded for the session
// to reject. A late tag still goes in the header, so the session can refer
// to the message's MsgSeqNum when it rejects it.
func (mp *msgParser) addHeaderField(f field) error {
	tag := fieldTag(f)
	if err := mp.markSeen(tag); err != nil {
		return err
	}

	if mp.foundBody {
		mp.rejectMessage(tagSpecifiedOutOfRequiredOrder(tag))
	}

	if !mp.msg.Header.addIfAbsent(f) {
		switch tag {
		case tagHopCompID, tagHopSendingTime, tagHopRefID:
			// Members of the NoHops group repeat, and keep their last value.
			mp.msg.Header.add(f)
		default:
			mp.rejectMessage(tagAppearsMoreThanOnce(tag))
		}
	}
	return nil
}

// addTrailerField adds a trailer field, rejecting a duplicate singleton tag.
// Any other repeated trailer tag is recorded for the session to reject.
func (mp *msgParser) addTrailerField(f field) error {
	tag := fieldTag(f)
	if err := mp.markSeen(tag); err != nil {
		return err
	}

	if !mp.msg.Trailer.addIfAbsent(f) {
		mp.rejectMessage(tagAppearsMoreThanOnce(tag))
	}
	return nil
}

// checksBodyDuplicates reports whether a repeat of the body tag must be
// rejected: when the message's definition shows the tag is not a group member,
// or when the session acts on it. Without a dictionary any other repeat may be
// a group member, and keeps its last value as before.
func (mp *msgParser) checksBodyDuplicates(tag Tag) bool {
	return mp.msgDef != nil || sessionBodyTag(tag)
}

// addUniqueBodyField adds a body field or a repeating group whose repeat must
// be rejected. A repeat is recorded for the session to reject, and keeps the
// first value.
func (mp *msgParser) addUniqueBodyField(f field) {
	if !mp.msg.Body.addIfAbsent(f) {
		mp.rejectMessage(tagAppearsMoreThanOnce(fieldTag(f)))
	}
}

// in the message header, the first 3 tags in the message header must be 8,9,35.
func headerFieldOrdering(i, j Tag) bool {
	var ordering = func(t Tag) uint32 {
		switch t {
		case tagBeginString:
			return 1
		case tagBodyLength:
			return 2
		case tagMsgType:
			return 3
		}

		return math.MaxUint32
	}

	orderi := ordering(i)
	orderj := ordering(j)

	switch {
	case orderi < orderj:
		return true
	case orderi > orderj:
		return false
	}

	return i < j
}

// Init initializes the Header instance.
func (h *Header) Init() {
	h.initWithOrdering(headerFieldOrdering)
}

// Body is the primary application section of a FIX message.
type Body struct{ FieldMap }

// Init initializes the FIX message.
func (b *Body) Init() {
	b.init()
}

// Trailer is the last section of a FIX message.
type Trailer struct{ FieldMap }

// In the trailer, CheckSum (tag 10) must be last.
func trailerFieldOrdering(i, j Tag) bool {
	switch {
	case i == tagCheckSum:
		return false
	case j == tagCheckSum:
		return true
	}

	return i > j
}

// Init initializes the FIX message.
func (t *Trailer) Init() {
	t.initWithOrdering(trailerFieldOrdering)
}

// Message is a FIX Message abstraction.
type Message struct {
	Header  Header
	Trailer Trailer
	Body    Body

	// ReceiveTime is the time that this message was read from the socket connection.
	ReceiveTime time.Time

	rawMessage *bytes.Buffer

	// Slice of Bytes corresponding to the message body.
	bodyBytes []byte

	// Field bytes as they appear in the raw message.
	fields []TagValue

	// parseReject is the first problem found while parsing that the session
	// must reject the message for: a tag that appears twice, or a header tag
	// after the body. A repeated tag keeps its first value, so the checks the
	// session makes before rejecting see the same value as everything else.
	parseReject MessageRejectError
}

// ToMessage returns the message itself.
func (m *Message) ToMessage() *Message { return m }

// parseError is returned when bytes cannot be parsed as a FIX message.
type parseError struct {
	OrigError string
}

func (e parseError) Error() string { return fmt.Sprintf("error parsing message: %s", e.OrigError) }

// NewMessage returns a newly initialized Message instance.
func NewMessage() *Message {
	// One allocation covers the Message and the locks of all three sections.
	ml := new(messageWithLocks)
	m := &ml.Message
	m.Header.initWithLock(headerFieldOrdering, &ml.locks[0])
	m.Body.initWithLock(normalFieldOrder, &ml.locks[1])
	m.Trailer.initWithLock(trailerFieldOrdering, &ml.locks[2])
	m.Header.spareHint = typicalHeaderFields
	m.Trailer.spareHint = 1 // Usually just CheckSum.

	return m
}

type messageWithLocks struct {
	Message
	locks [3]sync.RWMutex
}

// CopyInto erases the dest messages and copies the currency message content
// into it.
func (m *Message) CopyInto(to *Message) {
	m.Header.CopyInto(&to.Header.FieldMap)
	m.Body.CopyInto(&to.Body.FieldMap)
	m.Trailer.CopyInto(&to.Trailer.FieldMap)

	to.ReceiveTime = m.ReceiveTime
	to.parseReject = m.parseReject
	to.bodyBytes = make([]byte, len(m.bodyBytes))
	copy(to.bodyBytes, m.bodyBytes)
	to.fields = make([]TagValue, len(m.fields))
	for i := range to.fields {
		to.fields[i].init(m.fields[i].tag, m.fields[i].value)
	}
}

// ParseMessage constructs a Message from a byte slice wrapping a FIX message.
func ParseMessage(msg *Message, rawMessage *bytes.Buffer) (err error) {
	return ParseMessageWithDataDictionary(msg, rawMessage, nil, nil)
}

// ParseMessageWithDataDictionary constructs a Message from a byte slice wrapping a FIX message using an optional session and application DataDictionary for reference.
func ParseMessageWithDataDictionary(
	msg *Message,
	rawMessage *bytes.Buffer,
	transportDataDictionary *datadictionary.DataDictionary,
	appDataDictionary *datadictionary.DataDictionary,
) (err error) {
	// Create msgparser before we go any further.
	mp := &msgParser{
		msg:                     msg,
		transportDataDictionary: transportDataDictionary,
		appDataDictionary:       appDataDictionary,
	}
	mp.msg.rawMessage = rawMessage
	mp.rawBytes = rawMessage.Bytes()

	return doParsing(mp)
}

// Presizing for the sections of a freshly parsed Message. A header of eight
// or fewer fields fits a map's smallest table. The body's estimate counts
// repeating group members, which do not get map entries of their own when a
// data dictionary is in use, so it is capped.
const (
	typicalHeaderFields   = 8
	typicalTrailerFields  = 2
	maxPresizedBodyFields = 64
)

// doParsing executes the message parsing process.
func doParsing(mp *msgParser) (err error) {
	// Inbound messages are parsed on goroutines that nothing recovers, so
	// malformed input that trips a bug here is returned as a parse error
	// rather than ending the process. Registered first, this runs after the
	// unlocks below.
	defer func() {
		if r := recover(); r != nil {
			err = parseError{OrigError: fmt.Sprintf("doParsing: %v", r)}
		}
	}()

	mp.msg.Header.rwLock.Lock()
	defer mp.msg.Header.rwLock.Unlock()
	mp.msg.Body.rwLock.Lock()
	defer mp.msg.Body.rwLock.Unlock()
	mp.msg.Trailer.rwLock.Lock()
	defer mp.msg.Trailer.rwLock.Unlock()

	// Initialize for parsing.
	mp.msg.Header.clearNoLock()
	mp.msg.Body.clearNoLock()
	mp.msg.Trailer.clearNoLock()
	mp.msg.parseReject = nil

	// Allocate expected message fields in one chunk.
	fieldCount := bytes.Count(mp.rawBytes, []byte{'\001'})
	if fieldCount == 0 {
		return parseError{OrigError: fmt.Sprintf("No Fields detected in %s", string(mp.rawBytes))}
	}
	if fieldCount > maxParsedFields {
		return parseError{OrigError: fmt.Sprintf("too many fields: %d", fieldCount)}
	}
	if cap(mp.msg.fields) < fieldCount {
		mp.msg.fields = make([]TagValue, fieldCount)
	} else {
		mp.msg.fields = mp.msg.fields[0:fieldCount]
	}

	// Size a fresh Message's sections up front rather than growing them field
	// by field. Which section a field lands in is not known until it is
	// parsed, so the header and trailer get typical sizes and the body gets
	// the rest; the three tags slices share one allocation.
	if mp.msg.Body.tagLookup == nil {
		const headerSize, trailerSize = typicalHeaderFields, typicalTrailerFields
		bodySize := min(max(fieldCount-headerSize-trailerSize, 1), maxPresizedBodyFields)
		tags := make([]Tag, headerSize+trailerSize+bodySize)
		mp.msg.Header.reserve(headerSize, tags[0:0:headerSize])
		mp.msg.Trailer.reserve(trailerSize, tags[headerSize:headerSize:headerSize+trailerSize])
		mp.msg.Body.reserve(bodySize, tags[headerSize+trailerSize:headerSize+trailerSize])
	}

	// Message must start with begin string, body length, msg type.
	// Get begin string.
	if mp.rawBytes, err = extractSpecificField(&mp.msg.fields[mp.fieldIndex], tagBeginString, mp.rawBytes); err != nil {
		return
	}
	if err = mp.addHeaderField(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1]); err != nil {
		return
	}

	// Get body length.
	mp.fieldIndex++
	mp.parsedFieldBytes = &mp.msg.fields[mp.fieldIndex]
	if mp.rawBytes, err = extractSpecificField(mp.parsedFieldBytes, tagBodyLength, mp.rawBytes); err != nil {
		return
	}
	if err = mp.addHeaderField(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1]); err != nil {
		return
	}

	// Get msg type.
	mp.fieldIndex++
	mp.parsedFieldBytes = &mp.msg.fields[mp.fieldIndex]
	if mp.rawBytes, err = extractSpecificField(mp.parsedFieldBytes, tagMsgType, mp.rawBytes); err != nil {
		return
	}
	if err = mp.addHeaderField(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1]); err != nil {
		return
	}

	// Resolve the message definition once, rather than per field.
	mp.msgDef = nil
	if mp.appDataDictionary != nil {
		mp.msgDef = mp.appDataDictionary.Messages[string(mp.parsedFieldBytes.value)]
	}

	// Start parsing.
	mp.fieldIndex++
	var (
		dataLen        int
		pendingDataTag Tag
	)
	mp.trailerBytes = []byte{}
	mp.foundBody = false
	mp.foundTrailer = false
	for {
		mp.parsedFieldBytes = &mp.msg.fields[mp.fieldIndex]
		if pendingDataTag != 0 {
			mp.rawBytes, err = extractDataField(mp.parsedFieldBytes, mp.rawBytes, dataLen, pendingDataTag)
			pendingDataTag = 0
		} else {
			mp.rawBytes, err = extractField(mp.parsedFieldBytes, mp.rawBytes)
		}
		if err != nil {
			return
		}

		switch {
		case isHeaderField(mp.parsedFieldBytes.tag, mp.transportDataDictionary):
			if err = mp.addHeaderField(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1]); err != nil {
				return
			}
		case isTrailerField(mp.parsedFieldBytes.tag, mp.transportDataDictionary):
			if err = mp.addTrailerField(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1]); err != nil {
				return
			}
			mp.foundTrailer = true
		case isNumInGroupField(mp.msgDef, mp.parsedFieldBytes.tag):
			if err = parseGroup(mp, mp.parsedFieldBytes.tag); err != nil {
				return
			}
		default:
			mp.foundBody = true
			mp.trailerBytes = mp.rawBytes
			// Adding without checking stays inline, which matters for the
			// many repeated group members of a message parsed without a
			// dictionary.
			if mp.checksBodyDuplicates(mp.parsedFieldBytes.tag) {
				mp.addUniqueBodyField(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1])
			} else {
				mp.msg.Body.add(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1])
			}
		}
		if mp.parsedFieldBytes.tag == tagCheckSum {
			break
		}

		if !mp.foundBody {
			mp.msg.bodyBytes = mp.rawBytes
		}

		if nextTag := dataTagFor(mp.parsedFieldBytes.tag); nextTag != 0 {
			n, parseErr := atoi(mp.parsedFieldBytes.value)
			if parseErr != nil || n < 0 {
				return parseError{OrigError: fmt.Sprintf("invalid length for data tag %d", mp.parsedFieldBytes.tag)}
			}
			dataLen, pendingDataTag = n, nextTag
		}
		mp.fieldIndex++
	}

	// A frame of mostly delimiters would otherwise leave stale zero entries
	// after the fields that were actually parsed.
	mp.msg.fields = mp.msg.fields[:mp.fieldIndex+1]

	// This will happen if there are no fields in the body
	if mp.foundTrailer && !mp.foundBody {
		mp.trailerBytes = mp.rawBytes
		mp.msg.bodyBytes = nil
	}

	// Body length would only be larger than trailer if fields out of order.
	if len(mp.msg.bodyBytes) > len(mp.trailerBytes) {
		mp.msg.bodyBytes = mp.msg.bodyBytes[:len(mp.msg.bodyBytes)-len(mp.trailerBytes)]
	}

	length := 0
	total := 0
	for _, field := range mp.msg.fields {
		switch field.tag {
		case tagBeginString, tagBodyLength, tagCheckSum: // Tags do not contribute to length.
		default:
			length += field.length()
		}
		if field.tag != tagCheckSum {
			total += field.total()
		}
	}

	bodyLength, err := mp.msg.Header.getIntNoLock(tagBodyLength)
	if err != nil {
		err = parseError{OrigError: err.Error()}
		return
	}
	if length != bodyLength {
		err = parseError{OrigError: fmt.Sprintf("Incorrect Message Length, expected %d, got %d", bodyLength, length)}
		return
	}

	checkSumBytes, reject := mp.msg.Trailer.getBytesNoLock(tagCheckSum)
	if reject != nil {
		err = parseError{OrigError: "missing CheckSum"}
		return
	}
	if len(checkSumBytes) != 3 {
		err = parseError{OrigError: fmt.Sprintf("invalid CheckSum %q", checkSumBytes)}
		return
	}
	checkSum, parseErr := atoi(checkSumBytes)
	if parseErr != nil || checkSum < 0 || checkSum > 255 {
		err = parseError{OrigError: fmt.Sprintf("invalid CheckSum %q", checkSumBytes)}
		return
	}
	if checkSum != total%256 {
		err = parseError{OrigError: fmt.Sprintf("Incorrect CheckSum, expected %03d, got %s", total%256, checkSumBytes)}
	}

	return
}

// parseGroup iterates through a repeating group to maintain correct order of those fields.
// It runs only when the message's definition is known, so every repeated body
// tag it adds is rejected.
func parseGroup(mp *msgParser, groupTag Tag) error {
	mp.foundBody = true
	dm := mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1]

	// Group nesting is rarely deep, so the tag path normally stays on the stack.
	var tagBuf [8]Tag
	tags := append(tagBuf[:0], groupTag)
	group := getGroupFieldDef(mp.msgDef, tags)

	var (
		dataLen        int
		pendingDataTag Tag
	)

parseLoop:
	for {
		mp.fieldIndex++
		mp.parsedFieldBytes = &mp.msg.fields[mp.fieldIndex]
		preExtractBytes := mp.rawBytes
		var err error
		if pendingDataTag != 0 {
			mp.rawBytes, err = extractDataField(mp.parsedFieldBytes, mp.rawBytes, dataLen, pendingDataTag)
			pendingDataTag = 0
		} else {
			mp.rawBytes, err = extractField(mp.parsedFieldBytes, mp.rawBytes)
		}
		if err != nil {
			return err
		}
		mp.trailerBytes = mp.rawBytes

		if nextTag := dataTagFor(mp.parsedFieldBytes.tag); nextTag != 0 {
			n, parseErr := atoi(mp.parsedFieldBytes.value)
			if parseErr != nil || n < 0 {
				return parseError{OrigError: fmt.Sprintf("invalid length for data tag %d", mp.parsedFieldBytes.tag)}
			}
			dataLen, pendingDataTag = n, nextTag
		}

		// Is this field a member for the group.
		if isGroupMember(mp.parsedFieldBytes.tag, group) {
			// Is this field a nested repeating group.
			if nested := getGroupFieldDef(mp.msgDef, append(tags, mp.parsedFieldBytes.tag)); nested != nil {
				dm = append(dm, *mp.parsedFieldBytes)
				tags = append(tags, mp.parsedFieldBytes.tag)
				group = nested
				continue
			}
			// Add the field member to the group.
			dm = append(dm, *mp.parsedFieldBytes)
		} else if isHeaderField(mp.parsedFieldBytes.tag, mp.transportDataDictionary) {
			// Found a header tag for some reason..
			mp.addUniqueBodyField(dm)
			if err = mp.addHeaderField(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1]); err != nil {
				return err
			}
			break
		} else if isTrailerField(mp.parsedFieldBytes.tag, mp.transportDataDictionary) {
			// Found the trailer at the end of the message.
			mp.addUniqueBodyField(dm)
			if err = mp.addTrailerField(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1]); err != nil {
				return err
			}
			mp.foundTrailer = true
			// Restore the buffer that still holds the trailer so the caller strips
			// it from bodyBytes; otherwise the trailer stays embedded and resend
			// emits a duplicate 10= tag.
			mp.trailerBytes = preExtractBytes
			break
		} else {
			// Found a body field outside the group.
			// Is this a new group not inside the existing group.
			if isNumInGroupField(mp.msgDef, mp.parsedFieldBytes.tag) {
				// Add the current repeating group.
				mp.addUniqueBodyField(dm)
				// Cycle again with the new group.
				dm = mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1]
				group = mp.msgDef.Fields[int(mp.parsedFieldBytes.tag)]
				continue
			}
			// The tag isn't a member of the current group. Walk up: if an
			// ancestor group includes it, resume there; otherwise it's body-level.
			for len(tags) > 1 {
				tags = tags[:len(tags)-1]
				group = getGroupFieldDef(mp.msgDef, tags)
				if isGroupMember(mp.parsedFieldBytes.tag, group) {
					dm = append(dm, *mp.parsedFieldBytes)
					continue parseLoop
				}
			}
			// Add the repeating group.
			mp.addUniqueBodyField(dm)
			// Add the next body field.
			mp.addUniqueBodyField(mp.msg.fields[mp.fieldIndex : mp.fieldIndex+1])

			break
		}
	}

	return nil
}

// isNumInGroupField evaluates if this tag is the start of a top-level repeating group.
func isNumInGroupField(msgDef *datadictionary.MessageDef, tag Tag) bool {
	if msgDef == nil {
		return false
	}
	fd, ok := msgDef.Fields[int(tag)]
	return ok && len(fd.Fields) > 0
}

// getGroupFieldDef returns the definition of the repeating group the tags path
// leads to, or nil if it does not lead to a repeating group.
// tags slice will contain multiple tags if the tag in question is found while processing a group already.
func getGroupFieldDef(msgDef *datadictionary.MessageDef, tags []Tag) *datadictionary.FieldDef {
	if msgDef == nil {
		return nil
	}

	// A nil parent means the lookup is at message level.
	var parent *datadictionary.FieldDef
	for idx, tag := range tags {
		var fd *datadictionary.FieldDef
		var ok bool
		if parent == nil {
			fd, ok = msgDef.Fields[int(tag)]
		} else {
			fd, ok = parent.ChildField(int(tag))
		}
		if !ok {
			continue
		}

		if idx == len(tags)-1 {
			if len(fd.Fields) > 0 {
				return fd
			}
			return nil
		}
		parent = fd
	}

	return nil
}

// isGroupMember evaluates if this tag belongs to a repeating group.
func isGroupMember(tag Tag, group *datadictionary.FieldDef) bool {
	if group == nil {
		return false
	}
	_, ok := group.ChildField(int(tag))
	return ok
}

func isHeaderField(tag Tag, dataDict *datadictionary.DataDictionary) bool {
	if tag.IsHeader() {
		return true
	}

	if dataDict == nil {
		return false
	}

	_, ok := dataDict.Header.Fields[int(tag)]
	return ok
}

func isTrailerField(tag Tag, dataDict *datadictionary.DataDictionary) bool {
	if tag.IsTrailer() {
		return true
	}

	if dataDict == nil {
		return false
	}

	_, ok := dataDict.Trailer.Fields[int(tag)]
	return ok
}

// MsgType returns MsgType (tag 35) field's value.
func (m *Message) MsgType() (string, MessageRejectError) {
	return m.Header.GetString(tagMsgType)
}

// IsMsgTypeOf returns true if the Header contains MsgType (tag 35) field and its value is the specified one.
func (m *Message) IsMsgTypeOf(msgType string) bool {
	if v, err := m.MsgType(); err == nil {
		return v == msgType
	}
	return false
}

// reverseRoute returns a message builder with routing header fields initialized as the reverse of this message.
func (m *Message) reverseRoute() *Message {
	reverseMsg := NewMessage()

	copyFunc := func(src Tag, dest Tag) {
		// SetBytes copies the value, so the reverse message does not share
		// this message's buffer.
		if value, err := m.Header.GetBytes(src); err == nil && len(value) != 0 {
			reverseMsg.Header.SetBytes(dest, value)
		}
	}

	copyFunc(tagSenderCompID, tagTargetCompID)
	copyFunc(tagSenderSubID, tagTargetSubID)
	copyFunc(tagSenderLocationID, tagTargetLocationID)

	copyFunc(tagTargetCompID, tagSenderCompID)
	copyFunc(tagTargetSubID, tagSenderSubID)
	copyFunc(tagTargetLocationID, tagSenderLocationID)

	copyFunc(tagOnBehalfOfCompID, tagDeliverToCompID)
	copyFunc(tagOnBehalfOfSubID, tagDeliverToSubID)
	copyFunc(tagDeliverToCompID, tagOnBehalfOfCompID)
	copyFunc(tagDeliverToSubID, tagOnBehalfOfSubID)

	// Tags added in 4.1.
	if beginString, err := m.Header.GetBytes(tagBeginString); err == nil {
		if string(beginString) != BeginStringFIX40 {
			copyFunc(tagOnBehalfOfLocationID, tagDeliverToLocationID)
			copyFunc(tagDeliverToLocationID, tagOnBehalfOfLocationID)
		}
	}

	return reverseMsg
}

func extractSpecificField(field *TagValue, expectedTag Tag, buffer []byte) (remBuffer []byte, err error) {
	remBuffer, err = extractField(field, buffer)
	switch {
	case err != nil:
		return
	case field.tag != expectedTag:
		err = parseError{OrigError: fmt.Sprintf("extractSpecificField: Fields out of order, expected %d, got %d", expectedTag, field.tag)}
		return
	}

	return
}

// dataTagFor returns the data field that must follow a length field, or zero
// if tag is not a length field. It covers every LENGTH field paired with a
// DATA field in the FIX 4.0 to 5.0 SP2 and FIXT 1.1 specifications.
func dataTagFor(tag Tag) Tag {
	switch tag {
	case tagSignatureLength:
		// The only data field not numbered one above its length field.
		return tagSignature
	case tagSecureDataLen, // SecureData
		tagRawDataLength, // RawData
		tagXMLDataLen,    // XmlData
		348,              // EncodedIssuerLen
		350,              // EncodedSecurityDescLen
		352,              // EncodedListExecInstLen
		354,              // EncodedTextLen
		356,              // EncodedSubjectLen
		358,              // EncodedHeadlineLen
		360,              // EncodedAllocTextLen
		362,              // EncodedUnderlyingIssuerLen
		364,              // EncodedUnderlyingSecurityDescLen
		445,              // EncodedListStatusTextLen
		618,              // EncodedLegIssuerLen
		621,              // EncodedLegSecurityDescLen
		1184,             // SecurityXMLLen
		1277,             // DerivativeEncodedIssuerLen
		1280,             // DerivativeEncodedSecurityDescLen
		1282,             // DerivativeSecurityXMLLen
		1359,             // EncodedSymbolLen
		1397,             // EncodedMktSegmDescLen
		1401,             // EncryptedPasswordLen
		1403,             // EncryptedNewPasswordLen
		1468,             // EncodedSecurityListDescLen
		1618,             // RelationshipRiskEncodedSecurityDescLen
		1620:             // RiskEncodedSecurityDescLen
		return tag + 1
	}
	return 0
}

// extractDataField parses a length-prefixed DATA field. The value may contain
// SOH, so its end comes from dataLen rather than from the next delimiter.
func extractDataField(parsedFieldBytes *TagValue, buffer []byte, dataLen int, expectedTag Tag) (remBytes []byte, err error) {
	if dataLen < 0 {
		return buffer, parseError{OrigError: fmt.Sprintf("negative length for data tag %d", expectedTag)}
	}

	endIndex := bytes.IndexByte(buffer, '=')
	if endIndex < 0 {
		return buffer, parseError{OrigError: "extractDataField: No '=' in " + string(buffer)}
	}

	tag, tagErr := atoi(buffer[:endIndex])
	if tagErr != nil || Tag(tag) != expectedTag {
		return buffer, parseError{OrigError: fmt.Sprintf("extractDataField: expected tag %d, got %q", expectedTag, buffer[:endIndex])}
	}

	if dataLen > len(buffer)-endIndex-2 {
		return buffer, parseError{OrigError: fmt.Sprintf("extractDataField: length %d for tag %d runs past the end of the message", dataLen, expectedTag)}
	}
	endIndex += dataLen + 1

	err = parsedFieldBytes.parse(buffer[:endIndex+1])
	return buffer[(endIndex + 1):], err
}

func extractField(parsedFieldBytes *TagValue, buffer []byte) (remBytes []byte, err error) {
	endIndex := bytes.IndexByte(buffer, '\001')
	if endIndex == -1 {
		err = parseError{OrigError: "extractField: No Trailing Delim in " + string(buffer)}
		remBytes = buffer
		return
	}

	err = parsedFieldBytes.parse(buffer[:endIndex+1])
	return buffer[(endIndex + 1):], err
}

func (m *Message) Bytes() []byte {
	if m.rawMessage != nil {
		return m.rawMessage.Bytes()
	}

	return m.build()
}

func (m *Message) String() string {
	if m.rawMessage != nil {
		return m.rawMessage.String()
	}

	return string(m.build())
}

// checkSumDigits returns a checksum in the range 0-255 as the three
// zero-padded digits that CheckSum(10) requires.
func checkSumDigits(value int) [3]byte {
	return [3]byte{byte('0' + value/100), byte('0' + value/10%10), byte('0' + value%10)}
}

// Build constructs a []byte from a Message instance.
func (m *Message) build() []byte {
	b := bytes.NewBuffer(make([]byte, 0, m.cook(m.Body.lengthAndTotal(0))))
	m.Header.write(b)
	m.Body.write(b)
	m.Trailer.write(b)
	return b.Bytes()
}

// Constructs a []byte from a Message instance, using the given bodyBytes.
// This is a workaround for the fact that we currently rely on the generated Message types to properly serialize/deserialize RepeatingGroups.
// In other words, we cannot go from bytes to a Message then back to bytes, which is exactly what we need to do in the case of a Resend.
// This func lets us pull the Message from the Store, parse it, update the Header, and then build it back into bytes using the original Body.
// Note: The only standard non-Body group is NoHops.  If that is used in the Header, this workaround may fail.
func (m *Message) buildWithBodyBytes(bodyBytes []byte) []byte {
	b := bytes.NewBuffer(make([]byte, 0, m.cook(len(bodyBytes), bytesTotal(bodyBytes))))
	m.Header.write(b)
	b.Write(bodyBytes)
	m.Trailer.write(b)
	return b.Bytes()
}

// cook sets BodyLength and CheckSum from the body's length and total, and
// returns the size of the built message.
func (m *Message) cook(bodyLen, bodyTotal int) int {
	// BodyLength is left out of the header's total because it is about to
	// change; the new field's total is added once it is set.
	headerLen, headerTotal := m.Header.lengthAndTotal(tagBodyLength)
	trailerLen, trailerTotal := m.Trailer.lengthAndTotal(0)

	bodyLength := headerLen + bodyLen + trailerLen
	m.Header.SetInt(tagBodyLength, bodyLength)
	bodyLengthField := m.Header.fieldBytes(tagBodyLength)

	checkSum := (headerTotal + bytesTotal(bodyLengthField) + bodyTotal + trailerTotal) % 256
	digits := checkSumDigits(checkSum)
	m.Trailer.SetBytes(tagCheckSum, digits[:])

	return bodyLength + len(m.Header.fieldBytes(tagBeginString)) + len(bodyLengthField) + len(m.Trailer.fieldBytes(tagCheckSum))
}
