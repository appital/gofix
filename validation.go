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

	"github.com/appital/gofix/datadictionary"
)

const (
	UserDefinedTagMin int = 5000
)

// Validator validates a FIX message.
type Validator interface {
	Validate(*Message) MessageRejectError
}

// ValidatorSettings describe validation behavior.
type ValidatorSettings struct {
	CheckFieldsOutOfOrder     bool
	RejectInvalidMessage      bool
	AllowUnknownMessageFields bool
	CheckUserDefinedFields    bool
	CheckFieldsHaveValues     bool
}

// Default configuration for message validation.
// See http://www.quickfixengine.org/quickfix/doc/html/configuration.html.
var defaultValidatorSettings = ValidatorSettings{
	CheckFieldsOutOfOrder:     true,
	CheckFieldsHaveValues:     true,
	RejectInvalidMessage:      true,
	AllowUnknownMessageFields: false,
	CheckUserDefinedFields:    true,
}

type fixValidator struct {
	dataDictionary *datadictionary.DataDictionary
	settings       ValidatorSettings
}

type fixtValidator struct {
	transportDataDictionary *datadictionary.DataDictionary
	appDataDictionary       *datadictionary.DataDictionary
	settings                ValidatorSettings
}

// NewValidator creates a FIX message validator from the given data dictionaries.
func NewValidator(settings ValidatorSettings, appDataDictionary, transportDataDictionary *datadictionary.DataDictionary) Validator {
	if transportDataDictionary != nil {
		return &fixtValidator{
			transportDataDictionary: transportDataDictionary,
			appDataDictionary:       appDataDictionary,
			settings:                settings,
		}
	}
	return &fixValidator{
		dataDictionary: appDataDictionary,
		settings:       settings,
	}
}

// Validate tests the message against the provided data dictionary.
func (v *fixValidator) Validate(msg *Message) MessageRejectError {
	rawMsgType, err := msg.Header.GetBytes(tagMsgType)
	if err != nil {
		return RequiredTagMissing(tagMsgType)
	}

	return validateFIX(v.dataDictionary, v.settings, msgTypeString(v.dataDictionary, rawMsgType), msg)
}

// Validate tests the message against the provided transport and app data dictionaries.
// If the message is an admin message, it will be validated against the transport data dictionary.
func (v *fixtValidator) Validate(msg *Message) MessageRejectError {
	rawMsgType, err := msg.Header.GetBytes(tagMsgType)
	if err != nil {
		return RequiredTagMissing(tagMsgType)
	}

	if isAdminMessageType(rawMsgType) {
		return validateFIX(v.transportDataDictionary, v.settings, msgTypeString(v.transportDataDictionary, rawMsgType), msg)
	}
	return validateFIXT(v.transportDataDictionary, v.appDataDictionary, v.settings, msgTypeString(v.appDataDictionary, rawMsgType), msg)
}

// msgTypeString returns rawMsgType as a string. When d defines the message
// type, the dictionary's own copy of the string is returned so that validating
// a known message type does not allocate.
func msgTypeString(d *datadictionary.DataDictionary, rawMsgType []byte) string {
	if d != nil {
		if def, ok := d.Messages[string(rawMsgType)]; ok {
			return def.MsgType
		}
	}
	return string(rawMsgType)
}

func validateFIX(d *datadictionary.DataDictionary, settings ValidatorSettings, msgType string, msg *Message) MessageRejectError {
	if d != nil {
		if err := validateMsgType(d, msgType, msg); err != nil {
			return err
		}

		if err := validateRequired(d, d, msgType, msg); err != nil {
			return err
		}
	}

	if err := validateFieldContent(msg, settings.CheckFieldsHaveValues, settings.CheckFieldsOutOfOrder); err != nil {
		return err
	}

	if settings.RejectInvalidMessage && d != nil {
		if err := validateFields(d, d, settings, msgType, msg); err != nil {
			return err
		}

		if err := validateWalk(d, d, settings, msgType, msg); err != nil {
			return err
		}
	}

	return nil
}

func validateFIXT(transportDD, appDD *datadictionary.DataDictionary, settings ValidatorSettings, msgType string, msg *Message) MessageRejectError {
	if appDD != nil && transportDD != nil {
		if err := validateMsgType(appDD, msgType, msg); err != nil {
			return err
		}

		if err := validateRequired(transportDD, appDD, msgType, msg); err != nil {
			return err
		}
	}

	if err := validateFieldContent(msg, settings.CheckFieldsHaveValues, settings.CheckFieldsOutOfOrder); err != nil {
		return err
	}

	if settings.RejectInvalidMessage && appDD != nil && transportDD != nil {
		if err := validateFields(transportDD, appDD, settings, msgType, msg); err != nil {
			return err
		}

		if err := validateWalk(transportDD, appDD, settings, msgType, msg); err != nil {
			return err
		}
	}

	return nil
}

func validateMsgType(d *datadictionary.DataDictionary, msgType string, _ *Message) MessageRejectError {
	if _, validMsgType := d.Messages[msgType]; !validMsgType {
		return InvalidMessageType()
	}
	return nil
}

func validateWalk(transportDD *datadictionary.DataDictionary, appDD *datadictionary.DataDictionary, settings ValidatorSettings, msgType string, msg *Message) MessageRejectError {
	remainingFields := msg.fields
	var iteratedTags seenTags

	var messageDef *datadictionary.MessageDef
	var fieldDef *datadictionary.FieldDef
	var err MessageRejectError
	var ok bool

	for len(remainingFields) > 0 {
		field := remainingFields[0]
		tag := field.tag

		switch {
		case tag.IsHeader():
			messageDef = transportDD.Header
		case tag.IsTrailer():
			messageDef = transportDD.Trailer
		default: // is body
			messageDef = appDD.Messages[msgType]
		}

		if iteratedTags.visit(tag) {
			return tagAppearsMoreThanOnce(tag)
		}

		if fieldDef, ok = messageDef.Fields[int(tag)]; !ok {
			if !checkFieldNotDefined(settings, tag) {
				return TagNotDefinedForThisMessageType(tag)
			}
			remainingFields = remainingFields[1:]
			continue
		}

		if remainingFields, err = validateVisitField(fieldDef, remainingFields); err != nil {
			return err
		}
	}

	if len(remainingFields) != 0 {
		return TagNotDefinedForThisMessageType(remainingFields[0].tag)
	}

	return nil
}

// seenTagsBitsetSize is the number of tags that seenTags tracks in its bitset.
// It covers the standard FIX tags and most of the user-defined range.
const seenTagsBitsetSize = 8192

// seenTags records the tags visited while walking a message. Tags below
// seenTagsBitsetSize are kept in a fixed-size bitset, so the common case needs
// no allocation; higher tags fall back to a map created on first use.
type seenTags struct {
	low  [seenTagsBitsetSize / 64]uint64
	high map[Tag]struct{}
}

// visit marks tag as seen and reports whether it had already been seen.
func (s *seenTags) visit(tag Tag) bool {
	if tag >= 0 && tag < seenTagsBitsetSize {
		word, bit := tag/64, uint64(1)<<(tag%64)
		seen := s.low[word]&bit != 0
		s.low[word] |= bit
		return seen
	}

	if _, seen := s.high[tag]; seen {
		return true
	}
	if s.high == nil {
		s.high = make(map[Tag]struct{})
	}
	s.high[tag] = struct{}{}
	return false
}

func validateVisitField(fieldDef *datadictionary.FieldDef, fields []TagValue) ([]TagValue, MessageRejectError) {
	if fieldDef.IsGroup() {
		var err MessageRejectError
		if fields, err = validateVisitGroupField(fieldDef, fields); err != nil {
			return nil, err
		}
		return fields, nil
	}

	return fields[1:], nil
}

func validateVisitGroupField(fieldDef *datadictionary.FieldDef, fieldStack []TagValue) ([]TagValue, MessageRejectError) {
	numInGroupTag := fieldStack[0].tag
	var numInGroup FIXInt

	if err := numInGroup.Read(fieldStack[0].value); err != nil {
		return nil, IncorrectDataFormatForValue(numInGroupTag)
	}

	fieldStack = fieldStack[1:]

	var childDefs []*datadictionary.FieldDef
	groupCount := 0

	for len(fieldStack) > 0 {

		// Start of repeating group.
		if int(fieldStack[0].tag) == fieldDef.Fields[0].Tag() {
			childDefs = fieldDef.Fields
			groupCount++
		}

		// Group complete.
		if len(childDefs) == 0 {
			break
		}

		if int(fieldStack[0].tag) == childDefs[0].Tag() {
			var err MessageRejectError
			if fieldStack, err = validateVisitField(childDefs[0], fieldStack); err != nil {
				return fieldStack, err
			}
		} else {
			if childDefs[0].Required() {
				return fieldStack, RequiredTagMissing(Tag(childDefs[0].Tag()))
			}
		}

		childDefs = childDefs[1:]
	}

	if groupCount != int(numInGroup) {
		return fieldStack, incorrectNumInGroupCountForRepeatingGroup(numInGroupTag)
	}

	return fieldStack, nil
}

func validateFieldContent(msg *Message, checkFieldsHaveValues, checkFieldsOutOfOrder bool) MessageRejectError {
	if !checkFieldsHaveValues && !checkFieldsOutOfOrder {
		return nil
	}
	inHeader := true
	inTrailer := false
	for _, field := range msg.fields {
		t := field.tag
		if checkFieldsHaveValues && len(field.value) == 0 {
			return TagSpecifiedWithoutAValue(t)
		}
		switch {
		case inHeader && t.IsHeader():
		case inHeader && !t.IsHeader():
			inHeader = false
		case !inHeader && t.IsHeader() && checkFieldsOutOfOrder:
			return tagSpecifiedOutOfRequiredOrder(t)
		case t.IsTrailer():
			inTrailer = true
		case inTrailer && !t.IsTrailer() && checkFieldsOutOfOrder:
			return tagSpecifiedOutOfRequiredOrder(t)
		}
	}

	return nil
}

func validateRequired(transportDD *datadictionary.DataDictionary, appDD *datadictionary.DataDictionary, msgType string, message *Message) MessageRejectError {
	if err := validateRequiredFieldMap(message, transportDD.Header.RequiredTags, message.Header.FieldMap); err != nil {
		return err
	}

	if err := validateRequiredFieldMap(message, appDD.Messages[msgType].RequiredTags, message.Body.FieldMap); err != nil {
		return err
	}

	if err := validateRequiredFieldMap(message, transportDD.Trailer.RequiredTags, message.Trailer.FieldMap); err != nil {
		return err
	}

	return nil
}

func validateRequiredFieldMap(_ *Message, requiredTags map[int]struct{}, fieldMap FieldMap) MessageRejectError {
	for required := range requiredTags {
		requiredTag := Tag(required)
		if !fieldMap.Has(requiredTag) {
			return RequiredTagMissing(requiredTag)
		}
	}

	return nil
}

func validateFields(transportDD *datadictionary.DataDictionary,
	appDD *datadictionary.DataDictionary,
	settings ValidatorSettings,
	msgType string,
	message *Message,
) MessageRejectError {
	for _, field := range message.fields {
		switch {
		case field.tag.IsHeader():
			if err := validateField(transportDD, settings, transportDD.Header.Tags, field); err != nil {
				return err
			}
		case field.tag.IsTrailer():
			if err := validateField(transportDD, settings, transportDD.Trailer.Tags, field); err != nil {
				return err
			}
		default:
			if err := validateField(appDD, settings, appDD.Messages[msgType].Tags, field); err != nil {
				return err
			}
		}
	}

	return nil
}

func getFieldType(d *datadictionary.DataDictionary, field int) (*datadictionary.FieldType, bool) {
	fieldType, isMessageField := d.FieldTypeByTag[field]
	return fieldType, isMessageField
}

func checkFieldNotDefined(settings ValidatorSettings, field Tag) bool {
	fail := false
	if int(field) < UserDefinedTagMin {
		fail = !settings.AllowUnknownMessageFields
	} else {
		fail = settings.CheckUserDefinedFields
	}
	return !fail
}

func validateField(d *datadictionary.DataDictionary,
	settings ValidatorSettings,
	_ datadictionary.TagSet,
	field TagValue,
) MessageRejectError {
	if len(field.value) == 0 {
		return TagSpecifiedWithoutAValue(field.tag)
	}

	fieldType, isMessageField := getFieldType(d, int(field.tag))
	if !isMessageField && !checkFieldNotDefined(settings, field.tag) {
		return InvalidTagNumber(field.tag)
	}

	if !isMessageField {
		return nil
	}

	allowedValues := fieldType.Enums
	if len(allowedValues) != 0 {
		switch fieldType.Type {
		case "MULTIPLESTRINGVALUE", "MULTIPLEVALUESTRING", "MULTIPLECHARVALUE":
			// These fields carry a space-delimited set of enum tokens, each of
			// which must be individually valid (e.g. ExecInst "1 5" = two enums).
			for component := range bytes.SplitSeq(field.value, []byte{' '}) {
				if _, validValue := allowedValues[string(component)]; !validValue {
					return ValueIsIncorrect(field.tag)
				}
			}
		default:
			if _, validValue := allowedValues[string(field.value)]; !validValue {
				return ValueIsIncorrect(field.tag)
			}
		}
	}

	if !validFieldFormat(fieldType.Type, field.value) {
		return IncorrectDataFormatForValue(field.tag)
	}

	return nil
}

// validFieldFormat reports whether value is well formed for the data
// dictionary type fieldType. Values are read into concrete locals rather than
// through a FieldValue, so checking a valid field does not allocate.
func validFieldFormat(fieldType string, value []byte) bool {
	switch fieldType {
	case "BOOLEAN":
		var v FIXBoolean
		return v.Read(value) == nil

	case "LENGTH", "DAYOFMONTH", "NUMINGROUP", "SEQNUM", "INT":
		var v FIXInt
		return v.Read(value) == nil

	case "UTCTIMESTAMP", "TIME":
		var v FIXUTCTimestamp
		return v.Read(value) == nil

	case "QTY", "QUANTITY", "AMT", "PRICE", "PRICEOFFSET", "PERCENTAGE", "FLOAT":
		var v FIXFloat
		return v.Read(value) == nil

	default:
		// The string-like types (STRING, CHAR, CURRENCY, DATA, the date and
		// time-only types and so on) accept any non-empty value, which
		// validateField has already checked. Types this switch does not
		// know are treated the same way.
		return true
	}
}
