package provider

import "context"

// CommentDeleter is implemented by a forge that can remove one of its pull
// request comments (ADR-0079). Deleting a comment that is already gone is
// not an error.
type CommentDeleter interface {
	// DeleteComment removes comment id from pull request pr of repo.
	DeleteComment(ctx context.Context, repo string, pr int, id int64) error
}
