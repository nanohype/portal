package config

import (
	"os"
	"reflect"
	"strings"
	"sync"
)

// ownEnvKeys is every environment variable name Config reads, taken from the
// struct's own `env` tags.
//
// Derived rather than listed, because a list is a thing that goes stale. What
// this replaced was a hand-written denylist of six S3_* keys in the local
// executor: correct on the day it was written, and silently wrong from the
// first secret added to Config afterwards — which is how ENCRYPTION_KEY,
// JWT_SECRET and DATABASE_URL ended up in the environment of a shell script
// checked out of a user's repo. Reflection means adding a field to Config is
// what keeps it out of every child process, with no second place to remember.
var ownEnvKeys = sync.OnceValue(func() map[string]struct{} {
	keys := make(map[string]struct{})
	t := reflect.TypeOf(Config{})
	for i := range t.NumField() {
		if name := t.Field(i).Tag.Get("env"); name != "" {
			keys[name] = struct{}{}
		}
	}
	return keys
})

// IsOwnEnvKey reports whether name is an environment variable portal itself is
// configured by.
func IsOwnEnvKey(name string) bool {
	_, ok := ownEnvKeys()[name]
	return ok
}

// ChildEnviron returns the process environment with every variable portal is
// itself configured by removed.
//
// Everything portal shells out to runs code portal did not write. tofu executes
// provider plugins and provisioners out of the workspace's repo; `terragrunt
// render` evaluates that repo's own get_env() and run_cmd() calls; the `test`
// operation runs a smoke-test.sh straight out of it. All three read whatever
// environment they were started in.
//
// Portal's environment is its master keys. ENCRYPTION_KEY derives every
// workspace's state-encryption passphrase and decrypts every sensitive variable
// in every org; JWT_SECRET signs every session; DATABASE_URL is the database.
// A run needs none of it, so a run does not get it.
//
// What survives is what an operator deliberately put on the process for runs to
// use — the chart's extraEnv and extraEnvFrom, the pod's AWS credentials, PATH,
// HOME, proxy and CA settings. Subtracting a known set rather than allowing one
// is what keeps that extension point working, and deriving the known set from
// Config is what stops it falling behind.
//
// This bounds what a run can READ. It does not isolate the process: a local run
// still executes on the worker, with its filesystem and its network position.
// Isolation is what EXECUTOR_TYPE=kubernetes is for.
func ChildEnviron() []string {
	all := os.Environ()
	out := make([]string, 0, len(all))
	for _, kv := range all {
		name, _, ok := strings.Cut(kv, "=")
		if ok && IsOwnEnvKey(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
