package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"swarmjet/internal/domain"
)

type TaskPageOptions struct {
	ProjectID string
	// ProjectIDs is applied in SQL before pagination and aggregate counts.
	ProjectIDs       []string
	RestrictProjects bool
	Stage            string
	View             string
	Search           string
	Cursor           string
	Limit            int
}

type TaskCounts struct {
	All       int `json:"all"`
	Active    int `json:"active"`
	Waiting   int `json:"waiting"`
	Attention int `json:"attention"`
	Ended     int `json:"ended"`
	Completed int `json:"completed"`
}

type TaskPage struct {
	Items      []domain.Task `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
	Total      int           `json:"total"`
	Counts     TaskCounts    `json:"counts"`
}

type taskCursor struct {
	UpdatedAt time.Time `json:"updatedAt"`
	ID        string    `json:"id"`
}

const attentionCondition = `(NOT terminal AND (COALESCE((body->>'paused')::boolean,false) OR stage IN ('blocked','waiting_approval','waiting_manual_merge','partial_merge')))`
const admissionWaitingCondition = `(stage='queued' AND body->>'admissionStatus'='waiting')`

func (s *Store) TaskPage(ctx context.Context, options TaskPageOptions) (TaskPage, error) {
	page := TaskPage{Items: []domain.Task{}}
	if options.Limit < 1 || options.Limit > 100 {
		options.Limit = 50
	}
	var cursor taskCursor
	if options.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(options.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.UpdatedAt.IsZero() || !domain.IDPattern.MatchString(cursor.ID) {
			return page, errors.New("invalid task cursor")
		}
	}
	search := strings.TrimSpace(options.Search)
	if len(search) > 200 {
		return page, errors.New("task search is too long")
	}
	var cursorTime any
	if !cursor.UpdatedAt.IsZero() {
		cursorTime = cursor.UpdatedAt
	}
	const base = ` FROM tasks WHERE ($1='' OR project_id=$1) AND ($2='' OR stage=$2) AND ($3='' OR id ILIKE '%'||$3||'%' OR body->'issue'->>'title' ILIKE '%'||$3||'%' OR body->'issue'->>'iid' ILIKE '%'||$3||'%' OR body->'project'->>'name' ILIKE '%'||$3||'%') AND ($4='all' OR $4='' OR ($4='ended' AND terminal) OR ($4='waiting' AND ` + admissionWaitingCondition + `) OR ($4='attention' AND ` + attentionCondition + `) OR ($4='active' AND NOT terminal AND NOT ` + attentionCondition + ` AND NOT ` + admissionWaitingCondition + `)) AND (NOT $5::boolean OR project_id=ANY($6::text[]))`
	args := []any{options.ProjectID, options.Stage, search, options.View, options.RestrictProjects, options.ProjectIDs}
	rows, err := s.Pool.Query(ctx, `SELECT body,updated_at,id`+base+` AND ($7::timestamptz IS NULL OR (updated_at,id)<($7,$8)) ORDER BY updated_at DESC,id DESC LIMIT $9`, append(args, cursorTime, cursor.ID, options.Limit+1)...)
	if err != nil {
		return page, err
	}
	cursors := []taskCursor{}
	for rows.Next() {
		var body []byte
		var task domain.Task
		var rowCursor taskCursor
		if err = rows.Scan(&body, &rowCursor.UpdatedAt, &rowCursor.ID); err != nil {
			break
		}
		if err = json.Unmarshal(body, &task); err != nil {
			break
		}
		page.Items = append(page.Items, task)
		cursors = append(cursors, rowCursor)
		if len(page.Items) == options.Limit+1 {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Items) > options.Limit {
		page.Items = page.Items[:options.Limit]
		encoded, _ := json.Marshal(cursors[options.Limit-1])
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*)`+base, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE NOT terminal AND NOT `+attentionCondition+` AND NOT `+admissionWaitingCondition+`),count(*) FILTER(WHERE `+admissionWaitingCondition+`),count(*) FILTER(WHERE `+attentionCondition+`),count(*) FILTER(WHERE terminal),count(*) FILTER(WHERE stage='completed') FROM tasks WHERE ($1='' OR project_id=$1) AND (NOT $2::boolean OR project_id=ANY($3::text[]))`, options.ProjectID, options.RestrictProjects, options.ProjectIDs).Scan(&page.Counts.All, &page.Counts.Active, &page.Counts.Waiting, &page.Counts.Attention, &page.Counts.Ended, &page.Counts.Completed); err != nil {
		return page, err
	}
	return page, nil
}
