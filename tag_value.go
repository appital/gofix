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
	"strconv"
)

// TagValue is a low-level FIX field abstraction.
type TagValue struct {
	tag   Tag
	value []byte
	bytes []byte
}

func (tv *TagValue) init(tag Tag, value []byte) {
	var tagBuf [20]byte
	tagBytes := strconv.AppendInt(tagBuf[:0], int64(tag), 10)

	// Size the field exactly so it takes a single allocation.
	n := len(tagBytes) + 1 + len(value) + 1
	tv.bytes = make([]byte, 0, n)
	tv.bytes = append(tv.bytes, tagBytes...)
	tv.bytes = append(tv.bytes, '=')
	tv.bytes = append(tv.bytes, value...)
	tv.bytes = append(tv.bytes, '\x01')

	tv.tag = tag
	// The value is a view of the field's own copy, so the field does not
	// alias, and is not changed by later writes to, the caller's slice.
	tv.value = tv.bytes[len(tagBytes)+1 : n-1 : n-1]
}

func (tv *TagValue) parse(rawFieldBytes []byte) error {
	var sepIndex int

	// Most of the Fix tags are 4 or less characters long, so we can optimize
	// for that by checking the 5 first characters without looping over the
	// whole byte slice.
	if len(rawFieldBytes) >= 5 {
		if rawFieldBytes[1] == '=' {
			sepIndex = 1
			goto PARSE
		} else if rawFieldBytes[2] == '=' {
			sepIndex = 2
			goto PARSE
		} else if rawFieldBytes[3] == '=' {
			sepIndex = 3
			goto PARSE
		} else if rawFieldBytes[4] == '=' {
			sepIndex = 4
			goto PARSE
		}
	}

	sepIndex = bytes.IndexByte(rawFieldBytes, '=')
	switch sepIndex {
	case -1:
		return fmt.Errorf("tagValue.Parse: No '=' in '%s'", rawFieldBytes)
	case 0:
		return fmt.Errorf("tagValue.Parse: No tag in '%s'", rawFieldBytes)
	}

PARSE:
	parsedTag, err := atoi(rawFieldBytes[:sepIndex])
	if err != nil {
		return fmt.Errorf("tagValue.Parse: %s", err.Error())
	}

	tv.tag = Tag(parsedTag)
	n := len(rawFieldBytes)
	tv.value = rawFieldBytes[(sepIndex + 1):(n - 1):(n - 1)]
	tv.bytes = rawFieldBytes[:n:n]

	return nil
}

func (tv TagValue) String() string {
	return string(tv.bytes)
}

func bytesTotal(bytes []byte) (total int) {
	for _, b := range bytes {
		total += int(b)
	}
	return
}

func (tv TagValue) total() int {
	return bytesTotal(tv.bytes)
}

func (tv TagValue) length() int {
	return len(tv.bytes)
}
