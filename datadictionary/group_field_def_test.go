package datadictionary_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/appital/gofix/datadictionary"
)

func TestNewGroupField(t *testing.T) {
	ft := datadictionary.NewFieldType("aname", 11, "INT")
	fg := datadictionary.NewGroupFieldDef(ft, true, []datadictionary.MessagePart{})
	assert.NotNil(t, fg)
	assert.Equal(t, "aname", fg.Name())
	assert.Equal(t, true, fg.Required())
}

func TestGroupFieldDefChildField(t *testing.T) {
	member := datadictionary.NewFieldDef(datadictionary.NewFieldType("member", 21, "STRING"), false)
	nested := datadictionary.NewGroupFieldDef(datadictionary.NewFieldType("nested", 22, "NUMINGROUP"), false,
		[]datadictionary.MessagePart{datadictionary.NewFieldDef(datadictionary.NewFieldType("inner", 23, "STRING"), false)})
	group := datadictionary.NewGroupFieldDef(datadictionary.NewFieldType("group", 20, "NUMINGROUP"), true,
		[]datadictionary.MessagePart{member, nested})

	for _, fd := range []*datadictionary.FieldDef{
		group,
		// Built without NewGroupFieldDef, so there is no index to consult.
		{FieldType: group.FieldType, Fields: group.Fields},
	} {
		child, ok := fd.ChildField(21)
		assert.True(t, ok)
		assert.Same(t, member, child)

		child, ok = fd.ChildField(22)
		assert.True(t, ok)
		assert.True(t, child.IsGroup())

		_, ok = fd.ChildField(23) // A grandchild is not a child.
		assert.False(t, ok)
		_, ok = fd.ChildField(99)
		assert.False(t, ok)
	}

	_, ok := member.ChildField(21)
	assert.False(t, ok)
}
