// Copyright 2020 Liquidata, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package enginetest_test

import (
	"context"
	"github.com/liquidata-inc/go-mysql-server/enginetest"
	"github.com/opentracing/opentracing-go"
	"io"
	"strings"
	"testing"

	sqle "github.com/liquidata-inc/go-mysql-server"
	"github.com/liquidata-inc/go-mysql-server/memory"
	"github.com/liquidata-inc/go-mysql-server/sql"
	"github.com/liquidata-inc/go-mysql-server/sql/analyzer"
	"github.com/liquidata-inc/go-mysql-server/sql/plan"
	"github.com/liquidata-inc/go-mysql-server/test"

	"github.com/stretchr/testify/require"
)

func TestSessionSelectLimit(t *testing.T) {
	q := []struct {
		query    string
		expected []sql.Row
	}{
		{
			"SELECT * FROM mytable ORDER BY i",
			[]sql.Row{{int64(1), "first row"}},
		},
		{
			"SELECT * FROM mytable ORDER BY i LIMIT 2",
			[]sql.Row{
				{int64(1), "first row"},
				{int64(2), "second row"},
			},
		},
		{
			"SELECT i FROM (SELECT i FROM mytable LIMIT 2) t ORDER BY i",
			[]sql.Row{{int64(1)}},
		},
		// TODO: this is broken: the session limit is applying inappropriately to the subquery
		// {
		// 	"SELECT i FROM (SELECT i FROM mytable ORDER BY i DESC) t ORDER BY i LIMIT 2",
		// 	[]sql.Row{{int64(1)}},
		// },
	}

	e, idxReg := NewEngine(t)

	ctx := enginetest.NewCtx(idxReg)
	err := ctx.Session.Set(ctx, "sql_select_limit", sql.Int64, int64(1))
	require.NoError(t, err)

	t.Run("sql_select_limit", func(t *testing.T) {
		for _, tt := range q {
			enginetest.TestQuery(t, ctx, e, tt.query, tt.expected)
		}
	})
}

func TestSessionDefaults(t *testing.T) {
	enginetest.TestSessionDefaults(t, newDefaultMemoryHarness())
}

func TestSessionVariables(t *testing.T) {
	enginetest.TestSessionVariables(t, newDefaultMemoryHarness())
}

func TestSessionVariablesONOFF(t *testing.T) {
	enginetest.TestSessionVariablesONOFF(t, newDefaultMemoryHarness())
}

func TestWarnings(t *testing.T) {
	t.Run("sequential", func(t *testing.T) {
		enginetest.TestWarnings(t, newDefaultMemoryHarness())
	})

	t.Run("parallel", func(t *testing.T) {
		enginetest.TestWarnings(t, newMemoryHarness("parallel", 2, testNumPartitions, false, nil))
	})
}

func TestClearWarnings(t *testing.T) {
	enginetest.TestClearWarnings(t, newDefaultMemoryHarness())
}

func TestDescribe(t *testing.T) {
	query := `DESCRIBE FORMAT=TREE SELECT * FROM mytable`
	expectedSeq := []sql.Row{
		sql.NewRow("Table(mytable): Projected "),
	}

	expectedParallel := []sql.Row{
		{"Exchange(parallelism=2)"},
		{" └─ Table(mytable): Projected "},
	}

	e, idxReg := enginetest.NewEngineWithDbs(t, 1, enginetest.CreateTestData(t, newMemoryHarness("TODO", 1, testNumPartitions, false, nil)), nil)
	t.Run("sequential", func(t *testing.T) {
		testQuery(t, e, idxReg, query, expectedSeq)
	})

	ep, idxReg := enginetest.NewEngineWithDbs(t, 2, enginetest.CreateTestData(t, newMemoryHarness("TODO", 1, testNumPartitions, false, nil)), nil)
	t.Run("parallel", func(t *testing.T) {
		testQuery(t, ep, idxReg, query, expectedParallel)
	})
}

const testNumPartitions = 5

func TestAmbiguousColumnResolution(t *testing.T) {
	require := require.New(t)

	table := memory.NewPartitionedTable("foo", sql.Schema{
		{Name: "a", Type: sql.Int64, Source: "foo"},
		{Name: "b", Type: sql.Text, Source: "foo"},
	}, testNumPartitions)

	enginetest.InsertRows(
		t, table,
		sql.NewRow(int64(1), "foo"),
		sql.NewRow(int64(2), "bar"),
		sql.NewRow(int64(3), "baz"),
	)

	table2 := memory.NewPartitionedTable("bar", sql.Schema{
		{Name: "b", Type: sql.Text, Source: "bar"},
		{Name: "c", Type: sql.Int64, Source: "bar"},
	}, testNumPartitions)
	enginetest.InsertRows(
		t, table2,
		sql.NewRow("qux", int64(3)),
		sql.NewRow("mux", int64(2)),
		sql.NewRow("pux", int64(1)),
	)

	db := memory.NewDatabase("mydb")
	db.AddTable("foo", table)
	db.AddTable("bar", table2)

	e := sqle.NewDefault()
	e.AddDatabase(db)

	q := `SELECT f.a, bar.b, f.b FROM foo f INNER JOIN bar ON f.a = bar.c`
	ctx := enginetest.NewCtx(sql.NewIndexRegistry())

	_, rows, err := e.Query(ctx, q)
	require.NoError(err)

	var rs [][]interface{}
	for {
		row, err := rows.Next()
		if err == io.EOF {
			break
		}
		require.NoError(err)

		rs = append(rs, row)
	}

	expected := [][]interface{}{
		{int64(1), "pux", "foo"},
		{int64(2), "mux", "bar"},
		{int64(3), "qux", "baz"},
	}

	require.Equal(expected, rs)
}

func TestCreateTable(t *testing.T) {
	enginetest.TestCreateTable(t, newDefaultMemoryHarness())
}

func TestDropTable(t *testing.T) {
	require := require.New(t)

	e, idxReg := NewEngine(t)
	db, err := e.Catalog.Database("mydb")
	require.NoError(err)

	ctx := enginetest.NewCtx(idxReg)
	_, ok, err := db.GetTableInsensitive(ctx, "mytable")
	require.True(ok)

	testQuery(t, e, idxReg,
		"DROP TABLE IF EXISTS mytable, not_exist",
		[]sql.Row(nil),
	)

	_, ok, err = db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "othertable")
	require.NoError(err)
	require.True(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "tabletest")
	require.NoError(err)
	require.True(ok)

	testQuery(t, e, idxReg,
		"DROP TABLE IF EXISTS othertable, tabletest",
		[]sql.Row(nil),
	)

	_, ok, err = db.GetTableInsensitive(ctx, "othertable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "tabletest")
	require.NoError(err)
	require.False(ok)

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "DROP TABLE not_exist")
	require.Error(err)
}

func TestRenameTable(t *testing.T) {
	ctx := sql.NewContext(context.Background(), sql.WithIndexRegistry(sql.NewIndexRegistry()), sql.WithViewRegistry(sql.NewViewRegistry()))
	require := require.New(t)

	e, idxReg := NewEngine(t)
	db, err := e.Catalog.Database("mydb")
	require.NoError(err)

	_, ok, err := db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)

	testQuery(t, e, idxReg,
		"RENAME TABLE mytable TO newTableName",
		[]sql.Row(nil),
	)

	_, ok, err = db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "newTableName")
	require.NoError(err)
	require.True(ok)

	testQuery(t, e, idxReg,
		"RENAME TABLE othertable to othertable2, newTableName to mytable",
		[]sql.Row(nil),
	)

	_, ok, err = db.GetTableInsensitive(ctx, "othertable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "othertable2")
	require.NoError(err)
	require.True(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "newTableName")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)

	testQuery(t, e, idxReg,
		"ALTER TABLE mytable RENAME newTableName",
		[]sql.Row(nil),
	)

	_, ok, err = db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "newTableName")
	require.NoError(err)
	require.True(ok)


	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE not_exist RENAME foo")
	require.Error(err)
	require.True(sql.ErrTableNotFound.Is(err))

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE typestable RENAME niltable")
	require.Error(err)
	require.True(sql.ErrTableAlreadyExists.Is(err))
}

func TestRenameColumn(t *testing.T) {
	ctx := sql.NewContext(context.Background(), sql.WithIndexRegistry(sql.NewIndexRegistry()), sql.WithViewRegistry(sql.NewViewRegistry()))
	require := require.New(t)

	e, idxReg := NewEngine(t)
	db, err := e.Catalog.Database("mydb")
	require.NoError(err)

	testQuery(t, e, idxReg,
		"ALTER TABLE mytable RENAME COLUMN i TO i2",
		[]sql.Row(nil),
	)

	tbl, ok, err := db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "i2", Type: sql.Int64, Source: "mytable"},
		{Name: "s", Type: sql.Text, Source: "mytable"},
	}, tbl.Schema())

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE not_exist RENAME COLUMN foo TO bar")
	require.Error(err)
	require.True(sql.ErrTableNotFound.Is(err))

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE mytable RENAME COLUMN foo TO bar")
	require.Error(err)
	require.True(plan.ErrColumnNotFound.Is(err))
}

func TestAddColumn(t *testing.T) {
	ctx := sql.NewContext(context.Background(), sql.WithIndexRegistry(sql.NewIndexRegistry()), sql.WithViewRegistry(sql.NewViewRegistry()))
	require := require.New(t)

	e, idxReg := NewEngine(t)
	db, err := e.Catalog.Database("mydb")
	require.NoError(err)

	testQuery(t, e, idxReg,
		"ALTER TABLE mytable ADD COLUMN i2 INT COMMENT 'hello' default 42",
		[]sql.Row(nil),
	)

	tbl, ok, err := db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "i", Type: sql.Int64, Source: "mytable"},
		{Name: "s", Type: sql.Text, Source: "mytable"},
		{Name: "i2", Type: sql.Int32, Source: "mytable", Comment: "hello", Nullable: true, Default: int32(42)},
	}, tbl.Schema())

	testQuery(t, e, idxReg,
		"SELECT * FROM mytable ORDER BY i",
		[]sql.Row{
			sql.NewRow(int64(1), "first row", int32(42)),
			sql.NewRow(int64(2), "second row", int32(42)),
			sql.NewRow(int64(3), "third row", int32(42)),
		},
	)

	testQuery(t, e, idxReg,
		"ALTER TABLE mytable ADD COLUMN s2 TEXT COMMENT 'hello' AFTER i",
		[]sql.Row(nil),
	)

	tbl, ok, err = db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "i", Type: sql.Int64, Source: "mytable"},
		{Name: "s2", Type: sql.Text, Source: "mytable", Comment: "hello", Nullable: true},
		{Name: "s", Type: sql.Text, Source: "mytable"},
		{Name: "i2", Type: sql.Int32, Source: "mytable", Comment: "hello", Nullable: true, Default: int32(42)},
	}, tbl.Schema())

	testQuery(t, e, idxReg,
		"SELECT * FROM mytable ORDER BY i",
		[]sql.Row{
			sql.NewRow(int64(1), nil, "first row", int32(42)),
			sql.NewRow(int64(2), nil, "second row", int32(42)),
			sql.NewRow(int64(3), nil, "third row", int32(42)),
		},
	)

	testQuery(t, e, idxReg,
		"ALTER TABLE mytable ADD COLUMN s3 TEXT COMMENT 'hello' default 'yay' FIRST",
		[]sql.Row(nil),
	)

	tbl, ok, err = db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "s3", Type: sql.Text, Source: "mytable", Comment: "hello", Nullable: true, Default: "yay"},
		{Name: "i", Type: sql.Int64, Source: "mytable"},
		{Name: "s2", Type: sql.Text, Source: "mytable", Comment: "hello", Nullable: true},
		{Name: "s", Type: sql.Text, Source: "mytable"},
		{Name: "i2", Type: sql.Int32, Source: "mytable", Comment: "hello", Nullable: true, Default: int32(42)},
	}, tbl.Schema())

	testQuery(t, e, idxReg,
		"SELECT * FROM mytable ORDER BY i",
		[]sql.Row{
			sql.NewRow("yay", int64(1), nil, "first row", int32(42)),
			sql.NewRow("yay", int64(2), nil, "second row", int32(42)),
			sql.NewRow("yay", int64(3), nil, "third row", int32(42)),
		},
	)

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE not_exist ADD COLUMN i2 INT COMMENT 'hello'")
	require.Error(err)
	require.True(sql.ErrTableNotFound.Is(err))

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE mytable ADD COLUMN b BIGINT COMMENT 'ok' AFTER not_exist")
	require.Error(err)
	require.True(plan.ErrColumnNotFound.Is(err))

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE mytable ADD COLUMN b INT NOT NULL")
	require.Error(err)
	require.True(plan.ErrNullDefault.Is(err))

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE mytable ADD COLUMN b INT NOT NULL DEFAULT 'yes'")
	require.Error(err)
	require.True(plan.ErrIncompatibleDefaultType.Is(err))
}

func TestModifyColumn(t *testing.T) {
	ctx := sql.NewContext(context.Background(), sql.WithIndexRegistry(sql.NewIndexRegistry()), sql.WithViewRegistry(sql.NewViewRegistry()))
	require := require.New(t)

	e, idxReg := NewEngine(t)
	db, err := e.Catalog.Database("mydb")
	require.NoError(err)

	testQuery(t, e, idxReg,
		"ALTER TABLE mytable MODIFY COLUMN i TEXT NOT NULL COMMENT 'modified'",
		[]sql.Row(nil),
	)

	tbl, ok, err := db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "i", Type: sql.Text, Source: "mytable", Comment:"modified"},
		{Name: "s", Type: sql.Text, Source: "mytable"},
	}, tbl.Schema())

	testQuery(t, e, idxReg,
		"ALTER TABLE mytable MODIFY COLUMN i TINYINT NULL COMMENT 'yes' AFTER s",
		[]sql.Row(nil),
	)

	tbl, ok, err = db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "s", Type: sql.Text, Source: "mytable"},
		{Name: "i", Type: sql.Int8, Source: "mytable", Comment:"yes", Nullable: true},
	}, tbl.Schema())

	testQuery(t, e, idxReg,
		"ALTER TABLE mytable MODIFY COLUMN i BIGINT NOT NULL COMMENT 'ok' FIRST",
		[]sql.Row(nil),
	)

	tbl, ok, err = db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "i", Type: sql.Int64, Source: "mytable", Comment:"ok"},
		{Name: "s", Type: sql.Text, Source: "mytable"},
	}, tbl.Schema())

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE mytable MODIFY not_exist BIGINT NOT NULL COMMENT 'ok' FIRST")
	require.Error(err)
	require.True(plan.ErrColumnNotFound.Is(err))

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE mytable MODIFY i BIGINT NOT NULL COMMENT 'ok' AFTER not_exist")
	require.Error(err)
	require.True(plan.ErrColumnNotFound.Is(err))

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE not_exist MODIFY COLUMN i INT NOT NULL COMMENT 'hello'")
	require.Error(err)
	require.True(sql.ErrTableNotFound.Is(err))
}

func TestDropColumn(t *testing.T) {
	require := require.New(t)

	e, idxReg := NewEngine(t)
	ctx := enginetest.NewCtx(idxReg)
	db, err := e.Catalog.Database("mydb")
	require.NoError(err)

	testQuery(t, e, idxReg,
		"ALTER TABLE mytable DROP COLUMN i",
		[]sql.Row(nil),
	)

	tbl, ok, err := db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "s", Type: sql.Text, Source: "mytable"},
	}, tbl.Schema())

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE not_exist DROP COLUMN s")
	require.Error(err)
	require.True(sql.ErrTableNotFound.Is(err))

	_, _, err = e.Query(enginetest.NewCtx(idxReg), "ALTER TABLE mytable DROP COLUMN i")
	require.Error(err)
	require.True(plan.ErrColumnNotFound.Is(err))
}

func testQuery(t *testing.T, e *sqle.Engine, idxReg *sql.IndexRegistry, q string, expected []sql.Row) {
	enginetest.TestQuery(t, enginetest.NewCtx(idxReg), e, q, expected)
}

func NewEngine(t *testing.T) (*sqle.Engine, *sql.IndexRegistry) {
	return enginetest.NewEngineWithDbs(t, 1, enginetest.CreateTestData(t, newMemoryHarness("default", 1, testNumPartitions, false, nil)), nil)
}

func TestTracing(t *testing.T) {
	require := require.New(t)
	e, idxReg := NewEngine(t)

	tracer := new(test.MemTracer)

	ctx := sql.NewContext(context.TODO(), sql.WithTracer(tracer), sql.WithIndexRegistry(idxReg), sql.WithViewRegistry(sql.NewViewRegistry())).WithCurrentDB("mydb")

	_, iter, err := e.Query(ctx, `SELECT DISTINCT i
		FROM mytable
		WHERE s = 'first row'
		ORDER BY i DESC
		LIMIT 1`)
	require.NoError(err)

	rows, err := sql.RowIterToRows(iter)
	require.Len(rows, 1)
	require.NoError(err)

	spans := tracer.Spans
	var expectedSpans = []string{
		"plan.Limit",
		"plan.Sort",
		"plan.Distinct",
		"plan.Project",
		"plan.ResolvedTable",
	}

	var spanOperations []string
	for _, s := range spans {
		// only check the ones inside the execution tree
		if strings.HasPrefix(s, "plan.") ||
			strings.HasPrefix(s, "expression.") ||
			strings.HasPrefix(s, "function.") ||
			strings.HasPrefix(s, "aggregation.") {
			spanOperations = append(spanOperations, s)
		}
	}

	require.Equal(expectedSpans, spanOperations)
}

func TestUse(t *testing.T) {
	require := require.New(t)
	e, idxReg := NewEngine(t)

	ctx := enginetest.NewCtx(idxReg)
	require.Equal("mydb", ctx.GetCurrentDatabase())

	_, _, err := e.Query(ctx, "USE bar")
	require.Error(err)

	require.Equal("mydb", ctx.GetCurrentDatabase())

	_, iter, err := e.Query(ctx, "USE foo")
	require.NoError(err)

	rows, err := sql.RowIterToRows(iter)
	require.NoError(err)
	require.Len(rows, 0)

	require.Equal("foo", ctx.GetCurrentDatabase())
}

func TestLocks(t *testing.T) {
	require := require.New(t)

	t1 := newLockableTable(memory.NewTable("t1", nil))
	t2 := newLockableTable(memory.NewTable("t2", nil))
	t3 := memory.NewTable("t3", nil)
	catalog := sql.NewCatalog()
	db := memory.NewDatabase("db")
	db.AddTable("t1", t1)
	db.AddTable("t2", t2)
	db.AddTable("t3", t3)
	catalog.AddDatabase(db)

	analyzer := analyzer.NewDefault(catalog)
	engine := sqle.New(catalog, analyzer, new(sqle.Config))
	idxReg := sql.NewIndexRegistry()

	_, iter, err := engine.Query(enginetest.NewCtx(idxReg).WithCurrentDB("db"), "LOCK TABLES t1 READ, t2 WRITE, t3 READ")
	require.NoError(err)

	_, err = sql.RowIterToRows(iter)
	require.NoError(err)

	_, iter, err = engine.Query(enginetest.NewCtx(idxReg).WithCurrentDB("db"), "UNLOCK TABLES")
	require.NoError(err)

	_, err = sql.RowIterToRows(iter)
	require.NoError(err)

	require.Equal(1, t1.readLocks)
	require.Equal(0, t1.writeLocks)
	require.Equal(1, t1.unlocks)
	require.Equal(0, t2.readLocks)
	require.Equal(1, t2.writeLocks)
	require.Equal(1, t2.unlocks)
}

type mockSpan struct {
	opentracing.Span
	finished bool
}

func (m *mockSpan) Finish() {
	m.finished = true
}

func TestRootSpanFinish(t *testing.T) {
	e, idxReg := NewEngine(t)
	fakeSpan := &mockSpan{Span: opentracing.NoopTracer{}.StartSpan("")}
	ctx := sql.NewContext(
		context.Background(),
		sql.WithRootSpan(fakeSpan),
		sql.WithIndexRegistry(idxReg),
		sql.WithViewRegistry(sql.NewViewRegistry()),
	).WithCurrentDB("mydb")

	_, iter, err := e.Query(ctx, "SELECT 1")
	require.NoError(t, err)

	_, err = sql.RowIterToRows(iter)
	require.NoError(t, err)

	require.True(t, fakeSpan.finished)
}

type lockableTable struct {
	sql.Table
	readLocks  int
	writeLocks int
	unlocks    int
}

func newLockableTable(t sql.Table) *lockableTable {
	return &lockableTable{Table: t}
}

var _ sql.Lockable = (*lockableTable)(nil)

func (l *lockableTable) Lock(ctx *sql.Context, write bool) error {
	if write {
		l.writeLocks++
	} else {
		l.readLocks++
	}
	return nil
}

func (l *lockableTable) Unlock(ctx *sql.Context, id uint32) error {
	l.unlocks++
	return nil
}