package node_command

import "testing"

func TestRedactURLCredentialsInDetails(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// What an older agent (anything before this ships) still reports.
			name: "agent transport error keeps the target but drops the query",
			in:   `download failed: Get "https://files.example.test/pkg.tar?token=s3cr3t": dial tcp: i/o timeout`,
			want: `download failed: Get "https://files.example.test/pkg.tar?[redacted]": dial tcp: i/o timeout`,
		},
		{
			name: "the platform's own capability URL is a bearer credential",
			in:   "install_os failed: https://app.example.test/cdn/img.xz?exp=1&sig=deadbeef",
			want: "install_os failed: https://app.example.test/cdn/img.xz?[redacted]",
		},
		{
			name: "userinfo is dropped even with no query",
			in:   "failed for https://user:hunter2@files.example.test/pkg.tar",
			want: "failed for https://files.example.test/pkg.tar",
		},
		{
			name: "a plain URL survives, so triage still has a target",
			in:   "download failed: https://files.example.test/pkg.tar (404)",
			want: "download failed: https://files.example.test/pkg.tar (404)",
		},
		{
			name: "ordinary details are untouched",
			in:   "the node is offline",
			want: "the node is offline",
		},
	}
	for _, tc := range cases {
		if got := RedactURLCredentialsInDetails(tc.in); got != tc.want {
			t.Errorf("%s:\n RedactURLCredentialsInDetails(%q)\n            = %q\n       want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
