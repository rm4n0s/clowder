package api

import "github.com/rm4n0s/clowder/internal/app"

// uploadJSON is the answer of POST /uploads: the ID that upload_ids of a post
// takes and what the server found out about the file.
type uploadJSON struct {
	ID          string `json:"id"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Width       *int   `json:"width"`
	Height      *int   `json:"height"`
}

// uploadOf makes the JSON of a staged upload.
func uploadOf(s app.StagedUpload) uploadJSON {
	u := s.Upload
	out := uploadJSON{ID: s.ID, FileName: u.FileName, ContentType: u.ContentType, Size: u.Size}
	if u.Width > 0 && u.Height > 0 {
		w, h := u.Width, u.Height
		out.Width, out.Height = &w, &h
	}
	return out
}

// boardTaskJSON is a card of the board: the task of a post, with the post it
// was written in and its place in its column.
type boardTaskJSON struct {
	taskJSON
	PostID   uint64 `json:"post_id"`
	Position int    `json:"position"`
}

// boardTaskOf makes the JSON of a card.
func boardTaskOf(t app.TaskCard) boardTaskJSON {
	return boardTaskJSON{taskJSON: taskOf(t), PostID: t.PostID, Position: t.Position}
}

// columnJSON is a column of a board with its cards.
type columnJSON struct {
	ID       uint64          `json:"id"`
	Name     string          `json:"name"`
	Position int             `json:"position"`
	Done     bool            `json:"done"`
	Tasks    []boardTaskJSON `json:"tasks"`
}

// columnOf makes the JSON of a column.
func columnOf(c app.ColumnView) columnJSON {
	out := columnJSON{ID: c.ID, Name: c.Name, Position: c.Position, Done: c.Last, Tasks: make([]boardTaskJSON, len(c.Tasks))}
	for i, t := range c.Tasks {
		out.Tasks[i] = boardTaskOf(t)
	}
	return out
}

// boardResponse is the answer of GET /rooms/{id}/board.
type boardResponse struct {
	RoomID  uint64       `json:"room_id"`
	Columns []columnJSON `json:"columns"`
}

// roomRefJSON names a room where a search result or a notification is from.
type roomRefJSON struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

// snippetSegmentJSON is a piece of the snippet of a search result.
type snippetSegmentJSON struct {
	Text  string `json:"text"`
	Match bool   `json:"match"`
}

// searchResultJSON is a post or comment that a search found.
type searchResultJSON struct {
	PostID    uint64               `json:"post_id"`
	RootID    uint64               `json:"root_id"`
	Room      roomRefJSON          `json:"room"`
	Author    userRefJSON          `json:"author"`
	CreatedAt string               `json:"created_at"`
	Kind      string               `json:"kind"`
	Snippet   []snippetSegmentJSON `json:"snippet"`
}

// searchResultOf makes the JSON of a search result. The root of a top-level
// post is the post itself.
func searchResultOf(r app.SearchResult) searchResultJSON {
	out := searchResultJSON{
		PostID:    r.PostID,
		RootID:    r.RootID,
		Room:      roomRefJSON{ID: r.RoomID, Name: r.RoomName},
		Author:    userRefOf(r.Author),
		CreatedAt: rfc3339(r.CreatedAt),
		Kind:      "post",
		Snippet:   make([]snippetSegmentJSON, len(r.Segments)),
	}
	if out.RootID == 0 {
		out.RootID = r.PostID
	}
	if r.IsComment {
		out.Kind = "comment"
	}
	for i, seg := range r.Segments {
		out.Snippet[i] = snippetSegmentJSON{Text: seg.Text, Match: seg.Match}
	}
	return out
}

// searchResponse is the answer of GET /search.
type searchResponse struct {
	Results []searchResultJSON `json:"results"`
	Page    int                `json:"page"`
	HasMore bool               `json:"has_more"`
}

// notificationJSON is a notification of the caller.
type notificationJSON struct {
	ID        uint64       `json:"id"`
	Kind      string       `json:"kind"`
	Actor     *userRefJSON `json:"actor"`
	Room      *roomRefJSON `json:"room"`
	PostID    *uint64      `json:"post_id"`
	TaskID    *uint64      `json:"task_id"`
	CreatedAt string       `json:"created_at"`
	Read      bool         `json:"read"`
}

// notificationOf makes the JSON of a notification. A part that the
// notification does not have is null: the actor of one whose actor is gone,
// the post of an addition to a room, the task of everything but an
// assignment.
func notificationOf(n app.Notification) notificationJSON {
	out := notificationJSON{ID: n.ID, Kind: string(n.Kind), CreatedAt: rfc3339(n.CreatedAt), Read: n.Read}
	if n.Actor.ID != 0 {
		actor := userRefOf(n.Actor)
		out.Actor = &actor
	}
	if n.RoomID != 0 {
		out.Room = &roomRefJSON{ID: n.RoomID, Name: n.RoomName}
	}
	if n.PostID != 0 {
		post := n.PostID
		out.PostID = &post
	}
	if n.TaskID != 0 {
		task := n.TaskID
		out.TaskID = &task
	}
	return out
}

// notificationsResponse is the answer of GET /notifications.
type notificationsResponse struct {
	Notifications []notificationJSON `json:"notifications"`
	NextBefore    *uint64            `json:"next_before"`
}
