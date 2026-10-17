package yamlschema_test

import (
	"context"
	"testing"

	"github.com/crhntr/yamlschema"
)

func TestParse(t *testing.T) {
	for _, tt := range []struct {
		name, yaml, want string
	}{
		{"scalars", "a: 1\nb: true\nc: ~\nd: 1.5\ne: text\n", `{"a":1,"b":true,"c":null,"d":1.5,"e":"text"}`},
		{"order kept", "z: 1\na: 2\n", `{"z":1,"a":2}`},
		{"sequence", "- 1\n- [2, 3]\n", `[1,[2,3]]`},
		{"alias", "base: &b {x: 1}\nuse: *b\n", `{"base":{"x":1},"use":{"x":1}}`},
		{"merge overridden", "base: &b {x: 1, y: 2}\nuse:\n  <<: *b\n  y: 3\n", `{"base":{"x":1,"y":2},"use":{"x":1,"y":3}}`},
		{"merge list", "a: &a {x: 1}\nb: &b {y: 2}\nc:\n  <<: [*a, *b]\n", `{"a":{"x":1},"b":{"y":2},"c":{"x":1,"y":2}}`},
		{"number key", "1: one\n", `{"1":"one"}`},
		{"infinity", "a: .inf\nb: -.inf\nc: .nan\n", `{"a":"Infinity","b":"-Infinity","c":"NaN"}`},
		{"tag str", "a: !!str 123\n", `{"a":"123"}`},
		{"empty", "", `null`},
		{"literal", "a: |\n  one\n  two\n", `{"a":"one\ntwo\n"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			docs, err := yamlschema.Parse([]byte(tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if len(docs) != 1 {
				t.Fatalf("Parse(%q) = %d documents, want 1", tt.yaml, len(docs))
			}
			if got := string(docs[0].JSON); got != tt.want {
				t.Errorf("Parse(%q) = %s, want %s", tt.yaml, got, tt.want)
			}
		})
	}
}

func TestParseTags(t *testing.T) {
	docs, err := yamlschema.Parse([]byte("secret: !secret hunter2\nblob: !!binary |\n  aGVs\n  bG8=\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(docs[0].JSON), `{"secret":"hunter2","blob":"aGVsbG8="}`; got != want {
		t.Errorf("JSON = %s, want %s", got, want)
	}
	if got := docs[0].Tags["/secret"]; got != "!secret" {
		t.Errorf("Tags[/secret] = %q, want !secret", got)
	}
	if got := docs[0].Tags["/blob"]; got != "!!binary" {
		t.Errorf("Tags[/blob] = %q, want !!binary", got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tt := range []struct{ name, yaml string }{
		{"missing anchor", "a: *nope\n"},
		{"collection key", "? [1, 2]\n: x\n"},
		{"billion laughs", "a: &a [x,x,x,x,x,x,x,x,x,x]\nb: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a,*a]\nc: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b,*b]\nd: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c,*c]\ne: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d,*d]\nf: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e,*e]\ng: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f,*f]\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := yamlschema.Parse([]byte(tt.yaml)); err == nil {
				t.Errorf("Parse(%q) succeeded, want an error", tt.yaml)
			}
		})
	}
}

const schema = `{
  "type": "object",
  "required": ["name", "port"],
  "properties": {
    "name": {"type": "string", "minLength": 3},
    "port": {"type": "integer", "maximum": 65535},
    "tags": {"type": "array", "items": {"type": "string"}}
  },
  "additionalProperties": false
}`

func TestValidate(t *testing.T) {
	s, err := yamlschema.ParseSchema(context.Background(), nil, []byte(schema))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, yaml string
		want       []yamlschema.Pos // positions of the problems
	}{
		{"valid", "name: web\nport: 80\n", nil},
		{"wrong type on its line", "name: web\nport: eighty\n", []yamlschema.Pos{{2, 7}}},
		{"in a sequence", "name: web\nport: 80\ntags:\n  - a\n  - 2\n", []yamlschema.Pos{{5, 5}}},
		{"extra key on its key", "name: web\nport: 80\nhost: x\n", []yamlschema.Pos{{3, 1}}},
		{"second document", "name: web\nport: 80\n---\nname: ab\nport: 80\n", []yamlschema.Pos{{4, 7}}},
		{"missing required", "name: web\n", []yamlschema.Pos{{1, 1}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems, err := s.Validate([]byte(tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			var got []yamlschema.Pos
			for _, p := range problems {
				got = append(got, p.Pos)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Validate(%q) problems = %v, want positions %v", tt.yaml, problems, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("problem %d at %v, want %v: %v", i, got[i], tt.want[i], problems[i])
				}
			}
		})
	}
}

func TestSchemaRef(t *testing.T) {
	for _, tt := range []struct{ src, want string }{
		{"# yaml-language-server: $schema=https://x.test/s.json\na: 1\n", "https://x.test/s.json"},
		{"---\n# a note\n# yaml-language-server: $schema=./s.json extra\na: 1\n", "./s.json"},
		{"a: 1\n# yaml-language-server: $schema=late.json\n", ""},
		{"a: 1\n", ""},
		{"# yaml-language-server: $schema=\n", ""},
	} {
		if got := yamlschema.SchemaRef([]byte(tt.src)); got != tt.want {
			t.Errorf("SchemaRef(%q) = %q, want %q", tt.src, got, tt.want)
		}
	}
}
