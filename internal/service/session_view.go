package service

import (
	"context"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/domain"
	"github.com/google/uuid"
)

// VersionView is one version as the editor sees it. Width and Height are one
// frame's size; SizeBytes is the exact size of the file a download gives.
type VersionView struct {
	ID         uuid.UUID               `json:"id"`
	Seq        int                     `json:"seq"`
	Operation  contracts.Operation     `json:"operation"`
	Status     contracts.Status        `json:"status"`
	Step       *contracts.ImageStep    `json:"step,omitempty"`
	DocStep    *contracts.DocumentStep `json:"doc_step,omitempty"`
	Encode     *contracts.Encode       `json:"encode,omitempty"`
	Animation  *contracts.Animation    `json:"animation,omitempty"`
	Width      int                     `json:"width,omitempty"`
	Height     int                     `json:"height,omitempty"`
	FileName   string                  `json:"file_name,omitempty"`
	SizeBytes  int64                   `json:"size_bytes,omitempty"`
	MimeType   string                  `json:"mime_type,omitempty"`
	PreviewURL string                  `json:"preview_url,omitempty"`
	// PageCount is a document version's number of pages.
	PageCount int `json:"page_count,omitempty"`
	// Pages is filled for the head of a document session only: it is one URL
	// per page, too many to repeat for every version in the history.
	Pages     []PageView `json:"pages,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// PageView is one page of a document. ID names its preview, which several
// versions share, so the editor can keep an image it already loaded even
// though the URL is signed afresh on every read. The preview is upright;
// Rotate is how far to turn it clockwise.
type PageView struct {
	ID         string  `json:"id"`
	PreviewURL string  `json:"preview_url"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	Rotate     int     `json:"rotate"`
}

// SessionView is everything the editor needs to draw a session. Head is nil
// until the original has been opened.
type SessionView struct {
	ID         uuid.UUID      `json:"id"`
	Kind       contracts.Kind `json:"kind"`
	FileID     uuid.UUID      `json:"file_id"`
	Processing bool           `json:"processing"`
	Pending    *VersionView   `json:"pending,omitempty"`
	Error      string         `json:"error,omitempty"`
	// ErrorCode is the contracts.Fail* code behind Error, when there is one,
	// so the client can say it in the user's language.
	ErrorCode string       `json:"error_code,omitempty"`
	Head      *VersionView `json:"head,omitempty"`
	// History is the chain from the original to the head.
	History      []VersionView `json:"history"`
	CanUndo      bool          `json:"can_undo"`
	Edited       bool          `json:"edited"`
	VersionsLeft int           `json:"versions_left"`
	Original     OriginalView  `json:"original"`
}

type OriginalView struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	MimeType  string `json:"mime_type"`
}

func (s *SessionService) Get(ctx context.Context, kind contracts.Kind, id uuid.UUID) (SessionView, error) {
	session, err := s.load(ctx, kind, id)
	if err != nil {
		return SessionView{}, err
	}
	jobs, err := s.jobs.ListBySession(ctx, id)
	if err != nil {
		return SessionView{}, err
	}
	file, err := s.files.Get(ctx, session.FileID)
	if err != nil {
		return SessionView{}, err
	}

	byID := make(map[uuid.UUID]domain.Job, len(jobs))
	for _, j := range jobs {
		byID[j.ID] = j
	}

	v := SessionView{
		ID:           session.ID,
		Kind:         session.Kind,
		FileID:       session.FileID,
		Error:        session.LastError,
		History:      []VersionView{},
		VersionsLeft: max(0, contracts.MaxVersions-session.NextSeq),
		Original: OriginalView{
			Name:      file.OriginalName,
			SizeBytes: file.SizeBytes,
			MimeType:  file.MimeType,
		},
	}

	now := time.Now()
	if session.LockJobID != uuid.Nil {
		if pending, ok := byID[session.LockJobID]; ok {
			switch {
			case session.Locked(now):
				v.Processing = true
				pv := s.version(ctx, pending)
				v.Pending = &pv
			case !pending.IsTerminal():
				v.Error = "The operation took too long and was abandoned."
			}
		}
	}

	if v.Error != "" && !v.Processing {
		v.ErrorCode = lastFailureCode(jobs)
	}

	head, ok := byID[session.HeadID]
	if !ok || head.Status != contracts.StatusSucceeded {
		if ok && head.Status == contracts.StatusFailed && v.Error == "" {
			v.Error = failedMessage(session.Kind, head.ErrorCode)
			v.ErrorCode = head.ErrorCode
		}
		return v, nil
	}

	// Walk from the head back to the original; parents always exist, but the
	// walk is bounded in case of a broken chain.
	chain := []domain.Job{}
	for cur, ok := head, true; ok && len(chain) <= contracts.MaxVersions; cur, ok = byID[cur.ParentID] {
		chain = append(chain, cur)
		if cur.ParentID == uuid.Nil {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		v.History = append(v.History, s.version(ctx, chain[i]))
	}
	hv := v.History[len(v.History)-1]
	hv.Pages = s.pages(ctx, head)
	v.Head = &hv
	v.CanUndo = head.ParentID != uuid.Nil
	v.Edited = head.ID != session.SourceID
	return v, nil
}

func (s *SessionService) version(ctx context.Context, j domain.Job) VersionView {
	v := VersionView{
		ID:        j.ID,
		Seq:       j.Seq,
		Operation: j.Operation,
		Status:    j.Status,
		Step:      j.Step,
		DocStep:   j.DocStep,
		PageCount: len(j.Pages),
		Encode:    j.Encode,
		Animation: j.Animation,
		Width:     j.Width,
		Height:    j.Height,
		FileName:  j.ResultName,
		SizeBytes: j.ResultSize,
		MimeType:  j.ResultMime,
		CreatedAt: j.CreatedAt,
	}
	if j.PreviewKey != "" {
		if url, err := s.storage.ObjectURL(ctx, j.PreviewKey); err != nil {
			s.log.Error("failed to build preview url", "job_id", j.ID, "error", err)
		} else {
			v.PreviewURL = url
		}
	}
	return v
}

func (s *SessionService) pages(ctx context.Context, j domain.Job) []PageView {
	if len(j.Pages) == 0 {
		return nil
	}
	out := make([]PageView, 0, len(j.Pages))
	for _, p := range j.Pages {
		url, err := s.storage.ObjectURL(ctx, p.PreviewKey)
		if err != nil {
			s.log.Error("failed to build page preview url", "job_id", j.ID, "error", err)
			url = ""
		}
		out = append(out, PageView{
			ID:         p.PreviewKey,
			PreviewURL: url,
			Width:      p.Width,
			Height:     p.Height,
			Rotate:     p.Rotate,
		})
	}
	return out
}

// lastFailureCode is the failure code of the most recent failed version.
func lastFailureCode(jobs []domain.Job) string {
	for i := len(jobs) - 1; i >= 0; i-- {
		if jobs[i].Status == contracts.StatusFailed {
			return jobs[i].ErrorCode
		}
	}
	return ""
}
