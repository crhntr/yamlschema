package yamlschema

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/crhntr/jsonschema"
)

// Schema is a JSON Schema ready to check YAML documents with.
type Schema struct {
	schema *jsonschema.Schema
}

// ParseSchema reads a JSON Schema written as JSON or as YAML. References
// to other documents are fetched with client. A nil client fetches
// nothing, so a schema from an untrusted source cannot make requests; pass
// a client to allow it.
func ParseSchema(ctx context.Context, client *http.Client, src []byte) (*Schema, error) {
	body := src
	if docs, err := Parse(src); err == nil && len(docs) == 1 {
		body = docs[0].JSON
	}
	if client == nil {
		client = &http.Client{Transport: noNetwork{}}
	}
	r := jsonschema.NewResolver(client)
	const uri = "urn:yamlschema:schema"
	if _, err := r.Load(uri, body); err != nil {
		return nil, fmt.Errorf("loading schema: %w", err)
	}
	s, err := r.Resolve(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("resolving schema: %w", err)
	}
	return &Schema{schema: s}, nil
}

// Problem is one thing wrong with a YAML document.
type Problem struct {
	// Document is the 1-based index of the document in the file.
	Document int
	// Pointer is the JSON Pointer of the value at fault.
	Pointer string
	// Pos is where that value is written.
	Pos Pos
	// Message says what is wrong.
	Message string
}

func (p Problem) String() string {
	return fmt.Sprintf("%d:%d: %s", p.Pos.Line, p.Pos.Column, p.Message)
}

// Validate checks every document in src and returns the problems found,
// each on the line of YAML it is about. Syntax that cannot become JSON
// is returned as an error, not as a problem.
func (s *Schema) Validate(src []byte) ([]Problem, error) {
	docs, err := Parse(src)
	if err != nil {
		return nil, err
	}
	var problems []Problem
	for i, d := range docs {
		problems = append(problems, s.check(i+1, d)...)
	}
	return problems, nil
}

func (s *Schema) check(index int, d Document) []Problem {
	out := s.schema.Validate("", d.JSON)
	if out.Valid {
		return nil
	}
	var problems []Problem
	seen := map[string]bool{}
	var walk func(o jsonschema.Output)
	walk = func(o jsonschema.Output) {
		if len(o.Errors) > 0 {
			for _, e := range o.Errors {
				walk(e)
			}
			return
		}
		if o.Valid || o.Error == "" {
			return
		}
		key := o.InstanceLocation + "\x00" + o.Error
		if seen[key] {
			return
		}
		seen[key] = true
		at := d.Positions.At(o.InstanceLocation)
		if blamesKey(o.KeywordLocation) {
			at = d.Keys.At(o.InstanceLocation)
		}
		problems = append(problems, Problem{
			Document: index,
			Pointer:  o.InstanceLocation,
			Pos:      at,
			Message:  o.Error,
		})
	}
	walk(out)
	return problems
}

// SchemaRef finds the schema a file names for itself in a leading comment
// like the one the YAML language server reads:
//
//	# yaml-language-server: $schema=https://example.com/schema.json
//
// It returns "" when there is none.
func SchemaRef(src []byte) string {
	const marker = "yaml-language-server:"
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line == "---":
			continue
		case !strings.HasPrefix(line, "#"):
			return ""
		}
		_, rest, ok := strings.Cut(line, marker)
		if !ok {
			continue
		}
		if _, ref, ok := strings.Cut(rest, "$schema="); ok {
			if f := strings.Fields(ref); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}

// blamesKey reports whether a keyword's complaint is about a mapping key
// rather than the value under it.
func blamesKey(keywordLocation string) bool {
	for _, k := range []string{"additionalProperties", "unevaluatedProperties", "propertyNames"} {
		if strings.HasSuffix(keywordLocation, "/"+k) || strings.Contains(keywordLocation, "/"+k+"/") {
			return true
		}
	}
	return false
}

// noNetwork refuses every request.
type noNetwork struct{}

func (noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("fetching %s: network access is off", r.URL)
}
