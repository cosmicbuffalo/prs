package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ghHost is the only host account resolution handles. gh keys stored
// accounts by host, and GH_TOKEN only applies to github.com (GHE uses
// GH_ENTERPRISE_TOKEN), so enterprise remotes fall back to gh's own default
// account.
const ghHost = "github.com"

// activeToken is the token of the gh account resolved for the current repo
// (see ResolveAccount). When set, runGH passes it to every gh call as
// GH_TOKEN, so each call runs as that account without touching gh's global
// "active account" (which `gh auth switch` would change for every other
// terminal too). Empty means "let gh use its default".
var (
	activeTokenMu sync.RWMutex
	activeToken   string
)

func setActiveToken(token string) {
	activeTokenMu.Lock()
	activeToken = token
	activeTokenMu.Unlock()
}

func getActiveToken() string {
	activeTokenMu.RLock()
	defer activeTokenMu.RUnlock()
	return activeToken
}

// accountPins maps an "owner" or "owner/repo" to the gh login to use for it,
// from the config's "accounts" section. Set once at startup by Config.apply.
var accountPins map[string]string

// Account sources: where ResolvedAccount.Login came from.
const (
	accountSourceEnv     = "env"     // GH_TOKEN/GITHUB_TOKEN already set by the user
	accountSourceFlag    = "flag"    // --account
	accountSourceConfig  = "config"  // accounts pin in config.json
	accountSourceOwner   = "owner"   // logged-in account whose login is the repo owner
	accountSourceCommits = "commits" // one of several readable accounts, picked for having commits in the repo
	accountSourceProbed  = "probed"  // cached, the only account that could read the repo, or the first in order
	accountSourceDefault = "default" // nothing to choose between; gh's own default
)

// ResolvedAccount is the outcome of ResolveAccount.
type ResolvedAccount struct {
	// Login is the gh account whose token is used, or "" when gh's own
	// default applies (env token, single account, unknown host).
	Login string
	// Token is that account's token, or "" to let gh pick.
	Token string
	// Repo is the canonical owner/name GitHub reported while probing (fixes
	// case and follows renames), or the input repo when no probe ran.
	Repo   string
	Source string
}

// ghAccount is one logged-in account from `gh auth status --json hosts`.
type ghAccount struct {
	Login  string `json:"login"`
	Active bool   `json:"active"`
	State  string `json:"state"`
}

// listGHAccounts returns the github.com accounts gh is logged in to.
func listGHAccounts(ctx context.Context) ([]ghAccount, error) {
	out, err := runGHAs(ctx, "", "auth", "status", "--json", "hosts")
	if err != nil {
		return nil, fmt.Errorf("listing gh accounts: %w", err)
	}
	var resp struct {
		Hosts map[string][]ghAccount `json:"hosts"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parsing gh auth status: %w", err)
	}
	var accounts []ghAccount
	for _, a := range resp.Hosts[ghHost] {
		if a.Login != "" && a.State == "success" {
			accounts = append(accounts, a)
		}
	}
	return accounts, nil
}

// ghTokenFor returns the stored token for one of gh's logged-in accounts.
func ghTokenFor(ctx context.Context, login string) (string, error) {
	out, err := runGHAs(ctx, "", "auth", "token", "--hostname", ghHost, "--user", login)
	if err != nil {
		return "", fmt.Errorf("reading gh token for %s: %w", login, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// probeRepo checks whether token can read repo, returning GitHub's canonical
// owner/name for it.
func probeRepo(ctx context.Context, token, repo string) (string, error) {
	out, err := runGHAs(ctx, token, "api", "repos/"+repo, "-q", ".full_name")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// hasCommits reports whether login has authored any commits in repo, read with
// token. Best-effort: any error (e.g. an empty repo's 409) counts as no.
func hasCommits(ctx context.Context, token, repo, login string) bool {
	out, err := runGHAs(ctx, token, "api", "repos/"+repo+"/commits?per_page=1&author="+login, "-q", "length")
	return err == nil && strings.TrimSpace(string(out)) != "0"
}

// accountResolver bundles the gh interactions ResolveAccount needs, so tests
// can substitute fakes for the real gh CLI.
type accountResolver struct {
	list  func(ctx context.Context) ([]ghAccount, error)
	token func(ctx context.Context, login string) (string, error)
	probe func(ctx context.Context, token, repo string) (string, error)
	// commits reports whether login has authored commits in repo.
	commits func(ctx context.Context, token, repo, login string) bool
	cached  func(repo string) string
	save    func(repo, login string)
	pins    map[string]string
	envSet  bool
}

func defaultAccountResolver() accountResolver {
	return accountResolver{
		list:    listGHAccounts,
		token:   ghTokenFor,
		probe:   probeRepo,
		commits: hasCommits,
		cached:  loadCachedAccount,
		save:    saveCachedAccount,
		pins:    accountPins,
		envSet:  os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "",
	}
}

// ResolveAccount picks which logged-in gh account to use for repo, in order:
//
//  1. GH_TOKEN/GITHUB_TOKEN already in the environment — the user chose
//     explicitly, and gh ignores stored accounts anyway.
//
//  2. --account (flagLogin).
//
//  3. A config.json "accounts" pin for owner/repo, then for owner.
//
//  4. Probe the logged-in accounts for one that can read the repo:
//     a. the account whose login is the repo owner, if it can read it;
//     b. the account remembered from the last probe, if it still can;
//     c. otherwise every account is probed. A sole reader wins; among
//     several, the first (active account first) with commits in the repo
//     wins, else the first. The winner is remembered for next time.
//
//     Being able to read a repo doesn't make an account the right one — any
//     account can read a public repo — hence the owner and commit checks.
//     The owner check comes before the cache so it can't be shadowed by an
//     entry saved before it existed.
//
// Pinned accounts (2, 3) are still probed, but never fallen back from: the
// user asked for that identity, so switching behind their back would be
// wrong. The probe exists because GitHub's search API returns zero results
// rather than an error for a repo the token can't see, which would otherwise
// show up as silently empty tabs.
func ResolveAccount(ctx context.Context, repo, flagLogin string) (ResolvedAccount, error) {
	return defaultAccountResolver().resolve(ctx, repo, flagLogin)
}

func (r accountResolver) resolve(ctx context.Context, repo, flagLogin string) (ResolvedAccount, error) {
	if r.envSet {
		if flagLogin != "" {
			return ResolvedAccount{}, fmt.Errorf("--account %s conflicts with GH_TOKEN/GITHUB_TOKEN set in the environment", flagLogin)
		}
		return ResolvedAccount{Repo: repo, Source: accountSourceEnv}, nil
	}

	pinned, source := flagLogin, accountSourceFlag
	if pinned == "" {
		pinned, source = matchAccountPin(r.pins, repo), accountSourceConfig
	}

	accounts, err := r.list(ctx)
	if err != nil {
		if pinned != "" {
			return ResolvedAccount{}, err
		}
		// Can't enumerate accounts (old gh without --json, odd setup): carry
		// on with gh's default, exactly as before account resolution existed.
		return ResolvedAccount{Repo: repo, Source: accountSourceDefault}, nil
	}

	if pinned != "" {
		if !hasAccount(accounts, pinned) {
			return ResolvedAccount{}, fmt.Errorf("gh account %q (from %s) is not logged in; run `gh auth login` or check `gh auth status`", pinned, source)
		}
		token, err := r.token(ctx, pinned)
		if err != nil {
			return ResolvedAccount{}, err
		}
		canonical, err := r.probe(ctx, token, repo)
		if err != nil {
			return ResolvedAccount{}, fmt.Errorf("gh account %s (from %s) can't access %s: %w", pinned, source, repo, err)
		}
		if canonical == "" {
			canonical = repo
		}
		return ResolvedAccount{Login: pinned, Token: token, Repo: canonical, Source: source}, nil
	}

	if len(accounts) == 0 {
		return ResolvedAccount{Repo: repo, Source: accountSourceDefault}, nil
	}

	// tryAccount probes one login, recording failures for the final error.
	var tried []string
	var lastErr error
	probed := map[string]bool{}
	tryAccount := func(login string) (ResolvedAccount, bool) {
		probed[strings.ToLower(login)] = true
		token, err := r.token(ctx, login)
		if err == nil {
			var canonical string
			if canonical, err = r.probe(ctx, token, repo); err == nil {
				if canonical == "" {
					canonical = repo
				}
				return ResolvedAccount{Login: login, Token: token, Repo: canonical, Source: accountSourceProbed}, true
			}
		}
		lastErr = err
		tried = append(tried, login)
		return ResolvedAccount{}, false
	}

	owner, _, _ := strings.Cut(repo, "/")
	for _, a := range accounts {
		if strings.EqualFold(a.Login, owner) {
			if acct, ok := tryAccount(a.Login); ok {
				acct.Source = accountSourceOwner
				return acct, nil
			}
		}
	}

	cached := r.cached(repo)
	if hasAccount(accounts, cached) && !probed[strings.ToLower(cached)] {
		if acct, ok := tryAccount(cached); ok {
			return acct, nil
		}
	}

	var readable []ResolvedAccount
	for _, login := range candidateOrder(accounts) {
		if probed[strings.ToLower(login)] {
			continue
		}
		if acct, ok := tryAccount(login); ok {
			readable = append(readable, acct)
		}
	}
	if len(readable) == 0 {
		return ResolvedAccount{}, fmt.Errorf("none of your gh accounts (%s) can access %s — log in with one that can (`gh auth login`), or pin one under \"accounts\" in config.json: %w",
			strings.Join(tried, ", "), repo, lastErr)
	}
	chosen := readable[0]
	if len(readable) > 1 {
		for _, acct := range readable {
			if r.commits(ctx, acct.Token, acct.Repo, acct.Login) {
				chosen = acct
				chosen.Source = accountSourceCommits
				break
			}
		}
	}
	r.save(chosen.Repo, chosen.Login)
	return chosen, nil
}

// candidateOrder returns logins to probe: the active account, then the others
// in gh's order.
func candidateOrder(accounts []ghAccount) []string {
	var order []string
	seen := map[string]bool{}
	add := func(login string) {
		if login != "" && !seen[login] {
			seen[login] = true
			order = append(order, login)
		}
	}
	for _, a := range accounts {
		if a.Active {
			add(a.Login)
		}
	}
	for _, a := range accounts {
		add(a.Login)
	}
	return order
}

func hasAccount(accounts []ghAccount, login string) bool {
	for _, a := range accounts {
		if strings.EqualFold(a.Login, login) {
			return true
		}
	}
	return false
}

// matchAccountPin returns the pinned login for repo: an exact "owner/repo"
// entry wins over an "owner" entry. Keys match case-insensitively, as GitHub
// names do.
func matchAccountPin(pins map[string]string, repo string) string {
	owner, _, _ := strings.Cut(repo, "/")
	var ownerMatch string
	for key, login := range pins {
		switch {
		case strings.EqualFold(key, repo):
			return login
		case strings.EqualFold(key, owner):
			ownerMatch = login
		}
	}
	return ownerMatch
}

// accountCacheFile is the on-disk shape of accounts.json: repo → the gh login
// that last succeeded probing it. Kept apart from state.json because that
// file is rewritten wholesale by Store and is scoped per viewed user.
type accountCacheFile struct {
	Repos map[string]string `json:"repos"`
}

func accountCachePath() (string, error) {
	dir, err := stateBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "accounts.json"), nil
}

func readAccountCache() accountCacheFile {
	cf := accountCacheFile{Repos: map[string]string{}}
	path, err := accountCachePath()
	if err != nil {
		return cf
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cf
	}
	if json.Unmarshal(data, &cf) != nil || cf.Repos == nil {
		cf.Repos = map[string]string{}
	}
	return cf
}

// loadCachedAccount returns the login that last worked for repo, or "".
func loadCachedAccount(repo string) string {
	return readAccountCache().Repos[strings.ToLower(repo)]
}

// saveCachedAccount remembers login as working for repo. Best-effort: a write
// failure only costs an extra probe next launch.
func saveCachedAccount(repo, login string) {
	cf := readAccountCache()
	key := strings.ToLower(repo)
	if cf.Repos[key] == login {
		return
	}
	cf.Repos[key] = login
	path, err := accountCachePath()
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// repoFromGitRemote reads owner/name from the current repo's git remotes
// without any GitHub API call, so it works before an account is chosen (the
// old `gh repo view` needed one, and failed outright when gh's active
// account couldn't see the repo). It picks the remote the way gh does: a
// `gh repo set-default` choice first, then upstream, github, origin, then
// whatever remains.
func repoFromGitRemote(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "remote", "-v").Output()
	if err != nil {
		return "", fmt.Errorf("listing git remotes: %w", err)
	}
	urls := map[string]string{}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if _, ok := urls[fields[0]]; !ok {
			names = append(names, fields[0])
			urls[fields[0]] = fields[1]
		}
	}

	// gh repo set-default stores remote.<name>.gh-resolved = "base" or an
	// explicit owner/repo. Absent config makes git exit 1 — not an error.
	resolved, _ := exec.CommandContext(ctx, "git", "config", "--get-regexp", `^remote\..*\.gh-resolved$`).Output()
	return pickRepoFromRemotes(names, urls, string(resolved))
}

// pickRepoFromRemotes is the pure part of repoFromGitRemote. names keeps git's
// order; resolved is raw `git config --get-regexp` output for gh-resolved.
func pickRepoFromRemotes(names []string, urls map[string]string, resolved string) (string, error) {
	for _, line := range strings.Split(strings.TrimSpace(resolved), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		remote := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".gh-resolved")
		if value != "base" && strings.Count(value, "/") == 1 {
			return value, nil
		}
		if repo, ok := parseRemoteURL(urls[remote]); ok {
			return repo, nil
		}
	}

	ordered := []string{"upstream", "github", "origin"}
	for _, n := range names {
		if n != "upstream" && n != "github" && n != "origin" {
			ordered = append(ordered, n)
		}
	}
	for _, n := range ordered {
		if repo, ok := parseRemoteURL(urls[n]); ok {
			return repo, nil
		}
	}
	return "", fmt.Errorf("no git remote with an owner/repo path found")
}

// parseRemoteURL extracts owner/name from a git remote URL: scp-style
// (git@host:owner/name.git), ssh://, https://, or git://. The host isn't
// checked, so SSH host aliases (git@github-work:owner/name) work too; a
// wrong guess just fails the later access probe with a clear error.
func parseRemoteURL(url string) (string, bool) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", false
	}
	var path string
	if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", false
		}
		path = rest[slash+1:]
	} else if _, after, ok := strings.Cut(url, ":"); ok {
		path = after
	} else {
		return "", false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", false
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" {
		return "", false
	}
	return owner + "/" + name, true
}
