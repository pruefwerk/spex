package scheduling

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

// MongoStore persists one CAS-protected queue per host-selected capacity pool.
// Use a dedicated collection, not a database inside a disposable Kind cluster.
// It never creates TTL indexes or expires queue entries or active slots.
type MongoStore struct {
	collection *mongo.Collection
	pool       string
}
type document struct {
	ID       string `bson:"_id"`
	Schema   string `bson:"schema"`
	Revision string `bson:"revision"`
	State    []byte `bson:"state"`
}

func NewMongoStore(collection *mongo.Collection, pool string) (*MongoStore, error) {
	if collection == nil || !identifier(pool, 256) {
		return nil, ErrStore
	}
	configured := collection.Clone(options.Collection().SetReadConcern(readconcern.Majority()).SetWriteConcern(writeconcern.Majority()).SetReadPreference(readpref.Primary()))
	return &MongoStore{configured, pool}, nil
}

func decode(data []byte) (State, error) {
	var state State
	if len(data) > 8<<20 {
		return state, ErrStore
	}
	reader := json.NewDecoder(bytes.NewReader(data))
	reader.DisallowUnknownFields()
	if reader.Decode(&state) != nil {
		return State{}, ErrStore
	}
	var extra any
	if reader.Decode(&extra) != io.EOF || validate(state) != nil {
		return State{}, ErrStore
	}
	return state, nil
}

func (s *MongoStore) Transaction(ctx context.Context, action func(*State) error) error {
	for attempt := 0; attempt < 100; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var previous document
		err := s.collection.FindOne(ctx, bson.M{"_id": s.pool}).Decode(&previous)
		fresh := errors.Is(err, mongo.ErrNoDocuments)
		if err != nil && !fresh {
			return ErrStore
		}
		state := State{}
		if !fresh {
			if previous.Schema != "spex.scheduling/v1" || previous.Revision == "" {
				return ErrStore
			}
			state, err = decode(previous.State)
			if err != nil || state.Pool != s.pool {
				return ErrStore
			}
		}
		if err := action(&state); err != nil {
			return err
		}
		if validate(state) != nil || state.Pool != s.pool {
			return ErrStore
		}
		data, err := json.Marshal(state)
		if err != nil || len(data) > 8<<20 {
			return ErrStore
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return ErrStore
		}
		next := document{s.pool, "spex.scheduling/v1", hex.EncodeToString(nonce[:]), data}
		if fresh {
			_, err = s.collection.InsertOne(ctx, next)
			if err == nil {
				return nil
			}
			if !mongo.IsDuplicateKeyError(err) {
				return ErrStore
			}
		} else {
			result, err := s.collection.ReplaceOne(ctx, bson.M{"_id": s.pool, "revision": previous.Revision}, next)
			if err != nil {
				return ErrStore
			}
			if result.MatchedCount == 1 {
				return nil
			}
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ErrStore
}
