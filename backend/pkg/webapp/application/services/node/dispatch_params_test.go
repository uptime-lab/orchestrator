package node

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// Every refusal here happens BEFORE a command row exists. The defects they
// close all answered the request with success and failed — or did damage —
// afterwards.

func dispatch(t *testing.T, s *Service, n *node.Node, slug string, params map[string]interface{}) (*clampCapturingCommandService, error) {
	t.Helper()
	capture := s.nodeCommandService.(*clampCapturingCommandService)
	_, _, err := s.sendCommandToNode(context.Background(), n.ID, slug, params,
		payloadBoundAuth{accountID: n.AccountID}, nil, nil, "")
	return capture, err
}

func isOperatorRefusal(err error) bool {
	var refusal *app_errors.OperatorRefusalError
	return errors.As(err, &refusal)
}

// --- the node's state -------------------------------------------------------

// An imperative command for an offline node is an operator error with a
// stable code, not "failed to send command" with a 500.
func TestAnOfflineNodeRefusesANonQueueableCommandAsUnavailable(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	n.Status = node.StatusOffline

	_, err := dispatch(t, s, n, node_command.SlugReboot, nil)
	var unavailable *NodeUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want a NodeUnavailableError", err)
	}
	if unavailable.HTTPStatus() != 409 || unavailable.ErrorCode() != "node_unavailable" {
		t.Errorf("answered %d/%s, want 409/node_unavailable", unavailable.HTTPStatus(), unavailable.ErrorCode())
	}
	if !strings.Contains(err.Error(), "offline") {
		t.Errorf("the refusal does not name the node's status: %s", err)
	}
}

func TestAnUnapprovedNodeIsRefusedWithItsOwnCode(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	n.ApprovalStatus = node.ApprovalStatusPendingApproval

	_, err := dispatch(t, s, n, node_command.SlugReboot, nil)
	var unavailable *NodeUnavailableError
	if !errors.As(err, &unavailable) || unavailable.ErrorCode() != "node_not_approved" {
		t.Fatalf("err = %v, want a node_not_approved refusal", err)
	}
}

// --- install_os image -------------------------------------------------------

type installImageService struct {
	services.ImageService
	img *image.Image
	err error
}

func (f *installImageService) GetByID(context.Context, image_get.ImageGetQuery) (*image.Image, error) {
	return f.img, f.err
}

func readyImage() *image.Image {
	return &image.Image{
		ID:              uuid.New(),
		Title:           "ubuntu",
		FileStorageInfo: &image.FileStorageInfo{FilePath: "images/ubuntu.img.xz"},
		Checksum:        "abc",
	}
}

func installParams(imageID string) map[string]interface{} {
	return map[string]interface{}{"image_id": imageID, "target_device": "/dev/nvme0n1", "wipe": true}
}

// The device wiped its disk before the download of a half-compressed image
// failed. A busy image is refused before anything is dispatched.
func TestInstallOSRefusesAnImageStillBeingProcessed(t *testing.T) {
	for _, status := range image.BusyStatuses() {
		t.Run(status, func(t *testing.T) {
			s, n := clampService(&clampCapturingCommandService{})
			netbooted(n) // overwriting a used disk is a recovery-system operation
			img := readyImage()
			img.CompressionStatus = status
			s.imageService = &installImageService{img: img}

			capture, err := dispatch(t, s, n, node_command.SlugInstallOS, installParams(img.ID.String()))
			if !isOperatorRefusal(err) {
				t.Fatalf("err = %v, want an operator refusal", err)
			}
			if capture.slug != "" {
				t.Error("a command was registered for an image that cannot be installed")
			}
		})
	}
}

func TestInstallOSRefusesAnImageThatDoesNotResolveInTheAccount(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	netbooted(n) // overwriting a used disk is a recovery-system operation
	s.imageService = &installImageService{err: app_errors.ErrImageNotFound}

	_, err := dispatch(t, s, n, node_command.SlugInstallOS, installParams(uuid.New().String()))
	if !isOperatorRefusal(err) || !errors.Is(err, app_errors.ErrImageNotFound) {
		t.Fatalf("err = %v, want an image-not-found refusal", err)
	}
}

func TestInstallOSRefusesAMalformedImageID(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	netbooted(n) // overwriting a used disk is a recovery-system operation
	s.imageService = &installImageService{img: readyImage()}

	_, err := dispatch(t, s, n, node_command.SlugInstallOS, installParams("not-a-uuid"))
	if !isOperatorRefusal(err) {
		t.Fatalf("err = %v, want an operator refusal", err)
	}
}

func TestInstallOSRefusesAnImageWithNoFile(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	netbooted(n) // overwriting a used disk is a recovery-system operation
	img := readyImage()
	img.FileStorageInfo = nil
	s.imageService = &installImageService{img: img}

	_, err := dispatch(t, s, n, node_command.SlugInstallOS, installParams(img.ID.String()))
	if !isOperatorRefusal(err) {
		t.Fatalf("err = %v, want an operator refusal", err)
	}
}

// A lookup that fails for any reason other than "not found" defers to the
// proto build, which checks again: a database blip must not make the fleet
// un-installable.
func TestInstallOSDefersOnALookupFailure(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)
	netbooted(n) // overwriting a used disk is a recovery-system operation
	s.imageService = &installImageService{err: errors.New("connection reset")}

	_, err := dispatch(t, s, n, node_command.SlugInstallOS, installParams(uuid.New().String()))
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("err = %v, want the dispatch to reach registration", err)
	}
}

func TestInstallOSAcceptsAReadyImage(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)
	netbooted(n) // overwriting a used disk is a recovery-system operation
	img := readyImage()
	img.CompressionStatus = image.CompressionFailed // the original file is still installable
	s.imageService = &installImageService{img: img}

	_, err := dispatch(t, s, n, node_command.SlugInstallOS, installParams(img.ID.String()))
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("err = %v, want the dispatch to reach registration", err)
	}
}

// --- download_file headers --------------------------------------------------

// An object is the natural API shape — the device receives a map — and it was
// silently dropped while the download reported success without the header.
func TestDownloadFileHeadersAsAnObjectAreCarried(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)

	_, err := dispatch(t, s, n, node_command.SlugDownloadFile, map[string]interface{}{
		"url": "https://example.com/f", "dest_path": "/tmp/f",
		"headers": map[string]interface{}{"X-Api-Key": "k", "Authorization": "Bearer t"},
	})
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("err = %v, want the dispatch to reach registration", err)
	}
	if got := capture.payload["headers"]; got != "Authorization: Bearer t\nX-Api-Key: k" {
		t.Errorf("headers = %q, want both headers in the carried text form", got)
	}
}

func TestDownloadFileHeadersOfTheWrongShapeAreRefused(t *testing.T) {
	for name, headers := range map[string]interface{}{
		"a number":               float64(1),
		"an array":               []interface{}{"X-Api-Key: k"},
		"an object of non-text":  map[string]interface{}{"X-Retry": float64(3)},
		"a line with no colon":   "X-Api-Key k",
		"a value with a newline": map[string]interface{}{"X-A": "a\nX-B: b"},
	} {
		t.Run(name, func(t *testing.T) {
			s, n := clampService(&clampCapturingCommandService{})
			_, err := dispatch(t, s, n, node_command.SlugDownloadFile, map[string]interface{}{
				"url": "https://example.com/f", "dest_path": "/tmp/f", "headers": headers,
			})
			if !isOperatorRefusal(err) {
				t.Fatalf("err = %v, want an operator refusal", err)
			}
			// A header value can be a credential; the refusal never quotes it.
			if strings.Contains(err.Error(), "Bearer") || strings.Contains(err.Error(), "a\nX-B") {
				t.Errorf("the refusal quoted a header value: %s", err)
			}
		})
	}
}

// --- update_agent -------------------------------------------------------------

// An unvalidated version string reached the device's download URL and failed
// as a signature error that reads like a key-trust problem.
func TestUpdateAgentRefusesAVersionTheCDNCannotServe(t *testing.T) {
	for _, v := range []string{"../../0.8.1", "0.8", "1.2.3/arm64", "latest; rm"} {
		t.Run(v, func(t *testing.T) {
			s, n := clampService(&clampCapturingCommandService{})
			_, err := dispatch(t, s, n, node_command.SlugUpdateAgent, map[string]interface{}{"agent_version": v})
			if !isOperatorRefusal(err) {
				t.Fatalf("err = %v, want an operator refusal", err)
			}
		})
	}
}

func TestUpdateAgentAcceptsLatestAndReleaseVersions(t *testing.T) {
	for _, v := range []string{"", "latest", "1.2.3", "v1.2.3", "1.2.3-rc.1"} {
		t.Run(v, func(t *testing.T) {
			capture := &clampCapturingCommandService{}
			s, n := clampService(capture)
			_, err := dispatch(t, s, n, node_command.SlugUpdateAgent, map[string]interface{}{"agent_version": v})
			if !errors.Is(err, errStopAfterCapture) {
				t.Fatalf("err = %v, want the dispatch to reach registration", err)
			}
		})
	}
}

// allow_downgrade is the flag the agent's refusal tells the operator to set;
// a value of the wrong type used to read as false without a word.
func TestUpdateAgentRefusesANonBooleanAllowDowngrade(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	_, err := dispatch(t, s, n, node_command.SlugUpdateAgent, map[string]interface{}{
		"agent_version": "1.2.3", "allow_downgrade": "true",
	})
	if !isOperatorRefusal(err) || !strings.Contains(err.Error(), "allow_downgrade") {
		t.Fatalf("err = %v, want a refusal naming allow_downgrade", err)
	}
}

// Updating to the running version reinstalled the package and restarted the
// agent, abandoning every other command it was running.
func TestUpdateAgentToTheRunningVersionIsRefusedUnlessAReinstallIsAsked(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	n.AgentVersion = "0.8.1"

	_, err := dispatch(t, s, n, node_command.SlugUpdateAgent, map[string]interface{}{"agent_version": "v0.8.1"})
	var current *AgentAlreadyCurrentError
	if !errors.As(err, &current) {
		t.Fatalf("err = %v, want an AgentAlreadyCurrentError", err)
	}

	_, err = dispatch(t, s, n, node_command.SlugUpdateAgent, map[string]interface{}{"agent_version": "0.8.1", "reinstall": true})
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("an explicit reinstall was refused: %v", err)
	}

	// "latest" may well be what the node runs, but that is the CDN's answer,
	// not something to compare here.
	_, err = dispatch(t, s, n, node_command.SlugUpdateAgent, map[string]interface{}{"agent_version": "latest"})
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("update to latest was refused: %v", err)
	}
}

// A reinstall that is accepted must reach the device as one: the agent treats
// its running version as nothing to do, so a flag that only lifted the
// platform's refusal produced a green run that installed nothing. And it is a
// reinstall of the SAME version only — the wire's force flag also lifts the
// agent's anti-rollback check, so a reinstall naming another version must not
// become a downgrade nobody asked for.
func TestAReinstallIsCarriedToTheDeviceAndNeverRelaxesADowngrade(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	n.AgentVersion = "0.8.1"

	capture, err := dispatch(t, s, n, node_command.SlugUpdateAgent, map[string]interface{}{"agent_version": "0.8.1", "reinstall": true})
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("an explicit reinstall was refused: %v", err)
	}
	if got, _ := capture.payload["reinstall"].(bool); !got {
		t.Errorf("payload = %v, want reinstall carried to the send", capture.payload)
	}
	if got, _ := capture.payload["allow_downgrade"].(bool); got {
		t.Errorf("payload = %v: a reinstall must not be stored as a downgrade permission", capture.payload)
	}

	for _, version := range []string{"0.8.0", "0.9.0", "latest"} {
		capture.payload = nil
		_, err := dispatch(t, s, n, node_command.SlugUpdateAgent, map[string]interface{}{"agent_version": version, "reinstall": true})
		if !isOperatorRefusal(err) {
			t.Errorf("reinstall of %s on a node running 0.8.1: err = %v, want an operator refusal", version, err)
		}
		if capture.payload != nil {
			t.Errorf("reinstall of %s was registered: %v", version, capture.payload)
		}
	}
}

// A playbook block asking for the version a node already runs must not fail
// the block: "make sure the fleet runs X" is a reasonable playbook.
func TestUpdateAgentToTheRunningVersionFromAPlaybookIsNotRefused(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)
	n.AgentVersion = "0.8.1"
	block := uuid.New()

	_, _, err := s.sendCommandToNode(context.Background(), n.ID, node_command.SlugUpdateAgent,
		map[string]interface{}{"agent_version": "0.8.1"},
		payloadBoundAuth{accountID: n.AccountID}, &block, nil, "")
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("err = %v, want the dispatch to reach registration", err)
	}
}

// In a batch, a node already on the version is skipped, not failed.
func TestABatchUpdateSkipsANodeAlreadyOnTheVersion(t *testing.T) {
	s, n := clampService(&clampCapturingCommandService{})
	n.AgentVersion = "0.8.1"

	res, err := s.SendBatchCommand(context.Background(), batchCommand(n, node_command.SlugUpdateAgent, map[string]interface{}{"agent_version": "0.8.1"}))
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(res.Results) != 1 || !res.Results[0].Skipped || res.Results[0].SkipReason != "agent_already_current" {
		t.Fatalf("result = %+v, want one item skipped as agent_already_current", res.Results)
	}
}

func batchCommand(n *node.Node, slug string, params map[string]interface{}) node_command_cmd.BatchNodeCommandCommand {
	return node_command_cmd.BatchNodeCommandCommand{
		NodeIDs:     []uuid.UUID{n.ID},
		Command:     slug,
		Params:      params,
		AuthContext: commands.NewAuthContext(n.AccountID, uuid.New()),
	}
}

// install_os owns a disk, so its dispatch asks whether another command does.
func (c *clampCapturingCommandService) FindActiveDeviceCommand(context.Context, uuid.UUID) (*node_command.NodeCommand, error) {
	return nil, nil
}

// --- parameter shapes ---------------------------------------------------------

// A reboot with a delay of the wrong type rebooted at once: the builder's type
// assertion dropped the value and the command reported success.
func TestRebootWithANonNumericDelayIsRefused(t *testing.T) {
	for _, slug := range []string{node_command.SlugReboot, "restart", node_command.SlugShutdown} {
		t.Run(slug, func(t *testing.T) {
			s, n := clampService(&clampCapturingCommandService{})
			capture, err := dispatch(t, s, n, slug, map[string]interface{}{"delay_seconds": "abc"})
			if !isOperatorRefusal(err) || !strings.Contains(err.Error(), "delay_seconds") {
				t.Fatalf("err = %v, want a refusal naming delay_seconds", err)
			}
			if capture.slug != "" {
				t.Error("a command was registered with a parameter it would have dropped")
			}
		})
	}
}

func TestRebootWithANumericDelayCarriesIt(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)
	_, err := dispatch(t, s, n, node_command.SlugReboot, map[string]interface{}{"delay_seconds": float64(30)})
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("err = %v, want the dispatch to reach registration", err)
	}
	if got := capture.payload["delay_seconds"]; got != float64(30) {
		t.Errorf("delay_seconds = %v, want 30", got)
	}
}

// Every parameter the table knows is refused with the wrong shape, naming the
// parameter and never quoting the value, before a row exists.
func TestEveryKnownCommandParameterOfTheWrongShapeIsRefused(t *testing.T) {
	wrong := map[paramKind]interface{}{
		paramString:     float64(7),
		paramNumber:     "a-wrong-value",
		paramBool:       "a-wrong-value",
		paramStringList: float64(7),
	}
	for slug, kinds := range commandParamKinds {
		for name, kind := range kinds {
			t.Run(slug+"/"+name, func(t *testing.T) {
				s, n := clampService(&clampCapturingCommandService{})
				capture, err := dispatch(t, s, n, slug, map[string]interface{}{name: wrong[kind]})
				if !isOperatorRefusal(err) || !strings.Contains(err.Error(), name) {
					t.Fatalf("err = %v, want a refusal naming %s", err, name)
				}
				if strings.Contains(err.Error(), "a-wrong-value") {
					t.Errorf("the refusal quoted the value: %s", err)
				}
				if capture.slug != "" {
					t.Error("a command was registered")
				}
			})
		}
	}
}

// A null is "not given", as every builder already reads it.
func TestANullCommandParameterIsNotRefused(t *testing.T) {
	if err := validateCommandParamKinds(node_command.SlugReboot, map[string]interface{}{"delay_seconds": nil}); err != nil {
		t.Fatalf("a null delay was refused: %v", err)
	}
}

// Every slug the generic route authorizes is either in the shape table or
// takes no parameter at all, so a new slug cannot silently skip the check.
func TestEveryGenericCommandSlugHasItsParameterShapes(t *testing.T) {
	noParams := map[string]bool{
		node_command.SlugStatusCheck:       true,
		node_command.SlugEnableEEPROM:      true,
		node_command.SlugApplyAccessConfig: true,
	}
	for _, slug := range authz.MappedCommandSlugs() {
		resolved := node_command.ResolveSlug(slug)
		if _, ok := commandParamKinds[resolved]; !ok && !noParams[resolved] {
			t.Errorf("%s is dispatchable over the generic route but has no parameter shapes", slug)
		}
	}
}
