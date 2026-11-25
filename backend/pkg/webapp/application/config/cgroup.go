package config

import (
	"os"
	"strconv"
	"strings"
)

// CgroupMemoryLimitMB reports the memory limit of the cgroup this process
// runs in, in MiB, or 0 when there is none or it cannot be read (no cgroup,
// "max", a non-Linux host). Read from the unified hierarchy first, then the
// v1 file. It exists so the compression memory budget can be checked against
// the limit the container ACTUALLY has at boot, rather than against a number
// somebody remembered from a compose file.
func CgroupMemoryLimitMB() int {
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(raw))
		if v == "" || v == "max" {
			return 0
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return 0
		}
		// v1 reports "no limit" as a huge number, not "max".
		if n >= 1<<60 {
			return 0
		}
		return int(n / (1024 * 1024))
	}
	return 0
}
