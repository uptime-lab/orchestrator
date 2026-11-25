package image

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
)

// VerifyCaptureUploadCapability is what the capture handler asks BEFORE it
// releases the request-body idle deadline, so it must accept
// exactly what CaptureUploadURL mints and nothing else: the same name, the
// same derived key, the same expiry binding. A drift between the two would
// either refuse every real capture or release the deadline for anyone.
func TestVerifyCaptureUploadCapabilityMatchesWhatIsMinted(t *testing.T) {
	cfg := &config.Config{}
	cfg.JWT.SecretKey = "capture-capability-test-secret"
	svc := NewService(nil, nil, cfg)

	imageID := uuid.New()
	key := cdnsign.DeriveKey(cfg.CDNSigningSecret())
	exp, sig := cdnsign.Sign(captureUploadName(imageID), key, time.Minute)

	if err := svc.VerifyCaptureUploadCapability(imageID, exp, sig); err != nil {
		t.Fatalf("a capability minted over the capture name was refused: %v", err)
	}

	// A valid signature with the same key over a DIFFERENT name (a CDN download
	// capability), to show the check is bound to the capture name and not
	// merely to the key.
	otherExp, otherSig := cdnsign.Sign("cdn/"+imageID.String(), key, time.Minute)

	cases := map[string]struct {
		id       uuid.UUID
		exp, sig string
	}{
		"another image":     {uuid.New(), exp, sig},
		"tampered sig":      {imageID, exp, flipFirstHexDigit(t, sig)},
		"missing sig":       {imageID, exp, ""},
		"missing exp":       {imageID, "", sig},
		"expired":           {imageID, "1", sig},
		"garbage exp":       {imageID, "soon", sig},
		"signed other name": {imageID, otherExp, otherSig},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := svc.VerifyCaptureUploadCapability(tc.id, tc.exp, tc.sig)
			if !errors.Is(err, cdnsign.ErrInvalidSignature) {
				t.Fatalf("expected ErrInvalidSignature, got %v", err)
			}
		})
	}
}

// flipFirstHexDigit returns sig with its first digit CHANGED — "0"+sig[1:]
// left a signature that already began with 0 intact, which made the tampering
// case pass for the wrong reason about one run in sixteen.
func flipFirstHexDigit(t *testing.T, sig string) string {
	t.Helper()
	if sig == "" {
		t.Fatal("empty signature")
	}
	first := "0"
	if sig[0] == '0' {
		first = "1"
	}
	out := first + sig[1:]
	if out == sig {
		t.Fatal("the tampered signature equals the original")
	}
	return out
}
