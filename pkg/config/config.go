// Package config loads ploegd's routing and roster configuration from a FILE
// rather than from environment variables.
//
// The env-var forms it replaces — PLOEG_TARGET_MAP's
// "11/bronze=webgrip/ploeg@development,…", PLOEG_TEAM_MAP's "jake=bronze,…"
// and PLOEG_TEAM_PLANS' inlined JSON — were three hand-rolled DSLs with no
// schema, no comments and no diff worth reading. They also forced the
// operator to write a Vikunja project ID into cluster config, where a bare
// `11` says nothing about which board it is and silently routes work to the
// wrong repository the day somebody rebuilds the project.
//
// So a project is named here, not numbered:
//
//	trackers:
//	  vikunja:
//	    projects:
//	      - name: "Ploeg Test"          # resolved to an id at boot
//	        repo: webgrip/ploeg
//	        branch: development
//
// ploegd asks the tracker which id that name has when it starts, logs the
// mapping it resolved, and refuses to start if a name matches nothing. A
// typo becomes a boot failure with the available names in the message,
// instead of work quietly routed somewhere plausible.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/ploeg-hq/ploeg/pkg/flow"
	"github.com/ploeg-hq/ploeg/pkg/followup"
	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/playkpi"
	"github.com/ploeg-hq/ploeg/pkg/rarity"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// File is the whole of ploegd's file-backed configuration.
type File struct {
	Trackers Trackers `yaml:"trackers"`
	// Targets is the registry of repositories a `repo/<key>` label may
	// select (ADR-0038), keyed by the label's key. Omitted = labels never
	// affect routing.
	Targets map[string]Target `yaml:"targets"`
	// Teams is the roster: what each team is made of and what it may spend.
	Teams map[string]Team `yaml:"teams"`
}

type Trackers struct {
	Vikunja TrackerConfig `yaml:"vikunja"`
	Clickup TrackerConfig `yaml:"clickup"`
}

type TrackerConfig struct {
	Projects []Project `yaml:"projects"`
}

// Target is one registered repository, selected by the label
// `repo/<key>` on a board that allows it.
type Target struct {
	// Repo is "owner/name".
	Repo string `yaml:"repo"`
	// Branch is the base branch; empty means the repository's default.
	Branch string `yaml:"branch"`
	// Forge names which forge instance holds the repo. Empty = the
	// deployment's single forge.
	Forge string `yaml:"forge"`
	// CardStyle is how the operator consumer draws this repository's Run cards
	// (ADR-0046); Ploeg passes it through.
	// Omitted = the default skin and no theme.
	CardStyle *CardStyle `yaml:"cardStyle"`
	// Release names the environment whose first deploy releases a merged
	// change of this repository (ADR-0047). Omitted = production.
	Release *Release `yaml:"release"`
	// Rarity sets the path rules of this repository's Run card rarity
	// (ADR-0056). Omitted = the defaults of rarity.Formula.
	Rarity *Rarity `yaml:"rarity"`
	// CardShape sets which paths this repository's Run cards count as
	// tests and documentation (ADR-0058). Omitted = the defaults.
	CardShape *CardShape `yaml:"cardShape"`
}

// CardShape is one Work Target's change-shape path rules (ADR-0058), in
// the rarity path syntax.
type CardShape struct {
	// TestPaths replaces playkpi.DefaultTestPaths when set; an empty list
	// means no file is a test.
	TestPaths []string `yaml:"testPaths"`
	// DocPaths replaces playkpi.DefaultDocPaths when set; an empty list
	// means no file is documentation.
	DocPaths []string `yaml:"docPaths"`
}

// Rarity is one Work Target's Run card rarity path rules (ADR-0056). Each
// list holds path patterns: a pattern without a slash matches a file name
// at any depth, any other is anchored at the repository root, ** spans
// directories and {a,b} lists alternatives.
type Rarity struct {
	// SensitivePaths replaces rarity.DefaultSensitivePaths when set; an
	// empty list means no default sensitive ground.
	SensitivePaths []string `yaml:"sensitivePaths"`
	// AttentionPaths are more paths counted as sensitive ground, on top of
	// SensitivePaths or the defaults.
	AttentionPaths []string `yaml:"attentionPaths"`
	// SizeExclude replaces rarity.DefaultSizeExclude when set: the paths
	// whose lines a card's size does not count.
	SizeExclude []string `yaml:"sizeExclude"`
}

// Release configures when a Work Target's merged change counts as live.
type Release struct {
	// Environment is the deploy environment, lowercase, as a pipeline
	// reports it to POST /api/v1/deploys.
	Environment string `yaml:"environment"`
}

// CardStyle names the skin and the optional theme a Run card is drawn with.
// Ploeg passes both through to the operator consumer, which owns what they
// look like; Ploeg attaches no meaning to either name.
type CardStyle struct {
	// Skin is the card skin; empty means DefaultCardSkin.
	Skin string `yaml:"skin"`
	// Theme is a per-client theme on top of the skin; empty means none.
	Theme string `yaml:"theme"`
}

// DefaultCardSkin is the skin a Work Target without a cardStyle gets: the
// consumer's own default.
const DefaultCardSkin = "default"

// Project routes one tracker container to one repository, or to the
// registered targets it names.
type Project struct {
	// Name is the project's name on the board, exactly as a human sees it.
	// Resolved to the provider's id at boot.
	Name string `yaml:"name"`
	// ID pins the provider's id directly, skipping resolution. An escape
	// hatch for a board whose names are not unique or not readable by the
	// configured token — not the normal path.
	ID string `yaml:"id"`
	// Repo is "owner/name".
	Repo string `yaml:"repo"`
	// Branch is the base branch; empty means the repository's default, which
	// may be a stale stub (VIK-589), so pinning it is advised.
	Branch string `yaml:"branch"`
	// Forge names which forge instance holds the repo. Empty = the
	// deployment's single forge.
	Forge string `yaml:"forge"`
	// Team routes this project's work to one team. Empty means the assignee
	// decides, via the team's `assignees` list below.
	Team string `yaml:"team"`
	// Default is the registered target for work without a `repo/*` label,
	// instead of Repo. A project with neither requires the label.
	Default string `yaml:"default"`
	// Allow lists the other registered targets a `repo/<key>` label may
	// select on this project.
	Allow []string `yaml:"allow"`
	// CardStyle styles the Run cards of Repo, as on a registered target. It
	// requires Repo.
	CardStyle *CardStyle `yaml:"cardStyle"`
	// Release names Repo's release environment, as on a registered target.
	// It requires Repo.
	Release *Release `yaml:"release"`
	// Rarity sets Repo's Run card rarity path rules, as on a registered
	// target. It requires Repo.
	Rarity *Rarity `yaml:"rarity"`
	// CardShape sets Repo's change-shape path rules, as on a registered
	// target. It requires Repo.
	CardShape *CardShape `yaml:"cardShape"`
	// Gates maps this board's statuses or bucket titles to delivery gates
	// (ADR-0051). Omitted = Ploeg records no gate for this board's work.
	Gates *gate.Statuses `yaml:"gates"`
	// StatusKinds says which of this board's statuses or bucket titles are
	// active, waiting, blocked or done for the Run card's flow figures
	// (ADR-0057). A status not listed takes flow.DefaultKind. A board with
	// gates or statusKinds records every status move; an empty
	// `statusKinds: {}` records them with the defaults only.
	StatusKinds *flow.Kinds `yaml:"statusKinds"`
}

// Team is a roster entry: who works, at what cost, in what order.
type Team struct {
	// Assignees are the tracker usernames that dispatch to this team, so a
	// human assigns a colleague rather than an infrastructure tier.
	Assignees []string `yaml:"assignees"`
	// Plan is the ordered Rounds of a Shift; empty = a single writer.
	Plan *plan.TeamPlan `yaml:"plan"`
	// MaxRunning caps how many of the team's Runs may be running at once.
	// A claim over the cap returns no work. 0 = unlimited.
	MaxRunning int `yaml:"maxRunning"`
	// ForgeFollowUps opts this team in to acting on forge events on its pull
	// requests. Omitted = forge events are recorded and nothing else happens.
	ForgeFollowUps work.ForgeFollowUps `yaml:"forgeFollowUps"`
	// CreatedWork limits the Work Items this Team's Runs may create
	// (ADR-0031). Absent fields take followup.Default.
	CreatedWork *CreatedWork `yaml:"createdWork"`
	// Cards sets this Team's Run card rules (ADR-0052, ADR-0055). Omitted =
	// anyone uninvolved referees a disputed crack, the label "hotfix" marks a
	// hotfix, and no card comment is posted on pull requests.
	Cards *TeamCards `yaml:"cards"`
	// WorkingHours is the Team's working calendar, which the working
	// seconds of its Run cards' flow figures count in (ADR-0057). Omitted
	// fields take flow.DefaultHours: Monday to Friday, 09:00 to 17:00 in
	// Europe/Amsterdam, without holidays.
	WorkingHours *flow.Hours `yaml:"workingHours"`
}

// TeamCards are one Team's Run card rules.
type TeamCards struct {
	// Referees are the people, as the operator API names its actor, who
	// alone may resolve a disputed crack. Empty = anyone uninvolved.
	Referees []string `yaml:"referees"`
	// HotfixLabels are the pull request labels that mark a fix as a
	// hotfix. Empty = "hotfix".
	HotfixLabels []string `yaml:"hotfixLabels"`
	// PRComment posts and keeps one card comment, with the card as an
	// image, on the Team's merged pull requests at each card moment
	// (ADR-0055). Default false.
	PRComment bool `yaml:"prComment"`
}

// CreatedWork overrides followup.Default for one Team. Every field is
// optional.
type CreatedWork struct {
	AutoDispatch     *bool `yaml:"autoDispatch"`
	MaxCreatedPerRun *int  `yaml:"maxCreatedPerRun"`
	MaxDepth         *int  `yaml:"maxDepth"`
	MaxOpen          *int  `yaml:"maxOpen"`
	ItemBudgetUSD    *USD  `yaml:"itemBudgetUsd"`
	PoolUSD          *USD  `yaml:"poolUsd"`
	// RefinementTeam receives created Work Items that are not Ready.
	RefinementTeam string `yaml:"refinementTeam"`
	// RefinementRole names a planner Role instead; the Team whose plan runs
	// it receives the Work Items. It must match exactly one Team.
	RefinementRole string `yaml:"refinementRole"`
}

// USD is an amount of money that YAML may write as a number or a string.
type USD float64

// UnmarshalYAML accepts "2.00" and 2.00 alike.
func (u *USD) UnmarshalYAML(value *yaml.Node) error {
	s := strings.Trim(strings.TrimSpace(value.Value), `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("money value %q: %w", s, err)
	}
	*u = USD(f)
	return nil
}

// Load reads and validates the file. A missing path is not an error — it
// means "no file-backed config", and the caller keeps whatever defaults it
// has. A malformed one IS an error: config that does not parse must never
// boot into a half-applied state.
func Load(path string) (*File, error) {
	if path == "" {
		return &File{}, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &File{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true) // a typo'd key is a boot failure, never a silent default
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Validate catches what can be caught without talking to a tracker.
func (f *File) Validate() error {
	if err := f.validateTargets(); err != nil {
		return err
	}
	// One scope namespace across trackers, deliberately: the target map keys
	// on the provider's container id alone, so a vikunja project and a clickup
	// List sharing an id would silently route each other's work. Validating
	// them through one `seen` map turns that collision into a boot failure.
	seen := map[string]string{}
	for _, tr := range []struct {
		provider string
		projects []Project
	}{
		{"vikunja", f.Trackers.Vikunja.Projects},
		{"clickup", f.Trackers.Clickup.Projects},
	} {
		for i, p := range tr.projects {
			where := fmt.Sprintf("trackers.%s.projects[%d]", tr.provider, i)
			if tr.provider == "clickup" && p.ID == "" {
				// The clickup provider has no name resolver yet; a name-only
				// entry would boot into "resolve" with nothing to ask.
				return fmt.Errorf("%s: needs an id (the List id); clickup name resolution is not implemented", where)
			}
			if p.Name == "" && p.ID == "" {
				return fmt.Errorf("%s: needs a name (preferred) or an id", where)
			}
			if err := f.validateRoute(p); err != nil {
				return fmt.Errorf("%s (%s): %w", where, p.label(), err)
			}
			// Keyed on project AND team, because per-team routing on one project
			// is the feature: RoutingTable renders one rule per "<id>/<team>", and
			// pkg/target resolves the team-specific rule ahead of the bare one. A
			// project-only key would forbid the very config it then generates.
			if prev, dup := seen[p.routeKey()]; dup {
				forTeam := ""
				if p.Team != "" {
					forTeam = fmt.Sprintf(" for team %q", p.Team)
				}
				return fmt.Errorf("%s: project %q is routed twice%s (already to %s)", where, p.label(), forTeam, prev)
			}
			seen[p.routeKey()] = p.destination()
			if p.Team != "" {
				if _, ok := f.Teams[p.Team]; !ok {
					return fmt.Errorf("%s (%s): routes to team %q, which is not in teams", where, p.label(), p.Team)
				}
			}
		}
	}
	// One assignee, one team. Two teams claiming the same username makes
	// AssigneeTeams() a coin flip over Go's map iteration order, so the SAME
	// config dispatches the same person's tickets to a different team on
	// different boots (measured on rc.12: 168/200 vs 32/200 in one process).
	// Team names are walked in sorted order so the error names the same two
	// teams every time — a nondeterministic message is a flaky test.
	byAssignee := map[string]string{}
	for _, team := range sortedTeamNames(f.Teams) {
		for _, a := range f.Teams[team].Assignees {
			key := strings.ToLower(strings.TrimSpace(a))
			if key == "" {
				continue
			}
			if prev, dup := byAssignee[key]; dup && prev != team {
				return fmt.Errorf("assignee %q is on both team %q and team %q; one assignee routes to exactly one team", key, prev, team)
			}
			byAssignee[key] = team
		}
	}
	for _, name := range sortedTeamNames(f.Teams) {
		if f.Teams[name].MaxRunning < 0 {
			return fmt.Errorf("teams.%s.maxRunning: must be 0 (unlimited) or more, got %d", name, f.Teams[name].MaxRunning)
		}
	}
	for name, t := range f.Teams {
		if t.ForgeFollowUps.MaxRepairs < 0 {
			return fmt.Errorf("teams.%s.forgeFollowUps.maxRepairs: must not be negative", name)
		}
		if t.Plan == nil {
			continue
		}
		if err := plan.Validate(*t.Plan); err != nil {
			return fmt.Errorf("teams.%s.plan: %w", name, err)
		}
	}
	for _, name := range sortedTeamNames(f.Teams) {
		if _, err := f.createdWorkPolicy(name); err != nil {
			return fmt.Errorf("teams.%s.createdWork: %w", name, err)
		}
	}
	if _, err := f.CardStyles(); err != nil {
		return err
	}
	if _, err := f.ReleaseEnvironments(); err != nil {
		return err
	}
	if _, err := f.RarityRules(); err != nil {
		return err
	}
	if _, err := f.CardShapeRules(); err != nil {
		return err
	}
	if err := f.validateCards(); err != nil {
		return err
	}
	if err := f.validateGates(); err != nil {
		return err
	}
	if err := f.validateStatusKinds(); err != nil {
		return err
	}
	_, err := f.WorkingCalendars()
	return err
}

func (f *File) validateCards() error {
	for _, name := range sortedTeamNames(f.Teams) {
		cards := f.Teams[name].Cards
		if cards == nil {
			continue
		}
		for field, values := range map[string][]string{"referees": cards.Referees, "hotfixLabels": cards.HotfixLabels} {
			seen := map[string]bool{}
			for _, v := range values {
				key := strings.ToLower(strings.TrimSpace(v))
				if key == "" || len(v) > 128 || seen[key] {
					return fmt.Errorf("teams.%s.cards.%s: entries are non-empty, at most 128 characters and unique, got %q", name, field, v)
				}
				seen[key] = true
			}
		}
	}
	return nil
}

// TeamCardRules returns every Team's card rules that set any (ADR-0052,
// ADR-0055).
func (f *File) TeamCardRules() map[string]TeamCards {
	out := map[string]TeamCards{}
	for name, t := range f.Teams {
		if t.Cards != nil && (len(t.Cards.Referees) > 0 || len(t.Cards.HotfixLabels) > 0 || t.Cards.PRComment) {
			out[name] = *t.Cards
		}
	}
	return out
}

func (f *File) validateGates() error {
	for _, tr := range []struct {
		provider string
		projects []Project
	}{
		{"vikunja", f.Trackers.Vikunja.Projects},
		{"clickup", f.Trackers.Clickup.Projects},
	} {
		where := map[string]string{}
		mapped := map[string]gate.Statuses{}
		for i, p := range tr.projects {
			if p.Gates == nil {
				continue
			}
			at := fmt.Sprintf("trackers.%s.projects[%d]", tr.provider, i)
			if _, err := gate.NewMap(*p.Gates); err != nil {
				return fmt.Errorf("%s.gates: %w", at, err)
			}
			if prev, dup := mapped[p.label()]; dup && !reflect.DeepEqual(prev, *p.Gates) {
				return fmt.Errorf("%s.gates: project %q maps its statuses differently at %s", at, p.label(), where[p.label()])
			}
			mapped[p.label()], where[p.label()] = *p.Gates, at
		}
	}
	return nil
}

func (f *File) validateTargets() error {
	for _, key := range sortedTargetKeys(f.Targets) {
		if key == "" || strings.TrimSpace(key) != key {
			return fmt.Errorf("targets: key %q must be non-empty with no surrounding space", key)
		}
		if !ownerName(f.Targets[key].Repo) {
			return fmt.Errorf("targets.%s: repo %q must be owner/name", key, f.Targets[key].Repo)
		}
		if err := f.Targets[key].CardStyle.validate(); err != nil {
			return fmt.Errorf("targets.%s.cardStyle: %w", key, err)
		}
		if err := f.Targets[key].Release.validate(); err != nil {
			return fmt.Errorf("targets.%s.release: %w", key, err)
		}
	}
	return nil
}

func (r *Release) validate() error {
	if r == nil {
		return nil
	}
	normalized, ok := work.NormalizeEnvironment(r.Environment)
	if !ok || normalized != r.Environment {
		return fmt.Errorf("environment %q must be 1 to 63 lowercase letters, digits, dots, underscores and dashes", r.Environment)
	}
	return nil
}

// ReleaseEnvironments returns the release environment of every repository
// that names one, keyed by lowercased "owner/name". A repository given two
// different environments is an error. A repository absent from the result
// releases in work.DefaultReleaseEnvironment.
func (f *File) ReleaseEnvironments() (map[string]string, error) {
	out := map[string]string{}
	where := map[string]string{}
	add := func(repo string, release *Release, at string) error {
		if release == nil || repo == "" {
			return nil
		}
		key := strings.ToLower(repo)
		if prev, dup := out[key]; dup && prev != release.Environment {
			return fmt.Errorf("%s: release environment for %s differs from the one at %s", at, repo, where[key])
		}
		out[key], where[key] = release.Environment, at
		return nil
	}
	for _, key := range sortedTargetKeys(f.Targets) {
		if err := add(f.Targets[key].Repo, f.Targets[key].Release, "targets."+key); err != nil {
			return nil, err
		}
	}
	for _, tr := range []struct {
		provider string
		projects []Project
	}{
		{"vikunja", f.Trackers.Vikunja.Projects},
		{"clickup", f.Trackers.Clickup.Projects},
	} {
		for i, p := range tr.projects {
			if err := add(p.Repo, p.Release, fmt.Sprintf("trackers.%s.projects[%d]", tr.provider, i)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (r *Rarity) rules() rarity.Rules {
	return rarity.Rules{SensitivePaths: r.SensitivePaths, AttentionPaths: r.AttentionPaths, SizeExclude: r.SizeExclude}
}

// RarityRules returns the rarity path rules of every repository that sets
// them, keyed by lowercased "owner/name" (ADR-0056). A repository given two
// different sets of rules, or a pattern that does not compile, is an error.
// A repository absent from the result uses the defaults.
func (f *File) RarityRules() (map[string]rarity.Rules, error) {
	out := map[string]rarity.Rules{}
	where := map[string]string{}
	add := func(repo string, r *Rarity, at string) error {
		if r == nil || repo == "" {
			return nil
		}
		rules := r.rules()
		if _, err := rules.Compile(); err != nil {
			return fmt.Errorf("%s.rarity.%w", at, err)
		}
		key := strings.ToLower(repo)
		if prev, dup := out[key]; dup && !reflect.DeepEqual(prev, rules) {
			return fmt.Errorf("%s: rarity for %s differs from the one at %s", at, repo, where[key])
		}
		out[key], where[key] = rules, at
		return nil
	}
	for _, key := range sortedTargetKeys(f.Targets) {
		if err := add(f.Targets[key].Repo, f.Targets[key].Rarity, "targets."+key); err != nil {
			return nil, err
		}
	}
	for _, tr := range []struct {
		provider string
		projects []Project
	}{
		{"vikunja", f.Trackers.Vikunja.Projects},
		{"clickup", f.Trackers.Clickup.Projects},
	} {
		for i, p := range tr.projects {
			if err := add(p.Repo, p.Rarity, fmt.Sprintf("trackers.%s.projects[%d]", tr.provider, i)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// CardShapeRules returns the change-shape path rules of every repository
// that sets them, keyed by lowercased "owner/name" (ADR-0058). A repository
// given two different sets of rules, or a pattern that does not compile, is
// an error. A repository absent from the result uses the defaults.
func (f *File) CardShapeRules() (map[string]playkpi.Rules, error) {
	out := map[string]playkpi.Rules{}
	where := map[string]string{}
	add := func(repo string, c *CardShape, at string) error {
		if c == nil || repo == "" {
			return nil
		}
		rules := playkpi.Rules{TestPaths: c.TestPaths, DocPaths: c.DocPaths}
		if _, err := rules.Compile(); err != nil {
			return fmt.Errorf("%s.cardShape.%w", at, err)
		}
		key := strings.ToLower(repo)
		if prev, dup := out[key]; dup && !reflect.DeepEqual(prev, rules) {
			return fmt.Errorf("%s: cardShape for %s differs from the one at %s", at, repo, where[key])
		}
		out[key], where[key] = rules, at
		return nil
	}
	for _, key := range sortedTargetKeys(f.Targets) {
		if err := add(f.Targets[key].Repo, f.Targets[key].CardShape, "targets."+key); err != nil {
			return nil, err
		}
	}
	for _, tr := range []struct {
		provider string
		projects []Project
	}{
		{"vikunja", f.Trackers.Vikunja.Projects},
		{"clickup", f.Trackers.Clickup.Projects},
	} {
		for i, p := range tr.projects {
			if err := add(p.Repo, p.CardShape, fmt.Sprintf("trackers.%s.projects[%d]", tr.provider, i)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

var cardStyleName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func (c *CardStyle) validate() error {
	if c == nil {
		return nil
	}
	if c.Skin != "" && !cardStyleName.MatchString(c.Skin) {
		return fmt.Errorf("skin %q must be lowercase letters, digits and dashes, at most 64", c.Skin)
	}
	if c.Theme != "" && !cardStyleName.MatchString(c.Theme) {
		return fmt.Errorf("theme %q must be lowercase letters, digits and dashes, at most 64", c.Theme)
	}
	return nil
}

func (c CardStyle) withDefaults() CardStyle {
	if c.Skin == "" {
		c.Skin = DefaultCardSkin
	}
	return c
}

// CardStyles returns the card style of every repository that sets one,
// keyed by lowercased "owner/name". A repository styled differently in two
// places is an error. A repository absent from the result uses
// DefaultCardSkin and no theme.
func (f *File) CardStyles() (map[string]CardStyle, error) {
	out := map[string]CardStyle{}
	where := map[string]string{}
	add := func(repo string, style *CardStyle, at string) error {
		if style == nil || repo == "" {
			return nil
		}
		key := strings.ToLower(repo)
		resolved := style.withDefaults()
		if prev, dup := out[key]; dup && prev != resolved {
			return fmt.Errorf("%s: cardStyle for %s differs from the one at %s", at, repo, where[key])
		}
		out[key], where[key] = resolved, at
		return nil
	}
	for _, key := range sortedTargetKeys(f.Targets) {
		if err := add(f.Targets[key].Repo, f.Targets[key].CardStyle, "targets."+key); err != nil {
			return nil, err
		}
	}
	for _, tr := range []struct {
		provider string
		projects []Project
	}{
		{"vikunja", f.Trackers.Vikunja.Projects},
		{"clickup", f.Trackers.Clickup.Projects},
	} {
		for i, p := range tr.projects {
			if err := add(p.Repo, p.CardStyle, fmt.Sprintf("trackers.%s.projects[%d]", tr.provider, i)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (f *File) validateRoute(p Project) error {
	if p.Default != "" && (p.Repo != "" || p.Branch != "" || p.Forge != "") {
		return fmt.Errorf("set default or repo/branch/forge, not both")
	}
	if p.Repo == "" && p.Default == "" && len(p.Allow) == 0 {
		return fmt.Errorf("needs a repo, a default target or allowed targets")
	}
	if p.Repo != "" && !ownerName(p.Repo) {
		return fmt.Errorf("repo %q must be owner/name", p.Repo)
	}
	if p.CardStyle != nil && p.Repo == "" {
		return fmt.Errorf("cardStyle requires repo; style a registered target under targets instead")
	}
	if err := p.CardStyle.validate(); err != nil {
		return fmt.Errorf("cardStyle: %w", err)
	}
	if p.Release != nil && p.Repo == "" {
		return fmt.Errorf("release requires repo; set it on a registered target under targets instead")
	}
	if err := p.Release.validate(); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if p.Rarity != nil && p.Repo == "" {
		return fmt.Errorf("rarity requires repo; set it on a registered target under targets instead")
	}
	if p.CardShape != nil && p.Repo == "" {
		return fmt.Errorf("cardShape requires repo; set it on a registered target under targets instead")
	}
	for _, key := range append([]string{p.Default}, p.Allow...) {
		if key == "" {
			continue
		}
		if _, ok := f.Targets[key]; !ok {
			return fmt.Errorf("target %q is not in targets", key)
		}
	}
	return nil
}

func ownerName(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	return ok && owner != "" && name != ""
}

func (p Project) destination() string {
	switch {
	case p.Repo != "":
		return p.Repo
	case p.Default != "":
		return "target " + p.Default
	default:
		return "a repo label"
	}
}

func sortedTargetKeys(targets map[string]Target) []string {
	out := make([]string, 0, len(targets))
	for key := range targets {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// CreatedWorkPolicies returns every configured Team's created-work limits.
// A Team absent from the result uses followup.Default.
func (f *File) CreatedWorkPolicies() (map[string]followup.Policy, error) {
	out := map[string]followup.Policy{}
	for _, name := range sortedTeamNames(f.Teams) {
		p, err := f.createdWorkPolicy(name)
		if err != nil {
			return nil, fmt.Errorf("teams.%s.createdWork: %w", name, err)
		}
		out[name] = p
	}
	return out, nil
}

func (f *File) createdWorkPolicy(team string) (followup.Policy, error) {
	p := followup.Default()
	c := f.Teams[team].CreatedWork
	if c == nil {
		return p, nil
	}
	if c.AutoDispatch != nil {
		p.AutoDispatch = *c.AutoDispatch
	}
	for _, v := range []struct {
		name string
		src  *int
		dst  *int
	}{
		{"maxCreatedPerRun", c.MaxCreatedPerRun, &p.MaxCreatedPerRun},
		{"maxDepth", c.MaxDepth, &p.MaxDepth},
		{"maxOpen", c.MaxOpen, &p.MaxOpen},
	} {
		if v.src == nil {
			continue
		}
		if *v.src < 0 {
			return p, fmt.Errorf("%s must not be negative, got %d", v.name, *v.src)
		}
		*v.dst = *v.src
	}
	if c.ItemBudgetUSD != nil {
		p.ItemBudgetUSD = float64(*c.ItemBudgetUSD)
	}
	if c.PoolUSD != nil {
		p.PoolUSD = float64(*c.PoolUSD)
	}
	if p.ItemBudgetUSD <= 0 {
		return p, fmt.Errorf("itemBudgetUsd must be positive: a created Work Item's Shift is always metered")
	}
	if p.PoolUSD < p.ItemBudgetUSD {
		return p, fmt.Errorf("poolUsd %.2f is smaller than itemBudgetUsd %.2f, so no Work Item could ever be created", p.PoolUSD, p.ItemBudgetUSD)
	}
	if c.RefinementTeam != "" && c.RefinementRole != "" {
		return p, fmt.Errorf("set refinementTeam or refinementRole, not both")
	}
	if c.RefinementTeam != "" {
		if _, ok := f.Teams[c.RefinementTeam]; !ok {
			return p, fmt.Errorf("refinementTeam %q is not in teams", c.RefinementTeam)
		}
		p.RefinementTeam = c.RefinementTeam
	}
	if c.RefinementRole != "" {
		var owners []string
		for _, name := range sortedTeamNames(f.Teams) {
			if tp := f.Teams[name].Plan; tp != nil && tp.HasRole(c.RefinementRole) {
				owners = append(owners, name)
			}
		}
		if len(owners) != 1 {
			return p, fmt.Errorf("refinementRole %q must be in exactly one team's plan, found %d %v", c.RefinementRole, len(owners), owners)
		}
		p.RefinementTeam = owners[0]
	}
	return p, nil
}

// routeKey is what may only appear once: a project, per team. NUL separates
// the two halves so a team name can never be mistaken for part of a project
// name that happens to contain the separator.
func (p Project) routeKey() string {
	return p.label() + "\x00" + p.Team
}

func (p Project) label() string {
	if p.Name != "" {
		return p.Name
	}
	return "id " + p.ID
}

// Plans projects the roster into the shape the shift engine consumes.
func (f *File) Plans() plan.Plans {
	out := plan.Plans{}
	for name, t := range f.Teams {
		if t.Plan != nil {
			out[name] = *t.Plan
		}
	}
	return out
}

// RunCaps maps a team to its concurrency cap: the most Runs it may have
// running at once. A team that is absent or mapped to 0 is unlimited.
type RunCaps map[string]int

// MaxRunning returns the team's cap, 0 when unlimited.
func (c RunCaps) MaxRunning(team string) int { return c[team] }

// ParseRunCaps reads a JSON object of team name to cap, the shape the chart
// renders into PLOEG_TEAM_MAX_RUNNING. An empty string is no caps.
func ParseRunCaps(raw string) (RunCaps, error) {
	caps := RunCaps{}
	if strings.TrimSpace(raw) == "" {
		return caps, nil
	}
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		return nil, err
	}
	for team, n := range caps {
		if n < 0 {
			return nil, fmt.Errorf("team %s: maxRunning must be 0 (unlimited) or more, got %d", team, n)
		}
	}
	return caps, nil
}

// RunCaps overlays the roster's per-team maxRunning onto base, so a team
// capped in the file wins over the same team capped by environment.
func (f *File) RunCaps(base RunCaps) RunCaps {
	out := RunCaps{}
	for team, n := range base {
		out[team] = n
	}
	for team, t := range f.Teams {
		if t.MaxRunning > 0 {
			out[team] = t.MaxRunning
		}
	}
	return out
}

// ForgeFollowUps returns the forge-event policy of every team that enabled
// one. A team that is missing acts on no forge event.
func (f *File) ForgeFollowUps() map[string]work.ForgeFollowUps {
	out := map[string]work.ForgeFollowUps{}
	for name, t := range f.Teams {
		if t.ForgeFollowUps.Enabled() {
			out[name] = t.ForgeFollowUps
		}
	}
	return out
}

// AssigneeTeams projects the roster into assignee → team, lowercased.
//
// Sorted, not map order. Validate() rejects an assignee shared by two teams,
// but Validate() only runs from Load() — a File built in a test or from a
// future source would otherwise resolve routing by coin flip. Determinism
// here costs nothing and removes the failure mode entirely.
func (f *File) AssigneeTeams() map[string]string {
	out := map[string]string{}
	for _, team := range sortedTeamNames(f.Teams) {
		for _, a := range f.Teams[team].Assignees {
			out[strings.ToLower(a)] = team
		}
	}
	return out
}

// sortedTeamNames walks the roster in a stable order.
func sortedTeamNames(teams map[string]Team) []string {
	out := make([]string, 0, len(teams))
	for name := range teams {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
