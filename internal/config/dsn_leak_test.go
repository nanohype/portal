package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A DATABASE_URL carries the database password. Every binary here logs to stdout,
// which a collector ships to Loki, so a password written once is searchable for the
// retention window — and the write happens at startup, so a crashlooping pod
// repeats it every restart.
//
// The property is about the value, not about any particular sentence: no output a
// binary produces while failing on its connection string may contain the password
// out of it. So this gives each binary a DSN whose password is a token that appears
// nowhere else, drives the real startup path to its failure, and reads the bytes it
// actually wrote. Reading the code that builds the error would pass on a message
// nobody emits and fail to see a message someone else does.
const canaryPassword = "pA55w0rd-CANARY-b7f19c2e"

// A DSN that parses as a URL well enough to carry a password and fails once a
// connection string parser looks at it. The bad percent escape is in the path, so
// the password is intact and net/url refuses the whole string.
const canaryDSN = "postgres://portal:" + canaryPassword + "@localhost:5432/por%zztal"

// The binaries that read DATABASE_URL and log what went wrong with it.
var dsnBinaries = []string{
	"github.com/nanohype/portal/cmd/server",
	"github.com/nanohype/portal/cmd/worker",
	"github.com/nanohype/portal/cmd/migrate",
}

func TestBinariesNeverLogTheDatabasePassword(t *testing.T) {
	// One build for all three: they share most of their dependency graph, and
	// building them separately pays for it three times.
	binDir := t.TempDir()
	build := exec.Command("go", append([]string{"build", "-o", binDir + string(os.PathSeparator)}, dsnBinaries...)...)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the binaries under test: %v\n%s", err, out)
	}

	for _, pkg := range dsnBinaries {
		t.Run(filepath.Base(pkg), func(t *testing.T) {
			bin := filepath.Join(binDir, filepath.Base(pkg))

			cmd := exec.Command(bin)
			cmd.Env = append(os.Environ(),
				"DATABASE_URL="+canaryDSN,
				// Relaxed validation, so the run reaches the connection string
				// rather than stopping at a config rule before it.
				"ENVIRONMENT=development",
			)
			out, err := cmd.CombinedOutput()

			if err == nil {
				t.Fatalf("%s started on an unparseable DATABASE_URL; the fixture never reached the failure it is about", pkg)
			}
			if len(strings.TrimSpace(string(out))) == 0 {
				t.Fatalf("%s wrote nothing, so this asserts on an empty string rather than on a log", pkg)
			}
			if strings.Contains(string(out), canaryPassword) {
				t.Errorf("%s wrote the database password to its log:\n%s", pkg, out)
			}
		})
	}
}
