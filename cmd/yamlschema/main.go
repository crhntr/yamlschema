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
	os.Exit(run(context.Background(), ".", os.Args[1:], os.Stdout, os.Stderr))
}

// run is main with its inputs passed in. It returns the exit status:
// 0 when every file is valid, 1 when a file has problems or cannot be
// checked, 2 for bad usage.
func run(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("yamlschema", flag.ContinueOnError)
	flags.SetOutput(stderr)
	chdir := flags.String("C", "", "change to `dir` first")
	schemaFlag := flags.String("schema", "", "schema `file or URL` (default: the file's yaml-language-server comment)")
	remote := flags.Bool("remote", false, "let a file's schema comment name a URL")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: yamlschema [-C dir] [-schema file-or-url] [-remote] file.yaml ...")
		return 2
	}
	if *chdir != "" {
		dir = filepath.Join(dir, *chdir)
	}
	failed := false
	schemas := map[string]*yamlschema.Schema{}
	for _, name := range flags.Args() {
		ok, err := check(ctx, stdout, schemas, dir, *schemaFlag, *remote, name)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
		}
		failed = failed || err != nil || !ok
	}
	if failed {
		return 1
	}
	return 0
}

func check(ctx context.Context, w io.Writer, schemas map[string]*yamlschema.Schema, dir, schemaRef string, remote bool, name string) (bool, error) {
	shown := name
	name = filepath.Join(dir, name)
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
			body, err = readLimited(os.Open(filepath.Join(dir, ref)))
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
		fmt.Fprintf(w, "%s:%d:%d: %s\n", shown, p.Pos.Line, p.Pos.Column, p.Message)
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
