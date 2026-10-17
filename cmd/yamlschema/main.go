// Command yamlschema checks YAML files against a JSON Schema.
//
//	yamlschema [-C dir] [-schema file-or-url] file.yaml ...
//
// Without -schema, each file names its schema in a leading comment:
//
//	# yaml-language-server: $schema=./schema.json
//
// Problems print as file:line:column: message, and the exit status is 1
// when there are any.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/crhntr/yamlschema"
)

func main() {
	dir := flag.String("C", "", "change to `dir` first")
	schemaFlag := flag.String("schema", "", "schema `file or URL` (default: the file's yaml-language-server comment)")
	remote := flag.Bool("remote", false, "let a file's schema comment name a URL")
	flag.Parse()
	if *dir != "" {
		if err := os.Chdir(*dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	failed := false
	schemas := map[string]*yamlschema.Schema{}
	for _, name := range flag.Args() {
		ok, err := check(context.Background(), os.Stdout, schemas, *schemaFlag, *remote, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			failed = true
		}
		failed = failed || !ok
	}
	if failed {
		os.Exit(1)
	}
}

func check(ctx context.Context, w io.Writer, schemas map[string]*yamlschema.Schema, schemaRef string, remote bool, name string) (bool, error) {
	src, err := os.ReadFile(name)
	if err != nil {
		return false, err
	}
	ref := schemaRef
	if ref == "" {
		ref = yamlschema.SchemaRef(src)
		if ref == "" {
			return false, fmt.Errorf("no schema: pass -schema or add a yaml-language-server comment")
		}
		if strings.Contains(ref, "://") {
			if !remote {
				return false, fmt.Errorf("the file names the schema %s; pass -remote to fetch it", ref)
			}
		} else {
			// A comment in the file is not trusted to reach outside its directory.
			if !filepath.IsLocal(ref) {
				return false, fmt.Errorf("the schema path %q leaves the file's directory; pass -schema to use it", ref)
			}
			ref = filepath.Join(filepath.Dir(name), ref)
		}
	}
	s, ok := schemas[ref]
	if !ok {
		body, err := fetch(ctx, ref)
		if err != nil {
			return false, fmt.Errorf("schema %s: %w", ref, err)
		}
		s, err = yamlschema.ParseSchema(ctx, nil, body)
		if err != nil {
			return false, fmt.Errorf("schema %s: %w", ref, err)
		}
		schemas[ref] = s
	}
	problems, err := s.Validate(src)
	if err != nil {
		return false, err
	}
	for _, p := range problems {
		fmt.Fprintf(w, "%s:%d:%d: %s\n", name, p.Pos.Line, p.Pos.Column, p.Message)
	}
	return len(problems) == 0, nil
}

func fetch(ctx context.Context, ref string) ([]byte, error) {
	if u, err := url.Parse(ref); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return os.ReadFile(ref)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer closeAndIgnoreError(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching: %s", res.Status)
	}
	const maxSchema = 10 << 20
	return io.ReadAll(io.LimitReader(res.Body, maxSchema))
}

func closeAndIgnoreError(c io.Closer) { _ = c.Close() }
