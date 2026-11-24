// Command specmerge joins the platform contract (api-spec/core.yaml) and the app
// contract (api-spec/app.yaml) into api-spec/openapi.gen.yaml, the only file the
// generators read (oapi-codegen, routegen, orval, gen-route-security.ts).
//
// The platform document is the base: its openapi, info, servers and root security are
// the merged ones, and the app's tags, paths and components are appended after the
// platform's, in the app's order. It works on yaml.Node rather than maps so that the
// output keeps the order of its input: the web generators walk paths in document
// order, and an empty app.yaml must reproduce the platform contract as it is.
//
// The app references platform components as `core.yaml#/components/...`, which
// keeps app.yaml readable on its own; the merge rewrites them to local references.
//
// Each tag is the key of one module: the module whose codegen.yaml (core/<m>/ or
// app/modules/<m>/) includes it, which is how generate finds the modules without a
// hand-written list. The merge refuses anything that would break that key or let the
// app override the platform (see the [contract-*] rules in docs/core/runbooks/ci.md).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	ruleOwned     = "contract-platform-owned"
	ruleDuplicate = "contract-duplicate"
	ruleModuleKey = "contract-module-key"
)

func main() {
	platform := flag.String("platform", "", "path of the platform contract")
	app := flag.String("app", "", "path of the app contract")
	root := flag.String("root", ".", "the api/ directory, where core/*/codegen.yaml and app/modules/*/codegen.yaml live")
	out := flag.String("out", "", "path of the merged contract to write")
	flag.Parse()
	if err := run(*platform, *app, *root, *out); err != nil {
		fmt.Fprintln(os.Stderr, "specmerge:", err)
		os.Exit(1)
	}
}

func run(platformPath, appPath, root, outPath string) error {
	if platformPath == "" || appPath == "" || outPath == "" {
		return errors.New("-platform, -app and -out are required")
	}
	pb, err := os.ReadFile(filepath.Clean(platformPath))
	if err != nil {
		return fmt.Errorf("read platform contract: %w", err)
	}
	ab, err := os.ReadFile(filepath.Clean(appPath))
	if err != nil {
		return fmt.Errorf("read app contract: %w", err)
	}
	mods, err := discoverModules(root)
	if err != nil {
		return err
	}
	src, err := merge(
		source{name: filepath.Base(platformPath), data: pb},
		source{name: filepath.Base(appPath), data: ab},
		mods,
	)
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

// zone says which contract a module, a tag or an operation belongs to.
type zone int

const (
	zonePlatform zone = iota
	zoneApp
)

// module is a folder with a codegen.yaml: the owner of the tag named like the folder.
type module struct {
	name   string
	zone   zone
	config string // path of its codegen.yaml relative to root, with forward slashes
}

type codegenConfig struct {
	Package       string `yaml:"package"`
	Output        string `yaml:"output"`
	OutputOptions struct {
		IncludeTags []string `yaml:"include-tags"`
	} `yaml:"output-options"`
}

// discoverModules reads core/*/codegen.yaml and app/modules/*/codegen.yaml under
// root, in alphabetical order, as generate does, and checks that each one generates
// exactly its folder's tag into gen/openapi/<m>/ as package <m>api.
func discoverModules(root string) ([]module, error) {
	var mods []module
	var errs []error
	seen := map[string]string{}
	for _, z := range []struct {
		zone    zone
		pattern string
	}{
		{zonePlatform, "core/*/codegen.yaml"},
		{zoneApp, "app/modules/*/codegen.yaml"},
	} {
		paths, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(z.pattern)))
		if err != nil {
			return nil, fmt.Errorf("find %s: %w", z.pattern, err)
		}
		slices.Sort(paths)
		for _, p := range paths {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil, fmt.Errorf("relative path of %s: %w", p, err)
			}
			m := module{name: filepath.Base(filepath.Dir(p)), zone: z.zone, config: filepath.ToSlash(rel)}
			if err := checkCodegen(p, m); err != nil {
				errs = append(errs, err)
				continue
			}
			if other, dup := seen[m.name]; dup {
				errs = append(errs, ruleErr(ruleModuleKey, "%s and %s: two modules are named %q, and a module name is the key of its tag; rename one", other, m.config, m.name))
				continue
			}
			seen[m.name] = m.config
			mods = append(mods, m)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return mods, nil
}

func checkCodegen(path string, m module) error {
	if !validModuleName(m.name) {
		return ruleErr(ruleModuleKey, "%s: the module folder %q must match [a-z][a-z0-9]*, since it names the tag and the package %sapi", m.config, m.name, m.name)
	}
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read %s: %w", m.config, err)
	}
	var cfg codegenConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", m.config, err)
	}
	var errs []error
	if !slices.Equal(cfg.OutputOptions.IncludeTags, []string{m.name}) {
		errs = append(errs, ruleErr(ruleModuleKey, "%s: output-options.include-tags is %v, want [%s]: a module generates exactly the operations tagged with its folder name", m.config, cfg.OutputOptions.IncludeTags, m.name))
	}
	if want := m.name + "api"; cfg.Package != want {
		errs = append(errs, ruleErr(ruleModuleKey, "%s: package is %q, want %q", m.config, cfg.Package, want))
	}
	if want := "gen/openapi/" + m.name + "/" + m.name + ".gen.go"; cfg.Output != want {
		errs = append(errs, ruleErr(ruleModuleKey, "%s: output is %q, want %q", m.config, cfg.Output, want))
	}
	return errors.Join(errs...)
}

func validModuleName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

type source struct {
	name string // base name, as messages and external references spell it
	data []byte
}

func ruleErr(rule, format string, args ...any) error {
	return fmt.Errorf("[%s] "+format, append([]any{rule}, args...)...)
}

func methods() []string {
	return []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
}

// appRootKeys are the root members app.yaml may have; the rest (security, servers,
// webhooks...) belong to the platform.
func appRootKeys() []string {
	return []string{"openapi", "info", "tags", "paths", "components"}
}

// merge returns the merged contract, or every rule the inputs break.
func merge(platform, app source, mods []module) ([]byte, error) {
	pdoc, p, err := parseDoc(platform)
	if err != nil {
		return nil, err
	}
	_, a, err := parseDoc(app)
	if err != nil {
		return nil, err
	}
	m := &merger{platform: platform.name, app: app.name, p: p, a: a, modules: map[string]module{}, used: map[string]bool{}}
	for _, mod := range mods {
		m.modules[mod.name] = mod
	}
	m.check(mods)
	if err := errors.Join(m.errs...); err != nil {
		return nil, err
	}
	m.join()

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Code generated by specmerge from api-spec/%s and api-spec/%s. DO NOT EDIT.\n", platform.name, app.name)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(pdoc); err != nil {
		return nil, fmt.Errorf("encode merged contract: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode merged contract: %w", err)
	}
	return buf.Bytes(), nil
}

func parseDoc(s source) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(s.data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", s.name, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("%s: the contract must be a YAML mapping", s.name)
	}
	return &doc, doc.Content[0], nil
}

type merger struct {
	platform, app string // file names, for messages
	p, a          *yaml.Node
	modules       map[string]module
	used          map[string]bool // modules with at least one operation
	errs          []error
}

func (m *merger) fail(rule, format string, args ...any) {
	m.errs = append(m.errs, ruleErr(rule, format, args...))
}

func (m *merger) check(mods []module) {
	m.checkRoot()
	m.checkComponents()
	m.checkRefs()
	ptags := m.checkTags()
	m.checkPaths()
	m.checkOperations(ptags)
	for _, mod := range mods {
		if !m.used[mod.name] {
			m.fail(ruleModuleKey, "%s: no operation has tag %q; tag the module's operations with it, or delete codegen.yaml if the module has no endpoints", mod.config, mod.name)
		}
	}
}

func (m *merger) checkRoot() {
	for _, kv := range pairs(m.a) {
		if !slices.Contains(appRootKeys(), kv.key.Value) {
			m.fail(ruleOwned, "%s:%d: root member %q is not allowed: only %s; the rest (root security, servers...) is defined in %s", m.app, kv.key.Line, kv.key.Value, strings.Join(appRootKeys(), ", "), m.platform)
		}
	}
	pv, av := scalar(get(m.p, "openapi")), scalar(get(m.a, "openapi"))
	if !strings.HasPrefix(pv, "3.1") {
		m.fail(ruleOwned, "%s: openapi is %q, want 3.1.x", m.platform, pv)
	}
	if av != pv {
		m.fail(ruleOwned, "%s: openapi is %q, want %q, as in %s", m.app, av, pv, m.platform)
	}
}

func (m *merger) tenantHeader() string {
	return scalar(get(get(get(get(m.p, "components"), "securitySchemes"), "tenant"), "name"))
}

func (m *merger) checkComponents() {
	tenant := m.tenantHeader()
	if tenant == "" {
		m.fail(ruleOwned, "%s: components.securitySchemes.tenant.name is missing", m.platform)
	}
	pc, ac := get(m.p, "components"), get(m.a, "components")
	for _, sec := range pairs(ac) {
		if sec.key.Value == "securitySchemes" {
			m.fail(ruleOwned, "%s:%d: components.securitySchemes is defined in %s; refer to its schemes by name in `security` (session, tenant)", m.app, sec.key.Line, m.platform)
			continue
		}
		taken := map[string]string{} // nameKey -> where the name is defined
		for _, c := range pairs(get(pc, sec.key.Value)) {
			taken[nameKey(c.key.Value)] = m.platform
		}
		for _, c := range pairs(sec.value) {
			k := nameKey(c.key.Value)
			other, dup := taken[k]
			switch {
			case dup && other == m.platform && get(get(pc, sec.key.Value), c.key.Value) != nil:
				m.fail(ruleDuplicate, "%s:%d: components.%s.%s already exists in %s; rename it, or reference the platform one as %s#/components/%s/%s", m.app, c.key.Line, sec.key.Value, c.key.Value, m.platform, m.platform, sec.key.Value, c.key.Value)
			case dup:
				m.fail(ruleDuplicate, "%s:%d: components.%s.%s collides with a name of the same section in %s: generated type names ignore case, `_` and `-`; rename it", m.app, c.key.Line, sec.key.Value, c.key.Value, other)
			default:
				taken[k] = m.app
			}
		}
	}
	if tenant == "" {
		return
	}
	for _, h := range pairs(get(ac, "headers")) {
		if strings.EqualFold(h.key.Value, tenant) {
			m.fail(ruleOwned, "%s:%d: components.headers.%s redefines the tenant header; it is the `tenant` security scheme of %s: declare `security` on the operation instead", m.app, h.key.Line, h.key.Value, m.platform)
		}
	}
	walk(m.a, func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode && scalar(get(n, "in")) == "header" && strings.EqualFold(scalar(get(n, "name")), tenant) {
			m.fail(ruleOwned, "%s:%d: a parameter redefines the %s header; it is the `tenant` security scheme of %s: the default security (or `security: [{session: [], tenant: []}]`) already requires it", m.app, n.Line, tenant, m.platform)
		}
	})
}

// checkRefs checks that the platform only references itself and that every app
// reference resolves, to its own components or to `<platform>#/...`, which it rewrites
// to a local reference (the merged document holds both).
func (m *merger) checkRefs() {
	walk(m.p, func(n *yaml.Node) {
		ref := get(n, "$ref")
		if n.Kind != yaml.MappingNode || ref == nil {
			return
		}
		if !strings.HasPrefix(ref.Value, "#/") {
			m.fail(ruleOwned, "%s:%d: $ref %q: the platform contract references only itself", m.platform, ref.Line, ref.Value)
		} else if resolve(m.p, ref.Value) == nil {
			m.fail(ruleOwned, "%s:%d: $ref %q leads nowhere", m.platform, ref.Line, ref.Value)
		}
	})
	// Only components: a reference to anything else of the platform (a path item, say)
	// would copy a platform route into the app, out of reach of the checks below.
	external := m.platform + "#/components/"
	walk(m.a, func(n *yaml.Node) {
		ref := get(n, "$ref")
		if n.Kind != yaml.MappingNode || ref == nil {
			return
		}
		switch {
		case strings.HasPrefix(ref.Value, external):
			local := strings.TrimPrefix(ref.Value, m.platform)
			if resolve(m.p, local) == nil {
				m.fail(ruleOwned, "%s:%d: $ref %q: %s has no %s", m.app, ref.Line, ref.Value, m.platform, local)
				return
			}
			ref.Value = local
		case strings.HasPrefix(ref.Value, "#/components/"):
			if resolve(m.a, ref.Value) == nil {
				m.fail(ruleOwned, "%s:%d: $ref %q leads nowhere in %s; a platform component is referenced as %s%s", m.app, ref.Line, ref.Value, m.app, m.platform, ref.Value)
			}
		default:
			m.fail(ruleOwned, "%s:%d: $ref %q: the app contract references only components, its own (#/components/...) or the platform's (%s...)", m.app, ref.Line, ref.Value, external)
		}
	})
}

// checkTags checks the declared tags and returns the platform's. Every declared tag
// must be the key of a module of its own zone.
func (m *merger) checkTags() map[string]bool {
	ptags := map[string]bool{}
	for _, t := range items(get(m.p, "tags")) {
		name := scalar(get(t, "name"))
		if ptags[name] {
			m.fail(ruleDuplicate, "%s:%d: tag %q is declared twice", m.platform, t.Line, name)
		}
		ptags[name] = true
		m.checkTagModule(fmt.Sprintf("%s:%d", m.platform, t.Line), name, zonePlatform)
	}
	atags := map[string]bool{}
	for _, t := range items(get(m.a, "tags")) {
		name := scalar(get(t, "name"))
		switch {
		case ptags[name]:
			m.fail(ruleDuplicate, "%s:%d: tag %q belongs to the platform (%s); an app module declares its own tag, named like its folder in app/modules/", m.app, t.Line, name, m.platform)
		case atags[name]:
			m.fail(ruleDuplicate, "%s:%d: tag %q is declared twice", m.app, t.Line, name)
		default:
			m.checkTagModule(fmt.Sprintf("%s:%d", m.app, t.Line), name, zoneApp)
		}
		atags[name] = true
	}
	return ptags
}

func (m *merger) checkTagModule(where, tag string, z zone) {
	mod, ok := m.modules[tag]
	want := "app/modules/" + tag + "/codegen.yaml"
	if z == zonePlatform {
		want = "core/" + tag + "/codegen.yaml"
	}
	if !ok || mod.zone != z {
		m.fail(ruleModuleKey, "%s: tag %q has no module: there is no api/%s; every tag is the key of exactly one module, so create it or use the tag of an existing one", where, tag, want)
	}
}

// nameKey is how generators see a name: oapi-codegen and orval change its case and
// drop separators, so `getMe`, `GetMe` and `get_me` end as the same identifier.
func nameKey(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// pathShape is the template with its parameter names erased: ServeMux treats
// /a/{id} and /a/{x} as the same route.
func pathShape(p string) string {
	var b strings.Builder
	depth := 0
	for _, c := range p {
		switch {
		case c == '{':
			depth++
			if depth == 1 {
				b.WriteString("{}")
			}
		case c == '}' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (m *merger) checkPaths() {
	shapes := map[string]string{}
	for _, kv := range pairs(get(m.p, "paths")) {
		shapes[pathShape(kv.key.Value)] = m.platform + " " + kv.key.Value
	}
	for _, kv := range pairs(get(m.a, "paths")) {
		shape := pathShape(kv.key.Value)
		if other, dup := shapes[shape]; dup {
			m.fail(ruleDuplicate, "%s:%d: path %s repeats %s; a route is defined once, in the contract of the module that owns it", m.app, kv.key.Line, kv.key.Value, other)
			continue
		}
		shapes[shape] = m.app + " " + kv.key.Value
	}
}

func (m *merger) checkOperations(ptags map[string]bool) {
	ids := map[string]string{}
	for _, f := range []struct {
		file string
		root *yaml.Node
		zone zone
	}{{m.platform, m.p, zonePlatform}, {m.app, m.a, zoneApp}} {
		declared := map[string]bool{}
		for _, t := range items(get(f.root, "tags")) {
			declared[scalar(get(t, "name"))] = true
		}
		for _, item := range pairs(get(f.root, "paths")) {
			if ref := get(item.value, "$ref"); ref != nil {
				m.fail(ruleOwned, "%s:%d: path %s is a $ref (%s); declare its operations inline, so that their tag and operationId are checked and no route is borrowed from another module", f.file, ref.Line, item.key.Value, ref.Value)
			}
			for _, op := range pairs(item.value) {
				if !slices.Contains(methods(), op.key.Value) {
					continue // parameters, summary...
				}
				where := fmt.Sprintf("%s:%d: %s %s", f.file, op.key.Line, strings.ToUpper(op.key.Value), item.key.Value)
				if id := scalar(get(op.value, "operationId")); id != "" {
					if other, dup := ids[nameKey(id)]; dup {
						m.fail(ruleDuplicate, "%s: operationId %q is already used by %s; operationIds are unique across platform and app, ignoring case, `_` and `-` (generated names would collide)", where, id, other)
					} else {
						ids[nameKey(id)] = where
					}
				}
				m.checkOperationTag(where, op.value, f.zone, declared, ptags)
			}
		}
	}
}

func (m *merger) checkOperationTag(where string, op *yaml.Node, z zone, declared, ptags map[string]bool) {
	tags := items(get(op, "tags"))
	if len(tags) != 1 {
		names := make([]string, 0, len(tags))
		for _, t := range tags {
			names = append(names, t.Value)
		}
		m.fail(ruleModuleKey, "%s: tags is [%s]; an operation has exactly one tag, the name of its module", where, strings.Join(names, ", "))
		return
	}
	tag := tags[0].Value
	switch {
	case z == zoneApp && ptags[tag]:
		m.fail(ruleDuplicate, "%s: tag %q belongs to the platform; tag the operation with its app module (app/modules/<m>)", where, tag)
	case !declared[tag]:
		m.fail(ruleModuleKey, "%s: tag %q is not declared in the root `tags` of its contract", where, tag)
		m.checkTagModule(where, tag, z)
	default:
		if mod, ok := m.modules[tag]; ok && mod.zone == z {
			m.used[tag] = true
		} // a module-less declared tag is already reported by checkTags
	}
}

// join appends the app's tags, paths and components to the platform document.
func (m *merger) join() {
	if at := get(m.a, "tags"); at != nil && len(at.Content) > 0 {
		pt := ensure(m.p, "tags", yaml.SequenceNode)
		pt.Content = append(pt.Content, at.Content...)
	}
	if ap := get(m.a, "paths"); ap != nil && len(ap.Content) > 0 {
		pp := ensure(m.p, "paths", yaml.MappingNode)
		pp.Content = append(pp.Content, ap.Content...)
	}
	for _, sec := range pairs(get(m.a, "components")) {
		if len(sec.value.Content) == 0 {
			continue
		}
		ps := ensure(ensure(m.p, "components", yaml.MappingNode), sec.key.Value, yaml.MappingNode)
		ps.Content = append(ps.Content, sec.value.Content...)
	}
}

type pair struct{ key, value *yaml.Node }

// pairs returns the members of a mapping node; nothing for any other node or nil.
func pairs(n *yaml.Node) []pair {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]pair, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, pair{n.Content[i], n.Content[i+1]})
	}
	return out
}

// items returns the elements of a sequence node; nothing for any other node or nil.
func items(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

func get(n *yaml.Node, key string) *yaml.Node {
	for _, kv := range pairs(n) {
		if kv.key.Value == key {
			return kv.value
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// ensure returns the member key of mapping n, appending an empty one of kind if absent.
func ensure(n *yaml.Node, key string, kind yaml.Kind) *yaml.Node {
	if v := get(n, key); v != nil {
		return v
	}
	tag := "!!map"
	if kind == yaml.SequenceNode {
		tag = "!!seq"
	}
	v := &yaml.Node{Kind: kind, Tag: tag}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return v
}

func walk(n *yaml.Node, visit func(*yaml.Node)) {
	if n == nil {
		return
	}
	visit(n)
	for _, c := range n.Content {
		walk(c, visit)
	}
}

// resolve follows a local JSON pointer (#/a/b) from root; nil if it leads nowhere.
func resolve(root *yaml.Node, ref string) *yaml.Node {
	ptr, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil
	}
	n := root
	for tok := range strings.SplitSeq(ptr, "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		if n.Kind == yaml.SequenceNode {
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(n.Content) {
				return nil
			}
			n = n.Content[i]
		} else {
			n = get(n, tok) // nil unless n is a mapping
		}
		if n == nil {
			return nil
		}
	}
	return n
}
