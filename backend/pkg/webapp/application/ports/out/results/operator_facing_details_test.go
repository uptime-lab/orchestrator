package results

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

type detailedRefusal struct{ cap int }

func (e *detailedRefusal) Error() string     { return fmt.Sprintf("the cap is %d", e.cap) }
func (e *detailedRefusal) HTTPStatus() int   { return http.StatusConflict }
func (e *detailedRefusal) ErrorCode() string { return "detailed_refusal" }
func (e *detailedRefusal) ErrorDetails() any { return map[string]any{"cap": e.cap} }

// A refusal that offers structured details sees them in `data`, beside the
// message and the code, through the wrap — so a client can render its own
// copy around the numbers instead of parsing the sentence.
func TestOperatorFacingDetailsRideInData(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	wrapped := fmt.Errorf("creating: %w", &detailedRefusal{cap: 25})
	if err := SendInternalServerError(c, "failed to create", wrapped); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	var body struct {
		Message string         `json:"message"`
		Code    string         `json:"code"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "detailed_refusal" || body.Message != "the cap is 25" {
		t.Errorf("body = %+v", body)
	}
	if body.Data["cap"] != float64(25) {
		t.Errorf("data = %v, want the cap", body.Data)
	}
}
