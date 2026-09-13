package exports

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/jobs"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose the accountant export.
type Handlers struct {
	svc         *Service
	queue       *jobs.Queue
	pool        *db.Pool
	audit       *audit.Recorder
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the export HTTP layer.
func NewHandlers(svc *Service, queue *jobs.Queue, pool *db.Pool, recorder *audit.Recorder, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, queue: queue, pool: pool, audit: recorder, currentUser: currentUser}
}

// Routes mounts the export endpoints. The accountant is the audience, so they
// may request and download exports as well as the owner (plan.md 6).
func (h *Handlers) Routes(r chi.Router) {
	r.Post("/", h.request)
	r.Get("/{jobID}", h.status)
	r.Get("/{jobID}/download", h.download)
}

type exportRequest struct {
	From               string `json:"from"`
	To                 string `json:"to"`
	IncludePDFs        *bool  `json:"include_pdfs"`
	IncludeAttachments *bool  `json:"include_attachments"`
}

// request queues a build. Building can take a while on a full year, so it runs
// in the background and the operator polls for the result (plan.md 17).
func (h *Handlers) request(w http.ResponseWriter, r *http.Request) {
	var body exportRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	problems := map[string]string{}
	if body.From != "" {
		if _, err := time.Parse("2006-01-02", body.From); err != nil {
			problems["from"] = "יש להזין תאריך בפורמט YYYY-MM-DD"
		}
	}
	if body.To != "" {
		if _, err := time.Parse("2006-01-02", body.To); err != nil {
			problems["to"] = "יש להזין תאריך בפורמט YYYY-MM-DD"
		}
	}
	if len(problems) > 0 {
		httpx.WriteError(w, r, httpx.ValidationError(problems))
		return
	}

	// Files are included by default: an export without the PDFs is not the
	// hand-off plan.md 15 describes.
	includePDFs := body.IncludePDFs == nil || *body.IncludePDFs
	includeAttachments := body.IncludeAttachments == nil || *body.IncludeAttachments

	jobID, err := h.svc.Queue(r.Context(), h.currentUser(r), Request{
		From:               body.From,
		To:                 body.To,
		IncludePDFs:        includePDFs,
		IncludeAttachments: includeAttachments,
		RequestID:          httpx.RequestID(r.Context()),
	})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}

	httpx.WriteJSON(w, r, http.StatusAccepted, map[string]any{
		"job_id": jobID,
		"state":  jobs.StatePending,
	})
}

// status reports how a build is going.
func (h *Handlers) status(w http.ResponseWriter, r *http.Request) {
	job, err := h.queue.Get(r.Context(), h.pool, chi.URLParam(r, "jobID"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound.WithMessage("הייצוא לא נמצא.").WithCause(err))
		return
	}
	if job.Kind != jobs.KindBuildExport {
		httpx.WriteError(w, r, httpx.ErrNotFound.WithMessage("הייצוא לא נמצא."))
		return
	}

	response := map[string]any{
		"job_id":     job.ID,
		"state":      job.State,
		"attempts":   job.Attempts,
		"created_at": job.CreatedAt,
	}
	if job.LastError != nil {
		response["error"] = *job.LastError
	}
	if len(job.Result) > 0 {
		var result Result
		if err := json.Unmarshal(job.Result, &result); err == nil {
			response["filename"] = result.Filename
			response["bytes"] = result.Bytes
			response["sha256"] = result.SHA256
		}
	}

	httpx.WriteJSON(w, r, http.StatusOK, response)
}

// download streams a finished archive.
func (h *Handlers) download(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	job, err := h.queue.Get(r.Context(), h.pool, jobID)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound.WithMessage("הייצוא לא נמצא.").WithCause(err))
		return
	}
	if job.State != jobs.StateSucceeded || len(job.Result) == 0 {
		httpx.WriteError(w, r, httpx.ErrConflict.WithMessage("הייצוא עדיין אינו מוכן."))
		return
	}

	var result Result
	if err := json.Unmarshal(job.Result, &result); err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}

	// Load verifies the archive against the hash recorded when it was built.
	data, err := h.svc.Load(r.Context(), jobID, result.SHA256)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.
			WithMessage("קובץ הייצוא חסר או אינו תואם את החתימה השמורה.").WithCause(err))
		return
	}

	actor := h.currentUser(r)
	if err := h.audit.Record(r.Context(), h.pool, audit.Entry{
		ActorUserID: actor.ID,
		ActorEmail:  actor.Email,
		Operation:   audit.OpExportDownloaded,
		EntityType:  audit.EntityExport,
		EntityID:    jobID,
		RequestID:   httpx.RequestID(r.Context()),
		After:       map[string]any{"filename": result.Filename, "bytes": result.Bytes},
	}); err != nil {
		// The download itself is fine; a missing audit line is worth a log.
		httpx.Logger(r).Error("audit export download", "error", err)
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+result.Filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if _, err := w.Write(data); err != nil {
		httpx.Logger(r).Error("write export", "error", err)
	}
}
