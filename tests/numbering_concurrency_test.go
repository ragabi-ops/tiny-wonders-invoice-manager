package tests

// The numbering gate (plan.md 10, 20).
//
// "Sequence allocation must occur inside the same DB transaction as issuance
//  using row locking or equivalent safe PostgreSQL semantics. Required
//  guarantees: no duplicate numbers, retry-safe issuance, the browser never
//  chooses the next number. Concurrency tests must pass before moving on."
//
// These tests hit the real database with real concurrency. They are the reason
// Phase 3 may not be declared finished on a green unit-test run alone.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/documents"
)

// TestSequenceAllocatorIssuesNoDuplicatesUnderConcurrency hammers the allocator
// from many goroutines, each in its own transaction, and asserts that the
// numbers handed out are exactly 1..N with no repeats and no gaps.
func TestSequenceAllocatorIssuesNoDuplicatesUnderConcurrency(t *testing.T) {
	h := newHarness(t)

	const workers = 40
	allocator := documents.NewSequenceAllocator()
	ctx := context.Background()

	var (
		mu       sync.Mutex
		numbers  []int64
		failures []error
		start    sync.WaitGroup
		done     sync.WaitGroup
	)
	start.Add(1)

	for range workers {
		done.Add(1)
		go func() {
			defer done.Done()
			// Every goroutine waits on the same gate, so the allocations really
			// do collide rather than happening to run one after another.
			start.Wait()

			var allocated int64
			err := db.InTx(ctx, h.pool, func(tx pgx.Tx) error {
				number, err := allocator.Allocate(ctx, tx, documents.TypeTransactionInvoice, "CONC")
				allocated = number
				return err
			})

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			numbers = append(numbers, allocated)
		}()
	}

	start.Done()
	done.Wait()

	if len(failures) > 0 {
		t.Fatalf("%d allocations failed, first: %v", len(failures), failures[0])
	}
	if len(numbers) != workers {
		t.Fatalf("got %d numbers, want %d", len(numbers), workers)
	}

	seen := make(map[int64]bool, len(numbers))
	for _, number := range numbers {
		if seen[number] {
			t.Fatalf("number %d was allocated twice", number)
		}
		seen[number] = true
	}
	for expected := int64(1); expected <= workers; expected++ {
		if !seen[expected] {
			t.Fatalf("number %d was never allocated; the sequence has a gap", expected)
		}
	}
}

// TestRolledBackIssuanceReturnsItsNumber proves the number lives and dies with
// its transaction: a failed issuance must not burn an official number.
func TestRolledBackIssuanceReturnsItsNumber(t *testing.T) {
	h := newHarness(t)

	allocator := documents.NewSequenceAllocator()
	ctx := context.Background()
	sentinel := fmt.Errorf("deliberate rollback")

	var rolledBack int64
	err := db.InTx(ctx, h.pool, func(tx pgx.Tx) error {
		number, err := allocator.Allocate(ctx, tx, documents.TypeReceipt, "ROLLBACK")
		if err != nil {
			return err
		}
		rolledBack = number
		return sentinel
	})
	if err == nil {
		t.Fatal("the transaction should have rolled back")
	}

	var next int64
	if err := db.InTx(ctx, h.pool, func(tx pgx.Tx) error {
		number, err := allocator.Allocate(ctx, tx, documents.TypeReceipt, "ROLLBACK")
		next = number
		return err
	}); err != nil {
		t.Fatalf("second allocation failed: %v", err)
	}

	if next != rolledBack {
		t.Fatalf("after a rollback the next number is %d, want %d reused: a failed issuance burned a number",
			next, rolledBack)
	}
}

// TestSequencesAreIndependentPerTypeAndSeries makes sure one counter cannot
// consume another's numbers.
func TestSequencesAreIndependentPerTypeAndSeries(t *testing.T) {
	h := newHarness(t)

	allocator := documents.NewSequenceAllocator()
	ctx := context.Background()

	allocate := func(documentType documents.Type, series string) int64 {
		t.Helper()
		var number int64
		if err := db.InTx(ctx, h.pool, func(tx pgx.Tx) error {
			allocated, err := allocator.Allocate(ctx, tx, documentType, series)
			number = allocated
			return err
		}); err != nil {
			t.Fatalf("allocate %s/%s: %v", documentType, series, err)
		}
		return number
	}

	// Each counter starts at 1 regardless of what the others have handed out.
	if got := allocate(documents.TypeTransactionInvoice, "A"); got != 1 {
		t.Fatalf("first invoice number = %d, want 1", got)
	}
	if got := allocate(documents.TypeReceipt, "A"); got != 1 {
		t.Fatalf("first receipt number = %d, want 1: receipts share the invoice counter", got)
	}
	if got := allocate(documents.TypeTransactionInvoice, "TEST"); got != 1 {
		t.Fatalf("first test-series number = %d, want 1: the test series shares the official counter", got)
	}
	if got := allocate(documents.TypeTransactionInvoice, "A"); got != 2 {
		t.Fatalf("second invoice number = %d, want 2", got)
	}
}

// TestDatabaseRefusesADuplicateNumber is the belt to the allocator's braces: if
// the allocator were ever bypassed, the unique index must still stop a
// duplicate official number reaching the table (plan.md 10).
func TestDatabaseRefusesADuplicateNumber(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	customer := createCustomer(t, c, map[string]any{"display_name": "לקוח מספור"})
	ctx := context.Background()

	insert := func(number int64) error {
		_, err := h.pool.Exec(ctx, `
			INSERT INTO documents
				(document_type, state, series, number, customer_id, document_date,
				 subtotal_agorot, vat_agorot, total_agorot,
				 issued_at, snapshot, pdf_path, pdf_sha256, template_version)
			VALUES ('TRANSACTION_INVOICE', 'ISSUED', 'DUP', $1, $2, current_date,
			        100, 0, 100,
			        now(), '{}'::jsonb, 'x.pdf',
			        '0000000000000000000000000000000000000000000000000000000000000000',
			        'document/v1')`, number, customer.ID)
		return err
	}

	if err := insert(7); err != nil {
		t.Fatalf("first insert failed: %v", err)
	}
	if err := insert(7); err == nil {
		t.Fatal("the database accepted a duplicate official number")
	}
}

// TestConcurrentIssueOfTheSameDraftIssuesItOnce drives the real HTTP endpoint
// from several clients at once. Exactly one must succeed; the rest must be
// refused, and the draft must end up with exactly one number.
func TestConcurrentIssueOfTheSameDraftIssuesItOnce(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	draft := createDraft(t, c, h.aCustomer(t, c), []map[string]any{
		{"description": "סדנה", "quantity_milli": 1000, "unit_price_agorot": 15000},
	})

	const attempts = 8
	var (
		mu       sync.Mutex
		statuses []int
		start    sync.WaitGroup
		done     sync.WaitGroup
	)
	start.Add(1)

	for range attempts {
		done.Add(1)
		// Each attempt gets its own client, so they are genuinely separate
		// sessions racing on the same draft.
		racer := h.newClient()
		racer.loginOK(ownerEmail, ownerPassword)

		go func() {
			defer done.Done()
			start.Wait()

			resp := racer.post("/api/v1/documents/"+draft.ID+"/issue", nil)

			mu.Lock()
			defer mu.Unlock()
			statuses = append(statuses, resp.status)
		}()
	}

	start.Done()
	done.Wait()

	succeeded := 0
	for _, status := range statuses {
		if status == http.StatusOK {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d of %d concurrent issue attempts succeeded, want exactly 1 (statuses %v)",
			succeeded, attempts, statuses)
	}

	var numbered int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM documents WHERE id = $1 AND number IS NOT NULL`, draft.ID).
		Scan(&numbered); err != nil {
		t.Fatalf("count numbered documents: %v", err)
	}
	if numbered != 1 {
		t.Fatalf("the draft carries %d numbers, want 1", numbered)
	}
}

// TestConcurrentIssueOfDifferentDraftsGivesEachItsOwnNumber is the realistic
// case: several operators issuing different documents at the same moment.
func TestConcurrentIssueOfDifferentDraftsGivesEachItsOwnNumber(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	setup := h.newClient()
	setup.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, setup)

	const drafts = 6
	ids := make([]string, 0, drafts)
	for index := range drafts {
		draft := createDraft(t, setup, customerID, []map[string]any{
			{
				"description":       fmt.Sprintf("שורה %d", index+1),
				"quantity_milli":    1000,
				"unit_price_agorot": 10000 + int64(index),
			},
		})
		ids = append(ids, draft.ID)
	}

	var (
		mu      sync.Mutex
		issued  []string
		errored []int
		start   sync.WaitGroup
		done    sync.WaitGroup
	)
	start.Add(1)

	for _, id := range ids {
		done.Add(1)
		racer := h.newClient()
		racer.loginOK(ownerEmail, ownerPassword)

		go func() {
			defer done.Done()
			start.Wait()

			resp := racer.post("/api/v1/documents/"+id+"/issue", nil)

			mu.Lock()
			defer mu.Unlock()
			if resp.status != http.StatusOK {
				errored = append(errored, resp.status)
				return
			}
			var document struct {
				FullNumber string `json:"full_number"`
			}
			resp.decode(t, &document)
			issued = append(issued, document.FullNumber)
		}()
	}

	start.Done()
	done.Wait()

	if len(errored) > 0 {
		t.Fatalf("%d concurrent issuances failed (statuses %v)", len(errored), errored)
	}

	seen := make(map[string]bool, len(issued))
	for _, number := range issued {
		if number == "" {
			t.Fatal("an issued document has no number")
		}
		if seen[number] {
			t.Fatalf("number %s was issued twice", number)
		}
		seen[number] = true
	}
	if len(seen) != drafts {
		t.Fatalf("%d distinct numbers for %d documents", len(seen), drafts)
	}
}

// TestNoDuplicateNumbersExistAfterAllConcurrencyWork is a database-wide
// invariant check, independent of how the rows got there.
func TestNoDuplicateNumbersExistAfterAllConcurrencyWork(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	for range 5 {
		draft := createDraft(t, c, customerID, []map[string]any{
			{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 1000},
		})
		if resp := c.post("/api/v1/documents/"+draft.ID+"/issue", nil); resp.status != http.StatusOK {
			t.Fatalf("issue = %d %s", resp.status, resp.body)
		}
	}

	var duplicates int
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM (
			SELECT document_type, series, number
			FROM documents
			WHERE number IS NOT NULL
			GROUP BY document_type, series, number
			HAVING count(*) > 1
		) AS d`).Scan(&duplicates); err != nil {
		t.Fatalf("check for duplicates: %v", err)
	}
	if duplicates != 0 {
		t.Fatalf("%d (type, series, number) combinations are used more than once", duplicates)
	}
}
