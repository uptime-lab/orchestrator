package http_server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The API's shape conventions, pinned before external consumers exist. Both
// read the GENERATED
// backend/docs/swagger.json, like routes_documented_test.go, so an annotation
// nobody regenerated fails here rather than shipping stale.
//
//   - a timestamp named created_at / updated_at is an RFC3339 STRING. It was
//     an epoch integer in 8 definitions and a string in 14, and every consumer
//     had to know which one it was holding.
//   - DELETE answers 204 with no body. Two deliberate exceptions return the
//     RESULTING STATE because they are config resets, not resource removals,
//     and the panel repaints from the body.

// deleteBodyExceptions are DELETE operations that legitimately answer 200:
// each resets configuration and returns the post-reset state.
var deleteBodyExceptions = map[string]bool{
	"/api/v1/nodes/{id}/led-config": true,
	"/api/v1/nodes/{id}/watchdog":   true,
}

func swaggerDocument(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../../../docs/swagger.json")
	if err != nil {
		t.Fatalf("reading the generated swagger document: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing swagger.json: %v", err)
	}
	return doc
}

func TestTimestampFieldsAreStrings(t *testing.T) {
	doc := swaggerDocument(t)
	defs, _ := doc["definitions"].(map[string]any)
	for name, d := range defs {
		props, _ := d.(map[string]any)["properties"].(map[string]any)
		for _, field := range []string{"created_at", "updated_at"} {
			p, ok := props[field].(map[string]any)
			if !ok {
				continue
			}
			if p["type"] != "string" {
				t.Errorf("%s.%s is %v — created_at/updated_at are RFC3339 strings everywhere", name, field, p["type"])
			}
		}
	}
}

func TestDeleteAnswers204(t *testing.T) {
	doc := swaggerDocument(t)
	paths, _ := doc["paths"].(map[string]any)
	for path, ops := range paths {
		del, ok := ops.(map[string]any)["delete"].(map[string]any)
		if !ok || deleteBodyExceptions[path] {
			continue
		}
		resp, _ := del["responses"].(map[string]any)
		for code := range resp {
			if strings.HasPrefix(code, "2") && code != "204" {
				t.Errorf("DELETE %s documents %s — deletions answer 204; a config reset that returns state belongs in deleteBodyExceptions with its reason", path, code)
			}
		}
	}
}

// bareErrorBodies are the documented failure responses that deliberately do
// NOT answer with the results.APIResponse envelope, keyed "METHOD path code".
// Everything else that fails answers through results.Send* (or the envelope
// error handler for a returned *echo.HTTPError), and its @Failure annotation
// says so — this test is what stops the two from drifting apart again (670
// annotations once said map[string]string while the runtime emitted the
// envelope). Adding an entry here is a product decision, not a way to silence
// the test.
var bareErrorBodies = map[string]string{
	// The generic command route's clients parse the legacy
	// {"error": <code>, "message": …} body (see the comment in SendCommand).
	"POST /api/v1/nodes/{id}/command 409": "legacy client contract",
	"POST /api/v1/nodes/{id}/command 503": "legacy client contract",
	// An external uptime probe's body, kept minimal on purpose.
	"GET /ready 503": "readiness probe",
}

// bareErrorPathPrefixes are whole route families that stay bare: the CDN's
// consumers are a boot ROM, the agent's package fetch and curl; the
// corresponding-source offer's is a person with curl or a browser, following
// the URL in the recovery image's notice, and its refusals are one plain
// sentence each.
var bareErrorPathPrefixes = []string{"/cdn/", "/source/"}

func TestErrorBodiesAreTheEnvelope(t *testing.T) {
	doc := swaggerDocument(t)
	paths, _ := doc["paths"].(map[string]any)
	seen := map[string]bool{}
	for path, ops := range paths {
		for method, op := range ops.(map[string]any) {
			resp, _ := op.(map[string]any)["responses"].(map[string]any)
			for code, r := range resp {
				if len(code) != 3 || code[0] < '4' {
					continue
				}
				key := strings.ToUpper(method) + " " + path + " " + code
				bare := false
				for _, prefix := range bareErrorPathPrefixes {
					if strings.HasPrefix(path, prefix) {
						bare = true
					}
				}
				if _, ok := bareErrorBodies[key]; ok {
					seen[key] = true
					bare = true
				}
				schema, _ := r.(map[string]any)["schema"].(map[string]any)
				ref, _ := schema["$ref"].(string)
				isEnvelope := strings.HasSuffix(ref, "results.APIResponse") ||
					strings.HasSuffix(ref, "results.FileTooLargeResult")
				switch {
				case bare && isEnvelope:
					t.Errorf("%s is listed as deliberately bare but is documented as the envelope — drop it from the exception list", key)
				case !bare && !isEnvelope:
					t.Errorf("%s is documented as %v — failures answer with results.APIResponse; a deliberate exception belongs in bareErrorBodies with its reason", key, schema)
				}
			}
		}
	}
	for key := range bareErrorBodies {
		if !seen[key] {
			t.Errorf("bareErrorBodies lists %s, which the document no longer has — remove the stale exception", key)
		}
	}
}

// bareSuccessBodies are the documented 2xx responses that deliberately do NOT
// answer with the results.APIResponse envelope, keyed "METHOD path code". Every
// other 2xx with a body is written by results.SendOKResponse /
// SendCreatedResponse / SendSuccess, whose JSON is {success, data, message,
// timestamp}, and its @Success annotation says so with swag's composition
// syntax — `results.APIResponse{data=T}` — so a client reading the document
// sees the envelope it will have to unwrap (237 annotations once documented the
// bare payload while the runtime put it under `data`). A 204 is excluded by
// status: it carries no body to describe. Adding an entry here is a product
// decision, not a way to silence the test.
var bareSuccessBodies = map[string]string{
	// JSON documents served as DOWNLOADS (Content-Disposition: attachment)
	// through c.JSONPretty / c.JSON: the file the operator saves IS the
	// payload, and an envelope around it would have to be stripped by hand.
	"GET /api/v1/account/export 200":                 "account export document, a download",
	"GET /api/v1/workflow-templates/{id}/export 200": "portable playbook document, a download",
	// Text and binary bodies: c.String / c.Blob / a streamed ZIP, consumed by
	// curl, an installer or a flashing tool rather than a JSON client.
	"POST /api/v1/adopt/exchange 200":             "adoption blob for the install script",
	"GET /api/v1/nodes/{id}/adoption-file 200":    "adoption file text",
	"POST /api/v1/nodes/{id}/provisioning/sd 200": "ZIP stream",
	"POST /api/v1/provisioning/sd 200":            "ZIP stream",
	"GET /api/v1/eeprom/bootcode/{soc} 200":       "recovery.bin binary",
	"GET /api/v1/ssh-keys/{id}/public-key 200":    "public key text",
	"GET /install.sh 200":                         "shell script piped into sh",
	"GET /uninstall.sh 200":                       "shell script piped into sh",
	// The probes: a liveness literal and a readiness body an external monitor
	// reads without knowing the API (see server.go, healthHandler /
	// readinessHandler).
	"GET /health 200": "liveness probe",
	"GET /ready 200":  "readiness probe",
}

// bareSuccessPathPrefixes are whole route families that stay bare: the CDN's
// consumers are a boot ROM, the agent's package fetch and curl; the
// corresponding-source offer serves a plain-text listing and the archives
// themselves as attachments, to curl or a browser, never to the SPA.
var bareSuccessPathPrefixes = []string{"/cdn/", "/source/"}

// envelopeWithoutData are the 2xx responses documented as the plain envelope
// with no `data` — the handler calls results.SendOKResponse(c, nil, message),
// so the message is the whole answer. Listed so that a typed payload cannot be
// documented as "just the envelope" to dodge naming it.
var envelopeWithoutData = map[string]string{
	"POST /api/v1/nodes/{id}/clear-boot-failsafe 200":         "acknowledgement only",
	"POST /api/v1/eeprom/webusb-provision/{id}/committed 200": "acknowledgement only",
	"PUT /api/v1/members/{userId}/status 200":                 "acknowledgement only",
	"PUT /api/v1/account-role-settings 200":                   "acknowledgement only",
	"POST /auth/oidc/link 200":                                "acknowledgement only",
	"POST /auth/oidc/reauth 200":                              "acknowledgement only",
}

// successSchemaKind classifies a documented 2xx schema: "envelope+data" for
// the swag composition (allOf [APIResponse, {properties: {data}}]), "envelope"
// for a bare $ref to APIResponse, "none" for no schema, and "bare" otherwise.
func successSchemaKind(schema map[string]any) string {
	if len(schema) == 0 {
		return "none"
	}
	if ref, _ := schema["$ref"].(string); strings.HasSuffix(ref, "results.APIResponse") {
		return "envelope"
	}
	allOf, _ := schema["allOf"].([]any)
	if len(allOf) == 2 {
		first, _ := allOf[0].(map[string]any)
		ref, _ := first["$ref"].(string)
		second, _ := allOf[1].(map[string]any)
		props, _ := second["properties"].(map[string]any)
		if strings.HasSuffix(ref, "results.APIResponse") && props["data"] != nil {
			return "envelope+data"
		}
	}
	return "bare"
}

func TestSuccessBodiesAreTheEnvelope(t *testing.T) {
	doc := swaggerDocument(t)
	paths, _ := doc["paths"].(map[string]any)
	seenBare := map[string]bool{}
	seenNoData := map[string]bool{}
	for path, ops := range paths {
		for method, op := range ops.(map[string]any) {
			resp, _ := op.(map[string]any)["responses"].(map[string]any)
			for code, r := range resp {
				if len(code) != 3 || code[0] != '2' || code == "204" {
					continue
				}
				key := strings.ToUpper(method) + " " + path + " " + code
				bare := false
				for _, prefix := range bareSuccessPathPrefixes {
					if strings.HasPrefix(path, prefix) {
						bare = true
					}
				}
				if _, ok := bareSuccessBodies[key]; ok {
					seenBare[key] = true
					bare = true
				}
				schema, _ := r.(map[string]any)["schema"].(map[string]any)
				kind := successSchemaKind(schema)
				switch {
				case bare && kind != "bare":
					t.Errorf("%s is listed as deliberately bare but is documented as %s — drop it from the exception list", key, kind)
				case bare:
					// A bare body is documented as what it is; nothing more to check.
				case kind == "envelope+data":
					if _, ok := envelopeWithoutData[key]; ok {
						t.Errorf("%s documents a data payload but is listed in envelopeWithoutData — remove the stale entry", key)
					}
				case kind == "envelope":
					if _, ok := envelopeWithoutData[key]; ok {
						seenNoData[key] = true
					} else {
						t.Errorf("%s is documented as the plain envelope — name the payload as results.APIResponse{data=T}, or list it in envelopeWithoutData if the handler sends nil", key)
					}
				default:
					t.Errorf("%s is documented as %v — a 2xx with a body answers with the envelope; annotate it results.APIResponse{data=T}, or list it in bareSuccessBodies with its reason", key, schema)
				}
			}
		}
	}
	for key := range bareSuccessBodies {
		if !seenBare[key] {
			t.Errorf("bareSuccessBodies lists %s, which the document no longer has — remove the stale exception", key)
		}
	}
	for key := range envelopeWithoutData {
		if !seenNoData[key] {
			t.Errorf("envelopeWithoutData lists %s, which the document no longer has as a plain envelope — remove the stale entry", key)
		}
	}
}
