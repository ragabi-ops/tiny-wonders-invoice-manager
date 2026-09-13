package settings

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/storage"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// MaxLogoBytes caps a logo upload. A print-quality mark is far smaller; more
// than this is a photograph someone uploaded by mistake.
const MaxLogoBytes int64 = 4 << 20 // 4 MiB

// ErrNoLogo means the business has not uploaded one.
var ErrNoLogo = errors.New("no logo has been uploaded")

// ErrUnsupportedLogoType means the uploaded bytes are not an image this system
// will print.
var ErrUnsupportedLogoType = errors.New("unsupported logo type")

// logoTypes are the image formats the document renderer handles reliably. SVG
// is deliberately absent: it is a script-capable document format, and it would
// be embedded into a page the renderer executes.
var logoTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
}

// SetLogo stores a new logo for the business.
//
// The previous file is left on disk on purpose. Documents issued before this
// upload recorded the old logo's path in their snapshot, and re-rendering one
// must still show the logo that was actually printed on it (plan.md 11).
func (s *Service) SetLogo(ctx context.Context, actor *users.User, filename string, data []byte, requestID string) (*BusinessProfile, error) {
	if int64(len(data)) > MaxLogoBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrUnsupportedLogoType, MaxLogoBytes)
	}

	contentType, extension, ok := sniffImage(data)
	if !ok {
		return nil, ErrUnsupportedLogoType
	}

	var updated *BusinessProfile
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.businessProfile(ctx, tx)
		if err != nil {
			return err
		}

		var logoID string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&logoID); err != nil {
			return err
		}

		sha := storage.Fingerprint(data)
		path := storage.BusinessLogoPath(logoID, before.UpdatedAt, extension)
		if _, err := s.store.Save(path, storage.Blob{Bytes: data, SHA256: sha}); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE business_profile
			SET logo_path = $1, logo_content_type = $2, logo_sha256 = $3,
			    logo_byte_size = $4, logo_uploaded_at = now(), updated_by = $5
			WHERE id = true`,
			path, contentType, sha, int64(len(data)), actor.ID); err != nil {
			return err
		}

		profile, err := s.businessProfile(ctx, tx)
		if err != nil {
			return err
		}
		updated = &profile

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpSettingsUpdated,
			EntityType:  audit.EntityBusinessProfile,
			EntityID:    "business_profile.logo",
			RequestID:   requestID,
			Before:      map[string]any{"logo_sha256": before.LogoSHA256},
			After:       map[string]any{"logo_sha256": sha, "bytes": len(data), "filename": filename},
			Reason:      "the previous logo file is retained so earlier documents render as issued",
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// Logo returns the current logo bytes, verified against the recorded hash.
func (s *Service) Logo(ctx context.Context) ([]byte, string, error) {
	profile, err := s.BusinessProfile(ctx)
	if err != nil {
		return nil, "", err
	}
	if !profile.HasLogo {
		return nil, "", ErrNoLogo
	}

	data, err := s.store.Load(profile.LogoPath, profile.LogoSHA256)
	if err != nil {
		return nil, "", err
	}
	return data, profile.LogoContentType, nil
}

// LogoAt returns the logo a document was issued with, by the path and hash its
// snapshot recorded. This is what makes a historical document render with the
// logo it was actually printed with.
func (s *Service) LogoAt(path, sha256 string) ([]byte, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(sha256) == "" {
		return nil, ErrNoLogo
	}
	return s.store.Load(path, sha256)
}

// RemoveLogo clears the logo from future documents. The file stays on disk, for
// the documents that already reference it.
func (s *Service) RemoveLogo(ctx context.Context, actor *users.User, requestID string) (*BusinessProfile, error) {
	var updated *BusinessProfile

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.businessProfile(ctx, tx)
		if err != nil {
			return err
		}
		if !before.HasLogo {
			return ErrNoLogo
		}

		if _, err := tx.Exec(ctx, `
			UPDATE business_profile
			SET logo_path = NULL, logo_content_type = NULL, logo_sha256 = NULL,
			    logo_byte_size = NULL, logo_uploaded_at = NULL, updated_by = $1
			WHERE id = true`, actor.ID); err != nil {
			return err
		}

		profile, err := s.businessProfile(ctx, tx)
		if err != nil {
			return err
		}
		updated = &profile

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpSettingsUpdated,
			EntityType:  audit.EntityBusinessProfile,
			EntityID:    "business_profile.logo",
			RequestID:   requestID,
			Before:      map[string]any{"logo_sha256": before.LogoSHA256},
			Reason:      "logo removed from future documents; the stored file is retained",
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// sniffImage identifies an image from its leading bytes.
func sniffImage(data []byte) (contentType, extension string, ok bool) {
	if len(data) < 12 {
		return "", "", false
	}

	switch {
	case string(data[1:4]) == "PNG":
		contentType = "image/png"
	case data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		contentType = "image/jpeg"
	case string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		contentType = "image/webp"
	default:
		return "", "", false
	}

	return contentType, logoTypes[contentType], true
}
