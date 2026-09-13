package documents

import (
	"context"
	"fmt"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
)

// SequenceAllocator hands out official document numbers.
//
// This is the guarantee the whole system rests on: no two issued documents of a
// type and series may share a number, including under concurrent issuance
// (plan.md 3.3, 10).
type SequenceAllocator struct{}

// NewSequenceAllocator returns a stateless allocator.
func NewSequenceAllocator() *SequenceAllocator { return &SequenceAllocator{} }

// Allocate takes the next number for a type and series.
//
// It MUST be called inside the same transaction as the issuance it numbers.
// The UPDATE takes a row lock on the sequence row, so a second transaction
// allocating the same (type, series) blocks until this one commits or rolls
// back. A rolled-back issuance therefore returns its number rather than
// leaving a gap.
//
// The unique index on (document_type, series, number) is the belt to this
// braces: even if this function were bypassed, the database would refuse a
// duplicate.
func (a *SequenceAllocator) Allocate(ctx context.Context, q db.Querier, documentType Type, series string) (int64, error) {
	// Create the counter on first use, so a new series needs no migration.
	// A concurrent creator either wins this insert or is a no-op; either way the
	// UPDATE below is what actually serializes the allocation.
	if _, err := q.Exec(ctx, `
		INSERT INTO document_sequences (document_type, series, next_number)
		VALUES ($1, $2, 1)
		ON CONFLICT (document_type, series) DO NOTHING`, string(documentType), series); err != nil {
		return 0, fmt.Errorf("ensure sequence %s/%s: %w", documentType, series, err)
	}

	var allocated int64
	// UPDATE ... RETURNING is atomic and takes the row lock: the read, the
	// increment and the write cannot be interleaved by another transaction.
	if err := q.QueryRow(ctx, `
		UPDATE document_sequences
		SET next_number = next_number + 1, updated_at = now()
		WHERE document_type = $1 AND series = $2
		RETURNING next_number - 1`, string(documentType), series).Scan(&allocated); err != nil {
		return 0, fmt.Errorf("allocate number for %s/%s: %w", documentType, series, err)
	}

	if allocated < 1 {
		return 0, fmt.Errorf("allocated an invalid document number %d", allocated)
	}
	return allocated, nil
}

// SequenceState is one counter, for the numbering report (plan.md 15).
type SequenceState struct {
	DocumentType Type   `json:"document_type"`
	Series       string `json:"series"`
	NextNumber   int64  `json:"next_number"`
	// Issued is how many documents actually carry a number in this series. A
	// gap between Issued and NextNumber-1 means an issuance was rolled back,
	// which is expected and harmless; a document is never renumbered to fill it.
	Issued int64 `json:"issued_count"`
	// HighestIssued is the largest number actually present.
	HighestIssued *int64 `json:"highest_issued"`
}

// States returns every sequence with its usage, for the document status and
// sequence report.
func (a *SequenceAllocator) States(ctx context.Context, q db.Querier) ([]SequenceState, error) {
	rows, err := q.Query(ctx, `
		SELECT s.document_type, s.series, s.next_number,
		       count(d.id) AS issued,
		       max(d.number) AS highest
		FROM document_sequences s
		LEFT JOIN documents d
			ON d.document_type = s.document_type
			AND d.series = s.series
			AND d.number IS NOT NULL
		GROUP BY s.document_type, s.series, s.next_number
		ORDER BY s.document_type, s.series`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	states := []SequenceState{}
	for rows.Next() {
		var state SequenceState
		if err := rows.Scan(&state.DocumentType, &state.Series, &state.NextNumber,
			&state.Issued, &state.HighestIssued); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}
