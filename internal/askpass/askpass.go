// Package askpass implements the GIT_ASKPASS credential-helper mode shared by
// every SkillManage entrypoint (the CLI daemon AND the desktop app). gitsync
// wires the running executable as git's GIT_ASKPASS (SetAskpass) so a fetch or
// push needing HTTPS credentials reads them from the stored per-host file. Each
// binary that can be that executable MUST call Run() at the very top of main()
// when Active() is true — before any flag parsing, lock acquisition, or window
// init — otherwise git invokes a binary that tries to boot instead of printing
// the credential, and auth silently fails (the desktop app's original bug).
package askpass

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"skillmanage/internal/config"
)

// Active reports whether this process was launched by git as its askpass helper.
func Active() bool { return os.Getenv("SKILLMANAGE_ASKPASS") != "" }

// Run answers git's credential prompt from the stored per-host credentials. git
// calls it with one arg, e.g. "Username for 'https://host': " or "Password for
// 'https://user@host': ". It prints the username or token for that host; an
// unknown host (or missing central dir) prints nothing, so git fails exactly as
// it would with no credentials configured.
func Run() {
	prompt := ""
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}
	dir := os.Getenv("SKILLMANAGE_CENTRAL")
	if dir == "" {
		return
	}
	creds, err := config.LoadCredentials(dir)
	if err != nil {
		return
	}
	cred, ok := creds.Hosts[hostFromPrompt(prompt)]
	if !ok {
		return
	}
	switch {
	case strings.HasPrefix(strings.ToLower(prompt), "username"):
		fmt.Println(cred.Username)
	case strings.HasPrefix(strings.ToLower(prompt), "password"):
		fmt.Println(cred.Token)
	}
}

// hostFromPrompt extracts the host from a git askpass prompt by parsing the URL
// between single quotes (userinfo, if any, is dropped).
func hostFromPrompt(prompt string) string {
	i := strings.IndexByte(prompt, '\'')
	if i < 0 {
		return ""
	}
	rest := prompt[i+1:]
	j := strings.IndexByte(rest, '\'')
	if j < 0 {
		return ""
	}
	u, err := url.Parse(rest[:j])
	if err != nil {
		return ""
	}
	return u.Hostname()
}
