package comment

import (
	"context"
	"fmt"
	"github.com/fatih/color"
	log "github.com/sirupsen/logrus"
	"sort"
)

var defaultTag = "infracost-comment"

// Comment is an interface that represents a comment on any platform. It wraps
// the platform specific comment structures and is used to abstract the
// logic for finding, creating, updating, and deleting the comments.
type Comment interface {
	// Body returns the body of the comment.
	Body() string

	// Ref returns the reference of the comment, this can be a URL to the HTML page of the comment.
	Ref() string

	// Less compares the comment to another comment and returns true if this
	// comment should be sorted before the other comment.
	Less(c Comment) bool

	// IsHidden returns true if the comment is hidden or minimized.
	IsHidden() bool
}

// PlatformHandler is an interface that represents a platform specific handler.
// It is used to call the platform-specific APIs for finding, creating, updating
// and deleting comments.
type PlatformHandler interface {
	// CallFindMatchingComments calls the platform-specific API to find
	// comments that match the given tag, which has been embedded at the beginning
	// of the comment.
	CallFindMatchingComments(ctx context.Context, tag string) ([]Comment, error)

	// CallCreateComment calls the platform-specific API to create a new comment.
	CallCreateComment(ctx context.Context, body string) (Comment, error)

	// CallUpdateComment calls the platform-specific API to update the body of a comment.
	CallUpdateComment(ctx context.Context, comment Comment, body string) error

	// CallDeleteComment calls the platform-specific API to delete the comment.
	CallDeleteComment(ctx context.Context, comment Comment) error

	// CallHideComment calls the platform-specific API to minimize the comment.
	// This functionality is not supported by all platforms, in which case this
	// will throw a NotImplemented error.
	CallHideComment(ctx context.Context, comment Comment) error

	// AddMarkdownTag adds a tag to the given string.
	AddMarkdownTag(s string, tag string) string
}

// CommentHandler contains the logic for finding, creating, updating and deleting comments
// on any platform. It uses a PlatformHandler to call the platform-specific APIs.
type CommentHandler struct { //nolint
	PlatformHandler PlatformHandler
	Tag             string
}

// NewCommentHandler creates a new CommentHandler.
func NewCommentHandler(ctx context.Context, platformHandler PlatformHandler, tag string) *CommentHandler {
	if tag == "" {
		tag = defaultTag
	}

	return &CommentHandler{
		PlatformHandler: platformHandler,
		Tag:             tag,
	}
}

// CommentWithBehavior parses the behavior and calls the corresponding *Comment method. Returns
// boolean indicating if the comment was actually posted.
func (h *CommentHandler) CommentWithBehavior(ctx context.Context, skipNoDiff bool, behavior, body string) (bool, error) {
	var commentPosted bool
	var err error

	switch behavior {
	case "update":
		commentPosted, err = h.UpdateComment(ctx, skipNoDiff, body)
	case "new":
		err = h.NewComment(ctx, body)
		if err == nil {
			commentPosted = true
		}
	case "hide-and-new":
		commentPosted, err = h.HideAndNewComment(ctx, skipNoDiff, body)
	case "delete-and-new":
		commentPosted, err = h.DeleteAndNewComment(ctx, skipNoDiff, body)
	default:
		return commentPosted, fmt.Errorf("Unable to perform unknown behavior: %v", behavior)
	}

	return commentPosted, err
}

// matchingComments returns all comments that match the tag.
func (h *CommentHandler) matchingComments(ctx context.Context) ([]Comment, error) {
	log.Infof("Finding matching comments for tag %s", h.Tag)

	matchingComments, err := h.PlatformHandler.CallFindMatchingComments(ctx, h.Tag)
	if err != nil {
		return nil, h.newPlatformError(err)
	}

	if len(matchingComments) == 1 {
		log.Info("Found 1 matching comment")
	} else {
		log.Infof("Found %d matching comments", len(matchingComments))
	}

	return matchingComments, nil
}

// LatestMatchingComment returns the latest matching comment.
func (h *CommentHandler) LatestMatchingComment(ctx context.Context) (Comment, error) {
	matchingComments, err := h.matchingComments(ctx)
	if err != nil {
		return nil, err
	}

	sort.Slice(matchingComments, func(i, j int) bool {
		return matchingComments[i].Less(matchingComments[j])
	})

	if len(matchingComments) == 0 {
		return nil, nil
	}

	return matchingComments[len(matchingComments)-1], nil
}

// UpdateComment updates the comment with the given body. Returns boolean indicating if the comment was actually posted.
func (h *CommentHandler) UpdateComment(ctx context.Context, skipNoDiff bool, body string) (bool, error) {
	bodyWithTag := h.PlatformHandler.AddMarkdownTag(body, h.Tag)

	latestMatchingComment, err := h.LatestMatchingComment(ctx)
	if err != nil {
		return false, err
	}

	if latestMatchingComment != nil {
		if latestMatchingComment.Body() == bodyWithTag {
			log.Infof("Not updating comment since the latest one matches exactly: %s", color.HiBlueString(latestMatchingComment.Ref()))
			return false, nil
		}

		log.Infof("Updating comment %s", color.HiBlueString(latestMatchingComment.Ref()))

		err := h.PlatformHandler.CallUpdateComment(ctx, latestMatchingComment, bodyWithTag)
		if err != nil {
			return false, h.newPlatformError(err)
		}
	} else {
		if skipNoDiff {
			log.Infof("Not creating initial comment since there is no resource or cost difference")
			return false, nil
		}

		log.Info("Creating new comment")

		comment, err := h.PlatformHandler.CallCreateComment(ctx, bodyWithTag)
		if err != nil {
			return false, h.newPlatformError(err)
		}

		log.Infof("Created new comment %s", color.HiBlueString(comment.Ref()))
	}

	return true, nil
}

// NewComment creates a new comment with the given body.
func (h *CommentHandler) NewComment(ctx context.Context, body string) error {
	bodyWithTag := h.PlatformHandler.AddMarkdownTag(body, h.Tag)

	log.Info("Creating new comment")

	comment, err := h.PlatformHandler.CallCreateComment(ctx, bodyWithTag)
	if err != nil {
		return h.newPlatformError(err)
	}

	log.Infof("Created new comment: %s", color.HiBlueString(comment.Ref()))

	return err
}

// HideAndNewComment hides/minimizes all existing matching comment and creates a new one with the given body. Returns
// // boolean indicating if the comment was actually posted.
func (h *CommentHandler) HideAndNewComment(ctx context.Context, skipNoDiff bool, body string) (bool, error) {
	matchingComments, err := h.matchingComments(ctx)
	if err != nil {
		return false, err
	}

	if len(matchingComments) == 0 && skipNoDiff {
		log.Infof("Not creating initial comment since there is no resource or cost difference")
		return false, nil
	}

	err = h.hideComments(ctx, matchingComments)
	if err != nil {
		return false, err
	}

	err = h.NewComment(ctx, body)
	if err != nil {
		return false, err
	}

	return true, nil
}

// hideComments hides/minimizes all the given comments.
func (h *CommentHandler) hideComments(ctx context.Context, comments []Comment) error {
	visibleComments := []Comment{}

	for _, comment := range comments {
		if !comment.IsHidden() {
			visibleComments = append(visibleComments, comment)
		}
	}

	hiddenCommentCount := len(comments) - len(visibleComments)

	if hiddenCommentCount == 1 {
		log.Info("1 comment is already hidden")
	} else if hiddenCommentCount > 0 {
		log.Infof("%d comments are already hidden", hiddenCommentCount)
	}

	if len(visibleComments) == 1 {
		log.Info("Hiding 1 comment")
	} else {
		log.Infof("Hiding %d comments", len(visibleComments))
	}

	for _, comment := range visibleComments {
		log.Infof("Hiding comment %s", color.HiBlueString(comment.Ref()))
		err := h.PlatformHandler.CallHideComment(ctx, comment)
		if err != nil {
			return h.newPlatformError(err)
		}
	}

	return nil
}

// DeleteAndNewComment deletes all existing matching comments and creates a new one with the given body. Returns
// boolean indicating if the comment was actually posted.
func (h *CommentHandler) DeleteAndNewComment(ctx context.Context, skipNoDiff bool, body string) (bool, error) {
	matchingComments, err := h.matchingComments(ctx)
	if err != nil {
		return false, err
	}

	if len(matchingComments) == 0 && skipNoDiff {
		log.Infof("Not creating initial comment since there is no resource or cost difference")
		return false, nil
	}

	err = h.deleteComments(ctx, matchingComments)
	if err != nil {
		return false, err
	}

	err = h.NewComment(ctx, body)
	if err != nil {
		return false, err
	}

	return true, nil
}

// deleteComments hides/minimizes all the given comments.
func (h *CommentHandler) deleteComments(ctx context.Context, comments []Comment) error {
	if len(comments) == 1 {
		log.Info("Deleting 1 comment")
	} else {
		log.Infof("Deleting %d comments", len(comments))
	}

	for _, comment := range comments {
		log.Infof("Deleting comment %s", color.HiBlueString(comment.Ref()))
		err := h.PlatformHandler.CallDeleteComment(ctx, comment)
		if err != nil {
			return h.newPlatformError(err)
		}
	}

	return nil
}

// newPlatformError wraps a platform error with multi-line formatting and a link to the docs
func (h *CommentHandler) newPlatformError(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s\n%w\n\n%s",
		"The pull request comment was generated successfully but could not be posted:",
		err,
		"See https://infracost.io/docs/troubleshooting/#5-posting-comments for help.")
}
