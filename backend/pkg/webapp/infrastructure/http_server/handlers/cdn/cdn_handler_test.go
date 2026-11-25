package cdn

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
)

// fakeStorage is a storage.StorageClient that reports whatever backend type the
// test needs. The agent CDN only ever calls GetFileInfo and Retrieve, which is
// the point: nothing about serving a package depends on the files being local.
type fakeStorage struct {
	storageType string
	objects     map[string]string
}

func (f *fakeStorage) StoreWithOptions(context.Context, string, io.Reader, *storage.StorageOptions) (string, error) {
	return "", nil
}

func (f *fakeStorage) Retrieve(_ context.Context, name string) (io.ReadCloser, error) {
	body, ok := f.objects[name]
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func (f *fakeStorage) GetSignedURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (f *fakeStorage) Delete(context.Context, string) error { return nil }

func (f *fakeStorage) Exists(_ context.Context, name string) (bool, error) {
	_, ok := f.objects[name]
	return ok, nil
}

func (f *fakeStorage) GetFileInfo(_ context.Context, name string) (*file.FileInfo, error) {
	body, ok := f.objects[name]
	if !ok {
		return nil, nil
	}
	return &file.FileInfo{Name: name, FilePath: name, Size: int64(len(body))}, nil
}

func (f *fakeStorage) GetStorageType() string { return f.storageType }

func serveAgentPackage(t *testing.T, storageType string) *httptest.ResponseRecorder {
	t.Helper()
	st := &fakeStorage{
		storageType: storageType,
		objects: map[string]string{
			"agent-artifacts/orchestrator-agent_0.4.8_linux_arm64.deb": "PACKAGE-BYTES",
		},
	}
	cfg := &config.Config{}
	cfg.Storage.AgentCDNBasePath = "agent-artifacts"
	h := NewCDNHandler(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/cdn/agent/0.4.8/arm64/deb", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("version", "arch", "pkgType")
	c.SetParamValues("0.4.8", "arm64", "deb")
	if err := h.ServeAgentPackage(c); err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	return rec
}

// An S3-backed install must serve the agent CDN: the check is "can the CDN
// proxy it", not "is the storage local". Getting it wrong 404s every agent
// download, which takes install.sh and update_agent down for the whole fleet.
func TestAgentPackageIsServedFromObjectStorage(t *testing.T) {
	rec := serveAgentPackage(t, storage.TypeS3)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; an S3-backed install must still serve the agent CDN", rec.Code)
	}
	if body := rec.Body.String(); body != "PACKAGE-BYTES" {
		t.Errorf("body = %q, want the package bytes proxied out of storage", body)
	}
}

func TestAgentPackageIsServedFromLocalStorage(t *testing.T) {
	if rec := serveAgentPackage(t, storage.TypeLocal); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// A backend the CDN does not proxy must be refused explicitly, and NOT with a
// 404: "not found" sent an operator looking for a missing file when the real
// answer was that this route cannot serve this backend at all.
func TestUnproxiedBackendIsRefusedExplicitly(t *testing.T) {
	rec := serveAgentPackage(t, storage.TypeGCP)

	if rec.Code == http.StatusOK {
		t.Fatal("served a backend the CDN does not proxy")
	}
	if rec.Code == http.StatusNotFound {
		t.Fatalf("status = 404; that reads as a missing package rather than an unsupported backend")
	}
}

// serveFile drives ServeFile with a validly signed URL for one stored object.
func serveFile(t *testing.T, objectPath, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	const secret = "a-test-jwt-secret-long-enough-for-anything"
	st := &fakeStorage{
		storageType: storage.TypeLocal,
		objects:     map[string]string{objectPath: "IMAGE-BYTES"},
	}
	cfg := &config.Config{}
	cfg.JWT.SecretKey = secret
	h := NewCDNHandler(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	exp, sig := cdnsign.Sign(objectPath, cdnsign.DeriveKey(secret), time.Hour)
	q := url.Values{}
	q.Set(cdnsign.ParamExpires, exp)
	q.Set(cdnsign.ParamSignature, sig)
	if rawQuery != "" {
		extra, err := url.ParseQuery(rawQuery)
		if err != nil {
			t.Fatalf("bad test query: %v", err)
		}
		for k, vs := range extra {
			q[k] = vs
		}
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/cdn/"+objectPath+"?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("*")
	c.SetParamValues(objectPath)
	if err := h.ServeFile(c); err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	return rec
}

// Without this, an account taking three images out of the platform gets three
// files named after UUIDs, because the storage key IS a UUID.
func TestServeFileLabelsTheDownloadWithTheRequestedName(t *testing.T) {
	rec := serveFile(t, "acct/2f2d0f16.xz", cdnsign.ParamFileName+"=ubuntu-24.04.img.xz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="ubuntu-24.04.img.xz"` {
		t.Errorf("Content-Disposition = %q", got)
	}
}

// The parameter is NOT covered by the signature, so this header is built from
// attacker-controllable input on every request that reaches it. Sanitization is
// the only thing standing between that and a response-splitting header.
func TestServeFileSanitizesTheRequestedName(t *testing.T) {
	rec := serveFile(t, "acct/2f2d0f16.xz", url.Values{
		cdnsign.ParamFileName: {"evil.img\r\nSet-Cookie: session=stolen"},
	}.Encode())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("a header was injected through the filename hint: %q", got)
	}
	got := rec.Header().Get("Content-Disposition")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("Content-Disposition carries a line break: %q", got)
	}
	if strings.Count(got, `"`) != 2 {
		t.Errorf("Content-Disposition = %q, want exactly one quoted value", got)
	}
}

// An unusable hint must not break the transfer — the bytes are what the caller
// came for, and a name is a convenience. The download still arrives as an
// attachment, under the object's own name.
func TestServeFileIgnoresAnUnusableName(t *testing.T) {
	rec := serveFile(t, "acct/2f2d0f16.xz", cdnsign.ParamFileName+"=%E2%82%AC%E2%82%AC")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="2f2d0f16.xz"` {
		t.Errorf("Content-Disposition = %q, want the object's own name", got)
	}
	if rec.Body.String() != "IMAGE-BYTES" {
		t.Errorf("body = %q, want the file served anyway", rec.Body.String())
	}
}

// Existing callers — install_os, update_agent, the EEPROM package fetch — send
// no hint at all. They read the body and ignore headers, so the attachment
// disposition every response now carries costs them nothing; what must not
// change is the status and the bytes.
func TestServeFileWithoutAHintServesTheBytes(t *testing.T) {
	rec := serveFile(t, "acct/2f2d0f16.xz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "IMAGE-BYTES" {
		t.Errorf("body = %q", rec.Body.String())
	}
	assertInertDownload(t, rec, "2f2d0f16.xz")
}

// serveFileWithStoredType is serveFile with the object carrying a stored MIME
// type — what an S3 HeadObject returns for an object whose upload persisted
// the client's multipart Content-Type.
func serveFileWithStoredType(t *testing.T, objectPath, storedType, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	return serveFileFrom(t, &typedFakeStorage{
		fakeStorage: fakeStorage{
			storageType: storage.TypeS3,
			objects:     map[string]string{objectPath: "<script>alert(1)</script>"},
		},
		contentType: storedType,
	}, objectPath, rawQuery)
}

// serveFileFrom drives ServeFile with a validly signed URL against any storage.
func serveFileFrom(t *testing.T, st storage.StorageClient, objectPath, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	const secret = "a-test-jwt-secret-long-enough-for-anything"
	cfg := &config.Config{}
	cfg.JWT.SecretKey = secret
	h := NewCDNHandler(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	exp, sig := cdnsign.Sign(objectPath, cdnsign.DeriveKey(secret), time.Hour)
	q := url.Values{}
	q.Set(cdnsign.ParamExpires, exp)
	q.Set(cdnsign.ParamSignature, sig)
	if rawQuery != "" {
		extra, err := url.ParseQuery(rawQuery)
		if err != nil {
			t.Fatalf("bad test query: %v", err)
		}
		for k, vs := range extra {
			q[k] = vs
		}
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/cdn/"+objectPath+"?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("*")
	c.SetParamValues(objectPath)
	if err := h.ServeFile(c); err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	return rec
}

type typedFakeStorage struct {
	fakeStorage
	contentType string
}

func (f *typedFakeStorage) GetFileInfo(ctx context.Context, name string) (*file.FileInfo, error) {
	info, err := f.fakeStorage.GetFileInfo(ctx, name)
	if info != nil {
		info.ContentType = f.contentType
	}
	return info, err
}

type metaFakeStorage struct {
	fakeStorage
	metadata map[string]string
}

func (f *metaFakeStorage) GetFileInfo(ctx context.Context, name string) (*file.FileInfo, error) {
	info, err := f.fakeStorage.GetFileInfo(ctx, name)
	if info != nil {
		info.Metadata = f.metadata
	}
	return info, err
}

// assertInertDownload is the inert-download contract: whatever MIME the object
// carries, the response can only ever be saved, never rendered.
func assertInertDownload(t *testing.T, rec *httptest.ResponseRecorder, wantName string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="`+wantName+`"` {
		t.Errorf("Content-Disposition = %q, want attachment as %q", got, wantName)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Errorf("Content-Security-Policy = %q, want a sandbox policy", got)
	}
}

// A stored Content-Type may be whatever a client sent, and /cdn is served
// from the application's own origin, so reflecting it would let an
// `image.manage` holder mint a signed link that renders text/html there. The
// proxy in front is not relied on for nosniff or the CSP, so the headers have
// to come from here.
func TestServeFileNeverReflectsAStoredActiveMIME(t *testing.T) {
	rec := serveFileWithStoredType(t, "acct/2f2d0f16.img", "text/html; charset=utf-8", "")
	assertInertDownload(t, rec, "2f2d0f16.img")
}

func TestServeFileStaysInertWithAFileNameHint(t *testing.T) {
	rec := serveFileWithStoredType(t, "acct/2f2d0f16.img", "text/html", cdnsign.ParamFileName+"=ubuntu.img")
	assertInertDownload(t, rec, "ubuntu.img")
}

// Objects uploaded before the fix keep whatever they were stored with, and a
// content_disposition in object metadata is equally not ours to trust.
func TestServeFileIgnoresStoredDispositionMetadata(t *testing.T) {
	rec := serveFileFrom(t, &metaFakeStorage{
		fakeStorage: fakeStorage{storageType: storage.TypeS3, objects: map[string]string{"acct/2f2d0f16.img": "x"}},
		metadata:    map[string]string{"content_disposition": "inline"},
	}, "acct/2f2d0f16.img", "")
	assertInertDownload(t, rec, "2f2d0f16.img")
}
