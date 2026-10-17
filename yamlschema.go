// Package yamlschema checks YAML files against a JSON Schema. A YAML
// document is read with github.com/goccy/go-yaml, converted to the JSON it
// stands for, and validated with github.com/crhntr/jsonschema; each problem
// is reported on the line and column of the YAML it is about.
//
// The conversion follows the YAML syntax tree itself, so it keeps the
// order of mapping keys, expands anchors and aliases and applies merge
// keys ("<<"), and records where every value is written.
package yamlschema

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/crhntr/jsonschema/jsonptr"
)

// Pos is a position in YAML source, counted from 1.
type Pos struct {
	Line, Column int
}

// Positions maps the JSON Pointer of each value to where it is written in
// the YAML.
type Positions map[string]Pos

// At returns the position of ptr, or of the nearest enclosing value that
// has one. Values that come from a merge key or an alias have the
// position of the key or alias that brought them in.
func (p Positions) At(ptr string) Pos {
	for {
		if pos, ok := p[ptr]; ok {
			return pos
		}
		i := strings.LastIndexByte(ptr, '/')
		if i < 0 {
			return p[""]
		}
		ptr = ptr[:i]
	}
}

// Document is one YAML document of a file, as JSON.
type Document struct {
	// JSON is the document's value.
	JSON jsontext.Value
	// Positions says where each value of JSON is written.
	Positions Positions
	// Keys says where each mapping member's key is written.
	Keys Positions
	// Tags holds the explicit YAML tag (such as !!binary or !secret) of the
	// values that have one, by JSON Pointer. JSON cannot say this, so it
	// is here for callers that want to check it.
	Tags map[string]string
}

// Parse reads every document in a YAML file. A document with no content,
// such as an empty file, is null.
func Parse(src []byte) ([]Document, error) {
	file, err := parser.ParseBytes(src, 0)
	if err != nil {
		return nil, fmt.Errorf("reading YAML: %w", err)
	}
	var docs []Document
	for i, d := range file.Docs {
		doc, err := convert(d)
		if err != nil {
			if len(file.Docs) > 1 {
				err = fmt.Errorf("document %d: %w", i+1, err)
			}
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// maxDepth bounds nesting and alias expansion, which a small file can make
// enormous ("billion laughs").
const maxDepth = 128

// maxNodes bounds the size of the JSON a document may expand to.
const maxNodes = 1 << 20

type converter struct {
	enc     *jsontext.Encoder
	buf     bytes.Buffer
	pos     Positions
	keys    Positions
	tags    map[string]string
	anchors map[string]ast.Node
	nodes   int
}

func convert(d *ast.DocumentNode) (Document, error) {
	c := &converter{pos: Positions{}, keys: Positions{}, tags: map[string]string{}, anchors: map[string]ast.Node{}}
	c.enc = jsontext.NewEncoder(&c.buf)
	if d.Body == nil {
		if err := c.enc.WriteToken(jsontext.Null); err != nil {
			return Document{}, err
		}
	} else if err := c.value(d.Body, "", 0); err != nil {
		return Document{}, err
	}
	return Document{JSON: bytes.TrimSpace(c.buf.Bytes()), Positions: c.pos, Keys: c.keys, Tags: c.tags}, nil
}

func where(n ast.Node) Pos {
	if n == nil {
		return Pos{}
	}
	if tk := n.GetToken(); tk != nil && tk.Position != nil {
		return Pos{tk.Position.Line, tk.Position.Column}
	}
	return Pos{}
}

// start is where a value begins: a mapping starts at its first key.
func start(n ast.Node) Pos {
	switch n := n.(type) {
	case *ast.MappingNode:
		if len(n.Values) > 0 {
			return where(n.Values[0].Key)
		}
	case *ast.MappingValueNode:
		return where(n.Key)
	}
	return where(n)
}

func (c *converter) note(ptr string, at Pos) {
	if _, ok := c.pos[ptr]; !ok && at.Line > 0 {
		c.pos[ptr] = at
	}
}

func (c *converter) value(n ast.Node, ptr string, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%s: nested too deeply", pointerText(ptr))
	}
	if c.nodes++; c.nodes > maxNodes {
		return errors.New("the document expands to too much data")
	}
	c.note(ptr, start(n))
	switch n := n.(type) {
	case *ast.AnchorNode:
		c.anchors[nameOf(n.Name)] = n.Value
		return c.value(n.Value, ptr, depth+1)
	case *ast.AliasNode:
		target, ok := c.anchors[nameOf(n.Value)]
		if !ok {
			return fmt.Errorf("line %d: no anchor named %q", where(n).Line, nameOf(n.Value))
		}
		return c.value(target, ptr, depth+1)
	case *ast.TagNode:
		return c.tagged(n, ptr, depth)
	case *ast.MappingNode:
		return c.mapping(n.Values, ptr, depth)
	case *ast.MappingValueNode:
		return c.mapping([]*ast.MappingValueNode{n}, ptr, depth)
	case *ast.SequenceNode:
		if err := c.enc.WriteToken(jsontext.BeginArray); err != nil {
			return err
		}
		for i, item := range n.Values {
			if err := c.value(item, ptr+"/"+strconv.Itoa(i), depth+1); err != nil {
				return err
			}
		}
		return c.enc.WriteToken(jsontext.EndArray)
	case *ast.NullNode:
		return c.enc.WriteToken(jsontext.Null)
	case *ast.BoolNode:
		return c.enc.WriteToken(jsontext.Bool(n.Value))
	case *ast.IntegerNode:
		switch v := n.Value.(type) {
		case int64:
			return c.enc.WriteToken(jsontext.Int(v))
		case uint64:
			return c.enc.WriteToken(jsontext.Uint(v))
		}
		return fmt.Errorf("line %d: unsupported integer %v", where(n).Line, n.Value)
	case *ast.FloatNode:
		return c.float(n.Value, where(n))
	case *ast.InfinityNode:
		return c.float(n.Value, where(n))
	case *ast.NanNode:
		return c.float(math.NaN(), where(n))
	case *ast.StringNode:
		return c.enc.WriteToken(jsontext.String(n.Value))
	case *ast.LiteralNode:
		return c.enc.WriteToken(jsontext.String(n.Value.Value))
	case *ast.MergeKeyNode:
		return fmt.Errorf("line %d: a merge key needs a mapping", where(n).Line)
	}
	return fmt.Errorf("line %d: unsupported YAML %s", where(n).Line, n.Type())
}

// float writes f. JSON has no infinity or NaN, so they become the strings
// "Infinity", "-Infinity" and "NaN"; a schema can allow them with an enum.
func (c *converter) float(f float64, _ Pos) error {
	switch {
	case math.IsNaN(f):
		return c.enc.WriteToken(jsontext.String("NaN"))
	case math.IsInf(f, 1):
		return c.enc.WriteToken(jsontext.String("Infinity"))
	case math.IsInf(f, -1):
		return c.enc.WriteToken(jsontext.String("-Infinity"))
	}
	return c.enc.WriteToken(jsontext.Float(f))
}

// tagged handles an explicit tag. The tag is recorded in Document.Tags.
// !!str makes a scalar a string whatever it looks like, and !!binary is the
// base64 text it holds; any other tag is read as the value it tags.
func (c *converter) tagged(n *ast.TagNode, ptr string, depth int) error {
	tag := ""
	if n.Start != nil {
		tag = n.Start.Value
	}
	if _, ok := c.tags[ptr]; !ok && tag != "" {
		c.tags[ptr] = tag
	}
	switch tag {
	case "!!str", "!!binary":
		if s, ok := n.Value.(ast.ScalarNode); ok {
			text := fmt.Sprint(s.GetValue())
			if lit, ok := n.Value.(*ast.LiteralNode); ok {
				text = lit.Value.Value
			}
			if tag == "!!binary" {
				text = strings.Join(strings.Fields(text), "")
			}
			return c.enc.WriteToken(jsontext.String(text))
		}
	}
	return c.value(n.Value, ptr, depth+1)
}

func nameOf(n ast.Node) string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.GetToken().Value)
}

type member struct {
	key   string
	at    Pos
	value ast.Node
}

func (c *converter) mapping(values []*ast.MappingValueNode, ptr string, depth int) error {
	members, err := c.members(values, depth)
	if err != nil {
		return fmt.Errorf("%s: %w", pointerText(ptr), err)
	}
	if err := c.enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	for _, m := range members {
		child := string(jsonptr.Pointer(ptr).Append(m.key))
		c.keys[child] = m.at
		if err := c.enc.WriteToken(jsontext.String(m.key)); err != nil {
			return err
		}
		if err := c.value(m.value, child, depth+1); err != nil {
			return err
		}
	}
	return c.enc.WriteToken(jsontext.EndObject)
}

// members lists a mapping's keys in order: merged keys first, then the
// mapping's own, which override them.
func (c *converter) members(values []*ast.MappingValueNode, depth int) ([]member, error) {
	if depth > maxDepth {
		return nil, errors.New("merges nested too deeply")
	}
	var merged, own []member
	for _, mv := range values {
		if _, ok := mv.Key.(*ast.MergeKeyNode); ok {
			from, err := c.mergeSources(mv.Value, depth)
			if err != nil {
				return nil, err
			}
			for _, source := range from {
				inner, err := c.members(source, depth+1)
				if err != nil {
					return nil, err
				}
				at := where(mv)
				for i := range inner {
					inner[i].at = at
				}
				merged = setMembers(merged, inner...)
			}
			continue
		}
		key, err := c.keyText(mv.Key)
		if err != nil {
			return nil, err
		}
		own = setMembers(own, member{key: key, at: where(mv.Key), value: mv.Value})
	}
	return setMembers(merged, own...), nil
}

// mergeSources returns the mappings a merge key brings in.
func (c *converter) mergeSources(n ast.Node, depth int) ([][]*ast.MappingValueNode, error) {
	switch n := n.(type) {
	case *ast.AnchorNode:
		c.anchors[nameOf(n.Name)] = n.Value
		return c.mergeSources(n.Value, depth+1)
	case *ast.AliasNode:
		target, ok := c.anchors[nameOf(n.Value)]
		if !ok {
			return nil, fmt.Errorf("line %d: no anchor named %q", where(n).Line, nameOf(n.Value))
		}
		return c.mergeSources(target, depth+1)
	case *ast.MappingNode:
		return [][]*ast.MappingValueNode{n.Values}, nil
	case *ast.MappingValueNode:
		return [][]*ast.MappingValueNode{{n}}, nil
	case *ast.SequenceNode:
		var out [][]*ast.MappingValueNode
		for _, item := range n.Values {
			more, err := c.mergeSources(item, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("line %d: a merge key needs a mapping", where(n).Line)
}

// keyText is a mapping key as a JSON property name. Scalar keys are the text
// they were written as; JSON has no other kind of name.
func (c *converter) keyText(k ast.MapKeyNode) (string, error) {
	switch k := k.(type) {
	case *ast.MappingKeyNode:
		inner, ok := k.Value.(ast.MapKeyNode)
		if !ok {
			return "", fmt.Errorf("line %d: only scalar keys can become JSON property names", where(k).Line)
		}
		return c.keyText(inner)
	case *ast.StringNode:
		return k.Value, nil
	case *ast.AnchorNode:
		c.anchors[nameOf(k.Name)] = k.Value
		inner, ok := k.Value.(ast.MapKeyNode)
		if !ok {
			return "", fmt.Errorf("line %d: only scalar keys can become JSON property names", where(k).Line)
		}
		return c.keyText(inner)
	case ast.ScalarNode:
		return nameText(k), nil
	}
	return "", fmt.Errorf("line %d: only scalar keys can become JSON property names", where(k).Line)
}

func nameText(n ast.ScalarNode) string {
	if tk := n.GetToken(); tk != nil {
		return tk.Value
	}
	return fmt.Sprint(n.GetValue())
}

// setMembers adds members to list, replacing any with the same key in place.
func setMembers(list []member, add ...member) []member {
next:
	for _, a := range add {
		for i, m := range list {
			if m.key == a.key {
				list[i] = a
				continue next
			}
		}
		list = append(list, a)
	}
	return list
}

// pointerText words a JSON Pointer for an error message.
func pointerText(ptr string) string {
	if ptr == "" {
		return "the document"
	}
	return ptr
}
