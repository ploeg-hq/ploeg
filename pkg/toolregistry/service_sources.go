package toolregistry

import (
	"context"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

// SourceRequest is a create or replace of a skill source. Credential nil
// keeps a stored one on replace.
type SourceRequest struct {
	Key        string
	Name       string
	Kind       string
	Ref        string
	URL        string
	Commit     string
	Digest     string
	Credential *string
}

// SourceResult is a change's outcome, or a dry run's plan.
type SourceResult struct {
	Source  SkillSource
	Created bool
	Checks  []Check
}

func (r SourceRequest) input(hasStored bool) SourceInput {
	in := SourceInput{Key: r.Key, Name: r.Name, Kind: r.Kind, Ref: r.Ref, URL: r.URL, Commit: r.Commit, Digest: r.Digest,
		HasCredential: r.Credential != nil && *r.Credential != "" || r.Credential == nil && hasStored}
	if r.Credential != nil {
		in.CredentialLen = len(*r.Credential)
	}
	return in
}

func pullCredential(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	if user, pass, ok := strings.Cut(raw, ":"); ok && !strings.ContainsAny(user, " \t") && user != "" {
		return map[string]string{"username": user, "password": pass}
	}
	return map[string]string{"token": raw}
}

func (s *Service) fetch(ctx context.Context, src SkillSource, credential string) (Bundle, []Check, *Error) {
	if s.Fetcher == nil {
		return Bundle{}, nil, refuse(422, "source_unreachable", "This installation fetches no skill sources.")
	}
	b, e := s.Fetcher.Fetch(ctx, src, pullCredential(credential))
	if e != nil {
		return Bundle{}, []Check{{Name: "fetch", OK: false, Detail: e.Message}}, e
	}
	checks := []Check{{Name: "digest", OK: true, Detail: b.Digest}}
	for _, name := range b.Skills {
		checks = append(checks, Check{Name: "skill", OK: true, Detail: name})
	}
	return b, checks, nil
}

// CreateSource validates, fetches, verifies and stores a skill source and
// its bundle; dryRun stops after verifying and writes nothing.
func (s *Service) CreateSource(ctx context.Context, audit Audit, req SourceRequest, dryRun bool) (SourceResult, *Error) {
	if e := s.refuseRuntime(); e != nil {
		return SourceResult{}, e
	}
	if e := s.Policy.CheckSource(ctx, req.input(false)); e != nil {
		return SourceResult{}, e
	}
	if s.staticSourceKey(req.Key) {
		return SourceResult{}, refuseField(409, "conflicts_with_static", "key", "The configuration file registers a skill source with this key; static wins.")
	}
	credential := ""
	if req.Credential != nil {
		credential = *req.Credential
	}
	src := SkillSource{Tenant: s.tenant(), Key: req.Key, Name: req.Name, Kind: req.Kind, Ref: req.Ref, URL: req.URL, Commit: req.Commit,
		Digest: req.Digest, Source: FromRuntime, State: "active"}
	if src.Kind == SourceOCI {
		src.Digest = src.Ref[strings.LastIndex(src.Ref, "@")+1:]
	}
	b, checks, e := s.fetch(ctx, src, credential)
	result := SourceResult{Source: src, Checks: checks}
	if e != nil {
		return result, e
	}
	result.Source.BundleDigest, result.Source.Skills = b.Digest, b.Skills
	if dryRun {
		return result, nil
	}
	if credential != "" && !secretstore.Configured(s.Secrets) {
		return result, refuse(422, "store_unavailable", "No secret store is configured, so a credential cannot be kept; nothing changed.")
	}
	id, err := newID("skl", s.now())
	if err != nil {
		return result, storeFault(err)
	}
	result.Source.ID = id
	if err := s.Store.PutSkillBundle(ctx, b.Digest, b.Archive(), b.Skills); err != nil {
		return result, storeFault(err)
	}
	path := ""
	if credential != "" {
		path = "skill-sources/" + id
		ref, err := s.Secrets.Put(ctx, path, map[string]string{"value": credential})
		if err != nil {
			return result, refuse(422, "store_unavailable", "The secret store refused the credential; nothing changed.")
		}
		result.Source.Credential = ref
	}
	out, created, err := s.Store.CreateSkillSource(ctx, result.Source, s.limit(), audit)
	if path != "" && (err != nil || !created) {
		_ = s.Secrets.Delete(ctx, path)
	}
	if err != nil {
		return result, s.mapStoreError(err, "skill source")
	}
	result.Source, result.Created = out, created
	return result, nil
}

// ReplaceSource replaces a runtime skill source at revision.
func (s *Service) ReplaceSource(ctx context.Context, audit Audit, ref string, revision int64, req SourceRequest, dryRun bool) (SourceResult, *Error) {
	if e := s.refuseRuntime(); e != nil {
		return SourceResult{}, e
	}
	current, e := s.Source(ctx, ref)
	if e != nil {
		return SourceResult{}, e
	}
	if current.Source == FromStatic {
		return SourceResult{}, refuse(409, "conflicts_with_static", "This skill source is registered in the configuration file; change it there.")
	}
	if e := s.Policy.CheckSource(ctx, req.input(!current.Credential.IsZero())); e != nil {
		return SourceResult{}, e
	}
	if !strings.EqualFold(req.Key, current.Key) && s.staticSourceKey(req.Key) {
		return SourceResult{}, refuseField(409, "conflicts_with_static", "key", "The configuration file registers a skill source with this key; static wins.")
	}
	if current.Revision != revision {
		return SourceResult{}, refuse(412, "revision_stale", fmt.Sprintf("The skill source's revision is %d.", current.Revision))
	}
	credential := ""
	if req.Credential != nil {
		credential = *req.Credential
	} else if !current.Credential.IsZero() {
		values, err := s.Secrets.Get(ctx, current.Credential)
		if err != nil {
			return SourceResult{}, refuse(422, "store_unavailable", "The stored credential could not be read; nothing changed.")
		}
		credential = values["value"]
	}
	next := SkillSource{ID: current.ID, Tenant: s.tenant(), Key: req.Key, Name: req.Name, Kind: req.Kind, Ref: req.Ref, URL: req.URL,
		Commit: req.Commit, Digest: req.Digest, Source: FromRuntime, State: "active"}
	if next.Kind == SourceOCI {
		next.Digest = next.Ref[strings.LastIndex(next.Ref, "@")+1:]
	}
	b, checks, e := s.fetch(ctx, next, credential)
	result := SourceResult{Source: next, Checks: checks}
	if e != nil {
		return result, e
	}
	result.Source.BundleDigest, result.Source.Skills = b.Digest, b.Skills
	if dryRun {
		result.Source.Revision = current.Revision
		return result, nil
	}
	if err := s.Store.PutSkillBundle(ctx, b.Digest, b.Archive(), b.Skills); err != nil {
		return result, storeFault(err)
	}
	if req.Credential != nil && *req.Credential != "" {
		ref, err := s.Secrets.Put(ctx, "skill-sources/"+current.ID, map[string]string{"value": *req.Credential})
		if err != nil {
			return result, refuse(422, "store_unavailable", "The secret store refused the credential; nothing changed.")
		}
		result.Source.Credential = ref
	}
	_, out, err := s.Store.ReplaceSkillSource(ctx, result.Source, revision, audit)
	if err != nil {
		return result, s.mapStoreError(err, "skill source")
	}
	result.Source = out
	return result, nil
}

// RemoveSource removes a runtime skill source and its stored credential.
func (s *Service) RemoveSource(ctx context.Context, audit Audit, ref string) *Error {
	if e := s.refuseRuntime(); e != nil {
		return e
	}
	current, e := s.Source(ctx, ref)
	if e != nil {
		if e.Code == "unknown_source" {
			return nil
		}
		return e
	}
	if current.Source == FromStatic {
		return refuse(409, "conflicts_with_static", "This skill source is registered in the configuration file; remove it there.")
	}
	if teams := s.staticTeamsNaming(current.ID); len(teams) > 0 {
		return &Error{Status: 409, Code: "source_in_use", Message: "Attachments name this skill source: " + strings.Join(teams, ", ") + "."}
	}
	if _, err := s.Store.RemoveSkillSource(ctx, s.tenant(), current.ID, audit); err != nil {
		if err == ErrNotFound {
			return nil
		}
		return s.mapStoreError(err, "skill source")
	}
	if !current.Credential.IsZero() {
		_ = s.Secrets.Delete(ctx, current.Credential.Path)
	}
	return nil
}

// SkillBundle is the stored archive of a bundle digest.
func (s *Service) SkillBundle(ctx context.Context, digest string) ([]byte, *Error) {
	if !digestPattern.MatchString(digest) {
		return nil, refuse(404, "unknown_source", "No bundle has this digest.")
	}
	archive, err := s.Store.SkillBundle(ctx, digest)
	if err == ErrNotFound {
		return nil, refuse(404, "unknown_source", "No bundle has this digest.")
	}
	if err != nil {
		return nil, storeFault(err)
	}
	return archive, nil
}

// LoadStatic fetches and stores every static skill source's bundle and
// records its digest and skills. A source that cannot be fetched or fails
// its pin is left out of every grant, with the reason logged; a Run never
// gets content that does not match its pin.
func (s *Service) LoadStatic(ctx context.Context) {
	kept := s.StaticSources[:0]
	for _, src := range s.StaticSources {
		credential := ""
		if env := s.StaticSourceEnv[src.Key]; env != "" {
			credential = s.env(env)
		}
		b, _, e := s.fetch(ctx, src, credential)
		if e != nil {
			if s.Log != nil {
				s.Log.Warn("static skill source left out", "source", src.Key, "code", e.Code, "reason", e.Message)
			}
			continue
		}
		if err := s.Store.PutSkillBundle(ctx, b.Digest, b.Archive(), b.Skills); err != nil {
			if s.Log != nil {
				s.Log.Warn("static skill source left out", "source", src.Key, "code", "store_unavailable")
			}
			continue
		}
		src.BundleDigest, src.Skills = b.Digest, b.Skills
		kept = append(kept, src)
	}
	s.StaticSources = kept
}
