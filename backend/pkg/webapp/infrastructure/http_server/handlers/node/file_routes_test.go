package node

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// The file routes used to be `/files/*` and `/files/*/versions`. Echo cannot
// hold both: the second registration wins for every request, so GetFile was
// unreachable and the editor received a version list where it expected content.
// This locks the shape that replaced them — one handler per request, no
// wildcard suffixes — so the next person adding a file route notices.
func TestFileRoutesResolveDistinctly(t *testing.T) {
	e := echo.New()
	nodes := e.Group("/nodes")

	handler := func(name string) echo.HandlerFunc {
		return func(c echo.Context) error {
			return c.String(http.StatusOK, name)
		}
	}

	nodes.GET("/:id/files", handler("list"))
	nodes.GET("/:id/files/content", handler("content"))
	nodes.GET("/:id/files/versions", handler("versions"))
	nodes.PUT("/:id/files/content", handler("update"))
	nodes.POST("/:id/files/:file_id/apply", handler("apply"))

	cases := []struct {
		method string
		target string
		want   string
	}{
		{http.MethodGet, "/nodes/n1/files", "list"},
		{http.MethodGet, "/nodes/n1/files/content?path=%2Fetc%2Fhosts", "content"},
		{http.MethodGet, "/nodes/n1/files/versions?path=%2Fetc%2Fhosts", "versions"},
		{http.MethodPut, "/nodes/n1/files/content", "update"},
		{http.MethodPost, "/nodes/n1/files/f1/apply", "apply"},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
		if rec.Body.String() != tc.want {
			t.Errorf("%s %s resolved to %q, want %q", tc.method, tc.target, rec.Body.String(), tc.want)
		}
	}
}

// A path with awkward characters must survive as a query parameter — the old
// wildcard form mangled a file literally named ".../versions".
func TestPathQueryParameterSurvivesAwkwardNames(t *testing.T) {
	e := echo.New()
	var seen string
	e.GET("/nodes/:id/files/versions", func(c echo.Context) error {
		seen = c.QueryParam("path")
		return c.NoContent(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nodes/n1/files/versions?path=%2Fopt%2Fapp%2Fversions", nil))

	if seen != "/opt/app/versions" {
		t.Fatalf("expected the raw path back, got %q", seen)
	}
}
