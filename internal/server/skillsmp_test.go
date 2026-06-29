package server

import "testing"

func TestSkillsMpRepoRoot(t *testing.T) {
	cases := map[string]string{
		"https://github.com/jasonkneen/CopilotKit/tree/main/packages/react-core/skills/react-core": "https://github.com/jasonkneen/CopilotKit",
		"https://github.com/rowankid/ai-team":     "https://github.com/rowankid/ai-team",
		"https://github.com/owner/repo.git":       "https://github.com/owner/repo",
		"https://github.com/owner/repo/tree/main": "https://github.com/owner/repo",
		"https://gitlab.com/owner/repo":           "", // not github → not installable
		"https://example.com/x":                   "",
		"":                                        "",
	}
	for in, want := range cases {
		if got := skillsMpRepoRoot(in); got != want {
			t.Errorf("skillsMpRepoRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSkillsMp(t *testing.T) {
	// Real API envelope: {success, data:{skills:[...]}, meta}.
	body := []byte(`{"success":true,"data":{"skills":[
		{"name":"react-core","author":"jasonkneen","description":"d1","githubUrl":"https://github.com/jasonkneen/CopilotKit/tree/main/packages/react-core/skills/react-core","skillUrl":"https://skillsmp.com/x","stars":12},
		{"name":"weekly-report","author":"rowankid","description":"d2","githubUrl":"https://github.com/rowankid/ai-team","skillUrl":"https://skillsmp.com/y","stars":3},
		{"name":"no-repo","githubUrl":"https://gitlab.com/a/b","skillUrl":"z"},
		{"name":"","githubUrl":"https://github.com/a/b"}
	]}}`)
	got := parseSkillsMp(body)
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2 (non-github + empty-name dropped): %+v", len(got), got)
	}
	if got[0].Skill != "react-core" || got[0].RepoURL != "https://github.com/jasonkneen/CopilotKit" {
		t.Errorf("result[0] = %+v; want skill=react-core repoURL=.../CopilotKit (root)", got[0])
	}
	if got[1].Skill != "weekly-report" || got[1].RepoURL != "https://github.com/rowankid/ai-team" || got[1].Stars != 3 {
		t.Errorf("result[1] = %+v", got[1])
	}
}
