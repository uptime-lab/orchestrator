package results

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
)

// FileTooLargeResult is the 413 body of the image upload routes: the standard
// envelope plus the effective limit, so a client can tell the user what WOULD
// fit instead of only that this did not.
//
// The two limit fields sit at the top level beside the envelope — not inside
// `data` — because the frontend's upload error handling reads
// `response.data.max_size_mb` there, and a 413 is the one refusal whose body a
// client must read to act on. Moving them under `data` would turn every
// oversized upload into the bare "file too large" message.
type FileTooLargeResult struct {
	APIResponse
	MaxSize   string `json:"max_size"`
	MaxSizeMB string `json:"max_size_mb"`
}

// SendFileTooLarge answers 413 with the effective limit.
func SendFileTooLarge(c echo.Context, maxSizeBytes int64) error {
	resp := NewErrorResponse("file too large", nil)
	return c.JSON(http.StatusRequestEntityTooLarge, &FileTooLargeResult{
		APIResponse: *resp,
		MaxSize:     fmt.Sprintf("%d bytes", maxSizeBytes),
		MaxSizeMB:   fmt.Sprintf("%d MB", maxSizeBytes/(1024*1024)),
	})
}
