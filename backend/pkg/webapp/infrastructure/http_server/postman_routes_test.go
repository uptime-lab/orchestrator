package http_server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Postman is caught up but enforced by nothing, so "caught up" has always been a
// claim no test could check. This closes the direction that CAN be checked
// without pretending the collections are complete.
//
// They are deliberately partial — hand-curated requests with test scripts a
// generator would flatten, 55/16/181 against 243 operations — so "every
// operation has a request" would fail forever and teach nobody anything. What
// must hold is the other way round: every request in a collection has to name a
// route the server actually serves. A collection that drifts to a route which
// was renamed or removed hands whoever opens it a 404 and no way to tell that
// the collection, not the server, is wrong. That is exactly what
// `GET /nodes/{id}/files/{path}` did to the Swagger document.

// postmanCollectionDir is relative to this package.
const postmanCollectionDir = "../../../../../postman"

// postmanVariable matches Postman's {{var}} interpolation.
var postmanVariable = regexp.MustCompile(`\{\{[^}]*\}\}`)

type postmanURL struct {
	Raw string `json:"raw"`
}

type postmanRequest struct {
	Method string          `json:"method"`
	URL    json.RawMessage `json:"url"`
}

type postmanItem struct {
	Name    string          `json:"name"`
	Item    []postmanItem   `json:"item"`
	Request *postmanRequest `json:"request"`
}

type postmanCollection struct {
	Item []postmanItem `json:"item"`
}

// postmanRoutes walks the collections and returns "METHOD /canonical/path" for
// every request, with the request's own name for the failure message.
func postmanRoutes(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(postmanCollectionDir, "*.postman_collection.json"))
	if err != nil {
		t.Fatalf("globbing the collections: %v", err)
	}
	if len(files) == 0 {
		if _, statErr := os.Stat(postmanCollectionDir); os.IsNotExist(statErr) {
			// The docker module test builds from backend/ alone. The guard is
			// enforced from the full checkout by the static-checks CI step,
			// which fails the run if this SKIPS.
			t.Skipf("postman tree not present in this build context; enforced by static-checks")
		}
		t.Fatalf("no Postman collections found under %s; if they moved, this test moves with them", postmanCollectionDir)
	}

	routes := map[string]string{}
	var walk func(items []postmanItem, file string)
	walk = func(items []postmanItem, file string) {
		for _, it := range items {
			if len(it.Item) > 0 {
				walk(it.Item, file)
			}
			if it.Request == nil || it.Request.Method == "" {
				continue
			}
			raw := postmanRawURL(it.Request.URL)
			if raw == "" {
				continue
			}
			key := it.Request.Method + " " + canonicalPostmanPath(raw)
			if key == "" {
				continue
			}
			if _, seen := routes[key]; !seen {
				routes[key] = filepath.Base(file) + " → " + it.Name
			}
		}
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		var c postmanCollection
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		walk(c.Item, file)
	}
	return routes
}

// postmanRawURL reads the url field, which Postman writes either as a string or
// as an object with a `raw` key depending on how the request was created.
func postmanRawURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var asObject postmanURL
	if err := json.Unmarshal(raw, &asObject); err == nil {
		return asObject.Raw
	}
	return ""
}

// canonicalPostmanPath turns a Postman raw URL into the same shape
// registeredRoutes produces: {{base_url}} and the query string go, {{var}}
// segments become parameters, and canonical() erases parameter names so only
// the SHAPE is compared.
func canonicalPostmanPath(raw string) string {
	path := raw
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	// {{base_url}} is the only prefix the collections use; anything before the
	// first "/" after it is host, not path. A hard-coded sample value in a path
	// segment is left as the literal it is — routeMatches compares the way the
	// router does, so a served parameter accepts it.
	path = postmanVariable.ReplaceAllString(path, "\x00")
	if i := strings.Index(path, "/"); i > 0 {
		path = path[i:]
	}
	if !strings.HasPrefix(path, "/") {
		return ""
	}
	// A variable that was a whole segment is a path parameter; one embedded in a
	// segment (a host, a query) has already been dropped above.
	path = strings.ReplaceAll(path, "\x00", ":param")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		path = "/"
	}
	return canonical(path)
}

// routeMatches reports whether a request path is one the router would dispatch.
//
// Not string equality. A collection legitimately hard-codes a sample value where
// the route has a parameter (`/secret-tags/infra`), and that request is correct
// — the router matches it. So the comparison is the router's own: literal
// segments must agree, a served parameter accepts any single segment, and a
// served wildcard accepts the rest of the path.
//
// The direction matters. A served parameter matching a request LITERAL is the
// case above and is fine; the reverse would let a collection invent a route by
// putting a variable where the server has a literal, so it is not allowed.
func routeMatches(servedPath, requestPath string) bool {
	served := strings.Split(strings.Trim(servedPath, "/"), "/")
	request := strings.Split(strings.Trim(requestPath, "/"), "/")
	for i, seg := range served {
		if seg == "*" {
			return true
		}
		if i >= len(request) {
			return false
		}
		if seg == "{}" {
			// A parameter accepts one non-empty segment, literal or variable.
			if request[i] == "" {
				return false
			}
			continue
		}
		if seg != request[i] {
			return false
		}
	}
	return len(served) == len(request)
}

func TestEveryPostmanRequestNamesARealRoute(t *testing.T) {
	servedByMethod := map[string][]string{}
	for _, r := range registeredRoutes(t) {
		method, path, found := strings.Cut(r, " ")
		if !found {
			continue
		}
		servedByMethod[method] = append(servedByMethod[method], path)
	}

	requests := postmanRoutes(t)
	if len(requests) < 100 {
		t.Fatalf("only %d Postman requests parsed; the collections hold far more, so the parser has stopped matching their shape", len(requests))
	}

	var missing []string
	for route, where := range requests {
		method, path, found := strings.Cut(route, " ")
		if !found {
			continue
		}
		matched := false
		for _, served := range servedByMethod[method] {
			if routeMatches(served, path) {
				matched = true
				break
			}
		}
		if !matched {
			missing = append(missing, route+"   ("+where+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("Postman calls a route the server does not serve: %s", m)
	}
	if len(missing) > 0 {
		t.Log("A request kept against a renamed or removed route hands whoever opens the " +
			"collection a 404 with no hint that the collection, not the server, is wrong.")
	}
}
