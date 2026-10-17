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
	// A schema named on the command line is trusted. One named by a comment
	// in the file is not: it may be a local path inside the file's directory,
	// or a URL when -remote is set.
	ref, trusted := schemaRef, true
	if ref == "" {
		ref, trusted = yamlschema.SchemaRef(src), false
		if ref == "" {
			return false, fmt.Errorf("no schema: pass -schema or add a yaml-language-server comment")
		}
	}
	u, err := url.Parse(ref)
	if err != nil {
		return false, fmt.Errorf("schema %q: %w", ref, err)
	}
	// A one-letter scheme is a Windows drive, which is a path.
	isURL := len(u.Scheme) > 1
	if isURL && u.Scheme != "http" && u.Scheme != "https" {
		return false, fmt.Errorf("schema %q: only http and https URLs are supported", ref)
	}
	if isURL && !trusted && !remote {
		return false, fmt.Errorf("the file names the schema %s; pass -remote to fetch it", ref)
	}
	if !isURL && !trusted && !filepath.IsLocal(ref) {
		return false, fmt.Errorf("the schema path %q leaves the file's directory; pass -schema to use it", ref)
	}
	key := ref
	if !isURL && !trusted {
		key = filepath.Join(filepath.Dir(name), ref)
	}
	s, ok := schemas[key]
	if !ok {
		var body []byte
		switch {
		case isURL:
			body, err = fetch(ctx, ref)
		case trusted:
			body, err = readLimited(os.Open(ref))
		default:
			body, err = readInDir(filepath.Dir(name), ref)
		}
		if err != nil {
			return false, fmt.Errorf("schema %s: %w", ref, err)
		}
		s, err = yamlschema.ParseSchema(ctx, nil, body)
		if err != nil {
			return false, fmt.Errorf("schema %s: %w", ref, err)
		}
		schemas[key] = s
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

// maxSchema bounds the size of a schema read from anywhere.
const maxSchema = 10 << 20

// readInDir reads a file that cannot be reached outside dir, even through
// symbolic links.
func readInDir(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer closeAndIgnoreError(root)
	return readLimited(root.Open(name))
}

func readLimited(f *os.File, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	defer closeAndIgnoreError(f)
	return io.ReadAll(io.LimitReader(f, maxSchema))
}

func fetch(ctx context.Context, ref string) ([]byte, error) {
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
	return io.ReadAll(io.LimitReader(res.Body, maxSchema))
}

func closeAndIgnoreError(c io.Closer) { _ = c.Close() }
