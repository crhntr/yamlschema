# yamlschema

Check YAML files against a JSON Schema, with every problem reported on the
line and column of the YAML it is about.

    go run github.com/crhntr/yamlschema/cmd/yamlschema@latest -schema schema.json config.yaml
    config.yaml:3:7: type integer does not match string

Without `-schema`, a file names its own schema in the comment the YAML
language server reads, relative to the file or a URL:

    # yaml-language-server: $schema=./schema.json

The YAML is read with [goccy/go-yaml](https://github.com/goccy/go-yaml) and
walked as a syntax tree straight into JSON, then validated by
[crhntr/jsonschema](https://github.com/crhntr/jsonschema). That keeps key
order, expands anchors and aliases, applies `<<` merge keys, and knows where
each value was written. Anchor expansion is bounded, so a small file cannot
grow into an enormous one.

## Library

```go
s, err := yamlschema.ParseSchema(ctx, nil, schemaBytes) // JSON or YAML
problems, err := s.Validate(yamlBytes)                  // every document in the file
for _, p := range problems {
	fmt.Printf("%d:%d: %s\n", p.Pos.Line, p.Pos.Column, p.Message)
}
```

`Parse` gives the JSON, value positions, key positions and tags of each
document if you want to validate it yourself.

## What YAML can say that JSON cannot

| YAML | In the JSON |
|---|---|
| `.inf`, `-.inf`, `.nan` | the strings `"Infinity"`, `"-Infinity"`, `"NaN"` (allow them with `enum`) |
| `!!binary` | its base64 text, whitespace removed (`contentEncoding: base64`) |
| `!!str 123` | the string `"123"` |
| tags such as `!secret` | the tagged value; the tag is in `Document.Tags` by JSON Pointer |
| non-string scalar keys (`1: one`) | the property name `"1"` |
| anchors, aliases, `<<` merges | expanded |
| several documents (`---`) | each validated, problems carry `Document` |
| `# yaml-language-server: $schema=` | `SchemaRef` finds it |

Collection keys (`? [1, 2]`) have no JSON name and are an error.

## License

MIT, see [LICENSE](LICENSE).
