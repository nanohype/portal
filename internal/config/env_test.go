package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Everything portal is configured by is kept out of a child process.
//
// Derived from the Config struct rather than a list, so a secret added to
// Config later is covered the day it lands. The defect this closes was exactly
// the other shape — a hand-written denylist of six keys that was right when it
// was written and silently wrong forever after.
func TestChildEnvironDropsEveryVariablePortalIsConfiguredBy(t *testing.T) {
	tags := envTagsOnConfig(t)

	const sentinel = "LEAKED-a41f9c2e"
	for _, name := range tags {
		t.Setenv(name, sentinel)
	}

	got := ChildEnviron()

	for _, kv := range got {
		name, value, _ := strings.Cut(kv, "=")
		if value == sentinel {
			t.Errorf("%s reached the child environment; it is one of portal's own", name)
		}
	}
}

// The named secrets, asserted by name.
//
// Belt over the derived check: if Config were ever restructured so the
// reflection finds nothing, that test would pass vacuously and this one would
// not. These five are the ones whose disclosure is a full compromise —
// ENCRYPTION_KEY derives every workspace's state-encryption passphrase and
// decrypts every sensitive variable in every org.
func TestTheMasterSecretsNeverReachAChild(t *testing.T) {
	secrets := []string{
		"ENCRYPTION_KEY",
		"JWT_SECRET",
		"DATABASE_URL",
		"GITHUB_CLIENT_SECRET",
		"WEBHOOK_SECRET",
		"S3_SECRET_KEY",
		"S3_ACCESS_KEY",
	}

	for _, name := range secrets {
		if !IsOwnEnvKey(name) {
			t.Errorf("%s is not recognised as portal's own, so nothing strips it", name)
		}
		t.Setenv(name, "LEAKED-"+name)
	}

	env := strings.Join(ChildEnviron(), "\n")
	for _, name := range secrets {
		if strings.Contains(env, "LEAKED-"+name) {
			t.Errorf("%s is in the environment handed to tofu, terragrunt render, and smoke-test.sh", name)
		}
	}
}

// What a run legitimately needs survives.
//
// The fix subtracts a known set rather than allowing one, precisely so the
// chart's extraEnv/extraEnvFrom and the pod's own credentials keep working. An
// allowlist that missed AWS_WEB_IDENTITY_TOKEN_FILE would break every IRSA
// deployment, and the failure would look like a provider auth error rather than
// anything pointing back here.
func TestChildEnvironKeepsWhatARunNeeds(t *testing.T) {
	keep := map[string]string{
		"PATH":                        "/usr/local/bin:/usr/bin",
		"HOME":                        "/home/portal",
		"AWS_REGION":                  "us-west-2",
		"AWS_ROLE_ARN":                "arn:aws:iam::111111111111:role/runs",
		"AWS_WEB_IDENTITY_TOKEN_FILE": "/var/run/secrets/eks.amazonaws.com/serviceaccount/token",
		"HTTPS_PROXY":                 "http://proxy.internal:3128",
		"SSL_CERT_FILE":               "/etc/ssl/certs/ca-bundle.crt",
		"TF_PLUGIN_CACHE_DIR":         "/var/cache/tofu",
		"OPERATOR_SUPPLIED_TOKEN":     "from-extraEnv",
	}
	for name, value := range keep {
		t.Setenv(name, value)
	}

	got := map[string]string{}
	for _, kv := range ChildEnviron() {
		name, value, _ := strings.Cut(kv, "=")
		got[name] = value
	}

	for name, want := range keep {
		if got[name] != want {
			t.Errorf("%s = %q, want %q — a run cannot work without it", name, got[name], want)
		}
	}
}

// Nothing outside this file may hand os.Environ() to a subprocess.
//
// This is the gate that outlives the fix. Stripping the environment in the two
// places that shell out today does nothing about the third one someone adds
// next year, and the failure is silent: the run works, and the secrets ride
// along. There were two sites when this was written and the second was found by
// grep, not by a test.
//
// If a new call site genuinely needs the raw environment, it has to say so
// here, in a test named after the reason it is safe.
func TestNothingOutsideThisFileHandsOsEnvironToASubprocess(t *testing.T) {
	root := filepath.Join("..", "..")

	var offenders []string
	var scanned int

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "web", "dist", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Base(path) == "env.go" && filepath.Base(filepath.Dir(path)) == "config" {
			return nil // the one place that is allowed to read it
		}

		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		if strings.Contains(string(src), "os.Environ()") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	// Guard against the walk finding nothing and the assertion passing on air.
	if scanned < 50 {
		t.Fatalf("only scanned %d Go files; the walk is not seeing the tree", scanned)
	}

	for _, path := range offenders {
		t.Errorf("%s reads os.Environ() directly — use config.ChildEnviron() so portal's own secrets do not reach the subprocess", path)
	}
}

// envTagsOnConfig lists the env tags declared on Config, and fails if there are
// implausibly few — which would mean the reflection is looking at the wrong
// thing and every derived assertion above is vacuous.
func envTagsOnConfig(t *testing.T) []string {
	t.Helper()

	var names []string
	tp := reflect.TypeOf(Config{})
	for i := range tp.NumField() {
		if name := tp.Field(i).Tag.Get("env"); name != "" {
			names = append(names, name)
		}
	}
	if len(names) < 20 {
		t.Fatalf("found %d env tags on Config; the reflection is not reading the struct", len(names))
	}
	return names
}
