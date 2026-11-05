package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeResolver builds an accountResolver over in-memory accounts. readable
// lists the logins that can read the repo and committers those with commits in
// it; probes and commitChecks record every login probed / checked, in order,
// and saved records the cache write (if any).
type fakeResolver struct {
	accounts   []ghAccount
	readable   map[string]bool
	committers map[string]bool
	cached     string
	pins       map[string]string
	envSet     bool
	listErr    error

	probes       []string
	commitChecks []string
	saved        string
}

func (f *fakeResolver) resolver() accountResolver {
	return accountResolver{
		list: func(context.Context) ([]ghAccount, error) { return f.accounts, f.listErr },
		token: func(_ context.Context, login string) (string, error) {
			return "tok-" + login, nil
		},
		probe: func(_ context.Context, token, repo string) (string, error) {
			login := strings.TrimPrefix(token, "tok-")
			f.probes = append(f.probes, login)
			if !f.readable[login] {
				return "", errors.New("GraphQL: Could not resolve to a Repository")
			}
			return repo, nil
		},
		commits: func(_ context.Context, token, _, login string) bool {
			if token != "tok-"+login {
				panic("commit check for " + login + " used another account's token")
			}
			f.commitChecks = append(f.commitChecks, login)
			return f.committers[login]
		},
		cached: func(string) string { return f.cached },
		save:   func(_, login string) { f.saved = login },
		pins:   f.pins,
		envSet: f.envSet,
	}
}

var twoAccounts = []ghAccount{
	{Login: "personal", Active: true, State: "success"},
	{Login: "work", State: "success"},
}

func TestResolveAccountFallsBackToAccountThatCanReadRepo(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"work": true}}
	got, err := f.resolver().resolve(context.Background(), "Org/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "work" || got.Token != "tok-work" || got.Source != accountSourceProbed {
		t.Errorf("got %+v, want work via probe", got)
	}
	if want := []string{"personal", "work"}; !reflect.DeepEqual(f.probes, want) {
		t.Errorf("probe order = %v, want active account first: %v", f.probes, want)
	}
	if f.saved != "work" {
		t.Errorf("saved = %q, want the winning account cached", f.saved)
	}
}

func TestResolveAccountPrefersActiveWhenBothCanRead(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"personal": true, "work": true}}
	got, err := f.resolver().resolve(context.Background(), "Org/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "personal" || got.Source != accountSourceProbed {
		t.Errorf("got %+v, want personal (active) when neither has commits", got)
	}
	if want := []string{"personal", "work"}; !reflect.DeepEqual(f.commitChecks, want) {
		t.Errorf("commit checks = %v, want both readers checked: %v", f.commitChecks, want)
	}
}

func TestResolveAccountPrefersOwner(t *testing.T) {
	// Any account can read a public repo; the owner's account is the right one.
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"personal": true, "work": true}}
	got, err := f.resolver().resolve(context.Background(), "Work/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "work" || got.Source != accountSourceOwner {
		t.Errorf("got %+v, want work as the repo owner", got)
	}
	if !reflect.DeepEqual(f.probes, []string{"work"}) || len(f.commitChecks) != 0 {
		t.Errorf("probes %v, commit checks %v, want just the owner probed", f.probes, f.commitChecks)
	}
}

func TestResolveAccountOwnerBeatsCache(t *testing.T) {
	// A cache entry saved before the owner check existed mustn't shadow it.
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"personal": true, "work": true}, cached: "personal"}
	got, err := f.resolver().resolve(context.Background(), "work/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "work" || got.Source != accountSourceOwner {
		t.Errorf("got %+v, want the owner over the cached account", got)
	}
}

func TestResolveAccountOwnerWithoutAccessFallsBack(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"personal": true}}
	got, err := f.resolver().resolve(context.Background(), "work/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "personal" || !reflect.DeepEqual(f.probes, []string{"work", "personal"}) {
		t.Errorf("got %+v after probes %v, want personal with the owner probed only once", got, f.probes)
	}
}

func TestResolveAccountPrefersCommitter(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"personal": true, "work": true}, committers: map[string]bool{"work": true}}
	got, err := f.resolver().resolve(context.Background(), "Org/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "work" || got.Source != accountSourceCommits || f.saved != "work" {
		t.Errorf("got %+v saved %q, want work (has commits) cached", got, f.saved)
	}
}

func TestResolveAccountSoleReaderSkipsCommitCheck(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"work": true}, committers: map[string]bool{"personal": true}}
	if _, err := f.resolver().resolve(context.Background(), "Org/repo", ""); err != nil {
		t.Fatal(err)
	}
	if len(f.commitChecks) != 0 {
		t.Errorf("commit checks = %v, want none with a single reader", f.commitChecks)
	}
}

func TestResolveAccountTriesCachedAccountFirst(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"work": true}, cached: "work"}
	got, err := f.resolver().resolve(context.Background(), "Org/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "work" || !reflect.DeepEqual(f.probes, []string{"work"}) {
		t.Errorf("got %+v after probes %v, want work after one probe", got, f.probes)
	}
}

func TestResolveAccountStaleCacheReprobes(t *testing.T) {
	// The cached account lost access; the others are still tried.
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"personal": true}, cached: "work"}
	got, err := f.resolver().resolve(context.Background(), "Org/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "personal" || f.saved != "personal" {
		t.Errorf("got %+v saved %q, want personal re-cached", got, f.saved)
	}
}

func TestResolveAccountNoneCanRead(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts}
	_, err := f.resolver().resolve(context.Background(), "Org/repo", "")
	if err == nil {
		t.Fatal("want error when no account can read the repo")
	}
	for _, want := range []string{"personal, work", "Org/repo", "Could not resolve"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestResolveAccountConfigPin(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"personal": true, "work": true}, pins: map[string]string{"org": "work"}}
	got, err := f.resolver().resolve(context.Background(), "Org/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "work" || got.Source != accountSourceConfig {
		t.Errorf("got %+v, want pinned work over the active account", got)
	}
	if f.saved != "" {
		t.Errorf("pinned resolution cached %q; pins shouldn't touch the probe cache", f.saved)
	}
}

func TestResolveAccountPinWithoutAccessErrorsInsteadOfFallingBack(t *testing.T) {
	// Search returns empty results (not an error) for an unreadable repo, so a
	// bad pin must fail loudly rather than show empty tabs — and must not
	// quietly switch to the account that does work.
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"work": true}}
	_, err := f.resolver().resolve(context.Background(), "Org/repo", "personal")
	if err == nil || !strings.Contains(err.Error(), "personal") {
		t.Errorf("err = %v, want an access error naming the pinned account", err)
	}
	if !reflect.DeepEqual(f.probes, []string{"personal"}) {
		t.Errorf("probes = %v, want only the pinned account", f.probes)
	}
}

func TestResolveAccountFlagBeatsConfigPin(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts, readable: map[string]bool{"personal": true}, pins: map[string]string{"Org": "work"}}
	got, err := f.resolver().resolve(context.Background(), "Org/repo", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "personal" || got.Source != accountSourceFlag {
		t.Errorf("got %+v, want --account personal", got)
	}
}

func TestResolveAccountPinNotLoggedIn(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts}
	if _, err := f.resolver().resolve(context.Background(), "Org/repo", "someone-else"); err == nil {
		t.Error("want error for a pinned account gh isn't logged in to")
	}
}

func TestResolveAccountEnvTokenWins(t *testing.T) {
	f := &fakeResolver{accounts: twoAccounts, envSet: true, pins: map[string]string{"Org": "work"}}
	got, err := f.resolver().resolve(context.Background(), "Org/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "" || got.Source != accountSourceEnv {
		t.Errorf("got %+v, want gh left to use the env token", got)
	}
	if _, err := f.resolver().resolve(context.Background(), "Org/repo", "work"); err == nil {
		t.Error("want error when --account conflicts with an env token")
	}
}

func TestResolveAccountListFailureFallsBackToDefault(t *testing.T) {
	f := &fakeResolver{listErr: errors.New("unknown flag: --json")}
	got, err := f.resolver().resolve(context.Background(), "Org/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "" || got.Source != accountSourceDefault {
		t.Errorf("got %+v, want gh's default account", got)
	}
}

func TestMatchAccountPin(t *testing.T) {
	pins := map[string]string{"org": "work", "Org/special": "personal"}
	cases := map[string]string{
		"Org/repo":    "work",
		"org/Special": "personal",
		"other/repo":  "",
	}
	for repo, want := range cases {
		if got := matchAccountPin(pins, repo); got != want {
			t.Errorf("matchAccountPin(%q) = %q, want %q", repo, got, want)
		}
	}
}

func TestParseRemoteURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:PayPal-Braintree/snowglobe.git": "PayPal-Braintree/snowglobe",
		"git@github-work:owner/name":                    "owner/name",
		"ssh://git@github.com/owner/name.git":           "owner/name",
		"https://github.com/owner/name":                 "owner/name",
		"https://github.com/owner/name.git/":            "owner/name",
		"/local/path/repo":                              "",
		"https://github.com/owner":                      "",
		"":                                              "",
	}
	for url, want := range cases {
		got, ok := parseRemoteURL(url)
		if got != want || ok != (want != "") {
			t.Errorf("parseRemoteURL(%q) = %q, %v; want %q", url, got, ok, want)
		}
	}
}

func TestPickRepoFromRemotes(t *testing.T) {
	urls := map[string]string{
		"origin":   "git@github.com:me/fork.git",
		"upstream": "git@github.com:org/repo.git",
		"other":    "git@github.com:x/y.git",
	}
	names := []string{"other", "origin", "upstream"}

	if got, _ := pickRepoFromRemotes(names, urls, ""); got != "org/repo" {
		t.Errorf("default preference = %q, want upstream's org/repo", got)
	}
	if got, _ := pickRepoFromRemotes(names, urls, "remote.origin.gh-resolved base\n"); got != "me/fork" {
		t.Errorf("gh-resolved base = %q, want origin's me/fork", got)
	}
	if got, _ := pickRepoFromRemotes(names, urls, "remote.origin.gh-resolved pinned/repo\n"); got != "pinned/repo" {
		t.Errorf("gh-resolved explicit = %q, want pinned/repo", got)
	}
	if _, err := pickRepoFromRemotes(nil, nil, ""); err == nil {
		t.Error("want error with no remotes")
	}
}

func TestAccountCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PRS_STATE_DIR", dir)

	if got := loadCachedAccount("Org/Repo"); got != "" {
		t.Errorf("empty cache returned %q", got)
	}
	saveCachedAccount("Org/Repo", "work")
	if got := loadCachedAccount("org/repo"); got != "work" {
		t.Errorf("cached = %q, want work (case-insensitive repo key)", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "accounts.json")); err != nil {
		t.Errorf("accounts.json not written: %v", err)
	}
}

func TestConfigApplySetsAccountPins(t *testing.T) {
	prev := accountPins
	t.Cleanup(func() { accountPins = prev })

	Config{Accounts: map[string]string{"org": "work"}}.apply()
	if accountPins["org"] != "work" {
		t.Errorf("accountPins = %v, want org→work", accountPins)
	}
}
