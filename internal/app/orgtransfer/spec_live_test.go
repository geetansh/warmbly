package orgtransfer

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Live checks of the transfer spec against the real schema. Skipped unless
// WARMBLY_TEST_DB is set (same convention as internal/scheduler):
//
//	WARMBLY_TEST_DB=postgres://warmbly:warmbly@localhost:15432/warmbly_dev?sslmode=disable \
//	  go test ./internal/app/orgtransfer/ -run Live -v
//
// Both rules below are properties of spec.go paired with the live schema, so
// neither can be checked without a database. Both were broken, and each break
// was a whole archive that refuses to import: a NOT NULL reset column aborted
// on the first row, and a table ordered above something it references aborted
// on a foreign key. A unit test over spec.go alone cannot see either.

func specLivePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("WARMBLY_TEST_DB")
	if dsn == "" {
		t.Skip("WARMBLY_TEST_DB not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

// A reset column is omitted from the insert so the destination's DEFAULT
// applies. A NOT NULL column with no default has nothing to fall back on, so
// omitting it would abort the import just as writing NULL used to.
func TestLiveResetColumnsHaveSomethingToFallBackOn(t *testing.T) {
	pool := specLivePool(t)
	ctx := context.Background()

	for _, tbl := range Tables {
		if tbl.ImportSkip {
			continue
		}
		for _, col := range tbl.ResetOnImport {
			var nullable bool
			var hasDefault bool
			err := pool.QueryRow(ctx, `
				SELECT is_nullable = 'YES', column_default IS NOT NULL
				FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
			`, tbl.Name, col).Scan(&nullable, &hasDefault)
			if err != nil {
				t.Errorf("%s.%s is in ResetOnImport but not in the schema: %v", tbl.Name, col, err)
				continue
			}
			if !nullable && !hasDefault {
				t.Errorf("%s.%s is NOT NULL with no default, so resetting it aborts every import that carries this table",
					tbl.Name, col)
			}
		}
	}
}

// Tables is applied top to bottom, so a table must sit below everything it
// references. A reference to a table that this run does not write is cleared
// by referencePlan; a reference to one it writes LATER is not, and lands as a
// foreign-key violation that fails the whole import.
func TestLiveTablesAreInDependencyOrder(t *testing.T) {
	pool := specLivePool(t)
	ctx := context.Background()

	pos := make(map[string]int, len(Tables))
	for i, tbl := range Tables {
		pos[tbl.Name] = i
	}

	for i, tbl := range Tables {
		rows, err := pool.Query(ctx, `
			SELECT kcu.column_name, ccu.table_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu
			  ON kcu.constraint_name = tc.constraint_name
			JOIN information_schema.constraint_column_usage ccu
			  ON ccu.constraint_name = tc.constraint_name
			WHERE tc.constraint_type = 'FOREIGN KEY'
			  AND tc.table_schema = 'public' AND tc.table_name = $1
		`, tbl.Name)
		if err != nil {
			t.Fatalf("read foreign keys for %s: %v", tbl.Name, err)
		}
		type ref struct{ col, target string }
		var refs []ref
		for rows.Next() {
			var r ref
			if err := rows.Scan(&r.col, &r.target); err != nil {
				rows.Close()
				t.Fatalf("scan foreign key: %v", err)
			}
			refs = append(refs, r)
		}
		rows.Close()

		for _, r := range refs {
			if r.target == tbl.Name {
				continue // self-reference, ordered within the table's own rows
			}
			if slices.Contains(tbl.ResetOnImport, r.col) {
				continue // blanked on import, so it references nothing yet
			}
			target, carried := pos[r.target]
			if !carried {
				continue // not in the archive; referencePlan clears or keeps it
			}
			if target > i {
				t.Errorf("%s is at %d but its %s points at %s at %d: move it below",
					tbl.Name, i, r.col, r.target, target)
			}
		}
	}
}

// Every table with an organization_id is in Tables or ExcludedTables. A table in
// neither is silently absent from every archive; this is the coverage check
// ExcludedTables names. Tables scoped through a parent are not seen here.
func TestLiveEveryOrgTableIsRegistered(t *testing.T) {
	pool := specLivePool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT c.table_name
		FROM information_schema.columns c
		JOIN information_schema.tables t USING (table_schema, table_name)
		WHERE c.table_schema = 'public' AND c.column_name = 'organization_id'
		  AND t.table_type = 'BASE TABLE'
		ORDER BY c.table_name
	`)
	if err != nil {
		t.Fatalf("read org-scoped tables: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		if _, carried := TableByName[name]; carried {
			continue
		}
		if _, excluded := ExcludedTables[name]; excluded {
			continue
		}
		t.Errorf("%s has organization_id but is in neither Tables nor ExcludedTables", name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read org-scoped tables: %v", err)
	}
}
