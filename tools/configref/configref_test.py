"""Tests for configref.py against a tiny fake config.go.

The fixture mirrors the shapes the real loader uses — `os.Getenv`,
`secretEnv`, the parse helpers that decide the type, the nested default
literal with constants and duration arithmetic, and the `Problems()` body —
so that a change in the real file that the parser cannot follow shows up here
as well as in `--check`.
"""

from __future__ import annotations

import io
import shutil
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import configref  # noqa: E402

FAKE_CONFIG_GO = '''package config

const (
\tinsecureDefaultJWTSecret = "default-secret-key-change-in-production"
\tDefaultUploads = 6
)

const minProductionSecretLength = 32

var weakSecretMarkers = []string{
\t"change-in-production",
\t"placeholder",
}

var insecureEnvironments = map[string]bool{
\t"development": true,
\t"test":        true,
}

type Config struct {
\tServer ServerConfig `yaml:"server"`
\tJWT    JWTConfig    `yaml:"jwt"`
\tImage  ImageConfig  `yaml:"image"`
\tQuota  QuotaConfig  `yaml:"quota"`
}

type ServerConfig struct {
\tPort int    `yaml:"port"`
\tHost string `yaml:"host"`
\t// Environment selects the mode.
\tEnvironment string `yaml:"environment"`
\tCORS        CORSConfig `yaml:"cors"`
\tMaxAccounts int `yaml:"max_accounts"`
}

type CORSConfig struct {
\tAllowedOrigins []string `yaml:"allowedOrigins"`
}

type JWTConfig struct {
\tSecretKey      string        `yaml:"secret_key"`
\tAccessTokenTTL time.Duration `yaml:"access_token_ttl"`
}

type ImageConfig struct {
\tMaxUploadSize        int64 `yaml:"max_upload_size"`
\tMaxConcurrentUploads int   `yaml:"max_concurrent_uploads"`
\tReplaceOriginal      bool  `yaml:"replace_original"`
}

type QuotaConfig struct {
\tAPIRequestsPerSecond float64 `yaml:"api_requests_per_second"`
}

func (c *Config) secretProblems() []string {
\tvar problems []string
\tif problem := weakSecretProblem("JWT_SECRET_KEY", c.JWT.SecretKey); problem != "" {
\t\tproblems = append(problems, problem)
\t}
\treturn problems
}

func (c *Config) Problems() []string {
\tproblems := c.secretProblems()
\tif c.Server.MaxAccounts < 0 {
\t\tproblems = append(problems, "SERVER_MAX_ACCOUNTS is negative")
\t}
\treturn problems
}

func getDefaultConfig() *Config {
\treturn &Config{
\t\tServer: ServerConfig{
\t\t\tPort: 8080,
\t\t\tHost: "0.0.0.0", // bind everywhere
\t\t\tEnvironment: "",
\t\t\tCORS: CORSConfig{
\t\t\t\tAllowedOrigins: []string{"*"},
\t\t\t},
\t\t},
\t\tJWT: JWTConfig{
\t\t\tSecretKey:      insecureDefaultJWTSecret,
\t\t\tAccessTokenTTL: 15 * time.Minute,
\t\t},
\t\tImage: ImageConfig{
\t\t\tMaxUploadSize:        16 << 30,
\t\t\tMaxConcurrentUploads: DefaultUploads,
\t\t},
\t\tQuota: QuotaConfig{
\t\t\tAPIRequestsPerSecond: 20,
\t\t},
\t}
}

func ApplyEnvironment(cfg *Config) (*Config, error) {
\tif env := os.Getenv("APP_ENV"); env != "" {
\t\tcfg.Server.Environment = env
\t}
\tif port := os.Getenv("SERVER_PORT"); port != "" {
\t\tif p, err := strconv.Atoi(port); err == nil {
\t\t\tcfg.Server.Port = p
\t\t}
\t}
\tif secretKey := secretEnv("JWT_SECRET_KEY"); secretKey != "" {
\t\tcfg.JWT.SecretKey = secretKey
\t}
\tif accessTTL := os.Getenv("JWT_ACCESS_TOKEN_TTL"); accessTTL != "" {
\t\tif duration, err := time.ParseDuration(accessTTL); err == nil {
\t\t\tcfg.JWT.AccessTokenTTL = duration
\t\t}
\t}
\tif v := os.Getenv("IMAGE_MAX_UPLOAD_SIZE"); v != "" {
\t\tif n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
\t\t\tcfg.Image.MaxUploadSize = n
\t\t}
\t}
\tif v := os.Getenv("IMAGE_MAX_CONCURRENT_UPLOADS"); v != "" {
\t\tif n, err := strconv.Atoi(v); err == nil && n >= 0 {
\t\t\tcfg.Image.MaxConcurrentUploads = n
\t\t}
\t}
\tif v := os.Getenv("IMAGE_COMPRESS_REPLACE_ORIGINAL"); v != "" {
\t\tcfg.Image.ReplaceOriginal = v == "true" || v == "1"
\t}
\tif origins := os.Getenv("CORS_ALLOWED_ORIGINS"); strings.TrimSpace(origins) != "" {
\t\tvar list []string
\t\tfor _, o := range strings.Split(origins, ",") {
\t\t\tlist = append(list, o)
\t\t}
\t\tcfg.Server.CORS.AllowedOrigins = list
\t}
\tif v := os.Getenv("API_RATE_LIMIT_PER_ACCOUNT_RPS"); v != "" {
\t\tif f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
\t\t\tcfg.Quota.APIRequestsPerSecond = f
\t\t}
\t}
\tif v := os.Getenv("SERVER_MAX_ACCOUNTS"); v != "" {
\t\tif n, err := strconv.Atoi(v); err == nil && n >= 0 {
\t\t\tcfg.Server.MaxAccounts = n
\t\t}
\t}
\treturn cfg, nil
}
'''

FAKE_DESCRIPTIONS = '''
preamble = "Preamble."
loading = "Loading."
secrets_note = "Secrets note."
production_intro = "Envs {insecure_envs}; min {min_secret_length}; markers {markers}."

[sections]
order = ["server", "auth", "images"]
[sections.server]
title = "Server"
[sections.auth]
title = "Auth"
[sections.images]
title = "Images"
intro = "Image intro."

[variables.APP_ENV]
section = "server"
description = "Run mode."
[variables.SERVER_PORT]
section = "server"
description = "Port."
[variables.SERVER_MAX_ACCOUNTS]
section = "auth"
description = "Account ceiling."
[variables.CORS_ALLOWED_ORIGINS]
section = "server"
description = "Origins."
[variables.LOG_LEVEL]
section = "server"
outside_loader = true
read_in = "cmd/server"
type = "string"
default = "(empty)"
description = "Log level."
[variables.JWT_SECRET_KEY]
section = "auth"
description = "Signing key."
[variables.JWT_ACCESS_TOKEN_TTL]
section = "auth"
description = "Access TTL."
[variables.IMAGE_MAX_UPLOAD_SIZE]
section = "images"
description = "Upload cap."
[variables.IMAGE_MAX_CONCURRENT_UPLOADS]
section = "images"
description = "Upload slots."
[variables.IMAGE_COMPRESS_REPLACE_ORIGINAL]
section = "images"
description = "Replace original."
[variables.API_RATE_LIMIT_PER_ACCOUNT_RPS]
section = "server"
description = "Rate."

[[production_checks]]
variables = ["JWT_SECRET_KEY"]
refuses = "weak key"
satisfy = "generate one"
[[production_checks]]
variables = ["SERVER_MAX_ACCOUNTS"]
refuses = "negative"
satisfy = "use zero or more"
'''


class ConfigRefTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="configref-"))
        self.repo = self.tmp / "repo"
        (self.repo / configref.CONFIG_GO).parent.mkdir(parents=True)
        (self.repo / configref.CONFIG_GO).write_text(FAKE_CONFIG_GO, encoding="utf-8")
        self.descriptions = self.tmp / "descriptions.toml"
        self.descriptions.write_text(FAKE_DESCRIPTIONS, encoding="utf-8")

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def run_main(self, *args: str) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = configref.main(["--repo", str(self.repo), "--descriptions", str(self.descriptions), *args])
        return code, out.getvalue(), err.getvalue()

    # --- parsing ---------------------------------------------------------------

    def test_parses_every_variable_with_type_secret_and_default(self) -> None:
        src = configref.load_source(self.repo)
        by_name = {v.name: v for v in configref.parse_variables(src)}
        self.assertEqual(len(by_name), 10)
        self.assertEqual(by_name["APP_ENV"].kind, "string")
        self.assertEqual(by_name["APP_ENV"].default, "(empty)")
        self.assertEqual(by_name["SERVER_PORT"].kind, "int")
        self.assertEqual(by_name["SERVER_PORT"].default, "`8080`")
        self.assertTrue(by_name["JWT_SECRET_KEY"].secret)
        self.assertFalse(by_name["SERVER_PORT"].secret)
        self.assertEqual(by_name["JWT_SECRET_KEY"].default, "`default-secret-key-change-in-production`")
        self.assertEqual(by_name["JWT_ACCESS_TOKEN_TTL"].kind, "duration")
        self.assertEqual(by_name["JWT_ACCESS_TOKEN_TTL"].default, "`15m`")
        self.assertEqual(by_name["IMAGE_MAX_UPLOAD_SIZE"].kind, "bytes")
        self.assertEqual(by_name["IMAGE_MAX_UPLOAD_SIZE"].default, "`17179869184 (16 GiB)`")
        self.assertEqual(by_name["IMAGE_MAX_CONCURRENT_UPLOADS"].default, "`6`")  # via the constant
        self.assertEqual(by_name["IMAGE_COMPRESS_REPLACE_ORIGINAL"].kind, "bool")
        self.assertEqual(by_name["IMAGE_COMPRESS_REPLACE_ORIGINAL"].default, "`false`")  # zero value
        self.assertEqual(by_name["CORS_ALLOWED_ORIGINS"].kind, "list")
        self.assertEqual(by_name["CORS_ALLOWED_ORIGINS"].default, "`*`")
        self.assertEqual(by_name["API_RATE_LIMIT_PER_ACCOUNT_RPS"].kind, "float")
        self.assertEqual(by_name["API_RATE_LIMIT_PER_ACCOUNT_RPS"].default, "`20`")
        self.assertEqual(by_name["SERVER_MAX_ACCOUNTS"].default, "`0`")  # not in the literal

    def test_production_facts_come_from_the_source(self) -> None:
        facts = configref.production_facts(configref.load_source(self.repo))
        self.assertEqual(facts["markers"], ["change-in-production", "placeholder"])
        self.assertEqual(facts["insecure_envs"], ["development", "test"])
        self.assertEqual(facts["min_secret_length"], 32)
        self.assertEqual(facts["weak_secret_names"], ["JWT_SECRET_KEY"])

    def test_render_default_formats(self) -> None:
        consts = {"X": "7 * 24 * time.Hour"}
        self.assertEqual(configref.render_default("X", "duration", "time.Duration", consts), "`168h`")
        self.assertEqual(configref.render_default("30 * time.Second", "duration", None, {}), "`30s`")
        self.assertEqual(configref.render_default("int64(32) << 30", "bytes", "int64", {}), "`34359738368 (32 GiB)`")
        self.assertEqual(configref.render_default("16 * 1024 * 1024", "bytes", "int64", {}), "`16777216 (16 MiB)`")
        self.assertEqual(configref.render_default(None, "list", None, {}), "(empty)")

    # --- the sidecar contract ---------------------------------------------------

    def test_missing_description_fails(self) -> None:
        text = self.descriptions.read_text(encoding="utf-8")
        self.descriptions.write_text(text.replace('[variables.SERVER_PORT]\nsection = "server"\ndescription = "Port."\n', ""),
                                     encoding="utf-8")
        code, _, err = self.run_main()
        self.assertEqual(code, 1)
        self.assertIn("no description for: SERVER_PORT", err)
        self.assertFalse((self.repo / configref.OUTPUT_MD).exists())

    def test_stale_description_fails(self) -> None:
        self.descriptions.write_text(self.descriptions.read_text(encoding="utf-8")
                                     + '\n[variables.GONE]\nsection = "server"\ndescription = "Removed."\n',
                                     encoding="utf-8")
        code, _, err = self.run_main()
        self.assertEqual(code, 1)
        self.assertIn("described but not read by ApplyEnvironment: GONE", err)

    def test_outside_loader_entry_that_the_loader_reads_fails(self) -> None:
        text = self.descriptions.read_text(encoding="utf-8")
        text = text.replace('[variables.APP_ENV]\nsection = "server"\n',
                            '[variables.APP_ENV]\nsection = "server"\noutside_loader = true\nread_in = "x"\ntype = "string"\ndefault = "(empty)"\n')
        self.descriptions.write_text(text, encoding="utf-8")
        code, _, err = self.run_main()
        self.assertEqual(code, 1)
        self.assertIn("marked outside_loader but ApplyEnvironment reads it", err)

    def test_weak_secret_check_without_a_production_entry_fails(self) -> None:
        text = self.descriptions.read_text(encoding="utf-8")
        self.descriptions.write_text(text.replace('variables = ["JWT_SECRET_KEY"]', 'variables = ["SERVER_PORT"]'),
                                     encoding="utf-8")
        code, _, err = self.run_main()
        self.assertEqual(code, 1)
        self.assertIn("Problems() checks JWT_SECRET_KEY for weakness", err)

    def test_production_check_naming_an_uninspected_variable_fails(self) -> None:
        text = self.descriptions.read_text(encoding="utf-8")
        self.descriptions.write_text(text.replace('variables = ["SERVER_MAX_ACCOUNTS"]', 'variables = ["APP_ENV"]'),
                                     encoding="utf-8")
        code, _, err = self.run_main()
        self.assertEqual(code, 1)
        self.assertIn("production check names APP_ENV", err)

    # --- generation and --check -------------------------------------------------

    def test_generates_the_page_and_check_passes(self) -> None:
        code, _, err = self.run_main()
        self.assertEqual(code, 0, err)
        page = (self.repo / configref.OUTPUT_MD).read_text(encoding="utf-8")
        self.assertIn("| `JWT_SECRET_KEY` | string, secret |", page)
        self.assertIn("| `LOG_LEVEL` | string | (empty) | Log level. Read in `cmd/server`", page)
        self.assertIn("### Images\n\nImage intro.", page)
        self.assertIn("Envs `development`, `test`; min 32; markers `change-in-production`, `placeholder`.", page)
        self.assertIn("| weak key | generate one |", page)
        # Only secretEnv reads are listed as secrets; an outside-loader entry is not.
        self.assertIn("The variables read this way: `JWT_SECRET_KEY`.", page)
        code, _, err = self.run_main("--check")
        self.assertEqual(code, 0, err)

    def test_check_fails_on_a_stale_page(self) -> None:
        self.run_main()
        target = self.repo / configref.OUTPUT_MD
        target.write_text(target.read_text(encoding="utf-8").replace("Port.", "Edited by hand."), encoding="utf-8")
        code, out, err = self.run_main("--check")
        self.assertEqual(code, 1)
        self.assertIn("is stale", err)
        self.assertIn("Edited by hand.", out)

    def test_check_fails_when_the_page_is_missing(self) -> None:
        code, _, err = self.run_main("--check")
        self.assertEqual(code, 1)
        self.assertIn("is stale", err)

    def test_a_new_loader_variable_without_a_description_breaks_check(self) -> None:
        self.run_main()
        go = self.repo / configref.CONFIG_GO
        go.write_text(go.read_text(encoding="utf-8").replace(
            "\treturn cfg, nil\n}",
            '\tif v := os.Getenv("NEW_KNOB"); v != "" {\n\t\tcfg.Server.Host = v\n\t}\n\treturn cfg, nil\n}'),
            encoding="utf-8")
        code, _, err = self.run_main("--check")
        self.assertEqual(code, 1)
        self.assertIn("no description for: NEW_KNOB", err)


if __name__ == "__main__":
    unittest.main()
