package internal

import (
	"strings"
	"text/template"
)

var (
	HeaderTemplate  *template.Template
	TrailerTemplate *template.Template
	MessageTemplate *template.Template
	TagTemplate     *template.Template
	FieldTemplate   *template.Template
	EnumTemplate    *template.Template
)

func init() {
	tmplFuncs := template.FuncMap{
		"toLower":                               strings.ToLower,
		"requiredFields":                        requiredFields,
		"beginString":                           beginString,
		"routerBeginString":                     routerBeginString,
		"importRootPath":                        getImportPathRoot,
		"fixType":                               fixType,
		"fixValueType":                          fixValueType,
		"getGlobalFieldType":                    getGlobalFieldType,
		"collectStandardImports":                collectStandardImports,
		"collectExtraImports":                   collectExtraImports,
		"checkIfDecimalImportRequiredForFields": checkIfDecimalImportRequiredForFields,
		"decimalImport": func() string {
			if *useUDecimal {
				return "github.com/quagmt/udecimal"
			}
			return "github.com/shopspring/decimal"
		},
		"checkIfTimeImportRequiredForFields": checkIfTimeImportRequiredForFields,
		"checkIfEnumImportRequired":          checkIfEnumImportRequired,
	}

	baseTemplate := template.Must(template.New("Base").Funcs(tmplFuncs).Parse(`
{{ define "receiver" }}RECEIVER{{ end }}

{{ define "fieldsetter" -}}
{{- $field_type := getGlobalFieldType . -}}
{{- $qfix_type := fixType $field_type -}}
{{- if and ($field_type.Enums) (ne $qfix_type "FIXBoolean") -}}
Set{{ .Name }}(v enum.{{ .Name }}) {
	{{ template "receiver" }}.Set(field.New{{ .Name }}(v))
}
{{- else if eq $qfix_type "FIXDecimal" -}}
Set{{ .Name }}(value decimal.Decimal, scale int32) {
	{{ template "receiver" }}.Set(field.New{{ .Name }}(value, scale))
}
{{- else if eq $qfix_type "FIXUDecimal" -}}
Set{{ .Name }}(value udecimal.Decimal, scale uint8) {
	{{ template "receiver" }}.Set(field.New{{ .Name }}(value, scale))
}
{{- else -}}
Set{{ .Name }}(v {{ fixValueType $qfix_type }}) {
	{{ template "receiver" }}.Set(field.New{{ .Name }}(v))
}
{{- end }}{{ end }}

{{ define "groupsetter" -}}
Set{{ .Name }}(f {{ .Name }}RepeatingGroup){
	{{ template "receiver" }}.SetGroup(f)
}
{{- end }}

{{ define "setters" }}
{{ range .Fields }}
// Set{{ .Name }} sets {{ .Name }}, Tag {{ .Tag }}.
func ({{ template "receiver" }} {{ $.Name }}) {{ if .IsGroup }}{{ template "groupsetter" . }}{{ else }}{{ template "fieldsetter" . }}{{ end }}
{{ end }}{{ end }}

{{ define "fieldgetter" -}}
Get{{ .Name }}() (f field.{{ .Name }}Field, err gofix.MessageRejectError) {
	err = {{ template "receiver" }}.Get(&f)
	return
}
{{- end }}

{{ define "fieldvaluegetter" -}}
{{- $ft := getGlobalFieldType . -}}
{{- $bt := fixType $ft -}}
{{- if and $ft.Enums (ne $bt "FIXBoolean") -}}
Get{{ .Name }}() (v enum.{{ .Name }}, err gofix.MessageRejectError) {
{{- else if eq $bt "FIXDecimal" -}}
Get{{ .Name }}() (v decimal.Decimal, err gofix.MessageRejectError) {
{{- else if eq $bt "FIXUDecimal" -}}
Get{{ .Name }}() (v udecimal.Decimal, err gofix.MessageRejectError) {
{{- else -}}
Get{{ .Name }}() (v {{ fixValueType $bt }}, err gofix.MessageRejectError) {
{{- end }}
	var f field.{{ .Name }}Field
	if err = {{ template "receiver" }}.Get(&f); err == nil {
		v = f.Value()
	}
	return
}
{{- end }}

{{ define "groupgetter" -}}
Get{{ .Name }}() ({{ .Name }}RepeatingGroup, gofix.MessageRejectError) {
	f := New{{ .Name }}RepeatingGroup()
	err := {{ template "receiver" }}.GetGroup(f)
	return f, err
}
{{- end }}


{{ define "getters" }}
{{ range .Fields }}
// Get{{ .Name }} gets {{ .Name }}, Tag {{ .Tag }}.
func ({{ template "receiver" }} {{ $.Name }}) {{if .IsGroup}}{{ template "groupgetter" . }}{{ else }}{{ template "fieldvaluegetter" .}}{{ end }}
{{ end }}{{ end }}

{{ define "hasers" }}
{{range .Fields}}
// Has{{ .Name}} returns true if {{ .Name}} is present, Tag {{ .Tag}}.
func ({{ template "receiver" }} {{ $.Name }}) Has{{ .Name}}() bool {
	return {{ template "receiver" }}.Has(tag.{{ .Name}})
}
{{end}}{{ end }}

{{ define "group_template" }}
gofix.GroupTemplate{
{{- range $index, $field := . }}
	{{- if $field.IsGroup }}
	New{{ $field.Name }}RepeatingGroup(),
	{{- else}}
	gofix.GroupElement(tag.{{$field.Name}}),
	{{- end }}
{{- end }}
}
{{- end }}

{{ define "field_args" }}
{{- range $index, $field := . }}{{if $index}},{{end}}{{toLower $field.Name}} field.{{ .Name }}Field{{ end }}
{{- end }}

{{ define "groups" }}
{{ range .Fields }}
{{ if .IsGroup }}
// {{ .Name }} is a repeating group element, Tag {{ .Tag }}.
type {{ .Name }} struct {
	*gofix.Group
}

{{ template "setters" .}}
{{ template "getters" . }}
{{ template "hasers" . }}
{{ template "groups" . }}

// {{ .Name }}RepeatingGroup is a repeating group, Tag {{ .Tag }}.
type {{ .Name }}RepeatingGroup struct {
	*gofix.RepeatingGroup
}

// New{{ .Name }}RepeatingGroup returns an initialized, {{ .Name }}RepeatingGroup.
func New{{ .Name }}RepeatingGroup() {{ .Name }}RepeatingGroup {
	return {{ .Name }}RepeatingGroup{
		gofix.NewRepeatingGroup(
			tag.{{ .Name }},
			{{- template "group_template" .Fields }},
		),
	}
}

// Add create and append a new {{ .Name }} to this group.
func ({{ template "receiver" }} {{ .Name }}RepeatingGroup) Add() {{ .Name }} {
	g := {{ template "receiver" }}.RepeatingGroup.Add()
	return {{ .Name }}{g}
}

// Get returns the ith {{ .Name }} in the {{ .Name }}RepeatinGroup.
func ({{ template "receiver" }} {{ .Name}}RepeatingGroup) Get(i int) {{ .Name }} {
	return {{ .Name }}{ {{ template "receiver" }}.RepeatingGroup.Get(i) }
}

{{ end }}{{ end }}{{ end }}
`))

	HeaderTemplate = template.Must(template.Must(baseTemplate.Clone()).Parse(`
{{ define "receiver" }}h{{ end }}
// Code generated by gofix. DO NOT EDIT.
package {{ .Package }}

import (
	{{- if collectStandardImports .MessageDef }}
	{{- range collectStandardImports .MessageDef }}
	"{{ . }}"
	{{- end }}{{ "\n" }}
	{{- end }}
	{{- if collectExtraImports .MessageDef }}
	{{- range collectExtraImports .MessageDef }}
	"{{ . }}"
	{{- end }}{{ "\n" }}
	{{- end }}
	"github.com/appital/gofix"
	{{- if checkIfEnumImportRequired .MessageDef}}
	"{{ importRootPath }}/enum"
	{{- end }}
	"{{ importRootPath }}/field"
	"{{ importRootPath }}/tag"
)

// Header is the {{ .Package }} Header type.
type Header struct {
	*gofix.Header
}

// NewHeader returns a new, initialized Header instance.
func NewHeader(header *gofix.Header) (h Header) {
	h.Header = header
	h.SetBeginString("{{ beginString .FIXSpec }}")
	return
}

{{ template "setters" .}}
{{ template "getters" . }}
{{ template "hasers" . }}
{{ template "groups" . }}
	`))

	TrailerTemplate = template.Must(template.Must(baseTemplate.Clone()).Parse(`
{{ define "receiver" }}t{{ end }}
// Code generated by gofix. DO NOT EDIT.
package {{ .Package }}

import (
	{{- if collectStandardImports .MessageDef }}
	{{- range collectStandardImports .MessageDef }}
	"{{ . }}"
	{{- end }}{{ "\n" }}
	{{- end }}
	{{- if collectExtraImports .MessageDef }}
	{{- range collectExtraImports .MessageDef }}
	"{{ . }}"
	{{- end }}{{ "\n" }}
	{{- end }}
	"github.com/appital/gofix"
	{{- if checkIfEnumImportRequired .MessageDef}}
	"{{ importRootPath }}/enum"
	{{- end }}
	"{{ importRootPath }}/field"
	"{{ importRootPath }}/tag"
)

// Trailer is the {{ .Package }} Trailer type.
type Trailer struct {
	*gofix.Trailer
}

{{ template "setters" .}}
{{ template "getters" . }}
{{ template "hasers" . }}
{{ template "groups" . }}
`))

	MessageTemplate = template.Must(baseTemplate.Parse(`
{{ define "receiver" }}m{{ end }}
// Code generated by gofix. DO NOT EDIT.
package {{ .Package }}

import (
	{{- if collectStandardImports .MessageDef }}
	{{- range collectStandardImports .MessageDef }}
	"{{ . }}"
	{{- end }}{{ "\n" }}
	{{- end }}
	{{- if collectExtraImports .MessageDef }}
	{{- range collectExtraImports .MessageDef }}
	"{{ . }}"
	{{- end }}{{ "\n" }}
	{{- end }}
	"github.com/appital/gofix"
	{{- if checkIfEnumImportRequired .MessageDef}}
	"{{ importRootPath }}/enum"
	{{- end }}
	"{{ importRootPath }}/field"
	"{{ importRootPath }}/{{ .TransportPackage }}"
	"{{ importRootPath }}/tag"
)

// {{ .Name }} is the {{ .FIXPackage }} {{ .Name }} type, MsgType = {{ .MsgType }}.
type {{ .Name }} struct {
	{{ .TransportPackage }}.Header
	*gofix.Body
	{{ .TransportPackage }}.Trailer
	Message *gofix.Message
}

// FromMessage creates a {{ .Name }} from a gofix.Message instance.
func FromMessage(m *gofix.Message) {{ .Name }} {
	return {{ .Name }}{
		Header: {{ .TransportPackage}}.Header{Header: &m.Header},
		Body: &m.Body,
		Trailer: {{ .TransportPackage}}.Trailer{Trailer: &m.Trailer},
		Message: m,
	}
}

// ToMessage returns a gofix.Message instance.
func (m {{ .Name }}) ToMessage() *gofix.Message {
	return m.Message
}

{{ $required_fields := requiredFields .MessageDef -}}
// New returns a {{ .Name }} initialized with the required fields for {{ .Name }}.
func New({{template "field_args" $required_fields }}) (m {{ .Name }}) {
	m.Message = gofix.NewMessage()
	m.Header = {{ .TransportPackage }}.NewHeader(&m.Message.Header)
	m.Body = &m.Message.Body
	m.Trailer.Trailer = &m.Message.Trailer

	m.Header.Set(field.NewMsgType("{{ .MessageDef.MsgType }}"))
	{{- range $required_fields }}
	m.Set({{ toLower .Name }})
	{{- end }}

	return
}

// A RouteOut is the callback type that should be implemented for routing Message.
type RouteOut func(msg {{ .Name }}, sessionID gofix.SessionID) gofix.MessageRejectError

// Route returns the beginstring, message type, and MessageRoute for this Message type.
func Route(router RouteOut) (string, string, gofix.MessageRoute) {
	r:=func(msg *gofix.Message, sessionID gofix.SessionID) gofix.MessageRejectError {
		return router(FromMessage(msg), sessionID)
	}
	return "{{ routerBeginString .FIXSpec }}", "{{ .MessageDef.MsgType }}", r
}

{{ template "setters" . }}
{{ template "getters" . }}
{{ template "hasers" . }}
{{ template "groups" . }}
	`))

	TagTemplate = template.Must(template.New("Tag").Parse(`
// Code generated by gofix. DO NOT EDIT.
package tag
import "github.com/appital/gofix"

const (
{{- range .}}
{{ .Name }} gofix.Tag = {{ .Tag }}
{{- end }}
)
	`))

	FieldTemplate = template.Must(template.New("Field").Funcs(tmplFuncs).Parse(`
// Code generated by gofix. DO NOT EDIT.
package field
import (
	{{ if checkIfTimeImportRequiredForFields . }}"time"{{ end }}

	{{ if checkIfDecimalImportRequiredForFields . }}"{{ decimalImport }}"{{ end }}

	"github.com/appital/gofix"
	"{{ importRootPath }}/enum"
	"{{ importRootPath }}/tag"
)

{{ range . }}
{{- $base_type := fixType . -}}

{{ if and .Enums (ne $base_type "FIXBoolean") }}
// {{ .Name }}Field is a enum.{{ .Name }} field.
type {{ .Name }}Field struct { gofix.FIXString }
{{ else }}
// {{ .Name }}Field is a {{ .Type }} field.
type {{ .Name }}Field struct { gofix.{{ $base_type }} }
{{ end }}

// Tag returns tag.{{ .Name }} ({{ .Tag }}).
func (f {{ .Name }}Field) Tag() gofix.Tag { return tag.{{ .Name }} }

{{ if eq $base_type "FIXUTCTimestamp" }}
// New{{ .Name }} returns a new {{ .Name }}Field initialized with val.
func New{{ .Name }}(val time.Time) {{ .Name }}Field {
	return New{{ .Name }}WithPrecision(val, gofix.Millis)
}

// New{{ .Name }}NoMillis returns a new {{ .Name }}Field initialized with val without millisecs.
func New{{ .Name }}NoMillis(val time.Time) {{ .Name }}Field {
	return New{{ .Name }}WithPrecision(val, gofix.Seconds)
}

// New{{ .Name }}WithPrecision returns a new {{ .Name }}Field initialized with val of specified precision.
func New{{ .Name }}WithPrecision(val time.Time, precision gofix.TimestampPrecision) {{ .Name }}Field {
	return {{ .Name }}Field{ gofix.FIXUTCTimestamp{ Time: val, Precision: precision } }
}

{{ else if and  .Enums (ne $base_type "FIXBoolean") }}
func New{{ .Name }}(val enum.{{ .Name }}) {{ .Name }}Field {
	return {{ .Name }}Field{ gofix.FIXString(val) }
}
{{ else if eq $base_type "FIXDecimal" }}
// New{{ .Name }} returns a new {{ .Name }}Field initialized with val and scale.
func New{{ .Name }}(val decimal.Decimal, scale int32) {{ .Name }}Field {
	return {{ .Name }}Field{ gofix.FIXDecimal{ Decimal: val, Scale: scale} }
}
{{ else if eq $base_type "FIXUDecimal" }}
// New{{ .Name }} returns a new {{ .Name }}Field initialized with val and scale.
func New{{ .Name }}(val udecimal.Decimal, scale uint8) {{ .Name }}Field {
	return {{ .Name }}Field{ gofix.FIXUDecimal{ Decimal: val, Scale: scale} }
}
{{ else }}
// New{{ .Name }} returns a new {{ .Name }}Field initialized with val.
func New{{ .Name }}(val {{ fixValueType $base_type }}) {{ .Name }}Field {
	return {{ .Name }}Field{ gofix.{{ $base_type }}(val) }
}
{{ end }}

{{ if and  .Enums (ne $base_type "FIXBoolean") }}
func (f {{ .Name }}Field) Value() enum.{{ .Name }} { return enum.{{ .Name }}(f.String()) }
{{ else if eq $base_type "FIXDecimal" }}
func (f {{ .Name }}Field) Value() (val decimal.Decimal) { return f.Decimal }
{{ else if eq $base_type "FIXUDecimal" }}
func (f {{ .Name }}Field) Value() (val udecimal.Decimal) { return f.Decimal }
{{ else }}
func (f {{ .Name }}Field) Value() ({{ fixValueType $base_type }}) {
{{- if eq $base_type "FIXString" -}}
 return f.String() }
{{- else if eq $base_type "FIXBoolean" -}}
 return f.Bool() }
{{- else if eq $base_type "FIXInt" -}}
 return f.Int() }
{{- else if eq $base_type "FIXUTCTimestamp" -}}
 return f.Time }
{{- else if eq $base_type "FIXFloat" -}}
 return f.Float() }
{{- else -}}
 TEMPLATE ERROR: Value() for {{ $base_type }}
{{ end }}{{ end }}{{ end }}
`))

	EnumTemplate = template.Must(template.New("Enum").Parse(`
// Code generated by gofix. DO NOT EDIT.
package enum
{{ range $ft := . }}
{{ if $ft.Enums }}
// {{ $ft.Name }} field enumeration values.
type {{ $ft.Name }} string
const(
{{ range $ft.Enums }}
{{ $ft.Name }}_{{ .Description }} {{ $ft.Name }} = "{{ .Value }}"
{{- end }}
)
{{ end }}{{ end }}
	`))
}
