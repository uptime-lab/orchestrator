package node_command

// AllSlugs is every command slug this package defines — the inventory the
// cross-package guards check their own hardcoded lists against.
//
// It is a hand-maintained list on purpose, and slugs_test.go fails the build
// when it drifts from the constants above it. A reflective inventory would
// silently absorb a new slug, which is exactly the moment somebody has to
// decide whether it is queueable, managed-OS-only, rebooting, device-exclusive
// and authorized — the five maps a new command has to be considered against.
func AllSlugs() []string {
	return []string{
		SlugReboot,
		SlugShutdown,
		SlugStatusCheck,
		SlugInstallOS,
		SlugInstallAgent,
		SlugUpdateBootOrder,
		SlugUpdateFile,
		SlugProvisionConnection,
		SlugEnableEEPROM,
		SlugEraseDrive,
		SlugUpdateAgent,
		SlugSetHostname,
		SlugPatchFile,
		SlugReadFile,
		SlugWaitForFile,
		SlugRunScript,
		SlugApplyAccessConfig,
		SlugApplyAccessConfigOffline,
		SlugPatchFileOffline,
		SlugCaptureImage,
		SlugInstallPackages,
		SlugManageService,
		SlugDownloadFile,
		SlugWriteFile,
		SlugDeleteFile,
		SlugApplyWatchdogConfig,
		SlugReleaseIdentity,
		SlugApplyDeviceProfile,
		SlugIdentify,
		SlugApplyUtilityConfig,
		SlugApplyLEDConfig,
	}
}

// commandNameAliases are alternative spellings the generic dispatch accepts on
// the way IN, each normalized to a real slug before anything is stored. They
// are not slugs: nothing is ever recorded under one.
//
// Only "restart" exists, and only because the dispatch switch has always taken
// it (services/node/service.go, the SlugReboot arm). It is listed here so the
// authorization map can carry an entry for it without the staleness guard
// reading that entry as a typo — and so the next alias is added in ONE place
// rather than in a switch arm nothing else knows about.
var commandNameAliases = map[string]string{
	"restart": SlugReboot,
}

// ResolveCommandName maps an accepted command name to its slug. A real slug
// resolves to itself.
func ResolveCommandName(name string) (string, bool) {
	if IsKnownSlug(name) {
		return name, true
	}
	slug, ok := commandNameAliases[name]
	return slug, ok
}

// IsKnownSlug reports whether s is a slug this package defines. Used by the
// cross-package guards to catch a typo or a rename in a hardcoded map key,
// where the failure is silent: a misspelled authorization key protects nothing
// and the real slug stays unmapped.
func IsKnownSlug(s string) bool {
	for _, known := range AllSlugs() {
		if known == s {
			return true
		}
	}
	return false
}
