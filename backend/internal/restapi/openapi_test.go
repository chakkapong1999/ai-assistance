package restapi

import (
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The spec is hand-written; this keeps it honest: same operations, same
// required role as the route table the server actually registers.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openapiSpec, &doc); err != nil {
		t.Fatal(err)
	}

	spec := map[string]string{} // "METHOD path" -> role
	for path, item := range doc.Paths {
		for method, op := range item {
			m := strings.ToUpper(method)
			if m == "PARAMETERS" {
				continue
			}
			role, _ := op.(map[string]any)["x-required-role"].(string)
			spec[m+" "+path] = role
		}
	}
	code := map[string]string{}
	for _, r := range Routes() {
		code[r.Method+" "+r.Path] = string(r.Access)
	}

	keys := func(m map[string]string) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	for _, k := range keys(code) {
		role, ok := spec[k]
		switch {
		case !ok:
			t.Errorf("route %s is not documented in openapi.yaml", k)
		case role != code[k]:
			t.Errorf("route %s: spec says x-required-role %q, server enforces %q", k, role, code[k])
		}
	}
	for _, k := range keys(spec) {
		if _, ok := code[k]; !ok {
			t.Errorf("openapi.yaml documents %s, which the server does not serve", k)
		}
	}
}
