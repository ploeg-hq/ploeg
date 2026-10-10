package provider

import "context"

// Attachment is a file Ploeg stores on a forge for a comment to embed.
type Attachment struct {
	// Name is the file name the forge shows, e.g. "comment-42.svg".
	Name        string
	ContentType string
	Data        []byte
}

// CommentAttacher is implemented by a forge that can store a file for a pull
// request comment (ADR-0055). The returned URL is what the comment's
// Markdown embeds; it is absolute on Forgejo and project-relative on GitLab.
// A forge that refuses the file, by type or by size, returns an error and
// the caller posts the comment without it.
type CommentAttacher interface {
	// AttachToComment stores file for comment id on pull request pr of repo.
	// It replaces an earlier attachment of the same name on that comment
	// where the forge keeps attachments per comment.
	AttachToComment(ctx context.Context, repo string, pr int, commentID int64, file Attachment) (string, error)
}
