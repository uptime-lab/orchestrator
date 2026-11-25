package grpc

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// install_os must carry no env secrets, and the assertion is on the BUILDER.
//
// handleInstallOS on the agent reads image id, url, checksum, target device,
// reboot, wipe and strip_identity — and nothing else — so populating Env would
// send every environment secret's plaintext to the device for no functional
// reason, and whoever holds one node's adoption token would receive the
// account's secrets. The username/password/ssh_key fields stay empty for the
// same reason.
//
// Reading the source is deliberate. Driving buildProtoCommandFromNodeCommand
// would need an image store, a signed-URL minter and a policy resolver, and the
// property under test is one line in a struct literal — the same reasoning that
// makes TestNoRawHTMLSinksInUISource read source rather than render a page. A
// test that constructed the command itself, which was the first attempt here,
// asserts nothing at all: it proves the field can be left empty, not that the
// builder leaves it empty.
func TestTheInstallOSBuilderPopulatesNoCredentialFields(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading server.go: %v", err)
	}

	literal := installOSLiteral(t, string(source))
	for _, field := range []string{"Env:", "Username:", "Password:", "SshKey:"} {
		if strings.Contains(literal, field) {
			t.Errorf("the install_os builder populates %s — the agent reads none of these, so it is pure credential leak:\n%s",
				strings.TrimSuffix(field, ":"), literal)
		}
	}
	// And the fields it DOES need are still there, so a passing test cannot mean
	// "the literal moved and this stopped looking at anything".
	for _, field := range []string{"ImageId:", "ImageUrl:", "Checksum:", "TargetDevice:", "StripIdentity:"} {
		if !strings.Contains(literal, field) {
			t.Fatalf("the install_os literal has no %s; this test is no longer reading the builder:\n%s", field, literal)
		}
	}
}

// installOSLiteral extracts the proto.InstallOSCommand struct literal.
func installOSLiteral(t *testing.T, source string) string {
	t.Helper()
	start := regexp.MustCompile(`InstallOs: &proto\.InstallOSCommand\{`).FindStringIndex(source)
	if start == nil {
		t.Fatal("no install_os command literal found in server.go")
	}
	depth := 0
	for i := start[1] - 1; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[start[0] : i+1]
			}
		}
	}
	t.Fatal("the install_os literal is unterminated")
	return ""
}
