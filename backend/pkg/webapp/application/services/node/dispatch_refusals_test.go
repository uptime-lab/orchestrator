package node

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// Payload refusals the builder already worded reached the generic route as a
// 500, because nothing marked them as the operator's. Each one here must be an
// operator refusal and must stop before a command row exists.
func TestAPayloadRefusalIsTheOperatorsAndWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slug   string
		params map[string]interface{}
	}{
		{"empty hostname", node_command.SlugSetHostname, map[string]interface{}{"hostname": ""}},
		{"no hostname at all", node_command.SlugSetHostname, nil},
		{"empty script", node_command.SlugRunScript, map[string]interface{}{"script": "  "}},
		{"script with a NUL byte", node_command.SlugRunScript, map[string]interface{}{"script": "echo one\x00echo two"}},
		{"empty package list", node_command.SlugInstallPackages, map[string]interface{}{"packages": []interface{}{}}},
		{"package list of separators", node_command.SlugInstallPackages, map[string]interface{}{"packages": " , "}},
		{"relative read path", node_command.SlugReadFile, map[string]interface{}{"path": "etc/hostname"}},
		{"credential-bearing read path", node_command.SlugReadFile, map[string]interface{}{"path": "/etc/shadow"}},
		{"empty read path", node_command.SlugReadFile, map[string]interface{}{}},
		{"relative delete path", node_command.SlugDeleteFile, map[string]interface{}{"path": "tmp/x"}},
		{"relative patch path", node_command.SlugPatchFile, map[string]interface{}{"path": "boot/config.txt", "content": "x"}},
		{"bad patch marker", node_command.SlugPatchFile, map[string]interface{}{"content": "x", "marker": "a b"}},
		{"empty patch content", node_command.SlugPatchFile, map[string]interface{}{"content": ""}},
		{"no unit name", node_command.SlugManageService, map[string]interface{}{"action": "start"}},
		{"no download url", node_command.SlugDownloadFile, map[string]interface{}{"dest_path": "/tmp/f"}},
		{"no download destination", node_command.SlugDownloadFile, map[string]interface{}{"url": "https://example.com/f"}},
		{"no write path", node_command.SlugWriteFile, map[string]interface{}{"content": "x"}},
		{"no write content", node_command.SlugWriteFile, map[string]interface{}{"path": "/tmp/x"}},
		{"no update_file path", node_command.SlugUpdateFile, map[string]interface{}{"content": "x"}},
		{"no wait pattern", node_command.SlugWaitForFile, map[string]interface{}{"path": "/tmp/x"}},
		{"no erase target", node_command.SlugEraseDrive, map[string]interface{}{}},
		{"no capture title", node_command.SlugCaptureImage, map[string]interface{}{"title": ""}},
		{"no boot order", node_command.SlugUpdateBootOrder, map[string]interface{}{}},
		{"boot order of the wrong shape", node_command.SlugUpdateBootOrder, map[string]interface{}{"boot_order": "nvme"}},
		{"boot order without the EEPROM capability", node_command.SlugUpdateBootOrder, map[string]interface{}{"boot_order": []interface{}{"nvme", "http"}}},
		{"unknown install_agent variant", node_command.SlugInstallAgent, map[string]interface{}{"variant": "tiny"}},
		{"install_os with nothing", node_command.SlugInstallOS, nil},
		// The access reconciler's own files: a write, patch, download or delete
		// would grant or revoke access the access entries decide.
		{"write to a platform sudo grant", node_command.SlugWriteFile, map[string]interface{}{"path": "/etc/sudoers.d/90-orchestrator-ops", "content": "ops ALL=(ALL) NOPASSWD:ALL\n"}},
		{"write to the access sshd drop-in", node_command.SlugWriteFile, map[string]interface{}{"path": "/etc/ssh/sshd_config.d/00-orchestrator-access.conf", "content": "x"}},
		{"patch of a platform sudo grant", node_command.SlugPatchFile, map[string]interface{}{"path": "/etc/sudoers.d/90-orchestrator-ops", "content": "x"}},
		{"download over a principals file", node_command.SlugDownloadFile, map[string]interface{}{"url": "https://example.com/f", "dest_path": "/etc/ssh/auth_principals/ops"}},
		{"delete of a platform sudo grant", node_command.SlugDeleteFile, map[string]interface{}{"path": "/etc/sudoers.d/90-orchestrator-ops"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n := clampService(&clampCapturingCommandService{})
			capture, err := dispatch(t, s, n, tc.slug, tc.params)
			if !isOperatorRefusal(err) {
				t.Fatalf("err = %v, want an operator refusal", err)
			}
			if capture.slug != "" {
				t.Error("a command was registered for a refused payload")
			}
		})
	}
}

// A missing value is the operator's to fix whatever state the node is in, so
// it answers before the refusal about the node does.
func TestAMissingParameterAnswersBeforeTheNodesState(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	n.Status = node.StatusOffline

	_, err := dispatch(t, s, n, node_command.SlugRunScript, map[string]interface{}{"script": ""})
	if !isOperatorRefusal(err) || !strings.Contains(err.Error(), "script") {
		t.Fatalf("err = %v, want the missing script named", err)
	}
}

// A value that is wrong in itself is the operator's to fix too: on an offline
// node it must answer as theirs, not as "node unavailable", which sends them
// to wait for a reconnect that would only bring the same refusal.
func TestAWrongPayloadAnswersBeforeTheNodesState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slug   string
		params map[string]interface{}
	}{
		{"relative read path", node_command.SlugReadFile, map[string]interface{}{"path": "etc/hostname"}},
		{"credential-bearing read path", node_command.SlugReadFile, map[string]interface{}{"path": "/etc/shadow"}},
		{"unknown service action", node_command.SlugManageService, map[string]interface{}{"name": "app.service", "action": "create"}},
		{"headers of the wrong type", node_command.SlugDownloadFile, map[string]interface{}{
			"url": "https://example.com/f", "dest_path": "/tmp/f", "headers": []interface{}{"Authorization: x"},
		}},
		{"checksum of the wrong length", node_command.SlugDownloadFile, map[string]interface{}{
			"url": "https://example.com/f", "dest_path": "/tmp/f", "checksum": "abc123",
		}},
		{"protected delete path", node_command.SlugDeleteFile, map[string]interface{}{"path": "/boot/firmware/config.txt"}},
		{"bad patch marker", node_command.SlugPatchFile, map[string]interface{}{"content": "x", "marker": "a b"}},
		{"malformed agent version", node_command.SlugUpdateAgent, map[string]interface{}{"agent_version": "1.2"}},
		{"invalid hostname", node_command.SlugSetHostname, map[string]interface{}{"hostname": "Not_Valid"}},
		{"script with a NUL byte", node_command.SlugRunScript, map[string]interface{}{"script": "echo one\x00echo two"}},
		{"unknown install_agent variant", node_command.SlugInstallAgent, map[string]interface{}{"variant": "tiny"}},
		{"boot order without the EEPROM capability", node_command.SlugUpdateBootOrder, map[string]interface{}{"boot_order": []interface{}{"nvme", "http"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n := clampService(&clampCapturingCommandService{})
			n.Status = node.StatusOffline

			capture, err := dispatch(t, s, n, tc.slug, tc.params)
			var unavailable *NodeUnavailableError
			if errors.As(err, &unavailable) {
				t.Fatalf("err = %v, want the payload refused before the node's state", err)
			}
			if !isOperatorRefusal(err) && !errors.Is(err, node_command.ErrInvalidHostname) {
				t.Fatalf("err = %v, want an operator refusal", err)
			}
			if capture.slug != "" {
				t.Error("a command was registered for a refused payload")
			}
		})
	}
}

// manage_service writes and reloads the unit file before the agent looks at
// the action, so an action it does not implement must not leave the platform.
func TestManageServiceRefusesAnUnknownAction(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	capture, err := dispatch(t, s, n, node_command.SlugManageService, map[string]interface{}{
		"name": "qa-scratch.service", "action": "create", "unit_content": "[Unit]\n",
	})
	if !isOperatorRefusal(err) {
		t.Fatalf("err = %v, want an operator refusal", err)
	}
	for _, allowed := range manageServiceActions {
		if !strings.Contains(err.Error(), allowed) {
			t.Errorf("the refusal does not name the allowed action %q: %s", allowed, err)
		}
	}
	if capture.slug != "" {
		t.Error("a command was registered for an unknown action")
	}
}

func TestManageServiceCarriesEveryActionTheAgentRuns(t *testing.T) {
	for _, action := range append([]string{""}, manageServiceActions...) {
		capture := &clampCapturingCommandService{}
		s, n := clampService(capture)
		_, err := dispatch(t, s, n, node_command.SlugManageService, map[string]interface{}{
			"name": "app.service", "action": action,
		})
		if !errors.Is(err, errStopAfterCapture) {
			t.Fatalf("action %q: err = %v, want the dispatch to reach registration", action, err)
		}
		want := action
		if want == "" {
			want = "enable_now"
		}
		if got := capture.payload["action"]; got != want {
			t.Errorf("action %q carried as %v, want %q", action, got, want)
		}
	}
}

// The agent compares bare hex; a "sha256:" prefix failed on the device as a
// bare checksum mismatch.
func TestDownloadFileChecksumIsNormalised(t *testing.T) {
	const digest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	for _, given := range []string{
		digest,
		strings.ToUpper(digest),
		"sha256:" + digest,
		"SHA256:" + strings.ToUpper(digest),
		"  sha256: " + digest + " ",
	} {
		capture := &clampCapturingCommandService{}
		s, n := clampService(capture)
		_, err := dispatch(t, s, n, node_command.SlugDownloadFile, map[string]interface{}{
			"url": "https://example.com/f", "dest_path": "/tmp/f", "checksum": given,
		})
		if !errors.Is(err, errStopAfterCapture) {
			t.Fatalf("%q: err = %v, want the dispatch to reach registration", given, err)
		}
		if got := capture.payload["checksum"]; got != digest {
			t.Errorf("%q carried as %v, want %q", given, got, digest)
		}
	}
}

func TestDownloadFileChecksumThatIsNotSHA256IsRefused(t *testing.T) {
	for _, given := range []string{
		"md5:d41d8cd98f00b204e9800998ecf8427e",
		"d41d8cd98f00b204e9800998ecf8427e", // an MD5 by length
		"sha512:" + strings.Repeat("a", 128),
		"sha256:" + strings.Repeat("g", 64),
		"sha256:",
	} {
		s, n := clampService(&clampCapturingCommandService{})
		capture, err := dispatch(t, s, n, node_command.SlugDownloadFile, map[string]interface{}{
			"url": "https://example.com/f", "dest_path": "/tmp/f", "checksum": given,
		})
		if !isOperatorRefusal(err) {
			t.Errorf("%q: err = %v, want an operator refusal", given, err)
		}
		if capture.slug != "" {
			t.Errorf("%q: a command was registered", given)
		}
	}
}

// An omitted checksum stays acceptable: it is optional.
func TestDownloadFileWithoutAChecksumIsCarried(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)
	_, err := dispatch(t, s, n, node_command.SlugDownloadFile, map[string]interface{}{
		"url": "https://example.com/f", "dest_path": "/tmp/f", "checksum": "",
	})
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("err = %v, want the dispatch to reach registration", err)
	}
	if _, present := capture.payload["checksum"]; present {
		t.Error("an empty checksum was carried")
	}
}

func TestEraseOfTheRunningDiskIsAStateRefusal(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	n.SystemInfo = &node.SystemInfo{StorageDevices: map[string]*node.StorageDevice{
		"/dev/mmcblk0": {Partitions: []node.StoragePartition{{MountPoint: "/"}}},
	}}
	_, err := dispatch(t, s, n, node_command.SlugEraseDrive, map[string]interface{}{"target_device": "/dev/mmcblk0"})
	var bootDrive *BootDriveEraseError
	if !errors.As(err, &bootDrive) || bootDrive.HTTPStatus() != 409 {
		t.Fatalf("err = %v, want a 409 BootDriveEraseError", err)
	}
}

// --- capture_image ----------------------------------------------------------

type captureRecordService struct {
	services.ImageService
	created []uuid.UUID
	failed  []uuid.UUID
}

func (f *captureRecordService) CreateCaptureImage(_ context.Context, accountID uuid.UUID, title string, _ bool, _ uuid.UUID, _ string) (*image.Image, error) {
	img := &image.Image{ID: uuid.New(), AccountID: accountID, Title: title, CompressionStatus: image.CaptureInProgress}
	f.created = append(f.created, img.ID)
	return img, nil
}

func (f *captureRecordService) FailAwaitingCapture(_ context.Context, _, imageID uuid.UUID) error {
	f.failed = append(f.failed, imageID)
	return nil
}

func netbooted(n *node.Node) {
	n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{CurrentBootDevice: "http"}}}
}

// The agent refuses a capture on a live OS, but only after the image record
// exists; refused at dispatch, no record is created at all.
func TestCaptureOnALiveOSIsRefusedBeforeTheRecordExists(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	images := &captureRecordService{}
	s.imageService = images
	n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{CurrentBootDevice: "nvme"}}}

	capture, err := dispatch(t, s, n, node_command.SlugCaptureImage, map[string]interface{}{"title": "golden"})
	var required *RecoveryRequiredError
	if !errors.As(err, &required) || required.HTTPStatus() != 409 {
		t.Fatalf("err = %v, want a 409 RecoveryRequiredError", err)
	}
	if len(images.created) != 0 {
		t.Errorf("%d image records were created for a capture that cannot run", len(images.created))
	}
	if capture.slug != "" {
		t.Error("a command was registered")
	}
}

// A capture refused after its record exists — here by the registration —
// settles the record instead of leaving it "capturing".
func TestACaptureRefusedAfterItsRecordSettlesTheRecord(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	images := &captureRecordService{}
	s.imageService = images
	netbooted(n)

	_, err := dispatch(t, s, n, node_command.SlugCaptureImage, map[string]interface{}{"title": "golden", "target_device": "/dev/nvme0n1"})
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("err = %v, want the dispatch to reach registration", err)
	}
	if len(images.created) != 1 || !slices.Equal(images.failed, images.created) {
		t.Errorf("created %v, settled %v; want the one record settled", images.created, images.failed)
	}
}

// A malformed target_device is refused before the record exists, not by the
// registration after it.
func TestACaptureWithABadTargetCreatesNoRecord(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	images := &captureRecordService{}
	s.imageService = images
	netbooted(n)

	_, err := dispatch(t, s, n, node_command.SlugCaptureImage, map[string]interface{}{"title": "golden", "target_device": "../etc"})
	if !isOperatorRefusal(err) {
		t.Fatalf("err = %v, want an operator refusal", err)
	}
	if len(images.created) != 0 {
		t.Errorf("%d image records were created", len(images.created))
	}
}

// --- no regressions for playbook blocks ---------------------------------------

// A parameter this dispatch requires must be one the block editor requires
// too, or a playbook saved as valid is refused on every run; an action the
// block offers must be one the dispatch carries.
func TestEveryRequiredParameterIsRequiredByItsBlock(t *testing.T) {
	for slug, names := range commandRequiredParams {
		def, ok := workflow_block.FindBySlug(slug)
		if !ok {
			continue // not a playbook block; only the generic route reaches it
		}
		for _, name := range names {
			found := false
			for _, spec := range def.Params {
				if spec.Name == name {
					found = true
					if !spec.Required && spec.Default == nil {
						t.Errorf("%s.%s is required at dispatch but optional, with no default, in the block", slug, name)
					}
				}
			}
			if !found {
				t.Errorf("%s.%s is required at dispatch but the block has no such parameter", slug, name)
			}
		}
	}
	def, ok := workflow_block.FindBySlug(node_command.SlugManageService)
	if !ok {
		t.Fatal("manage_service has no block")
	}
	for _, spec := range def.Params {
		if spec.Name != "action" {
			continue
		}
		for _, v := range spec.EnumValues {
			if _, err := normalizeManageServiceAction(v); err != nil {
				t.Errorf("the block offers action %q, which dispatch refuses", v)
			}
		}
		if d, ok := spec.Default.(string); ok {
			if _, err := normalizeManageServiceAction(d); err != nil {
				t.Errorf("the block's default action %q is refused", d)
			}
		}
	}
}

type fullImageService struct{ captureRecordService }

func (f *fullImageService) CreateCaptureImage(context.Context, uuid.UUID, string, bool, uuid.UUID, string) (*image.Image, error) {
	return nil, fmt.Errorf("%w (max 1)", app_errors.ErrImageLimitReached)
}

// A capture the account has no room for is a refusal of the account's state,
// with the image service's own wording, not an internal error.
func TestACaptureOverTheImageCapIsAStateRefusal(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	s.imageService = &fullImageService{}
	netbooted(n)

	_, err := dispatch(t, s, n, node_command.SlugCaptureImage, map[string]interface{}{"title": "golden", "target_device": "/dev/nvme0n1"})
	var limit *CaptureImageLimitError
	if !errors.As(err, &limit) || limit.HTTPStatus() != 409 || !strings.Contains(err.Error(), "max 1") {
		t.Fatalf("err = %v, want a 409 CaptureImageLimitError carrying the limit", err)
	}
}
