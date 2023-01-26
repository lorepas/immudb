/*
Copyright 2022 Codenotary Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package database

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/codenotary/immudb/embedded/store"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encodeOffset(offset int64, vLogID byte) int64 {
	return int64(vLogID)<<56 | offset
}

func decodeOffset(offset int64) (byte, int64) {
	return byte(offset >> 56), offset & ^(0xff << 55)
}

func Test_vlogCompactor_Compact(t *testing.T) {
	entries := []*store.TxEntry{}
	entries = append(entries,
		store.NewTxEntry(nil, nil, 0, [32]byte{0}, encodeOffset(3, 12)),
		store.NewTxEntry(nil, nil, 0, [32]byte{0}, encodeOffset(3, 2)),
		store.NewTxEntry(nil, nil, 0, [32]byte{0}, encodeOffset(2, 1)),
		store.NewTxEntry(nil, nil, 0, [32]byte{0}, encodeOffset(3, 1)),
		store.NewTxEntry(nil, nil, 0, [32]byte{0}, encodeOffset(4, 2)),
		store.NewTxEntry(nil, nil, 0, [32]byte{0}, encodeOffset(1, 3)),
		store.NewTxEntry(nil, nil, 0, [32]byte{0}, encodeOffset(1, 2)),
	)
	sort.Slice(entries, func(i, j int) bool {
		v1, o1 := decodeOffset(entries[i].VOff())
		v2, o2 := decodeOffset(entries[j].VOff())
		if v1 == v2 {
			return o1 < o2
		}
		return v1 < v2
	})

	v, off := decodeOffset(entries[0].VOff())
	assert.Equal(t, v, byte(1))
	assert.Equal(t, int(off), 2)

	v, off = decodeOffset(entries[len(entries)-1].VOff())
	assert.Equal(t, v, byte(12))
	assert.Equal(t, int(off), 3)
}

// Test multiple log with single writer
func Test_vlogCompactor_WithMultipleIO(t *testing.T) {
	rootPath := t.TempDir()

	options := DefaultOption().WithDBRootPath(rootPath).WithCorruptionChecker(false)
	options.storeOpts.WithIndexOptions(options.storeOpts.IndexOpts.WithCompactionThld(2)).WithFileSize(6)
	options.storeOpts.MaxIOConcurrency = 5
	options.storeOpts.MaxConcurrency = 500
	options.storeOpts.VLogCacheSize = 0

	db := makeDbWith(t, "db", options)

	for i := 2; i <= 20; i++ {
		kv := &schema.KeyValue{
			Key:   []byte(fmt.Sprintf("key_%d", i)),
			Value: []byte(fmt.Sprintf("val_%d", i)),
		}
		_, err := db.Set(context.Background(), &schema.SetRequest{KVs: []*schema.KeyValue{kv}})
		require.NoError(t, err)
	}

	deletePointTx := uint64(15)
	hdr, err := db.st.ReadTxHeader(deletePointTx, false)
	require.NoError(t, err)
	c := NewVlogTruncator(db)
	require.NoError(t, c.Truncate(context.Background(), hdr.ID))

	for i := deletePointTx; i <= 20; i++ {
		tx := store.NewTx(db.st.MaxTxEntries(), db.st.MaxKeyLen())
		err = db.st.ReadTx(i, tx)
		for _, e := range tx.Entries() {
			_, err := db.st.ReadValue(e)
			require.NoError(t, err)
		}
	}
}

// Test single log with single writer
func Test_vlogCompactor_WithSingleIO(t *testing.T) {
	rootPath := t.TempDir()

	options := DefaultOption().WithDBRootPath(rootPath).WithCorruptionChecker(false)
	options.storeOpts.WithIndexOptions(options.storeOpts.IndexOpts.WithCompactionThld(2)).WithFileSize(6)
	options.storeOpts.MaxIOConcurrency = 1
	options.storeOpts.MaxConcurrency = 500
	options.storeOpts.VLogCacheSize = 0

	db := makeDbWith(t, "db", options)

	for i := 2; i <= 10; i++ {
		kv := &schema.KeyValue{
			Key:   []byte(fmt.Sprintf("key_%d", i)),
			Value: []byte(fmt.Sprintf("val_%d", i)),
		}
		_, err := db.Set(context.Background(), &schema.SetRequest{KVs: []*schema.KeyValue{kv}})
		require.NoError(t, err)
	}

	deletePointTx := uint64(5)
	hdr, err := db.st.ReadTxHeader(deletePointTx, false)
	require.NoError(t, err)
	c := NewVlogTruncator(db)
	require.NoError(t, c.Truncate(context.Background(), hdr.ID))

	for i := deletePointTx; i <= 10; i++ {
		tx := store.NewTx(db.st.MaxTxEntries(), db.st.MaxKeyLen())
		err = db.st.ReadTx(i, tx)
		for _, e := range tx.Entries() {
			_, err := db.st.ReadValue(e)
			require.NoError(t, err)
		}
	}

	for i := deletePointTx - 1; i > 0; i-- {
		tx := store.NewTx(db.st.MaxTxEntries(), db.st.MaxKeyLen())
		err = db.st.ReadTx(i, tx)
		for _, e := range tx.Entries() {
			_, err := db.st.ReadValue(e)
			require.Error(t, err)
		}
	}
}

// Test single log with concurrent writers
func Test_vlogCompactor_WithConcurrentWritersOnSingleIO(t *testing.T) {
	rootPath := t.TempDir()

	options := DefaultOption().WithDBRootPath(rootPath).WithCorruptionChecker(false)
	options.storeOpts.WithIndexOptions(options.storeOpts.IndexOpts.WithCompactionThld(2)).WithFileSize(6)
	options.storeOpts.MaxIOConcurrency = 1
	options.storeOpts.MaxConcurrency = 500
	options.storeOpts.VLogCacheSize = 0

	db := makeDbWith(t, "db", options)

	wg := sync.WaitGroup{}

	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			for k := 1*(j-1)*10 + 1; k < (j*10)+1; k++ {
				kv := &schema.KeyValue{
					Key:   []byte(fmt.Sprintf("key_%d", k)),
					Value: []byte(fmt.Sprintf("val_%d", k)),
				}
				_, err := db.Set(context.Background(), &schema.SetRequest{KVs: []*schema.KeyValue{kv}})
				require.NoError(t, err)
			}
		}(i)
	}

	wg.Wait()

	deletePointTx := uint64(15)
	hdr, err := db.st.ReadTxHeader(deletePointTx, false)
	require.NoError(t, err)
	c := NewVlogTruncator(db)
	require.NoError(t, c.Truncate(context.Background(), hdr.ID))

	for i := deletePointTx; i <= 30; i++ {
		tx := store.NewTx(db.st.MaxTxEntries(), db.st.MaxKeyLen())
		err = db.st.ReadTx(i, tx)
		for _, e := range tx.Entries() {
			_, err := db.st.ReadValue(e)
			require.NoError(t, err)
		}
	}

	for i := deletePointTx - 1; i > 0; i-- {
		tx := store.NewTx(db.st.MaxTxEntries(), db.st.MaxKeyLen())
		err = db.st.ReadTx(i, tx)
		for _, e := range tx.Entries() {
			_, err := db.st.ReadValue(e)
			require.Error(t, err)
		}
	}
}

func Test_newTruncatorMetrics(t *testing.T) {
	type args struct {
		db string
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "with default registerer",
			args: args{
				db: "foo",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti := time.Now()
			r := newTruncatorMetrics(tt.args.db)
			r.ran.Inc()
			r.duration.Observe(time.Since(ti).Seconds())
		})
	}
}

func Test_vlogCompactor_Plan(t *testing.T) {
	rootPath := t.TempDir()

	options := DefaultOption().WithDBRootPath(rootPath).WithCorruptionChecker(false)
	options.storeOpts.WithIndexOptions(options.storeOpts.IndexOpts.WithCompactionThld(2)).WithFileSize(600)
	options.storeOpts.MaxIOConcurrency = 1
	options.storeOpts.VLogCacheSize = 0

	db := makeDbWith(t, "db", options)

	var queryTime time.Time
	for i := 2; i <= 20; i++ {
		kv := &schema.KeyValue{
			Key:   []byte(fmt.Sprintf("key_%d", i)),
			Value: []byte(fmt.Sprintf("val_%d", i)),
		}
		_, err := db.Set(context.Background(), &schema.SetRequest{KVs: []*schema.KeyValue{kv}})
		require.NoError(t, err)
		if i == 10 {
			queryTime = time.Now()
		}
	}

	c := NewVlogTruncator(db)
	hdr, err := c.Plan(queryTime)
	require.NoError(t, err)
	require.LessOrEqual(t, time.Unix(hdr.Ts, 0), queryTime)
}

var sqlPrefix = []byte{2}

func closeStore(t *testing.T, st *store.ImmuStore) {
	err := st.Close()
	if !t.Failed() {
		// Do not pollute error output if test has already failed
		require.NoError(t, err)
	}
}

func setupCommonTest(t *testing.T) *db {
	rootPath := t.TempDir()

	options := DefaultOption().WithDBRootPath(rootPath).WithCorruptionChecker(false)
	options.storeOpts.WithIndexOptions(options.storeOpts.IndexOpts.WithCompactionThld(2)).WithFileSize(6)
	options.storeOpts.VLogCacheSize = 0

	db := makeDbWith(t, "db1", options)
	return db
}

func Test_vlogCompactor_with_sql(t *testing.T) {
	db := setupCommonTest(t)

	exec := func(t *testing.T, stmt string) {
		_, ctx, err := db.SQLExec(context.Background(), nil, &schema.SQLExecRequest{Sql: stmt})
		require.NoError(t, err)
		require.Len(t, ctx, 1)
	}

	query := func(t *testing.T, stmt string, expectedRows int) {
		res, err := db.SQLQuery(context.Background(), nil, &schema.SQLQueryRequest{Sql: stmt})
		require.NoError(t, err)
		require.NoError(t, err)
		require.Len(t, res.Rows, expectedRows)
	}

	// create a new table
	exec(t, "CREATE TABLE table1 (id INTEGER AUTO_INCREMENT, name VARCHAR[50], amount INTEGER, PRIMARY KEY id)")
	exec(t, "CREATE UNIQUE INDEX ON table1 (name)")
	exec(t, "CREATE UNIQUE INDEX ON table1 (name, amount)")

	// insert some data
	var deleteUptoTx *schema.TxHeader
	for i := 1; i <= 5; i++ {
		var err error
		kv := &schema.KeyValue{
			Key:   []byte(fmt.Sprintf("key_%d", i)),
			Value: []byte(fmt.Sprintf("val_%d", i)),
		}
		deleteUptoTx, err = db.Set(context.Background(), &schema.SetRequest{KVs: []*schema.KeyValue{kv}})
		require.NoError(t, err)
	}

	// alter table to add a new column
	t.Run("alter table and add data", func(t *testing.T) {
		exec(t, "ALTER TABLE table1 ADD COLUMN surname VARCHAR")
		exec(t, "INSERT INTO table1(name, surname, amount) VALUES('Foo', 'Bar', 0)")
		exec(t, "INSERT INTO table1(name, surname, amount) VALUES('Fin', 'Baz', 0)")
	})

	// delete txns in the store upto a certain txn
	t.Run("succeed truncating sql catalog", func(t *testing.T) {
		lastCommitTx := db.st.LastCommittedTxID()
		hdr, err := db.st.ReadTxHeader(deleteUptoTx.Id, false)
		require.NoError(t, err)
		c := NewVlogTruncator(db)
		require.NoError(t, c.Truncate(context.Background(), hdr.ID))

		// should add an extra transaction with catalogue
		require.Equal(t, lastCommitTx+1, db.st.LastCommittedTxID())
	})

	t.Run("verify transaction committed post truncation has truncation header", func(t *testing.T) {
		lastCommitTx := db.st.LastCommittedTxID()
		hdr, err := db.st.ReadTxHeader(lastCommitTx, false)
		require.NoError(t, err)
		require.NotNil(t, hdr.Metadata)
		require.True(t, hdr.Metadata.HasTruncatedTxID())

		truncatedTxId, err := hdr.Metadata.GetTruncatedTxID()
		require.NoError(t, err)
		require.Equal(t, deleteUptoTx.Id, truncatedTxId)
	})

	committedTxPostTruncation := make([]*schema.TxHeader, 0, 5)
	// add more data in table post truncation
	t.Run("succeed in adding data post truncation", func(t *testing.T) {
		// add sql data
		exec(t, "INSERT INTO table1(name, surname, amount) VALUES('John', 'Doe', 0)")
		exec(t, "INSERT INTO table1(name, surname, amount) VALUES('Smith', 'John', 0)")

		// add KV data
		for i := 6; i <= 10; i++ {
			var err error
			kv := &schema.KeyValue{
				Key:   []byte(fmt.Sprintf("key_%d", i)),
				Value: []byte(fmt.Sprintf("val_%d", i)),
			}
			hdr, err := db.Set(context.Background(), &schema.SetRequest{KVs: []*schema.KeyValue{kv}})
			require.NoError(t, err)
			committedTxPostTruncation = append(committedTxPostTruncation, hdr)
		}
	})

	// check if can query the table with new catalogue
	t.Run("succeed loading catalog from latest schema", func(t *testing.T) {
		query(t, "SELECT * FROM table1", 4)
	})

	t.Run("succeed reading KV data post truncation", func(t *testing.T) {
		for _, v := range committedTxPostTruncation {
			tx := store.NewTx(db.st.MaxTxEntries(), db.st.MaxKeyLen())
			err := db.st.ReadTx(v.Id, tx)
			require.NoError(t, err)
			for _, e := range tx.Entries() {
				val, err := db.st.ReadValue(e)
				require.NoError(t, err)
				require.NotNil(t, val)
			}
		}
	})
}

func Test_vlogCompactor_without_data(t *testing.T) {
	rootPath := t.TempDir()

	options := DefaultOption().WithDBRootPath(rootPath).WithCorruptionChecker(false)
	options.storeOpts.WithIndexOptions(options.storeOpts.IndexOpts.WithCompactionThld(2)).WithFileSize(6)
	options.storeOpts.MaxIOConcurrency = 1
	options.storeOpts.VLogCacheSize = 0

	db := makeDbWith(t, "db", options)

	require.Equal(t, uint64(1), db.st.LastCommittedTxID())

	deletePointTx := uint64(1)
	hdr, err := db.st.ReadTxHeader(deletePointTx, false)
	require.NoError(t, err)
	c := NewVlogTruncator(db)
	require.NoError(t, c.Truncate(context.Background(), hdr.ID))

	// ensure that a transaction is added for the sql catalog commit
	require.Equal(t, uint64(2), db.st.LastCommittedTxID())

	// verify that the transaction added for the sql catalog commit has the truncation header
	hdr, err = db.st.ReadTxHeader(2, false)
	require.NoError(t, err)
	require.NotNil(t, hdr.Metadata)
	require.True(t, hdr.Metadata.HasTruncatedTxID())

	// verify using the ReadTx API that the transaction added for the sql catalog commit has the truncation header
	ptx := store.NewTx(db.st.MaxTxEntries(), db.st.MaxKeyLen())
	err = db.st.ReadTx(2, ptx)
	require.NoError(t, err)
	require.True(t, ptx.Header().Metadata.HasTruncatedTxID())
}

func Test_vlogCompactor_with_multiple_truncates(t *testing.T) {
	db := setupCommonTest(t)

	exec := func(t *testing.T, stmt string) {
		_, ctx, err := db.SQLExec(context.Background(), nil, &schema.SQLExecRequest{Sql: stmt})
		require.NoError(t, err)
		require.Len(t, ctx, 1)
	}

	query := func(t *testing.T, stmt string, expectedRows int) {
		res, err := db.SQLQuery(context.Background(), nil, &schema.SQLQueryRequest{Sql: stmt})
		require.NoError(t, err)
		require.NoError(t, err)
		require.Len(t, res.Rows, expectedRows)
	}

	verify := func(t *testing.T, txID uint64) {
		lastCommitTx := db.st.LastCommittedTxID()
		hdr, err := db.st.ReadTxHeader(lastCommitTx, false)
		require.NoError(t, err)
		require.NotNil(t, hdr.Metadata)
		require.True(t, hdr.Metadata.HasTruncatedTxID())

		truncatedTxId, err := hdr.Metadata.GetTruncatedTxID()
		require.NoError(t, err)
		require.Equal(t, txID, truncatedTxId)
	}

	// create a new table
	exec(t, "CREATE TABLE table1 (id INTEGER AUTO_INCREMENT, name VARCHAR[50], amount INTEGER, PRIMARY KEY id)")
	exec(t, "CREATE UNIQUE INDEX ON table1 (name)")
	exec(t, "CREATE UNIQUE INDEX ON table1 (name, amount)")
	exec(t, "ALTER TABLE table1 ADD COLUMN surname VARCHAR")

	t.Run("succeed truncating sql catalog", func(t *testing.T) {
		lastCommitTx := db.st.LastCommittedTxID()
		hdr, err := db.st.ReadTxHeader(lastCommitTx, false)
		require.NoError(t, err)
		c := NewVlogTruncator(db)
		require.NoError(t, c.Truncate(context.Background(), hdr.ID))

		// should add an extra transaction with catalogue
		require.Equal(t, lastCommitTx+1, db.st.LastCommittedTxID())
		verify(t, hdr.ID)
	})

	t.Run("succeed loading catalog from latest schema", func(t *testing.T) {
		query(t, "SELECT * FROM table1", 0)
	})

	// insert some data
	var deleteUptoTx *schema.TxHeader
	for i := 1; i <= 5; i++ {
		var err error
		kv := &schema.KeyValue{
			Key:   []byte(fmt.Sprintf("key_%d", i)),
			Value: []byte(fmt.Sprintf("val_%d", i)),
		}
		deleteUptoTx, err = db.Set(context.Background(), &schema.SetRequest{KVs: []*schema.KeyValue{kv}})
		require.NoError(t, err)
	}

	// delete txns in the store upto a certain txn
	t.Run("succeed truncating sql catalog again", func(t *testing.T) {
		lastCommitTx := db.st.LastCommittedTxID()
		hdr, err := db.st.ReadTxHeader(deleteUptoTx.Id, false)
		require.NoError(t, err)
		c := NewVlogTruncator(db)
		require.NoError(t, c.Truncate(context.Background(), hdr.ID))

		// should add an extra transaction with catalogue
		require.Equal(t, lastCommitTx+1, db.st.LastCommittedTxID())
		verify(t, hdr.ID)
	})

	t.Run("insert sql transaction", func(t *testing.T) {
		exec(t, "INSERT INTO table1(name, surname, amount) VALUES('Foo', 'Bar', 0)")
		exec(t, "INSERT INTO table1(name, surname, amount) VALUES('Fin', 'Baz', 0)")
	})

	// check if can query the table with new catalogue
	t.Run("succeed loading catalog from latest schema", func(t *testing.T) {
		query(t, "SELECT * FROM table1", 2)
	})
}

func Test_vlogTruncator_isRetentionPeriodReached(t *testing.T) {
	type args struct {
		retentionPeriod time.Time
		txTs            time.Time
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "retention period not reached",
			args: args{
				retentionPeriod: TruncateToDay(time.Now().Add(-24 * time.Hour)),
				txTs:            TruncateToDay(time.Now()),
			},
			wantErr: true,
		},
		{
			name: "retention period reached",
			args: args{
				retentionPeriod: TruncateToDay(time.Now().Add(-1 * time.Hour)),
				txTs:            TruncateToDay(time.Now().Add(-2 * time.Hour)),
			},
			wantErr: false,
		},
		{
			name: "tx period before retention",
			args: args{
				retentionPeriod: TruncateToDay(time.Now()),
				txTs:            TruncateToDay(time.Now().Add(-48 * time.Hour)),
			},
			wantErr: false,
		},
		{
			name: "tx period after retention",
			args: args{
				retentionPeriod: TruncateToDay(time.Now()),
				txTs:            TruncateToDay(time.Now().Add(48 * time.Hour)),
			},
			wantErr: true,
		},
		{
			name: "tx period equal to retention",
			args: args{
				retentionPeriod: TruncateToDay(time.Now().Add(48 * time.Hour)),
				txTs:            TruncateToDay(time.Now().Add(48 * time.Hour)),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &vlogTruncator{}
			if err := v.isRetentionPeriodReached(tt.args.retentionPeriod, tt.args.txTs); (err != nil) != tt.wantErr {
				t.Errorf("vlogTruncator.isRetentionPeriodReached() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_vlogCompactor_for_read_conflict(t *testing.T) {
	rootPath := t.TempDir()

	options := DefaultOption().WithDBRootPath(rootPath).WithCorruptionChecker(false)
	options.storeOpts.WithFileSize(60)
	options.storeOpts.VLogCacheSize = 0

	db := makeDbWith(t, "db", options)
	require.Equal(t, uint64(1), db.st.LastCommittedTxID())

	for i := 1; i <= 10; i++ {
		kv := &schema.KeyValue{
			Key:   []byte(fmt.Sprintf("key_%d", i)),
			Value: []byte(fmt.Sprintf("val_%d", i)),
		}
		_, err := db.Set(context.Background(), &schema.SetRequest{KVs: []*schema.KeyValue{kv}})
		require.NoError(t, err)
	}

	once := sync.Once{}
	doneTruncateCh := make(chan bool, 0)
	startWritesCh := make(chan bool, 0)
	doneWritesCh := make(chan bool, 0)
	go func() {
		for i := 11; i <= 40; i++ {
			kv := &schema.KeyValue{
				Key:   []byte(fmt.Sprintf("key_%d", i)),
				Value: []byte(fmt.Sprintf("val_%d", i)),
			}
			_, err := db.Set(context.Background(), &schema.SetRequest{KVs: []*schema.KeyValue{kv}})
			once.Do(func() {
				close(startWritesCh)
			})
			require.NoError(t, err)
		}
		close(doneWritesCh)
	}()

	go func() {
		<-startWritesCh
		deletePointTx := uint64(5)
		hdr, err := db.st.ReadTxHeader(deletePointTx, false)
		require.NoError(t, err)
		c := NewVlogTruncator(db)
		require.NoError(t, c.Truncate(context.Background(), hdr.ID))
		close(doneTruncateCh)
	}()

	<-doneWritesCh
	<-doneTruncateCh
}

func Test_TruncateToDay(t *testing.T) {
	type args struct {
		t time.Time
	}
	tests := []struct {
		name string
		args args
		want time.Time
	}{
		{
			args: args{t: time.Date(2020, 1, 1, 10, 20, 30, 40, time.UTC)},
			want: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TruncateToDay(tt.args.t); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TruncateToDay() = %v, want %v", got, tt.want)
			}
		})
	}
}
