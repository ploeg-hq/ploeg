package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Deployment is a pipeline's report that commit SHA is live in Environment
// of one repository (ADR-0047). Forge is the forge dialect, as
// provider.ForgeProvider.Name returns it. Environment and SHA are already
// normalized; URL and Source may be empty.
type Deployment struct {
	Forge       string
	Owner       string
	Name        string
	Environment string
	SHA         string
	DeployedAt  time.Time
	URL         string
	Source      string
}

// RecordedDeployment is the stored row a report resolved to. Created is
// false when the same (repository, environment, sha) was reported before;
// DeployedAt is then the first report's.
type RecordedDeployment struct {
	ID         int64
	DeployedAt time.Time
	Created    bool
}

// RecordDeployment stores d once per (forge, repository, environment, sha)
// and audits the first report. A repeated report keeps the first one's
// facts.
func (s *Store) RecordDeployment(ctx context.Context, d Deployment) (RecordedDeployment, error) {
	if d.Forge == "" || d.Owner == "" || d.Name == "" || d.Environment == "" || d.SHA == "" || d.DeployedAt.IsZero() {
		return RecordedDeployment{}, errors.New("a deployment needs a forge, repository, environment, sha and time")
	}
	owner, name := strings.ToLower(d.Owner), strings.ToLower(d.Name)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RecordedDeployment{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	out := RecordedDeployment{Created: true}
	err = tx.QueryRow(ctx, `
		INSERT INTO deployments (forge, repo_owner, repo_name, environment, sha, deployed_at, url, source)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF(left($7, 2048), ''), NULLIF($8, ''))
		ON CONFLICT (forge, repo_owner, repo_name, environment, sha) DO NOTHING
		RETURNING id, deployed_at`,
		d.Forge, owner, name, d.Environment, d.SHA, d.DeployedAt.UTC(), d.URL,
		knownValue(d.Source, "ci", "gitops", "manual")).Scan(&out.ID, &out.DeployedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		out.Created = false
		err = tx.QueryRow(ctx, `SELECT id, deployed_at FROM deployments
			WHERE forge = $1 AND repo_owner = $2 AND repo_name = $3 AND environment = $4 AND sha = $5`,
			d.Forge, owner, name, d.Environment, d.SHA).Scan(&out.ID, &out.DeployedAt)
	}
	if err != nil {
		return RecordedDeployment{}, err
	}
	out.DeployedAt = out.DeployedAt.UTC()
	if out.Created {
		detail := map[string]any{"forge": d.Forge, "repo": owner + "/" + name, "environment": d.Environment,
			"sha": d.SHA, "deployed_at": out.DeployedAt.Format(time.RFC3339)}
		if d.URL != "" {
			detail["url"] = truncate(d.URL, 2048)
		}
		actor := "deploy"
		if source := knownValue(d.Source, "ci", "gitops", "manual"); source != "" {
			detail["source"] = source
			actor += ":" + source
		}
		if err := audit(ctx, tx, actor, "deploy.recorded", nil, detail); err != nil {
			return RecordedDeployment{}, err
		}
	}
	return out, tx.Commit(ctx)
}

// DeployCandidate is a merged Ploeg pull request whose merge commit may be
// in a deployment. Repo is owner/name as the forge reported it.
type DeployCandidate struct {
	PullRequestID  int64
	Repo           string
	Number         int
	MergeCommitSHA string
}

// DeployCandidates lists at most limit merged pull requests of the
// deployment's repository that have a merge commit, merged no later than
// skew after it was deployed, were not yet compared with this deployment,
// and have no deploy of its environment at or before its time. A pull
// request first marked by a later deploy is a candidate, so an earlier
// deploy reported late can correct its first time. The newest merge comes
// first.
func (s *Store) DeployCandidates(ctx context.Context, deploymentID int64, skew time.Duration, limit int) ([]DeployCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.repo_owner || '/' || p.repo_name, p.number, p.merge_commit_sha
		FROM deployments d
		JOIN pull_requests p ON p.forge = d.forge AND lower(p.repo_owner) = d.repo_owner AND lower(p.repo_name) = d.repo_name
		WHERE d.id = $1 AND p.state = 'merged' AND p.merge_commit_sha IS NOT NULL
		  AND (p.merged_at IS NULL OR p.merged_at <= d.deployed_at + $2::interval)
		  AND NOT EXISTS (SELECT 1 FROM pull_request_deployments pd
			WHERE pd.pull_request_id = p.id AND pd.environment = d.environment AND pd.first_deployed_at <= d.deployed_at)
		  AND NOT EXISTS (SELECT 1 FROM deployment_checks c WHERE c.deployment_id = d.id AND c.pull_request_id = p.id)
		ORDER BY p.merged_at DESC NULLS LAST, p.id DESC
		LIMIT $3`, deploymentID, skew, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeployCandidate
	for rows.Next() {
		var c DeployCandidate
		if err := rows.Scan(&c.PullRequestID, &c.Repo, &c.Number, &c.MergeCommitSHA); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkDeployed records that deployment carried pull request pullRequestID
// to its environment. It reports true only when the pull request had no
// deploy of that environment yet. A recorded deploy is replaced only by an
// earlier one, so the first time is kept.
func (s *Store) MarkDeployed(ctx context.Context, pullRequestID, deploymentID int64) (bool, error) {
	return markDeployed(ctx, s.pool, pullRequestID, deploymentID)
}

func markDeployed(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, pullRequestID, deploymentID int64) (bool, error) {
	var inserted bool
	err := q.QueryRow(ctx, `
		INSERT INTO pull_request_deployments (pull_request_id, environment, deployment_id, first_deployed_at)
		SELECT $1, d.environment, d.id, d.deployed_at FROM deployments d WHERE d.id = $2
		ON CONFLICT (pull_request_id, environment) DO UPDATE SET
			deployment_id = EXCLUDED.deployment_id,
			first_deployed_at = EXCLUDED.first_deployed_at,
			marked_at = now()
		WHERE EXCLUDED.first_deployed_at < pull_request_deployments.first_deployed_at
		RETURNING xmax = 0`, pullRequestID, deploymentID).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return inserted, err
}

// PendingDeployment is a recorded deployment whose candidate pull requests
// were not all compared yet. Attempts counts the passes in a row in which
// every comparison failed.
type PendingDeployment struct {
	ID int64
	Deployment
	Attempts int
}

// RecordDeployCheck stores that the forge compared pull request
// pullRequestID with deployment deploymentID, so the pair is never compared
// again. When carried is true it also marks the pull request as MarkDeployed
// does, and reports true only for a first mark of that environment.
func (s *Store) RecordDeployCheck(ctx context.Context, deploymentID, pullRequestID int64, carried bool) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO deployment_checks (deployment_id, pull_request_id, carried)
		VALUES ($1, $2, $3) ON CONFLICT (deployment_id, pull_request_id) DO NOTHING`,
		deploymentID, pullRequestID, carried); err != nil {
		return false, err
	}
	newly := false
	if carried {
		if newly, err = markDeployed(ctx, tx, pullRequestID, deploymentID); err != nil {
			return false, err
		}
	}
	return newly, tx.Commit(ctx)
}

// DueDeployChecks claims at most limit pending deployments that are due,
// oldest due first, and makes each due again only after lease, so another
// replica does not check the same deployment meanwhile.
func (s *Store) DueDeployChecks(ctx context.Context, lease time.Duration, limit int) ([]PendingDeployment, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE deployments d SET check_after = now() + $1::interval
		FROM (SELECT id FROM deployments WHERE checked_at IS NULL AND check_after <= now()
			ORDER BY check_after, id LIMIT $2 FOR UPDATE SKIP LOCKED) due
		WHERE d.id = due.id
		RETURNING d.id, d.forge, d.repo_owner, d.repo_name, d.environment, d.sha, d.deployed_at, d.check_attempts`,
		lease, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingDeployment
	for rows.Next() {
		var p PendingDeployment
		if err := rows.Scan(&p.ID, &p.Forge, &p.Owner, &p.Name, &p.Environment, &p.SHA, &p.DeployedAt, &p.Attempts); err != nil {
			return nil, err
		}
		p.DeployedAt = p.DeployedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// CompleteDeployCheck records that every candidate pull request of
// deployment id was compared, or that none can be.
func (s *Store) CompleteDeployCheck(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE deployments SET checked_at = now() WHERE id = $1 AND checked_at IS NULL`, id)
	return err
}

// DeferDeployCheck keeps deployment id pending. After a pass that compared
// at least one pull request (failed false) it is due at once. After a pass
// in which every comparison failed it waits backoff, doubled per such pass
// in a row and at most maxBackoff; the maxAttempts-th such pass gives the
// deployment up and reports true.
func (s *Store) DeferDeployCheck(ctx context.Context, id int64, failed bool, backoff, maxBackoff time.Duration, maxAttempts int) (bool, error) {
	var gaveUp bool
	err := s.pool.QueryRow(ctx, `
		UPDATE deployments SET
			check_attempts = CASE WHEN $2 THEN check_attempts + 1 ELSE 0 END,
			check_after = CASE WHEN $2 THEN now() + least($3::interval * power(2, least(check_attempts, 20)), $4::interval) ELSE now() END,
			checked_at = CASE WHEN $2 AND check_attempts + 1 >= $5 THEN now() END
		WHERE id = $1 AND checked_at IS NULL
		RETURNING checked_at IS NOT NULL`, id, failed, backoff, maxBackoff, maxAttempts).Scan(&gaveUp)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return gaveUp, err
}
