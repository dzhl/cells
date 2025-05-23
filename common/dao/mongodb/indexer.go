/*
 * Copyright (c) 2019-2022. Abstrium SAS <team (at) pydio.com>
 * This file is part of Pydio Cells.
 *
 * Pydio Cells is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * Pydio Cells is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with Pydio Cells.  If not, see <http://www.gnu.org/licenses/>.
 *
 * The latest code can be found at <https://pydio.com>.
 */

package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"

	"github.com/pydio/cells/v4/common/dao"
	"github.com/pydio/cells/v4/common/log"
	"github.com/pydio/cells/v4/common/utils/configx"
)

type IndexDAO interface {
	DAO
	dao.IndexDAO
	SetCollection(string)
}

type Indexer struct {
	DAO
	collection      string
	collectionModel Collection
	codec           dao.IndexCodex
	inserts         []interface{}
	deletes         []string
	tick            chan bool
	flush           chan bool
	done            chan bool
	bufferSize      int
	runtime         context.Context
}

func NewIndexer(ctx context.Context, dao dao.DAO) (dao.IndexDAO, error) {
	i := &Indexer{
		DAO:        dao.(DAO),
		bufferSize: 2000,
		tick:       make(chan bool),
		flush:      make(chan bool, 1),
		done:       make(chan bool, 1),
		runtime:    ctx,
	}
	go i.watch()
	return i, nil
}

func (i *Indexer) watch() {
	defer close(i.tick)
	defer close(i.flush)
	for {
		select {
		case <-i.tick:
			if len(i.inserts) > i.bufferSize || len(i.deletes) > i.bufferSize {
				i.Flush(i.runtime)
			}
		case <-time.After(3 * time.Second):
			i.Flush(i.runtime)
		case <-i.flush:
			i.Flush(i.runtime)
		case <-i.done:
			return
		}
	}
}

func (i *Indexer) mustTick() {
	// avoid send on closed panic
	defer func() {
		recover()
	}()
	i.tick <- true
}

func (i *Indexer) SetCollection(c string) {
	i.collection = c
}

func (i *Indexer) Init(ctx context.Context, cfg configx.Values) error {
	if i.collection == "" {
		return fmt.Errorf("indexer must provide a collection name")
	}
	if i.codec != nil {
		if mo, ok := i.codec.GetModel(cfg); ok {
			model := mo.(Model)
			if er := model.Init(context.Background(), i.DAO); er != nil {
				return er
			}
			for _, coll := range model.Collections {
				if coll.Name == i.collection {
					i.collectionModel = coll
				}
			}
		}
	}
	return i.DAO.Init(ctx, cfg)
}

func (i *Indexer) InsertOne(ctx context.Context, data interface{}) error {
	if m, e := i.codec.Marshal(data); e == nil {
		i.inserts = append(i.inserts, m)
		i.mustTick()
		return nil
	} else {
		return e
	}
}

func (i *Indexer) DeleteOne(ctx context.Context, data interface{}) error {
	var indexId string
	if id, ok := data.(string); ok {
		indexId = id
	} else if p, o := data.(dao.IndexIDProvider); o {
		indexId = p.IndexID()
	}
	if indexId == "" {
		return fmt.Errorf("data must be a string or an IndexIDProvider")
	}
	i.deletes = append(i.deletes, indexId)
	i.mustTick()
	return nil
}

func (i *Indexer) DeleteMany(ctx context.Context, query interface{}) (int32, error) {
	request, _, err := i.codec.BuildQuery(query, 0, 0, "", false)
	if err != nil {
		return 0, err
	}
	filters, ok := request.([]bson.E)
	if !ok {
		return 0, fmt.Errorf("cannot cast filter")
	}
	filter := bson.D{}
	filter = append(filter, filters...)
	res, e := i.Collection(i.collection).DeleteMany(ctx, filter)
	if e != nil {
		return 0, e
	} else {
		return int32(res.DeletedCount), nil
	}
}

func (i *Indexer) FindMany(ctx context.Context, query interface{}, offset, limit int32, sortFields string, sortDesc bool, customCodec dao.IndexCodex) (chan interface{}, error) {
	codec := i.codec
	if customCodec != nil {
		codec = customCodec
	}
	opts := &options.FindOptions{}
	if limit > 0 {
		l64 := int64(limit)
		opts.Limit = &l64
	}
	if offset > 0 {
		o64 := int64(offset)
		opts.Skip = &o64
	}
	var sendTotal *uint64
	// Eventually override options
	if op, ok := i.codec.(dao.QueryOptionsProvider); ok {
		if oo, e := op.BuildQueryOptions(query, offset, limit, sortFields, sortDesc); e == nil {
			opts = oo.(*options.FindOptions)
		}
	}
	// Build Query
	request, aggregation, err := codec.BuildQuery(query, offset, limit, sortFields, sortDesc)
	if err != nil {
		return nil, err
	}
	var searchCursor *mongo.Cursor
	var aggregationCursor *mongo.Cursor
	if request != nil {
		filters, ok := request.([]bson.E)
		if !ok {
			return nil, fmt.Errorf("cannot cast filter")
		}
		filter := bson.D{}
		filter = append(filter, filters...)
		if pc, ok := i.codec.(dao.QueryPreCountRequester); ok && pc.RequirePreCount() {
			total, er := i.Collection(i.collection).CountDocuments(ctx, filter)
			if er != nil {
				return nil, er
			}
			tt := uint64(total)
			sendTotal = &tt
		}
		cursor, err := i.Collection(i.collection).Find(ctx, filter, opts)
		if err != nil {
			return nil, err
		}
		searchCursor = cursor
	}
	if aggregation != nil {
		if c, e := i.Collection(i.collection).Aggregate(ctx, aggregation); e != nil {
			log.Logger(ctx).Error("Cannot perform aggregation:"+e.Error(), zap.Error(e))
			return nil, e
		} else {
			aggregationCursor = c
		}
	}

	res := make(chan interface{})
	fp, _ := codec.(dao.FacetParser)
	go func() {
		defer close(res)
		if sendTotal != nil {
			res <- *sendTotal
		}
		if searchCursor != nil {
			for searchCursor.Next(ctx) {
				if data, er := codec.Unmarshal(searchCursor); er == nil {
					res <- data
				} else {
					log.Logger(ctx).Warn("Cannot decode cursor data: "+err.Error(), zap.Error(err))
				}
			}
		}
		if aggregationCursor != nil {
			for aggregationCursor.Next(ctx) {
				if fp != nil {
					fp.UnmarshalFacet(aggregationCursor, res)
				} else if data, er := codec.Unmarshal(aggregationCursor); er == nil {
					res <- data
				} else {
					log.Logger(ctx).Warn("Cannot decode aggregation cursor data: "+err.Error(), zap.Error(err))
				}
			}
		}
	}()
	return res, nil

}

func (i *Indexer) Resync(ctx context.Context, logger func(string)) error {
	return fmt.Errorf("resync is not implemented on the mongo indexer")
}

type CollStats struct {
	Count       int64
	AvgObjSize  int64
	StorageSize int64
}

func (i *Indexer) collectionStats(ctx context.Context) (*CollStats, error) {
	directName := i.Collection(i.collection).Name()
	res := i.DB().RunCommand(ctx, bson.M{"collStats": directName})
	if er := res.Err(); er != nil {
		return nil, er
	}
	exp := &CollStats{}
	if e := res.Decode(exp); e != nil {
		return nil, fmt.Errorf("cannot read collection statistics to truncate based on size")
	}
	return exp, nil
}

// Truncate removes records from collection. If max is set, we find the starting index for deletion based on the collection
// average object size (using collStats command)
func (i *Indexer) Truncate(ctx context.Context, max int64, logger func(string)) error {
	var filter interface{}
	var opts []*options.DeleteOptions
	filter = bson.D{}
	var startCount int64

	if max > 0 {
		if i.collectionModel.TruncateSorterDesc == "" {
			return fmt.Errorf("collection model must declare a TruncateSorterDesc field to support this operation")
		}
		exp, er := i.collectionStats(ctx)
		if er != nil {
			return er
		}
		startCount = exp.Count
		if exp.Count == 0 {
			log.TasksLogger(ctx).Info("No row in collection, nothing to do")
			return nil
		}
		if exp.AvgObjSize == 0 {
			return fmt.Errorf("cannot read record average size to truncate based on size")
		}

		targetCount := int64(float64(max) / float64(exp.AvgObjSize))
		if targetCount >= exp.Count {
			log.TasksLogger(ctx).Info("Target size bigger than current size, nothing to do")
			return nil
		}

		log.TasksLogger(ctx).Info(fmt.Sprintf("Should truncate table at row %d on a total of %d", targetCount, exp.Count))
		limit := int64(1)
		cur, er := i.Collection(i.collection).Find(ctx, bson.D{}, &options.FindOptions{Sort: bson.M{i.collectionModel.TruncateSorterDesc: -1}, Skip: &targetCount, Limit: &limit})
		if er != nil {
			return fmt.Errorf("cannot find fist row for starting deletion: %v", er)
		}
		cur.Next(ctx)
		var record map[string]interface{}
		if er := cur.Decode(&record); er != nil {
			return fmt.Errorf("cannot decode first referecence record")
		}
		ref, ok := record[i.collectionModel.TruncateSorterDesc]
		if !ok {
			return fmt.Errorf("cannot locate correct record for deletion")
		}

		log.TasksLogger(ctx).Info(fmt.Sprintf("Will truncate table based on the following condition %s<%v", i.collectionModel.TruncateSorterDesc, ref))
		filter = bson.M{i.collectionModel.TruncateSorterDesc: bson.M{"$lte": ref}}
	}
	res, e := i.Collection(i.collection).DeleteMany(context.Background(), filter, opts...)
	if e != nil {
		return e
	}
	if max > 0 && startCount > 0 {
		if exp, er := i.collectionStats(ctx); er == nil {
			msg := fmt.Sprintf("Collection storage size reduced from %d records to %d", startCount, exp.Count)
			log.Logger(ctx).Info(msg)
			log.TasksLogger(ctx).Info(msg)
			return nil
		}
	}
	log.Logger(ctx).Info(fmt.Sprintf("Flushed Mongo index from %d records", res.DeletedCount))
	log.TasksLogger(ctx).Info(fmt.Sprintf("Flushed Mongo index from %d records", res.DeletedCount))
	return nil
}
func (i *Indexer) Close(ctx context.Context) error {
	close(i.done)
	return i.CloseConn(ctx)
}
func (i *Indexer) Flush(ctx context.Context) error {
	conn := i.Collection(i.collection)

	if len(i.inserts) > 0 {
		if i.collectionModel.IDName != "" {
			// First remove all entries with given ID
			var ors bson.A
			for _, insert := range i.inserts {
				if p, o := insert.(dao.IndexIDProvider); o {
					ors = append(ors, bson.M{i.collectionModel.IDName: p.IndexID()})
				}
			}
			if _, e := conn.DeleteMany(ctx, bson.M{"$or": ors}); e != nil {
				log.Logger(ctx).Error("error while flushing pre-deletes:" + e.Error())
				return e
			}
		}
		if _, e := conn.InsertMany(ctx, i.inserts); e != nil {
			log.Logger(ctx).Error("error while flushing index to db" + e.Error())
			return e
		} else {
			//fmt.Println("flushed index to db", len(res.InsertedIDs))
		}
		i.inserts = []interface{}{}
	}

	if len(i.deletes) > 0 && i.collectionModel.IDName != "" {
		var ors bson.A
		for _, d := range i.deletes {
			ors = append(ors, bson.M{i.collectionModel.IDName: d})
		}
		if _, e := conn.DeleteMany(context.Background(), bson.M{"$or": ors}); e != nil {
			log.Logger(ctx).Error("error while flushing deletes to index" + e.Error())
			return e
		} else {
			//fmt.Println("flushed index, deleted", res.DeletedCount)
		}
		i.deletes = []string{}
	}
	return nil
}

func (i *Indexer) SetCodex(c dao.IndexCodex) {
	i.codec = c
}
