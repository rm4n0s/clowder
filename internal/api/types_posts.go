package api

import (
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
)

// dateLayout is how a due date is written: a day, without a time.
const dateLayout = "2006-01-02"

// taskJSON is a task of a post in the answers of the API.
type taskJSON struct {
	ID        uint64        `json:"id"`
	Title     string        `json:"title"`
	ColumnID  uint64        `json:"column_id"`
	Assignees []userRefJSON `json:"assignees"`
	DueDate   *string       `json:"due_date"`
}

// attachmentJSON is a file of a post in the answers of the API.
type attachmentJSON struct {
	ID          uint64 `json:"id"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Width       *int   `json:"width"`
	Height      *int   `json:"height"`
	URL         string `json:"url"`
}

// reactionJSON is one emoji on a post in the answers of the API.
type reactionJSON struct {
	Emoji string        `json:"emoji"`
	Count int           `json:"count"`
	Users []userRefJSON `json:"users"`
	Mine  bool          `json:"mine"`
}

// postJSON is a post or a comment in the answers of the API.
type postJSON struct {
	ID           uint64           `json:"id"`
	RoomID       uint64           `json:"room_id"`
	ParentID     *uint64          `json:"parent_id"`
	RootID       uint64           `json:"root_id"`
	Author       *userRefJSON     `json:"author"`
	Format       string           `json:"format"`
	Body         *string          `json:"body"`
	HTML         *string          `json:"html"`
	Tags         []string         `json:"tags"`
	Tasks        []taskJSON       `json:"tasks"`
	Attachments  []attachmentJSON `json:"attachments"`
	Reactions    []reactionJSON   `json:"reactions"`
	CommentCount int              `json:"comment_count"`
	CreatedAt    string           `json:"created_at"`
	EditedAt     *string          `json:"edited_at"`
	Deleted      bool             `json:"deleted"`
	CanEdit      bool             `json:"can_edit"`
	CanDelete    bool             `json:"can_delete"`
	// Following is true when the user follows the post (for a comment: the
	// top-level post of its thread).
	Following bool `json:"following"`
}

// attachmentURL is the path a client downloads an attachment from.
func attachmentURL(id uint64) string {
	return Prefix + "/attachments/" + strconv.FormatUint(id, 10)
}

// postOf makes the JSON of a post or comment. A deleted one has no author, body
// or html; its tags, tasks, attachments and reactions are empty already.
func postOf(p app.Post) postJSON {
	out := postJSON{
		ID:           p.ID,
		RoomID:       p.RoomID,
		RootID:       p.RootID,
		Format:       string(p.Format),
		Tags:         p.Tags,
		Tasks:        make([]taskJSON, len(p.Tasks)),
		Attachments:  make([]attachmentJSON, len(p.Attachments)),
		Reactions:    reactionsOf(p.Reactions),
		CommentCount: p.CommentCount,
		CreatedAt:    rfc3339(p.CreatedAt),
		Deleted:      p.Deleted,
		CanEdit:      p.CanEdit,
		CanDelete:    p.CanDelete,
		Following:    p.Following,
	}
	if out.RootID == 0 {
		out.RootID = p.ID
	}
	if p.ParentID != 0 {
		parent := p.ParentID
		out.ParentID = &parent
	}
	if p.EditedAt != nil {
		edited := rfc3339(*p.EditedAt)
		out.EditedAt = &edited
	}
	if !p.Deleted {
		body, html := p.Body, string(p.HTML)
		out.Body, out.HTML = &body, &html
		if p.Author.ID != 0 {
			author := userRefOf(p.Author)
			out.Author = &author
		}
	}
	for i, t := range p.Tasks {
		out.Tasks[i] = taskOf(t)
	}
	for i, a := range p.Attachments {
		out.Attachments[i] = attachmentOf(a)
	}
	return out
}

// taskOf makes the JSON of a task.
func taskOf(t app.TaskCard) taskJSON {
	out := taskJSON{ID: t.ID, Title: t.Title, ColumnID: t.ColumnID, Assignees: userRefsOf(t.Assignees)}
	if t.DueDate != nil {
		day := t.DueDate.UTC().Format(dateLayout)
		out.DueDate = &day
	}
	return out
}

// attachmentOf makes the JSON of an attachment.
func attachmentOf(a app.AttachmentView) attachmentJSON {
	out := attachmentJSON{ID: a.ID, FileName: a.FileName, ContentType: a.ContentType, Size: a.Size, URL: attachmentURL(a.ID)}
	if a.Width > 0 && a.Height > 0 {
		w, h := a.Width, a.Height
		out.Width, out.Height = &w, &h
	}
	return out
}

// reactionsOf makes the JSON of the reactions of a post; never nil.
func reactionsOf(reactions []app.ReactionCount) []reactionJSON {
	out := make([]reactionJSON, len(reactions))
	for i, r := range reactions {
		out[i] = reactionJSON{Emoji: r.Emoji, Count: r.Count, Users: userRefsOf(r.Users), Mine: r.Mine}
	}
	return out
}

// feedResponse is the answer of GET /rooms/{id}/posts.
type feedResponse struct {
	Posts      []postJSON `json:"posts"`
	NextBefore *uint64    `json:"next_before"`
}

// threadCommentJSON is a comment of a thread with how deep it is.
type threadCommentJSON struct {
	Depth   int      `json:"depth"`
	Comment postJSON `json:"comment"`
}

// threadResponse is the answer of GET /posts/{id}/thread.
type threadResponse struct {
	Post     postJSON            `json:"post"`
	Comments []threadCommentJSON `json:"comments"`
	FocusID  *uint64             `json:"focus_id"`
}

// reactionsResponse is the answer of the routes of the reactions: the
// reactions of the post as the caller sees them.
type reactionsResponse struct {
	Reactions []reactionJSON `json:"reactions"`
}

// tagsResponse is the answer of GET /tags.
type tagsResponse struct {
	Tags []string `json:"tags"`
}
