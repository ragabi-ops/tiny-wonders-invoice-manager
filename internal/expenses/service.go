package expenses

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/storage"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// MaxAttachmentBytes caps an upload. A photographed receipt is well under this;
// anything larger is a mistake or an attack (SECURITY.md 6).
const MaxAttachmentBytes int64 = 20 << 20 // 20 MiB

// allowedContentTypes is what an expense attachment may be: a photo of a
// receipt or a supplier's PDF. The type is decided by sniffing the bytes, never
// by trusting the uploader's filename or declared type.
var allowedContentTypes = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/heic":      ".heic",
	"image/webp":      ".webp",
	"application/pdf": ".pdf",
}

// ErrUnsupportedFileType means the uploaded bytes are not an accepted type.
var ErrUnsupportedFileType = fmt.Errorf("unsupported attachment type")

// Service performs expense use cases.
type Service struct {
	pool  *db.Pool
	repo  *Repository
	audit *audit.Recorder
	store *storage.Store
}

// NewService wires the expense service.
func NewService(pool *db.Pool, repo *Repository, recorder *audit.Recorder, store *storage.Store) *Service {
	return &Service{pool: pool, repo: repo, audit: recorder, store: store}
}

// List returns a page of expenses.
func (s *Service) List(ctx context.Context, p ListParams) (*Page, error) {
	return s.repo.List(ctx, s.pool, p)
}

// Get returns one expense.
func (s *Service) Get(ctx context.Context, id string) (*Expense, error) {
	return s.repo.Get(ctx, s.pool, id)
}

// Categories returns the categories already in use.
func (s *Service) Categories(ctx context.Context) ([]string, error) {
	return s.repo.Categories(ctx, s.pool)
}

// TotalsByCategory summarizes expenses in a date range.
func (s *Service) TotalsByCategory(ctx context.Context, fromDate, toDate, activityID string) ([]CategoryTotal, error) {
	return s.repo.TotalsByCategory(ctx, s.pool, fromDate, toDate, activityID)
}

// Create adds an expense.
func (s *Service) Create(ctx context.Context, actor *users.User, in Input, requestID string) (*Expense, error) {
	in.Normalize()

	var created *Expense
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := s.repo.Create(ctx, tx, in, actor.ID)
		if err != nil {
			return err
		}
		if created, err = s.repo.Get(ctx, tx, id); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpExpenseCreated,
			EntityType:  audit.EntityExpense,
			EntityID:    id,
			RequestID:   requestID,
			After:       created,
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Update replaces an expense's details.
func (s *Service) Update(ctx context.Context, actor *users.User, id string, in Input, requestID string) (*Expense, error) {
	in.Normalize()

	var updated *Expense
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.repo.Update(ctx, tx, id, in, actor.ID); err != nil {
			return err
		}
		if updated, err = s.repo.Get(ctx, tx, id); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpExpenseUpdated,
			EntityType:  audit.EntityExpense,
			EntityID:    id,
			RequestID:   requestID,
			Before:      before,
			After:       updated,
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// Delete removes an expense and its attachments.
func (s *Service) Delete(ctx context.Context, actor *users.User, id, requestID string) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}

		// The attachment rows go with the expense; their files are left on disk
		// and swept later. Deleting the file first would risk losing it while
		// the transaction still might roll back.
		if _, err := tx.Exec(ctx,
			`DELETE FROM attachments WHERE entity_type = 'expense' AND entity_id = $1`, id); err != nil {
			return err
		}
		if err := s.repo.Delete(ctx, tx, id); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpExpenseDeleted,
			EntityType:  audit.EntityExpense,
			EntityID:    id,
			RequestID:   requestID,
			Before:      before,
		})
	})
}

// AddAttachment stores an uploaded file against an expense.
//
// The content type is decided by sniffing the bytes: a file named `receipt.pdf`
// that is really an executable is refused, and the stored name is derived from
// a generated identifier, never from what the uploader called it.
func (s *Service) AddAttachment(ctx context.Context, actor *users.User, expenseID, filename string, data []byte, requestID string) (*Attachment, error) {
	if int64(len(data)) > MaxAttachmentBytes {
		return nil, fmt.Errorf("%w: file is larger than %d bytes", ErrUnsupportedFileType, MaxAttachmentBytes)
	}

	contentType, extension, ok := sniff(data)
	if !ok {
		return nil, ErrUnsupportedFileType
	}

	var stored *Attachment
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Confirm the owner exists before writing anything to disk.
		expense, err := s.repo.Get(ctx, tx, expenseID)
		if err != nil {
			return err
		}

		id, err := s.repo.NewAttachmentID(ctx, tx)
		if err != nil {
			return err
		}

		sha := storage.Fingerprint(data)
		path := storage.AttachmentPath(id, expense.CreatedAt, extension)
		if _, err := s.store.Save(path, storage.Blob{Bytes: data, SHA256: sha}); err != nil {
			return err
		}

		if err := s.repo.InsertAttachmentWithID(ctx, tx, id, expenseID,
			sanitizeFilename(filename, extension), contentType, sha, path,
			int64(len(data)), actor.ID); err != nil {
			return err
		}

		attachments, err := s.repo.Attachments(ctx, tx, expenseID)
		if err != nil {
			return err
		}
		for index := range attachments {
			if attachments[index].ID == id {
				stored = &attachments[index]
			}
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpExpenseAttachmentAdded,
			EntityType:  audit.EntityExpense,
			EntityID:    expenseID,
			RequestID:   requestID,
			After:       map[string]any{"attachment_id": id, "sha256": sha, "bytes": len(data)},
		})
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// Attachment reads a stored attachment, verified against its recorded hash.
func (s *Service) Attachment(ctx context.Context, id string) ([]byte, *AttachmentRef, error) {
	ref, err := s.repo.Attachment(ctx, s.pool, id)
	if err != nil {
		return nil, nil, err
	}

	data, err := s.store.Load(ref.Path, ref.SHA256)
	if err != nil {
		return nil, nil, err
	}
	return data, ref, nil
}

// RemoveAttachment deletes an attachment row. The file itself stays on disk:
// storage is cheap, and an accidental delete of a receipt photo is not
// recoverable if the bytes are gone.
func (s *Service) RemoveAttachment(ctx context.Context, actor *users.User, expenseID, attachmentID, requestID string) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		path, err := s.repo.DeleteAttachment(ctx, tx, attachmentID)
		if err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpExpenseAttachmentRemoved,
			EntityType:  audit.EntityExpense,
			EntityID:    expenseID,
			RequestID:   requestID,
			Before:      map[string]any{"attachment_id": attachmentID, "storage_path": path},
			Reason:      "the stored file is retained on disk",
		})
	})
}

// sniff identifies a file from its leading bytes.
func sniff(data []byte) (contentType, extension string, ok bool) {
	if len(data) < 12 {
		return "", "", false
	}

	switch {
	case string(data[:4]) == "%PDF":
		contentType = "application/pdf"
	case data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		contentType = "image/jpeg"
	case string(data[1:4]) == "PNG":
		contentType = "image/png"
	case string(data[4:8]) == "ftyp" && (string(data[8:12]) == "heic" || string(data[8:12]) == "heix" ||
		string(data[8:12]) == "hevc" || string(data[8:12]) == "mif1"):
		contentType = "image/heic"
	case string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		contentType = "image/webp"
	default:
		return "", "", false
	}

	return contentType, allowedContentTypes[contentType], true
}

// sanitizeFilename keeps a readable name for the UI and for exports while
// stripping anything that could be read as a path.
func sanitizeFilename(name, extension string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.TrimLeft(name, ".")

	if name == "" {
		name = "attachment" + extension
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	return name
}
