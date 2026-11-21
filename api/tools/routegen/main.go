// Command routegen writes gen/routes from the OpenAPI contract, keyed by ServeMux
// pattern: the security each operation declares, so the session and tenant middlewares
// follow the contract instead of a hand-written list of routes, and the properties its
// JSON body accepts when the schema says additionalProperties: false, so
// httpx.KnownBodyFields can refuse the others.
//
// Only the two schemes of the design are understood, and at most one requirement per
// operation: anything else (alternatives, unknown schemes, a tenant without a session)
// fails the generation rather than being guessed.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	schemeSession = "session"
	schemeTenant  = "tenant"
)

func main() {
	spec := flag.String("spec", "", "path of the OpenAPI contract")
	out := flag.String("out", "", "path of the Go file to write")
	flag.Parse()
	if err := run(*spec, *out); err != nil {
		fmt.Fprintln(os.Stderr, "routegen:", err)
		os.Exit(1)
	}
}

func run(specPath, outPath string) error {
	if specPath == "" || outPath == "" {
		return errors.New("-spec and -out are required")
	}
	b, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	routes, err := parse(b)
	if err != nil {
		return err
	}
	src, err := render(routes)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(outPath, src, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}

// requirements is an OpenAPI security list. A nil pointer means "not declared".
type requirements = *[]map[string][]string

type document struct {
	Security   requirements `yaml:"security"`
	Components struct {
		SecuritySchemes map[string]yaml.Node `yaml:"securitySchemes"`
		Schemas         map[string]schema    `yaml:"schemas"`
	} `yaml:"components"`
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

type operation struct {
	Security    requirements `yaml:"security"`
	RequestBody *struct {
		Content map[string]struct {
			Schema schema `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"requestBody"`
}

// schema is the part of a JSON schema the body check needs. AdditionalProperties is
// any because it may be a boolean or a schema.
type schema struct {
	Ref                  string         `yaml:"$ref"`
	Properties           map[string]any `yaml:"properties"`
	AdditionalProperties any            `yaml:"additionalProperties"`
}

type route struct {
	pattern string
	session bool
	tenant  bool
	// fields are the properties the JSON body accepts; nil if any is accepted (no body,
	// or a schema that allows additional properties).
	fields []string
}

func methods() []string {
	return []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
}

func parse(b []byte) ([]route, error) {
	var doc document
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	if doc.Security == nil {
		return nil, errors.New("the contract must declare a root `security`: the default of operations that declare none")
	}
	for _, s := range []string{schemeSession, schemeTenant} {
		if _, ok := doc.Components.SecuritySchemes[s]; !ok {
			return nil, fmt.Errorf("components.securitySchemes.%s is missing", s)
		}
	}

	var routes []route
	for path, item := range doc.Paths {
		for method, node := range item {
			if !slices.Contains(methods(), method) {
				continue // parameters, summary...
			}
			var op operation
			if err := node.Decode(&op); err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			sec := op.Security
			if sec == nil {
				sec = doc.Security
			}
			pattern := strings.ToUpper(method) + " " + path
			r, err := toRoute(pattern, *sec)
			if err != nil {
				return nil, err
			}
			if r.fields, err = bodyFields(pattern, op, doc.Components.Schemas); err != nil {
				return nil, err
			}
			routes = append(routes, r)
		}
	}
	slices.SortFunc(routes, func(a, b route) int { return strings.Compare(a.pattern, b.pattern) })
	return routes, nil
}

func toRoute(pattern string, reqs []map[string][]string) (route, error) {
	r := route{pattern: pattern}
	switch len(reqs) {
	case 0:
		return r, nil
	case 1:
	default:
		return route{}, fmt.Errorf("%s: alternative security requirements are not supported", pattern)
	}
	for scheme := range reqs[0] {
		switch scheme {
		case schemeSession:
			r.session = true
		case schemeTenant:
			r.tenant = true
		default:
			return route{}, fmt.Errorf("%s: unknown security scheme %q", pattern, scheme)
		}
	}
	if r.tenant && !r.session {
		return route{}, fmt.Errorf("%s: a tenant requires a session", pattern)
	}
	if !r.session {
		// `security: [{}]` is OpenAPI's "optional" marker; say `security: []` instead.
		return route{}, fmt.Errorf("%s: an empty requirement is ambiguous; declare `security: []` for public routes", pattern)
	}
	return r, nil
}

const schemaRefPrefix = "#/components/schemas/"

// bodyFields returns the sorted properties of the operation's application/json body if
// its schema forbids additional properties. A $ref is followed once, to
// components.schemas; one that leads nowhere fails the generation.
func bodyFields(pattern string, op operation, schemas map[string]schema) ([]string, error) {
	if op.RequestBody == nil {
		return nil, nil
	}
	content, ok := op.RequestBody.Content["application/json"]
	if !ok {
		return nil, nil
	}
	sc := content.Schema
	if sc.Ref != "" {
		name, found := strings.CutPrefix(sc.Ref, schemaRefPrefix)
		target, exists := schemas[name]
		if !found || !exists {
			return nil, fmt.Errorf("%s: request body $ref %q is not a schema of components.schemas", pattern, sc.Ref)
		}
		sc = target
	}
	if forbid, ok := sc.AdditionalProperties.(bool); !ok || forbid {
		return nil, nil
	}
	fields := make([]string, 0, len(sc.Properties))
	for name := range sc.Properties {
		fields = append(fields, name)
	}
	slices.Sort(fields)
	return fields, nil
}

func render(routes []route) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`// Code generated by routegen from api-spec/openapi.yaml. DO NOT EDIT.

// Package routes holds, keyed by ServeMux pattern, the security that each operation of
// the OpenAPI contract declares and the properties its JSON body accepts.
package routes

import "chaskicode.dev/saas/platform/kit/httpx"

// Security returns the security of every operation of the contract.
func Security() map[string]httpx.Security {
	return map[string]httpx.Security{
`)
	for _, r := range routes {
		fmt.Fprintf(&buf, "\t\t%q: {Session: %t, Tenant: %t},\n", r.pattern, r.session, r.tenant)
	}
	buf.WriteString(`	}
}

// BodyFields returns the properties the JSON body of each operation accepts, for the
// operations whose body schema forbids additional properties.
func BodyFields() map[string][]string {
	return map[string][]string{
`)
	for _, r := range routes {
		if r.fields != nil {
			fmt.Fprintf(&buf, "\t\t%q: {", r.pattern)
			for i, f := range r.fields {
				if i > 0 {
					buf.WriteString(", ")
				}
				fmt.Fprintf(&buf, "%q", f)
			}
			buf.WriteString("},\n")
		}
	}
	buf.WriteString("\t}\n}\n")
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated code: %w", err)
	}
	return src, nil
}
